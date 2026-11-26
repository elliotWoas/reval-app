// Package handlers holds every Telegram conversation: menus, team creation, joining,
// plans, check-ins, evening reports, leaves and owner tools.
package handlers

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"taskbot/internal/config"
	"taskbot/internal/domain"
	"taskbot/internal/domain/member"
	"taskbot/internal/domain/team"
	"taskbot/internal/outbox"
	"taskbot/internal/platform/clock"
	"taskbot/internal/platform/i18n"
	"taskbot/internal/transport/telegram/callbackdata"
	"taskbot/internal/transport/telegram/fsm"
	kb "taskbot/internal/transport/telegram/keyboards"
	"taskbot/internal/transport/telegram/router"
	"taskbot/internal/transport/telegram/tgapi"
	"taskbot/internal/transport/telegram/views"
	"taskbot/internal/usecase/checkin"
	"taskbot/internal/usecase/dailycycle"
	"taskbot/internal/usecase/leave"
	"taskbot/internal/usecase/membership"
	"taskbot/internal/usecase/onboarding"
	"taskbot/internal/usecase/ports"
	"taskbot/internal/usecase/reporting"
	"taskbot/internal/usecase/settings"
)

// Deps are the handler dependencies.
type Deps struct {
	Cfg        *config.Config
	T          *i18n.Bundle
	V          *views.V
	Client     tgapi.Client
	Limiter    *outbox.Limiter
	Store      ports.Store
	FSM        *fsm.Manager
	Clock      clock.Clock
	Log        *slog.Logger
	Onboarding *onboarding.Service
	Members    *membership.Service
	Settings   *settings.Service
	Cycle      *dailycycle.Service
	Checkin    *checkin.Service
	Leaves     *leave.Service
	Reports    *reporting.Service
}

// H routes and handles updates.
type H struct {
	Deps
	r *router.Router
}

// New creates the handlers and registers routes.
func New(d Deps) *H {
	h := &H{Deps: d, r: router.New()}
	h.register()
	return h
}

// Ctx is the per-update context.
type Ctx struct {
	context.Context
	h        *H
	U        tgapi.Update
	User     *member.User
	Sess     *fsm.Session
	answered bool
}

type fn func(c *Ctx) error

func (h *H) wrap(f fn) router.Handler {
	return func(r *router.Request) error { return f(r.Extra.(*Ctx)) }
}

// Handle processes one update.
func (h *H) Handle(ctx context.Context, u tgapi.Update) error {
	c := &Ctx{Context: ctx, h: h, U: u}
	if u.From.ID != 0 {
		user, err := h.touchUser(ctx, u)
		if err != nil {
			return err
		}
		c.User = user
		if c.Sess, err = h.FSM.Load(ctx, u.From.ID); err != nil {
			return err
		}
	} else {
		c.Sess = &fsm.Session{Data: map[string]string{}}
	}
	req := &router.Request{Ctx: ctx, Update: u, State: c.Sess.State, Extra: c}
	hd, ok := h.r.Route(req)
	var err error
	if ok {
		err = hd(req)
	}
	if u.Kind == tgapi.KindCallback && !c.answered {
		_ = h.Client.Answer(ctx, u.CallbackID, "", false)
	}
	if err != nil {
		c.ReplyErr(err)
	}
	return nil
}

// touchUser creates or refreshes the sender. A private message also means the user
// can receive messages again (unblocked).
func (h *H) touchUser(ctx context.Context, u tgapi.Update) (*member.User, error) {
	now := h.Clock.Now()
	user, err := h.Store.GetUser(ctx, u.From.ID)
	if errors.Is(err, domain.ErrNotFound) {
		user = &member.User{ID: u.From.ID, CreatedAt: now}
	} else if err != nil {
		return nil, err
	}
	changed := user.FirstName != u.From.FirstName || user.LastName != u.From.LastName || user.Username != u.From.Username ||
		now.Sub(user.SeenAt).Hours() > 1
	user.FirstName, user.LastName, user.Username = u.From.FirstName, u.From.LastName, u.From.Username
	if u.Private() && u.Kind != tgapi.KindMembership && user.Blocked {
		user.Blocked = false
		changed = true
	}
	if u.Kind == tgapi.KindMembership && u.Private() {
		user.Blocked = u.NewStatus == "kicked"
		changed = true
	}
	if changed {
		user.SeenAt = now
		if err := h.Store.UpsertUser(ctx, user); err != nil {
			return nil, err
		}
	}
	return user, nil
}

// ---- Ctx helpers

// T translates.
func (c *Ctx) T(key string, kv ...any) string { return c.h.T.T(key, kv...) }

