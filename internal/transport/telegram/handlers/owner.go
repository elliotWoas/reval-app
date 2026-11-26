package handlers

import (
	"fmt"
	"slices"
	"strings"

	"taskbot/internal/domain"
	"taskbot/internal/domain/plan"
	"taskbot/internal/domain/team"
	"taskbot/internal/render"
	cb "taskbot/internal/transport/telegram/callbackdata"
	kb "taskbot/internal/transport/telegram/keyboards"
	"taskbot/internal/usecase/membership"
	"taskbot/internal/usecase/settings"
)

const stOwnerNote = "on_note"

func (h *H) registerOwner() {
	r, w := h.r, h.wrap
	r.State(stOwnerNote, w(h.onOwnerNoteText))
	r.Callback(cb.OwnerRemind, w(h.onOwnerRemind))
	r.Callback(cb.MemberAction, w(h.onMemberAction))
	r.Callback(cb.MemberConfirm, w(h.onMemberConfirm))
	r.Callback(cb.KeyRotate, w(h.onKeyRotate))
	r.Callback(cb.Settings, w(h.onSettingsCB))
	r.Callback(cb.OwnerNote, w(h.onOwnerNoteCB))
}

// ownerTeam returns the active team when the caller owns it.
func (h *H) ownerTeam(c *Ctx) (membership.TeamMember, error) {
	tm, ok, err := h.active(c)
	if err != nil {
		return tm, err
	}
	if !ok {
		return tm, domain.ErrNotFound
	}
	if !isOwner(c, tm.Team) {
		return tm, domain.ErrNotOwner
	}
	return tm, nil
}

// onStatus shows who sent their plan and report today, with manual reminder buttons.
func (h *H) onStatus(c *Ctx) error {
	_ = c.Reset()
	tm, err := h.ownerTeam(c)
	if err != nil {
		return err
	}
	t := tm.Team
	v, err := h.Cycle.TodayView(c, t)
	if err != nil {
		return err
	}
	head := h.V.Header(t, h.multi(c)) + c.T("status.title", "date", render.Date(v.Date))
	if !t.IsWorkDay(v.Date) {
		return c.Reply(head+"\n"+c.T("status.off_day"), "")
	}
	now := h.Clock.Now()
	eveningStarted := !now.Before(t.At(v.Date, t.Schedule.Evening))
	var lines []string
	var btns []kb.Btn
	for _, r := range v.Rows {
		name := r.Member.DisplayName
		var line string
		needs := false
		switch {
		case r.OnFullLeave():
			line = "🏖 " + name
		case r.Blocked:
			line = "🚫 " + name + " — " + c.T("status.blocked")
		case !r.Plan.Submitted():
			line = "⏳ " + name + " — " + c.T("status.no_plan")
			needs = true
		default:
			cnt := r.Plan.Count()
			line = "✅ " + name + " — " + c.T("status.tasks", "n", render.Num(cnt.Total), "done", render.Num(cnt.Done))
			if r.Plan.Status == plan.StatusLate {
				line += " ⏰"
			}
			if r.Plan.Stuck {
				line += " 🚧"
			}
			if r.Plan.Reported() {
				line += " · 🌙"
			} else if eveningStarted {
				line += " · " + c.T("status.no_report")
				needs = true
			}
		}
		lines = append(lines, line)
		if needs {
			btns = append(btns, kb.B(c.T("status.btn_remind", "name", name), cb.Encode(cb.OwnerRemind, t.ID, r.Member.ID)))
		}
	}
	if len(lines) == 0 {
		lines = append(lines, c.T("status.empty"))
	}
	return c.Reply(head+"\n"+strings.Join(lines, "\n"), kb.Inline(kb.Grid(btns, 2)...))
}

func (h *H) onOwnerRemind(c *Ctx) error {
	d := c.Data()
	tm, err := h.Members.OwnerContext(c, c.U.From.ID, d.Arg(1))
	if err != nil {
		return err
	}
	if err := h.Cycle.ManualRemind(c, tm.Team, d.Arg(2)); err != nil {
		return err
	}
	c.Answer(c.T("status.reminded"), false)
	return nil
}

