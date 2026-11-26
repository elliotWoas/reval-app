package handlers

import (
	"slices"
	"strings"

	"taskbot/internal/domain"
	"taskbot/internal/domain/leave"
	"taskbot/internal/domain/team"
	"taskbot/internal/render"
	cb "taskbot/internal/transport/telegram/callbackdata"
	kb "taskbot/internal/transport/telegram/keyboards"
	"taskbot/internal/usecase/membership"
)

const (
	stLeavePick = "lv_pick"
	stLeaveType = "lv_type"
	stLeaveNote = "lv_note"
	stLeaveConf = "lv_confirm"
	leavePage   = 8
)

func (h *H) registerLeave() {
	r, w := h.r, h.wrap
	r.State(stLeaveNote, w(h.onLeaveNote))
	r.Callback(cb.LeaveStart, w(h.onLeaveToday))
	r.Callback(cb.LeavePick, w(h.onLeavePick))
	r.Callback(cb.LeaveType, w(h.onLeaveType))
	r.Callback(cb.LeaveConfirm, w(h.onLeaveConfirm))
	r.Callback(cb.LeaveCancel, w(h.onLeaveCancel))
	r.Callback(cb.LeaveDecide, w(h.onLeaveDecide))
	r.Callback(cb.LeaveTodayPlan, w(h.onLeaveTodayPlan))
}

func compact(date string) string { return strings.ReplaceAll(date, "-", "") }

func expand(c string) string {
	if len(c) != 8 {
		return c
	}
	return c[:4] + "-" + c[4:6] + "-" + c[6:]
}

func (h *H) onLeaveMenu(c *Ctx) error {
	_ = c.Reset()
	return h.withTeam(c, "leave", func(tm membership.TeamMember) error { return h.startLeave(c, tm, "") })
}

// onLeaveToday is the "I'm off today" button of the morning message.
func (h *H) onLeaveToday(c *Ctx) error {
	tm, err := h.teamCtx(c, c.Data().Arg(1))
	if err != nil {
		return err
	}
	_ = c.Reset()
	today := h.Cycle.Today(tm.Team)
	c.Sess.Set("team", tm.Team.ID)
	c.Sess.Set("dates", today)
	if err := c.Enter(stLeaveType); err != nil {
		return err
	}
	return c.Reply(c.T("leave.ask_type", "dates", render.Date(today)), h.leaveTypeKB(c))
}

func (h *H) startLeave(c *Ctx, tm membership.TeamMember, preselect string) error {
	c.Sess.Set("team", tm.Team.ID)
	c.Sess.Set("dates", preselect)
	c.Sess.Set("page", "0")
	if err := c.Enter(stLeavePick); err != nil {
		return err
	}
	return c.Reply(h.V.Header(tm.Team, h.multi(c))+c.T("leave.ask_days"), h.leaveDaysKB(c, tm.Team))
}

func (h *H) selectedDates(c *Ctx) []string {
	if c.Sess.Get("dates") == "" {
		return nil
	}
	return strings.Split(c.Sess.Get("dates"), ",")
}

func (h *H) leaveDaysKB(c *Ctx, t *team.Team) string {
	days := h.Leaves.Selectable(t)
	today := t.LocalDate(h.Clock.Now())
	page := c.Sess.Int("page")
	start := page * leavePage
	if start >= len(days) {
		start, page = 0, 0
	}
	end := min(start+leavePage, len(days))
	sel := h.selectedDates(c)
	var btns []kb.Btn
	for _, d := range days[start:end] {
		label := render.Date(d)
		switch d {
		case today:
			label = c.T("leave.today")
		case team.AddDays(today, 1):
			label = c.T("leave.tomorrow")
		}
		if slices.Contains(sel, d) {
			label = "✅ " + label
		}
		btns = append(btns, kb.B(label, cb.Encode(cb.LeavePick, "t", compact(d))))
	}
	rows := kb.Grid(btns, 2)
	var nav []kb.Btn
	if page > 0 {
		nav = append(nav, kb.B(c.T("leave.prev_week"), cb.Encode(cb.LeavePick, "p")))
	}
	if end < len(days) {
		nav = append(nav, kb.B(c.T("leave.next_week"), cb.Encode(cb.LeavePick, "n")))
	}
	rows = append(rows, nav, []kb.Btn{kb.B(c.T("leave.continue"), cb.Encode(cb.LeavePick, "ok")), kb.B(c.T("common.cancel"), cb.Encode(cb.Cancel))})
	return kb.Inline(rows...)
}