// Reply sends a message to the current chat.
func (c *Ctx) Reply(text, markup string) error {
	return c.Send(c.U.ChatID, text, markup)
}

// Send sends a message right away (respecting rate limits).
func (c *Ctx) Send(chatID int64, text, markup string) error {
	if err := c.h.Limiter.Wait(c, chatID, chatID < 0); err != nil {
		return err
	}
	_, err := c.h.Client.Send(c, chatID, text, markup)
	if err != nil {
		var ae *outbox.APIError
		if errors.As(err, &ae) && ae.Kind == outbox.KindForbidden && chatID > 0 {
			_ = c.h.Store.SetUserBlocked(c, chatID, true)
		}
	}
	return err
}

// Edit replaces the message the callback came from; it falls back to a new message.
func (c *Ctx) Edit(text, markup string) error {
	if c.U.Kind != tgapi.KindCallback || c.U.MessageID == 0 {
		return c.Reply(text, markup)
	}
	err := c.h.Client.Edit(c, c.U.ChatID, c.U.MessageID, text, markup)
	if err != nil {
		ae := outbox.AsAPIError(err)
		if ae.Kind == outbox.KindNotModified {
			return nil
		}
		return c.Reply(text, markup)
	}
	return nil
}

// Answer answers the callback with a short toast.
func (c *Ctx) Answer(text string, alert bool) {
	if c.U.Kind != tgapi.KindCallback || c.answered {
		return
	}
	c.answered = true
	_ = c.h.Client.Answer(c, c.U.CallbackID, text, alert)
}

// ReplyErr shows a localized error.
func (c *Ctx) ReplyErr(err error) {
	code := domain.Code(err)
	text := c.T("errors.generic")
	if code != "" && c.h.T.Has("errors."+code) {
		text = c.T("errors." + code)
	} else {
		c.h.Log.Error("unexpected error", "user", c.U.From.ID, "err", err)
	}
	if c.U.Kind == tgapi.KindCallback && !c.answered {
		c.Answer(text, true)
		return
	}
	_ = c.Reply(text, "")
}

// Enter switches the conversation state.
func (c *Ctx) Enter(state string) error { return c.h.FSM.Enter(c, c.Sess, state) }

// SaveSess stores session data.
func (c *Ctx) SaveSess() error { return c.h.FSM.Save(c, c.Sess) }

// Reset clears the conversation.
func (c *Ctx) Reset() error { return c.h.FSM.Reset(c, c.Sess) }

// Data returns callback data.
func (c *Ctx) Data() callbackdata.Data { return callbackdata.Decode(c.U.Data) }

// Text returns the trimmed message text.
func (c *Ctx) Text() string { return strings.TrimSpace(c.U.Text) }

// ---- team context

// teams returns the caller's active teams.
func (h *H) teams(c *Ctx) ([]membership.TeamMember, error) {
	return h.Members.MyTeams(c, c.U.From.ID)
}

// active returns the caller's current team, or ok=false when the user has none.
func (h *H) active(c *Ctx) (membership.TeamMember, bool, error) {
	tms, err := h.teams(c)
	if err != nil || len(tms) == 0 {
		return membership.TeamMember{}, false, err
	}
	if c.User != nil {
		for _, tm := range tms {
			if tm.Team.ID == c.User.ActiveTeam {
				return tm, true, nil
			}
		}
	}
	return tms[0], true, nil
}

func (h *H) multi(c *Ctx) bool {
	tms, err := h.teams(c)
	return err == nil && len(tms) > 1
}

// teamCtx loads a team id from callback/session for the caller.
func (h *H) teamCtx(c *Ctx, teamID string) (membership.TeamMember, error) {
	return h.Members.Context(c, c.U.From.ID, teamID)
}

// ---- menus

// MainMenu shows the persistent reply keyboard for the caller's role in the active team.
func (h *H) mainMenu(c *Ctx, text string) error {
	tm, ok, err := h.active(c)
	if err != nil {
		return err
	}
	if !ok {
		if text == "" {
			text = c.T("start.welcome")
		}
		if err := c.Reply(text, kb.Reply([][]string{{c.T("menu.create")}, {c.T("menu.join")}}, true, "")); err != nil {
			return err
		}
		return c.Reply(c.T("start.choose"), kb.Inline(
			[]kb.Btn{kb.B(c.T("menu.create"), callbackdata.Encode(callbackdata.MainCreate))},
			[]kb.Btn{kb.B(c.T("menu.join"), callbackdata.Encode(callbackdata.MainJoin))},
		))
	}
	var rows [][]string
	owner := tm.Team.OwnerID == c.U.From.ID
	if owner {
		rows = append(rows, []string{c.T("menu.status"), c.T("menu.members")}, []string{c.T("menu.settings"), c.T("menu.invite")})
	}
	if tm.Member.Participates {
		rows = append(rows, []string{c.T("menu.today"), c.T("menu.leave")})
	}
	rows = append(rows, []string{c.T("menu.weekly"), c.T("menu.more")})
	if text == "" {
		text = c.T("menu.title", "team", tm.Team.Name)
	}
	return c.Reply(text, kb.Reply(rows, true, ""))
}

