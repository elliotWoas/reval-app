package handlers

import (
	"encoding/json"
	"errors"

	"taskbot/internal/domain"
	"taskbot/internal/domain/plan"
	"taskbot/internal/domain/task"
	"taskbot/internal/domain/team"
	"taskbot/internal/render"
	cb "taskbot/internal/transport/telegram/callbackdata"
	kb "taskbot/internal/transport/telegram/keyboards"
	"taskbot/internal/usecase/dayview"
	"taskbot/internal/usecase/membership"
)

const (
	stPlanPreview = "pl_preview" // text appends to the draft
	stPlanEdit    = "pl_edit"    // text replaces the draft
	stEveningNote = "ev_note"
)

func (h *H) registerPlan() {
	r, w := h.r, h.wrap
	r.State(stPlanPreview, w(h.onPlanText))
	r.State(stPlanEdit, w(h.onPlanText))
	r.State(stEveningNote, w(h.onEveningNote))
	r.Callback(cb.PlanPreview, w(h.onPlanPreviewCB))
	r.Callback(cb.Carry, w(h.onCarry))
	r.Callback(cb.PlanView, w(h.onPlanViewCB))
	r.Callback(cb.Checkin, w(h.onCheckinCB))
	r.Callback(cb.Evening, w(h.onEveningCB))
}

// onFreeText treats any other private text as today's plan ("one task per line").
func (h *H) onFreeText(c *Ctx) error {
	if c.U.HasVoice {
		return c.Reply(c.T("plan.voice_unsupported"), "")
	}
	text := c.Text()
	if text == "" {
		return h.mainMenu(c, "")
	}
	tms, err := h.teams(c)
	if err != nil {
		return err
	}
	if len(tms) == 0 {
		return h.mainMenu(c, c.T("start.no_team"))
	}
	c.Sess.Set("pending_text", text)
	return h.withTeam(c, "plan", func(tm membership.TeamMember) error {
		return h.previewDraft(c, tm, text, "append")
	})
}

func (h *H) onPlanText(c *Ctx) error {
	tm, err := h.teamCtx(c, c.Sess.Get("team"))
	if err != nil {
		return err
	}
	mode := c.Sess.Get("mode")
	if c.Sess.State == stPlanEdit {
		c.Sess.Set("draft", "")
	}
	return h.previewDraft(c, tm, c.Text(), mode)
}

func (h *H) getDraft(c *Ctx) []task.Task {
	var ts []task.Task
	_ = json.Unmarshal([]byte(c.Sess.Get("draft")), &ts)
	return ts
}

func (h *H) setTasksDraft(c *Ctx, ts []task.Task) {
	raw, _ := json.Marshal(ts)
	c.Sess.Set("draft", string(raw))
}

// previewDraft parses text, merges it with the current draft of the same team and shows the preview.
func (h *H) previewDraft(c *Ctx, tm membership.TeamMember, text, mode string) error {
	if err := h.checkCanPlan(c, tm); err != nil {
		return err
	}
	ts, err := task.Parse(text, h.Cfg.Limits.MaxTasks, h.Cfg.Limits.TaskMaxLen)
	if err != nil {
		return err
	}
	var draft []task.Task
	if c.Sess.Get("team") == tm.Team.ID && (c.Sess.State == stPlanPreview || c.Sess.State == stPlanEdit) {
		draft = h.getDraft(c)
	}
	draft = append(draft, ts...)
	return h.showPreview(c, tm, draft, mode)
}

func (h *H) showPreview(c *Ctx, tm membership.TeamMember, draft []task.Task, mode string) error {
	if len(draft) > h.Cfg.Limits.MaxTasks {
		return domain.ErrTooManyTasks
	}
	if mode == "" {
		mode = "append"
	}
	c.Sess.Set("team", tm.Team.ID)
	c.Sess.Set("mode", mode)
	h.setTasksDraft(c, draft)
	if err := c.Enter(stPlanPreview); err != nil {
		return err
	}
	msg := h.V.PlanPreview(tm.Team, draft, h.multi(c))
	return c.Reply(msg.Text, msg.Markup)
}

// checkCanPlan gives a friendly answer before parsing when today accepts no plan.
func (h *H) checkCanPlan(c *Ctx, tm membership.TeamMember) error {
	st, err := h.Cycle.State(c, tm.Team, tm.Member)
	if err != nil {
		return err
	}
	if !tm.Member.Participates {
		return domain.E("not_participating")
	}
	if !st.WorkDay {
		return domain.ErrNotWorkDay
	}
	if st.OnLeave {
		return domain.ErrOnLeave
	}
	return nil
}