func (h *H) onLeavePick(c *Ctx) error {
	if c.Sess.State != stLeavePick {
		return h.expired(c)
	}
	tm, err := h.teamCtx(c, c.Sess.Get("team"))
	if err != nil {
		return err
	}
	d := c.Data()
	switch d.Arg(1) {
	case "t":
		date := expand(d.Arg(2))
		sel := h.selectedDates(c)
		if i := slices.Index(sel, date); i >= 0 {
			sel = slices.Delete(sel, i, i+1)
		} else {
			sel = append(sel, date)
		}
		slices.Sort(sel)
		c.Sess.Set("dates", strings.Join(sel, ","))
	case "n":
		c.Sess.Set("page", "1")
	case "p":
		c.Sess.Set("page", "0")
	case "ok":
		sel := h.selectedDates(c)
		if len(sel) == 0 {
			c.Answer(c.T("leave.pick_one"), true)
			return nil
		}
		if err := c.Enter(stLeaveType); err != nil {
			return err
		}
		return c.Edit(c.T("leave.ask_type", "dates", h.datesText(sel)), h.leaveTypeKB(c))
	}
	if err := c.SaveSess(); err != nil {
		return err
	}
	return c.Edit(h.V.Header(tm.Team, h.multi(c))+c.T("leave.ask_days"), h.leaveDaysKB(c, tm.Team))
}

func (h *H) datesText(dates []string) string {
	var out []string
	for _, d := range dates {
		out = append(out, render.Date(d))
	}
	return strings.Join(out, "، ")
}

func (h *H) leaveTypeKB(c *Ctx) string {
	return kb.Inline(
		[]kb.Btn{kb.B(c.T("leave.full"), cb.Encode(cb.LeaveType, "full")), kb.B(c.T("leave.half"), cb.Encode(cb.LeaveType, "half"))},
		[]kb.Btn{kb.B(c.T("common.cancel"), cb.Encode(cb.Cancel))},
	)
}

func (h *H) onLeaveType(c *Ctx) error {
	if c.Sess.State != stLeaveType {
		return h.expired(c)
	}
	half := c.Data().Arg(1) == "half"
	c.Sess.Set("half", map[bool]string{true: "1", false: "0"}[half])
	if err := c.Enter(stLeaveNote); err != nil {
		return err
	}
	kind := c.T("leave.full")
	if half {
		kind = c.T("leave.half")
	}
	_ = c.Edit(c.T("leave.type_saved", "kind", kind), "")
	return c.Reply(c.T("leave.ask_note"), kb.Reply([][]string{{c.T("leave.no_note")}, {c.T("common.cancel")}}, false, ""))
}

func (h *H) onLeaveNote(c *Ctx) error {
	note := c.Text()
	if note == c.T("leave.no_note") {
		note = ""
	}
	c.Sess.Set("note", render.Truncate(note, h.Cfg.Limits.NoteMaxLen))
	if err := c.Enter(stLeaveConf); err != nil {
		return err
	}
	kind := c.T("leave.full")
	if c.Sess.Get("half") == "1" {
		kind = c.T("leave.half")
	}
	text := c.T("leave.confirm", "dates", h.datesText(h.selectedDates(c)), "kind", kind)
	if note != "" {
		text += "\n💬 " + note
	}
	_ = c.Reply(c.T("leave.check"), kb.Remove())
	return c.Reply(text, kb.Inline([]kb.Btn{kb.B(c.T("leave.btn_submit"), cb.Encode(cb.LeaveConfirm)), kb.B(c.T("common.cancel"), cb.Encode(cb.Cancel))}))
}