// cancel resets the conversation and shows the menu.
func (h *H) cancel(c *Ctx) error {
	if err := c.Reset(); err != nil {
		return err
	}
	if c.U.Kind == tgapi.KindCallback {
		c.Answer(c.T("common.cancelled"), false)
		_ = c.Edit(c.T("common.cancelled"), "")
	}
	return h.mainMenu(c, c.T("common.cancelled_menu"))
}

// cancelKB is a reply keyboard with only "cancel".
func (h *H) cancelKB(c *Ctx) string { return kb.Reply([][]string{{c.T("common.cancel")}}, false, "") }

// pickTeam asks which team an action is for. action is stored and resumed by onTeamPick.
func (h *H) pickTeam(c *Ctx, tms []membership.TeamMember, action string) error {
	c.Sess.Set("pick_action", action)
	if err := c.SaveSess(); err != nil {
		return err
	}
	var rows [][]kb.Btn
	for _, tm := range tms {
		rows = append(rows, []kb.Btn{kb.B(tm.Team.Name, callbackdata.Encode(callbackdata.TeamPick, tm.Team.ID))})
	}
	return c.Reply(c.T("common.pick_team"), kb.Inline(rows...))
}

func (h *H) register() {
	r := h.r
	w := h.wrap
	r.Command("/start", w(h.onStart))
	r.Command("/menu", w(func(c *Ctx) error { _ = c.Reset(); return h.mainMenu(c, "") }))
	r.Command("/cancel", w(h.cancel))
	r.Command("/help", w(h.onHelp))
	r.Command("/today", w(h.onToday))
	r.Command("/leave", w(h.onLeaveMenu))
	r.Command("/team", w(h.onSwitchTeam))

	r.Menu(h.T.T("common.cancel"), w(h.cancel))
	r.Menu(h.T.T("menu.create"), w(h.onCreateStart))
	r.Menu(h.T.T("menu.join"), w(h.onJoinStart))
	r.Menu(h.T.T("menu.today"), w(h.onToday))
	r.Menu(h.T.T("menu.leave"), w(h.onLeaveMenu))
	r.Menu(h.T.T("menu.weekly"), w(h.onWeekly))
	r.Menu(h.T.T("menu.more"), w(h.onMore))
	r.Menu(h.T.T("menu.status"), w(h.onStatus))
	r.Menu(h.T.T("menu.members"), w(h.onMembers))
	r.Menu(h.T.T("menu.settings"), w(h.onSettings))
	r.Menu(h.T.T("menu.invite"), w(h.onInvite))

	r.Callback(callbackdata.Cancel, w(h.cancel))
	r.Callback(callbackdata.Noop, w(func(c *Ctx) error { c.Answer("", false); return nil }))
	r.Callback(callbackdata.MainCreate, w(h.onCreateStart))
	r.Callback(callbackdata.MainJoin, w(h.onJoinStart))
	r.Callback(callbackdata.MainMore, w(h.onMoreAction))
	r.Callback(callbackdata.TeamPick, w(h.onTeamPick))

	h.registerOnboarding()
	h.registerJoin()
	h.registerPlan()
	h.registerLeave()
	h.registerOwner()

	r.Membership(w(h.onMembership))
	r.Group(w(h.onGroupMessage))
	r.Fallback(w(h.onFreeText))
}

func (h *H) onStart(c *Ctx) error {
	_ = c.Reset()
	parts := strings.Fields(c.Text())
	if len(parts) > 1 {
		return h.joinWithKey(c, parts[1])
	}
	return h.mainMenu(c, "")
}

func (h *H) onHelp(c *Ctx) error { return c.Reply(c.T("start.help"), "") }