func (h *H) onPlanPreviewCB(c *Ctx) error {
	if c.Sess.State != stPlanPreview && c.Sess.State != stPlanEdit {
		return h.expired(c)
	}
	switch c.Data().Arg(1) {
	case "edit":
		c.Sess.Set("draft", "")
		if err := c.Enter(stPlanEdit); err != nil {
			return err
		}
		_ = c.Edit(c.T("plan.edit_ask"), "")
		return nil
	case "add":
		_ = c.Edit(c.T("plan.add_ask"), "")
		return nil
	}
	tm, err := h.teamCtx(c, c.Sess.Get("team"))
	if err != nil {
		return err
	}
	draft := h.getDraft(c)
	if len(draft) == 0 {
		return domain.ErrNoTasks
	}
	p, err := h.Cycle.SubmitTasks(c, tm.Team, tm.Member, draft, c.Sess.Get("mode") == "replace")
	if err != nil {
		return err
	}
	_ = c.Reset()
	text := c.T("plan.saved", "n", render.Num(len(p.Tasks)))
	if p.Status == plan.StatusLate {
		text += "\n" + c.T("plan.saved_late")
	}
	return c.Edit(h.V.Header(tm.Team, h.multi(c))+text+"\n"+h.V.TaskList(p.Tasks, false), "")
}

// onCarry adds yesterday's unfinished tasks to the draft.
func (h *H) onCarry(c *Ctx) error {
	tm, err := h.teamCtx(c, c.Data().Arg(1))
	if err != nil {
		return err
	}
	date := h.Cycle.Today(tm.Team)
	ts, err := h.Cycle.CarryTasks(c, tm.Member, date)
	if err != nil {
		return err
	}
	if len(ts) == 0 {
		c.Answer(c.T("plan.nothing_to_carry"), true)
		return nil
	}
	if err := h.checkCanPlan(c, tm); err != nil {
		return err
	}
	c.Answer(c.T("plan.carried", "n", render.Num(len(ts))), false)
	var draft []task.Task
	if c.Sess.Get("team") == tm.Team.ID && c.Sess.State == stPlanPreview {
		draft = h.getDraft(c)
	}
	return h.showPreview(c, tm, append(ts, draft...), "append")
}

func (h *H) onToday(c *Ctx) error {
	_ = c.Reset()
	return h.withTeam(c, "today", func(tm membership.TeamMember) error { return h.showToday(c, tm, false) })
}

func (h *H) showToday(c *Ctx, tm membership.TeamMember, edit bool) error {
	st, err := h.Cycle.State(c, tm.Team, tm.Member)
	if err != nil {
		return err
	}
	head := h.V.Header(tm.Team, h.multi(c))
	switch {
	case !st.WorkDay:
		return c.Reply(head+c.T("plan.today_off"), "")
	case st.OnLeave:
		return c.Reply(head+c.T("plan.today_leave"), "")
	case !st.Plan.HasTasks():
		c.Sess.Set("team", tm.Team.ID)
		c.Sess.Set("mode", "append")
		c.Sess.Set("draft", "")
		if err := c.Enter(stPlanPreview); err != nil {
			return err
		}
		carry, _ := h.Cycle.CarryTasks(c, tm.Member, st.Date)
		mk := ""
		if len(carry) > 0 {
			mk = kb.Inline([]kb.Btn{kb.B(c.T("cycle.btn_carry", "n", render.Num(len(carry))), cb.Encode(cb.Carry, tm.Team.ID))})
		}
		return c.Reply(head+c.T("plan.today_empty", "deadline", render.Time(tm.Team.Schedule.Deadline)), mk)
	}
	open := tm.Team.PlanWindowOpen(st.Date, h.Clock.Now())
	msg := h.V.TodayPlan(tm.Team, st.Plan, open, h.multi(c))
	if edit {
		return c.Edit(msg.Text, msg.Markup)
	}
	return c.Reply(msg.Text, msg.Markup)
}

func (h *H) onPlanViewCB(c *Ctx) error {
	d := c.Data()
	switch d.Arg(1) {
	case "done":
		p, err := h.Cycle.SetFinal(c, c.U.From.ID, d.Arg(2), d.Int(3), task.Done, "")
		if err != nil {
			return err
		}
		c.Answer(c.T("plan.marked_done"), false)
		tm, err := h.teamCtx(c, p.TeamID)
		if err != nil {
			return err
		}
		return h.showToday(c, tm, true)
	case "add", "edit":
		tm, err := h.teamCtx(c, d.Arg(2))
		if err != nil {
			return err
		}
		c.Sess.Set("team", tm.Team.ID)
		c.Sess.Set("draft", "")
		state, mode, ask := stPlanPreview, "append", "plan.add_ask"
		if d.Arg(1) == "edit" {
			state, mode, ask = stPlanEdit, "replace", "plan.edit_ask"
		}
		c.Sess.Set("mode", mode)
		if err := c.Enter(state); err != nil {
			return err
		}
		return c.Reply(c.T(ask), h.cancelKB(c))
	}
	return nil
}

// ---- check-in