func (h *H) onLeaveConfirm(c *Ctx) error {
	if c.Sess.State != stLeaveConf {
		return h.expired(c)
	}
	tm, err := h.teamCtx(c, c.Sess.Get("team"))
	if err != nil {
		return err
	}
	res, err := h.Leaves.Request(c, tm.Team, tm.Member, h.selectedDates(c), c.Sess.Get("half") == "1", c.Sess.Get("note"))
	if err != nil {
		return err
	}
	_ = c.Reset()
	key := "leave.saved"
	if tm.Team.LeaveApproval {
		key = "leave.saved_pending"
	}
	var dates []string
	for _, l := range res.Leaves {
		dates = append(dates, l.Date)
	}
	_ = c.Edit(c.T(key, "dates", h.datesText(dates)), "")
	if res.TodayPlanID != "" {
		return c.Reply(c.T("leave.today_tasks"), kb.Inline([]kb.Btn{
			kb.B(c.T("leave.tasks_move"), cb.Encode(cb.LeaveTodayPlan, res.TodayPlanID, "move")),
			kb.B(c.T("leave.tasks_drop"), cb.Encode(cb.LeaveTodayPlan, res.TodayPlanID, "drop")),
		}))
	}
	return h.mainMenu(c, "")
}

func (h *H) onLeaveTodayPlan(c *Ctx) error {
	d := c.Data()
	if d.Arg(2) == "drop" {
		if err := h.Cycle.DropOpenTasks(c, c.U.From.ID, d.Arg(1)); err != nil {
			return err
		}
		return c.Edit(c.T("leave.tasks_dropped"), "")
	}
	return c.Edit(c.T("leave.tasks_moved"), "")
}

// onMyLeaves lists upcoming leaves with cancel buttons.
func (h *H) onMyLeaves(c *Ctx) error {
	tm, ok, err := h.active(c)
	if err != nil {
		return err
	}
	if !ok {
		return h.mainMenu(c, c.T("start.no_team"))
	}
	ls, err := h.Leaves.Upcoming(c, tm.Team, tm.Member)
	if err != nil {
		return err
	}
	if len(ls) == 0 {
		return c.Reply(h.V.Header(tm.Team, h.multi(c))+c.T("leave.none"), "")
	}
	var lines []string
	var rows [][]kb.Btn
	for _, l := range ls {
		lines = append(lines, "• "+h.leaveLine(c, tm.Team, l))
		rows = append(rows, []kb.Btn{kb.B(c.T("leave.btn_cancel", "date", render.DayMonth(l.Date)), cb.Encode(cb.LeaveCancel, l.ID))})
	}
	return c.Reply(h.V.Header(tm.Team, h.multi(c))+c.T("leave.list_title")+"\n"+strings.Join(lines, "\n"), kb.Inline(rows...))
}

func (h *H) leaveLine(c *Ctx, t *team.Team, l *leave.Leave) string {
	s := render.Date(l.Date)
	if l.Half {
		s += " " + c.T("leave.half_short")
	}
	if t.LeaveApproval && l.Status == leave.StatusRegistered {
		s += " " + c.T("leave.pending_short")
	}
	return s
}

func (h *H) onLeaveCancel(c *Ctx) error {
	l, err := h.Leaves.Cancel(c, c.U.From.ID, c.Data().Arg(1))
	if err != nil {
		return err
	}
	c.Answer(c.T("leave.cancelled", "date", render.DayMonth(l.Date)), false)
	return c.Edit(c.T("leave.cancelled", "date", render.Date(l.Date)), "")
}

func (h *H) onLeaveDecide(c *Ctx) error {
	d := c.Data()
	l, err := h.Leaves.Decide(c, c.U.From.ID, d.Arg(1), d.Arg(2) == "1")
	if err != nil {
		return err
	}
	m, err := h.Store.GetMembership(c, l.MembershipID)
	if err != nil {
		return err
	}
	key := "leave.owner_rejected"
	if l.Status == leave.StatusApproved {
		key = "leave.owner_approved"
	}
	c.Answer(c.T(key, "name", m.DisplayName, "date", render.DayMonth(l.Date)), false)
	return c.Reply(c.T(key, "name", m.DisplayName, "date", render.Date(l.Date)), "")
}

var _ = domain.ErrNotFound