// onMore shows secondary actions.
func (h *H) onMore(c *Ctx) error {
	_ = c.Reset()
	rows := [][]kb.Btn{
		{kb.B(c.T("more.my_leaves"), callbackdata.Encode(callbackdata.MainMore, "leaves")), kb.B(c.T("more.rename"), callbackdata.Encode(callbackdata.MainMore, "rename"))},
		{kb.B(c.T("more.switch_team"), callbackdata.Encode(callbackdata.MainMore, "switch")), kb.B(c.T("more.help"), callbackdata.Encode(callbackdata.MainMore, "help"))},
		{kb.B(c.T("menu.create"), callbackdata.Encode(callbackdata.MainCreate)), kb.B(c.T("menu.join"), callbackdata.Encode(callbackdata.MainJoin))},
		{kb.B(c.T("more.leave_team"), callbackdata.Encode(callbackdata.MainMore, "quit"))},
	}
	return c.Reply(c.T("more.title"), kb.Inline(rows...))
}

func (h *H) onMoreAction(c *Ctx) error {
	switch c.Data().Arg(1) {
	case "leaves":
		return h.onMyLeaves(c)
	case "rename":
		tm, ok, err := h.active(c)
		if err != nil || !ok {
			return err
		}
		c.Sess.Set("team", tm.Team.ID)
		if err := c.Enter(stRename); err != nil {
			return err
		}
		return c.Reply(c.T("more.rename_ask", "name", tm.Member.DisplayName), h.cancelKB(c))
	case "switch":
		return h.onSwitchTeam(c)
	case "help":
		return h.onHelp(c)
	case "quit":
		tm, ok, err := h.active(c)
		if err != nil || !ok {
			return err
		}
		return c.Edit(c.T("more.leave_confirm", "team", tm.Team.Name), kb.Inline([]kb.Btn{
			kb.B(c.T("common.yes"), callbackdata.Encode(callbackdata.MainMore, "quit_ok", tm.Team.ID)),
			kb.B(c.T("common.no"), callbackdata.Encode(callbackdata.Cancel)),
		}))
	case "quit_ok":
		if err := h.Members.Leave(c, c.U.From.ID, c.Data().Arg(2)); err != nil {
			return err
		}
		_ = c.Edit(c.T("more.left_team"), "")
		return h.mainMenu(c, "")
	}
	return nil
}

const stRename = "rename"

func (h *H) onRename(c *Ctx) error {
	if err := h.Members.SetDisplayName(c, c.U.From.ID, c.Sess.Get("team"), c.Text()); err != nil {
		return err
	}
	_ = c.Reset()
	return h.mainMenu(c, c.T("more.renamed", "name", c.Text()))
}

// onSwitchTeam changes the active team.
func (h *H) onSwitchTeam(c *Ctx) error {
	tms, err := h.teams(c)
	if err != nil {
		return err
	}
	if len(tms) < 2 {
		return c.Reply(c.T("more.single_team"), "")
	}
	return h.pickTeam(c, tms, "switch")
}

// onTeamPick resumes the action that needed a team choice.
func (h *H) onTeamPick(c *Ctx) error {
	teamID := c.Data().Arg(1)
	tm, err := h.teamCtx(c, teamID)
	if err != nil {
		return err
	}
	action := c.Sess.Get("pick_action")
	switch action {
	case "switch":
		if err := h.Store.SetActiveTeam(c, c.U.From.ID, teamID); err != nil {
			return err
		}
		c.User.ActiveTeam = teamID
		_ = c.Edit(c.T("more.switched", "team", tm.Team.Name), "")
		return h.mainMenu(c, "")
	case "plan":
		_ = c.Edit(c.T("common.team_header", "team", tm.Team.Name), "")
		return h.previewDraft(c, tm, c.Sess.Get("pending_text"), "append")
	case "leave":
		_ = c.Edit(c.T("common.team_header", "team", tm.Team.Name), "")
		return h.startLeave(c, tm, "")
	case "today":
		_ = c.Edit(c.T("common.team_header", "team", tm.Team.Name), "")
		return h.showToday(c, tm, false)
	}
	return nil
}

// withTeam runs f for the only team, or asks which team first.
func (h *H) withTeam(c *Ctx, action string, f func(tm membership.TeamMember) error) error {
	tms, err := h.teams(c)
	if err != nil {
		return err
	}
	var part []membership.TeamMember
	for _, tm := range tms {
		if tm.Member.Participates {
			part = append(part, tm)
		}
	}
	switch len(part) {
	case 0:
		if len(tms) == 0 {
			return h.mainMenu(c, c.T("start.no_team"))
		}
		return c.Reply(c.T("plan.not_participating"), "")
	case 1:
		return f(part[0])
	}
	return h.pickTeam(c, part, action)
}

func isOwner(c *Ctx, t *team.Team) bool { return t.OwnerID == c.U.From.ID }