func (h *H) ownedPlan(c *Ctx, planID string) (*plan.Plan, *team.Team, error) {
	p, t, _, err := dayview.Owned(c, h.Store, c.U.From.ID, planID)
	return p, t, err
}

func (h *H) onCheckinCB(c *Ctx) error {
	d := c.Data()
	planID := d.Arg(2)
	switch d.Arg(1) {
	case "ok":
		c.Answer(c.T("checkin.ok_saved"), false)
		return c.Edit(c.T("checkin.ok_saved"), "")
	case "fin":
		c.Answer(c.T("checkin.saved"), false)
		p, t, err := h.ownedPlan(c, planID)
		if err != nil {
			return err
		}
		return c.Edit(h.V.Header(t, h.multi(c))+c.T("checkin.saved")+"\n"+h.V.TaskList(p.Tasks, true), "")
	case "stuck":
		if err := h.Checkin.Stuck(c, c.U.From.ID, planID, 0); err != nil {
			return err
		}
		c.Answer(c.T("checkin.stuck_sent"), true)
		return nil
	case "p":
		if _, err := h.Checkin.SetProgress(c, c.U.From.ID, planID, d.Int(3), d.Int(4)); err != nil {
			return err
		}
		c.Answer("✔️", false)
	}
	p, t, err := h.ownedPlan(c, planID)
	if err != nil {
		return err
	}
	msg := h.V.CheckinEach(t, p, h.multi(c))
	return c.Edit(msg.Text, msg.Markup)
}

// ---- evening report

func (h *H) onEveningCB(c *Ctx) error {
	d := c.Data()
	planID := d.Arg(2)
	switch d.Arg(1) {
	case "open":
		p, t, err := h.ownedPlan(c, planID)
		if err != nil {
			return err
		}
		msg := h.V.Evening(t, p, h.multi(c))
		return c.Reply(msg.Text, msg.Markup)
	case "f":
		final := d.Arg(4)
		if final == task.NotDone {
			p, _, err := h.ownedPlan(c, planID)
			if err != nil {
				return err
			}
			msg := h.V.EveningReasons(p, d.Int(3))
			return c.Edit(msg.Text, msg.Markup)
		}
		if _, err := h.Cycle.SetFinal(c, c.U.From.ID, planID, d.Int(3), final, ""); err != nil {
			return err
		}
	case "r":
		if _, err := h.Cycle.SetFinal(c, c.U.From.ID, planID, d.Int(3), task.NotDone, d.Arg(4)); err != nil {
			return err
		}
	case "send":
		p, err := h.Cycle.SubmitReport(c, c.U.From.ID, planID, "", nil)
		if err != nil {
			return err
		}
		t, err := h.Store.GetTeam(c, p.TeamID)
		if err != nil {
			return err
		}
		key := "evening.sent"
		if p.ReportStatus == plan.ReportLate {
			key = "evening.sent_late"
		}
		c.Answer(c.T(key), false)
		_ = c.Edit(h.V.Header(t, h.multi(c))+c.T(key)+"\n"+h.V.TaskList(p.Tasks, true), "")
		c.Sess.Set("plan", p.ID)
		if err := c.Enter(stEveningNote); err != nil {
			return err
		}
		return c.Reply(c.T("evening.note_ask"), kb.Reply([][]string{{c.T("evening.no_note")}}, false, ""))
	}
	p, t, err := h.ownedPlan(c, planID)
	if err != nil {
		return err
	}
	msg := h.V.Evening(t, p, h.multi(c))
	return c.Edit(msg.Text, msg.Markup)
}

// onEveningNote stores an optional note, photo or file; files are copied to the owner as proof.
func (h *H) onEveningNote(c *Ctx) error {
	if c.Text() == c.T("evening.no_note") {
		_ = c.Reset()
		return h.mainMenu(c, c.T("evening.thanks"))
	}
	planID := c.Sess.Get("plan")
	note := render.Truncate(c.Text(), h.Cfg.Limits.NoteMaxLen)
	p, err := h.Cycle.SubmitReport(c, c.U.From.ID, planID, note, c.U.FileIDs)
	if err != nil {
		if errors.Is(err, domain.ErrWindowClosed) {
			_ = c.Reset()
		}
		return err
	}
	if len(c.U.FileIDs) > 0 {
		if t, err := h.Store.GetTeam(c, p.TeamID); err == nil && t.OwnerID != c.U.From.ID {
			if m, err := h.Store.GetMembership(c, p.MembershipID); err == nil {
				_ = c.Send(t.OwnerID, c.T("evening.proof_from", "name", m.DisplayName), "")
				_ = h.Client.Copy(c, t.OwnerID, c.U.ChatID, c.U.MessageID)
			}
		}
		return c.Reply(c.T("evening.file_saved"), "")
	}
	_ = c.Reset()
	return h.mainMenu(c, c.T("evening.note_saved"))
}
