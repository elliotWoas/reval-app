package handlers

import (
	"errors"
	"fmt"
	"strings"

	"taskbot/internal/domain"
	"taskbot/internal/domain/member"
	"taskbot/internal/render"
	cb "taskbot/internal/transport/telegram/callbackdata"
	kb "taskbot/internal/transport/telegram/keyboards"
	"taskbot/internal/transport/telegram/tgapi"
)

const (
	stJoinKey  = "jn_key"
	stJoinName = "jn_name"
)

func (h *H) registerJoin() {
	r, w := h.r, h.wrap
	r.State(stJoinKey, w(func(c *Ctx) error { return h.joinWithKey(c, c.Text()) }))
	r.State(stJoinName, w(h.onJoinName))
	r.Callback(cb.JoinConfirm, w(h.onJoinConfirm))
	r.Callback(cb.JoinDecide, w(h.onJoinDecide))
	r.Callback(cb.GroupConnect, w(h.onGroupConnect))
	r.Callback(cb.TransferAnswer, w(h.onTransferAnswer))
}

func (h *H) onJoinStart(c *Ctx) error {
	if err := c.Reset(); err != nil {
		return err
	}
	c.Answer("", false)
	if err := c.Enter(stJoinKey); err != nil {
		return err
	}
	return c.Reply(c.T("join.ask_key"), h.cancelKB(c))
}

// joinWithKey resolves a typed or deep-link key and asks for confirmation.
func (h *H) joinWithKey(c *Ctx, key string) error {
	t, err := h.Members.ResolveKey(c, c.U.From.ID, key)
	if err != nil {
		if errors.Is(err, domain.ErrKeyNotFound) {
			if c.Sess.State != stJoinKey {
				_ = c.Enter(stJoinKey)
			}
			return c.Reply(c.T("errors.key_not_found"), h.cancelKB(c))
		}
		return err
	}
	m, err := h.Store.FindMembership(c, t.ID, c.U.From.ID)
	if err != nil {
		return err
	}
	if m != nil && m.Active() {
		_ = c.Reset()
		return h.mainMenu(c, c.T("errors.already_member"))
	}
	c.Sess.Set("team", t.ID)
	if err := c.Enter(stJoinKey); err != nil {
		return err
	}
	return c.Reply(c.T("join.confirm", "team", t.Name), kb.Inline([]kb.Btn{
		kb.B(c.T("common.yes"), cb.Encode(cb.JoinConfirm, "1")),
		kb.B(c.T("common.no"), cb.Encode(cb.JoinConfirm, "0")),
	}))
}

func (h *H) onJoinConfirm(c *Ctx) error {
	if c.Sess.State != stJoinKey || c.Sess.Get("team") == "" {
		return h.expired(c)
	}
	if c.Data().Arg(1) != "1" {
		_ = c.Edit(c.T("common.cancelled"), "")
		return h.cancel(c)
	}
	_ = c.Edit(c.T("join.confirmed"), "")
	if err := c.Enter(stJoinName); err != nil {
		return err
	}
	name := displayName(c)
	return c.Reply(c.T("join.ask_name", "name", name), kb.Reply([][]string{{name}, {c.T("common.cancel")}}, false, ""))
}

func (h *H) onJoinName(c *Ctx) error {
	teamID := c.Sess.Get("team")
	m, err := h.Members.Join(c, c.U.From.ID, teamID, c.Text())
	if err != nil && !errors.Is(err, domain.ErrPendingApproval) {
		if domain.Code(err) == domain.ErrNameLength.Code {
			return c.Reply(c.T("errors.name_length", "max", render.Num(h.Cfg.Limits.DisplayNameMax)), "")
		}
		return err
	}
	_ = c.Reset()
	t, err := h.Store.GetTeam(c, teamID)
	if err != nil {
		return err
	}
	if m.Status == member.StatusPending {
		return c.Reply(c.T("join.pending", "team", t.Name), kb.Remove())
	}
	c.User.ActiveTeam = t.ID
	text := c.T("join.welcome", "team", t.Name) + "\n" + h.V.RoutineText(t)
	if m.StartDate > t.LocalDate(h.Clock.Now()) {
		text += "\n" + c.T("join.starts_tomorrow")
	}
	return h.mainMenu(c, text)
}