// ---- members

func (h *H) onMembers(c *Ctx) error {
	_ = c.Reset()
	tm, err := h.ownerTeam(c)
	if err != nil {
		return err
	}
	ms, err := h.Members.Members(c, c.U.From.ID, tm.Team.ID)
	if err != nil {
		return err
	}
	var lines []string
	var rows [][]kb.Btn
	for i, m := range ms {
		line := render.Num(i+1) + ". " + m.DisplayName
		if m.IsOwner() {
			line += " 👑"
		}
		if u, err := h.Store.GetUser(c, m.UserID); err == nil && u.Blocked {
			line += " 🚫"
		}
		lines = append(lines, line)
		if !m.IsOwner() {
			rows = append(rows, []kb.Btn{
				kb.B("❌ "+m.DisplayName, cb.Encode(cb.MemberAction, "rm", m.ID)),
				kb.B("👑 "+c.T("members.btn_transfer"), cb.Encode(cb.MemberAction, "tr", m.ID)),
			})
		}
	}
	text := h.V.Header(tm.Team, h.multi(c)) + c.T("members.title", "n", render.Num(len(ms))) + "\n" + strings.Join(lines, "\n")
	return c.Reply(text, kb.Inline(rows...))
}

func (h *H) onMemberAction(c *Ctx) error {
	d := c.Data()
	m, err := h.Store.GetMembership(c, d.Arg(2))
	if err != nil {
		return err
	}
	key := "members.confirm_remove"
	if d.Arg(1) == "tr" {
		key = "members.confirm_transfer"
	}
	return c.Reply(c.T(key, "name", m.DisplayName), kb.Inline([]kb.Btn{
		kb.B(c.T("common.yes"), cb.Encode(cb.MemberConfirm, d.Arg(1), m.ID)),
		kb.B(c.T("common.no"), cb.Encode(cb.Cancel)),
	}))
}

func (h *H) onMemberConfirm(c *Ctx) error {
	d := c.Data()
	if d.Arg(1) == "tr" {
		m, err := h.Members.RequestTransfer(c, c.U.From.ID, d.Arg(2))
		if err != nil {
			return err
		}
		return c.Edit(c.T("members.transfer_sent", "name", m.DisplayName), "")
	}
	m, err := h.Members.Remove(c, c.U.From.ID, d.Arg(2))
	if err != nil {
		return err
	}
	return c.Edit(c.T("members.removed", "name", m.DisplayName), "")
}

// ---- invite key

func (h *H) onInvite(c *Ctx) error {
	_ = c.Reset()
	tm, err := h.ownerTeam(c)
	if err != nil {
		return err
	}
	if err := h.sendInvite(c, tm.Team); err != nil {
		return err
	}
	return c.Reply(c.T("invite.rotate_hint"), kb.Inline([]kb.Btn{kb.B(c.T("invite.btn_rotate"), cb.Encode(cb.KeyRotate, tm.Team.ID))}))
}

func (h *H) onKeyRotate(c *Ctx) error {
	d := c.Data()
	if d.Arg(2) != "ok" {
		return c.Edit(c.T("invite.rotate_confirm"), kb.Inline([]kb.Btn{
			kb.B(c.T("common.yes"), cb.Encode(cb.KeyRotate, d.Arg(1), "ok")),
			kb.B(c.T("common.no"), cb.Encode(cb.Cancel)),
		}))
	}
	t, err := h.Members.RotateKey(c, c.U.From.ID, d.Arg(1))
	if err != nil {
		return err
	}
	_ = c.Edit(c.T("invite.rotated"), "")
	return h.sendInvite(c, t)
}

// ---- settings

func (h *H) onSettings(c *Ctx) error {
	_ = c.Reset()
	tm, err := h.ownerTeam(c)
	if err != nil {
		return err
	}
	return h.showSettings(c, tm.Team, false)
}

func onOff(c *Ctx, v bool) string {
	if v {
		return c.T("settings.on")
	}
	return c.T("settings.off")
}