func (h *H) onJoinDecide(c *Ctx) error {
	d := c.Data()
	m, err := h.Members.Decide(c, c.U.From.ID, d.Arg(1), d.Arg(2) == "1")
	if err != nil {
		return err
	}
	key := "join.decided_rejected"
	if m.Active() {
		key = "join.decided_approved"
	}
	return c.Edit(c.T(key, "name", m.DisplayName), "")
}

func (h *H) onTransferAnswer(c *Ctx) error {
	d := c.Data()
	accept := d.Arg(2) == "1"
	t, err := h.Members.AnswerTransfer(c, c.U.From.ID, d.Arg(1), accept)
	if err != nil {
		return err
	}
	if !accept {
		_ = c.Send(t.OwnerID, c.T("members.transfer_declined", "team", t.Name), "")
		return c.Edit(c.T("members.transfer_you_declined"), "")
	}
	_ = c.Edit(c.T("members.transfer_you_owner", "team", t.Name), "")
	_ = h.Store.SetActiveTeam(c, c.U.From.ID, t.ID)
	c.User.ActiveTeam = t.ID
	return h.mainMenu(c, "")
}

// ---- groups

// onMembership handles the bot being added to or removed from a chat.
func (h *H) onMembership(c *Ctx) error {
	u := c.U
	if u.Private() {
		return nil // user blocked/unblocked the bot: handled in touchUser
	}
	switch u.NewStatus {
	case "left", "kicked":
		return h.Members.GroupLost(c, u.ChatID)
	case "member", "administrator":
		if u.OldStatus == "member" || u.OldStatus == "administrator" {
			return nil // permission change only
		}
		teams, err := h.Members.OwnedTeams(c, u.From.ID)
		if err != nil {
			return err
		}
		if len(teams) == 0 {
			return c.Send(u.ChatID, c.T("group.no_team"), "")
		}
		c.Sess.Set("gtitle:"+fmt.Sprint(u.ChatID), u.ChatTitle)
		_ = c.SaveSess()
		var rows [][]kb.Btn
		for _, t := range teams {
			rows = append(rows, []kb.Btn{kb.B(c.T("group.btn_connect", "team", t.Name), cb.Encode(cb.GroupConnect, t.ID, fmt.Sprint(u.ChatID)))})
		}
		rows = append(rows, []kb.Btn{kb.B(c.T("common.no"), cb.Encode(cb.GroupSkip))})
		return c.Send(u.From.ID, c.T("group.confirm", "group", u.ChatTitle), kb.Inline(rows...))
	}
	return nil
}

func (h *H) onGroupConnect(c *Ctx) error {
	d := c.Data()
	teamID, chatID := d.Arg(1), d.Int64(2)
	admin, err := h.Client.IsAdmin(c, chatID, c.U.From.ID)
	if err != nil {
		return c.Edit(c.T("group.check_failed"), "")
	}
	if !admin {
		return domain.ErrNotGroupAdmin
	}
	title := c.Sess.Get("gtitle:" + fmt.Sprint(chatID))
	t, err := h.Members.ConnectGroup(c, c.U.From.ID, teamID, chatID, title)
	if err != nil {
		return err
	}
	return c.Edit(c.T("group.connected", "team", t.Name), "")
}

// onGroupMessage handles messages inside groups: supergroup migration and /start.
func (h *H) onGroupMessage(c *Ctx) error {
	u := c.U
	if u.MigrateTo != 0 {
		return h.Members.GroupMigrated(c, u.ChatID, u.MigrateTo)
	}
	if strings.HasPrefix(u.Text, "/start") || strings.HasPrefix(u.Text, "/help") {
		return c.Reply(c.T("group.hello"), "")
	}
	return nil
}

var _ = tgapi.KindMessage