func (h *H) showSettings(c *Ctx, t *team.Team, edit bool) error {
	id := t.ID
	b := func(label, item string) kb.Btn { return kb.B(label, cb.Encode(cb.Settings, item, id)) }
	rows := [][]kb.Btn{
		{b(c.T("settings.days"), "days"), b(c.T("settings.morning"), "morning")},
		{b(c.T("settings.deadline"), "deadline"), b(c.T("settings.checkins"), "checkins")},
		{b(c.T("settings.evening"), "evening"), b(c.T("settings.summary"), "summary")},
		{b(c.T("settings.weekly"), "weekly"), b(c.T("settings.offday"), "offday")},
		{b(c.T("settings.approval", "v", onOff(c, t.ManualApproval)), "approval")},
		{b(c.T("settings.leave_approval", "v", onOff(c, t.LeaveApproval)), "leaveapp")},
		{b(c.T("settings.show_missing", "v", onOff(c, t.ShowMissingInGroup)), "missing")},
		{b(c.T("settings.participate", "v", onOff(c, t.OwnerParticipates)), "part")},
		{b(c.T("settings.group"), "group")},
	}
	group := c.T("settings.no_group")
	if t.HasGroup() {
		group = c.T("settings.group_set")
		if t.GroupTitle != "" {
			group += " (" + t.GroupTitle + ")"
		}
	}
	text := h.V.Header(t, h.multi(c)) + c.T("settings.title", "team", t.Name) + "\n" + h.V.ScheduleText(t.Schedule) + "\n" + group
	if edit {
		return c.Edit(text, kb.Inline(rows...))
	}
	return c.Reply(text, kb.Inline(rows...))
}

func (h *H) onSettingsCB(c *Ctx) error {
	d := c.Data()
	item, teamID := d.Arg(1), d.Arg(2)
	tm, err := h.Members.OwnerContext(c, c.U.From.ID, teamID)
	if err != nil {
		return err
	}
	t := tm.Team
	flags := map[string]struct {
		flag string
		cur  bool
	}{
		"approval": {settings.FlagManualApproval, t.ManualApproval},
		"leaveapp": {settings.FlagLeaveApproval, t.LeaveApproval},
		"missing":  {settings.FlagShowMissing, t.ShowMissingInGroup},
		"part":     {settings.FlagParticipate, t.OwnerParticipates},
	}
	if f, ok := flags[item]; ok {
		t, err := h.Settings.SetFlag(c, c.U.From.ID, teamID, f.flag, !f.cur)
		if err != nil {
			return err
		}
		c.Answer(c.T("settings.saved"), false)
		if item == "part" {
			_ = h.mainMenu(c, c.T("settings.menu_updated"))
		}
		return h.showSettings(c, t, true)
	}
	switch item {
	case "back":
		return h.showSettings(c, t, true)
	case "group":
		return c.Reply(c.T("group.ask"), kb.Inline([]kb.Btn{kb.U(c.T("group.btn_add"), h.V.GroupLink(t.ID))}))
	case "offday":
		return h.showOffDays(c, t, false)
	case "off":
		date := expand(d.Arg(3))
		t, off, err := h.Settings.ToggleOffDay(c, c.U.From.ID, teamID, date)
		if err != nil {
			return err
		}
		key := "settings.offday_removed"
		if off {
			key = "settings.offday_added"
		}
		c.Answer(c.T(key, "date", render.DayMonth(date)), false)
		return h.showOffDays(c, t, true)
	}
	// schedule fields use the shared pickers in "settings" flow
	_ = c.Reset()
	c.Sess.Set("flow", flowSettings)
	c.Sess.Set("team", teamID)
	h.setDraft(c, t.Schedule)
	c.Answer("", false)
	if item == "days" {
		return h.askDays(c)
	}
	return h.askTime(c, item, "")
}

func (h *H) saveScheduleSetting(c *Ctx) error {
	t, err := h.Settings.UpdateSchedule(c, c.U.From.ID, c.Sess.Get("team"), h.draft(c))
	if err != nil {
		return err
	}
	_ = c.Reset()
	if err := h.mainMenu(c, c.T("settings.saved")); err != nil {
		return err
	}
	return h.showSettings(c, t, false)
}

func (h *H) showOffDays(c *Ctx, t *team.Team, edit bool) error {
	today := t.LocalDate(h.Clock.Now())
	var btns []kb.Btn
	for i := 0; i <= h.Cfg.Limits.LeaveDaysAhead; i++ {
		date := team.AddDays(today, i)
		if !slices.Contains(t.Schedule.WorkDays, int(team.Weekday(date))) {
			continue
		}
		label := render.Date(date)
		if slices.Contains(t.OffDays, date) {
			label = "🚫 " + label
		}
		btns = append(btns, kb.B(label, cb.Encode(cb.Settings, "off", t.ID, compact(date))))
	}
	rows := kb.Grid(btns, 2)
	rows = append(rows, []kb.Btn{kb.B(c.T("common.back"), cb.Encode(cb.Settings, "back", t.ID))})
	if edit {
		return c.Edit(c.T("settings.offday_title"), kb.Inline(rows...))
	}
	return c.Reply(c.T("settings.offday_title"), kb.Inline(rows...))
}

// ---- owner notes on tasks

func (h *H) onOwnerNoteCB(c *Ctx) error {
	d := c.Data()
	switch d.Arg(1) {
	case "m": // choose member: on|m|teamID|date
		tm, err := h.Members.OwnerContext(c, c.U.From.ID, d.Arg(2))
		if err != nil {
			return err
		}
		plans, err := h.Store.TeamPlans(c, tm.Team.ID, d.Arg(3))
		if err != nil {
			return err
		}
		var rows [][]kb.Btn
		for _, p := range plans {
			if !p.HasTasks() {
				continue
			}
			m, err := h.Store.GetMembership(c, p.MembershipID)
			if err != nil {
				continue
			}
			rows = append(rows, []kb.Btn{kb.B(m.DisplayName, cb.Encode(cb.OwnerNote, "p", p.ID))})
		}
		if len(rows) == 0 {
			c.Answer(c.T("owner.note_no_plans"), true)
			return nil
		}
		return c.Reply(c.T("owner.note_pick_member"), kb.Inline(rows...))
	case "p": // choose task: on|p|planID
		p, _, err := h.Cycle.PlanForOwner(c, c.U.From.ID, d.Arg(2))
		if err != nil {
			return err
		}
		var btns []kb.Btn
		for i, tk := range p.Tasks {
			btns = append(btns, kb.B(render.Num(i+1)+". "+render.Truncate(tk.Text, 24), cb.Encode(cb.OwnerNote, "t", p.ID, fmt.Sprint(tk.ID))))
		}
		return c.Edit(c.T("owner.note_pick_task")+"\n"+h.V.TaskList(p.Tasks, true), kb.Inline(kb.Grid(btns, 1)...))
	case "t":
		c.Sess.Set("plan", d.Arg(2))
		c.Sess.Set("task", d.Arg(3))
		if err := c.Enter(stOwnerNote); err != nil {
			return err
		}
		return c.Reply(c.T("owner.note_ask"), h.cancelKB(c))
	}
	return nil
}

func (h *H) onOwnerNoteText(c *Ctx) error {
	note := render.Truncate(c.Text(), h.Cfg.Limits.NoteMaxLen)
	if err := h.Cycle.OwnerNote(c, c.U.From.ID, c.Sess.Get("plan"), c.Sess.Int("task"), note); err != nil {
		return err
	}
	_ = c.Reset()
	return h.mainMenu(c, c.T("owner.note_sent"))
}

// ---- weekly report on demand

func (h *H) onWeekly(c *Ctx) error {
	_ = c.Reset()
	tm, ok, err := h.active(c)
	if err != nil {
		return err
	}
	if !ok {
		return h.mainMenu(c, c.T("start.no_team"))
	}
	return h.Reports.OnDemand(c, tm.Team, tm.Member)
}
