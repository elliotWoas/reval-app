package handlers

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"taskbot/internal/domain"
	"taskbot/internal/domain/team"
	"taskbot/internal/render"
	cb "taskbot/internal/transport/telegram/callbackdata"
	kb "taskbot/internal/transport/telegram/keyboards"
	"taskbot/internal/transport/telegram/views"
	"taskbot/internal/usecase/onboarding"
)

// Conversation states of team creation and schedule pickers.
const (
	stCreateName = "ct_name"
	stCreateDefs = "ct_defaults"
	stDays       = "days"
	stDaysManual = "days_manual"
	stTime       = "time"
	stTimeCustom = "time_custom"
	stCheckins   = "checkins"
	flowCreate   = "create"
	flowSettings = "settings"
)

// fields in the order of the create flow
var createOrder = []string{"morning", "deadline", "checkins", "evening", "summary"}

func (h *H) registerOnboarding() {
	r, w := h.r, h.wrap
	r.State(stCreateName, w(h.onCreateName))
	r.State(stCreateDefs, w(h.onCreateDefaults))
	r.State(stDays, w(h.onDaysPick))
	r.State(stTime, w(h.onTimePick))
	r.State(stTimeCustom, w(h.onTimeCustom))
	r.State(stCheckins, w(h.onCheckinsPick))
	r.State(stRename, w(h.onRename))
	r.Callback(cb.CreateName, w(h.onCreateNameCB))
	r.Callback(cb.CreatePart, w(h.onCreatePart))
	r.Callback(cb.DayToggle, w(h.onDayToggle))
	r.Callback(cb.InviteForward, w(h.onInviteForward))
	r.Callback(cb.GroupSkip, w(h.onGroupSkip))
}

func (h *H) onCreateStart(c *Ctx) error {
	if err := c.Reset(); err != nil {
		return err
	}
	if c.U.Kind == "callback" {
		c.Answer("", false)
	}
	if err := c.Enter(stCreateName); err != nil {
		return err
	}
	return c.Reply(c.T("create.ask_name", "min", render.Num(h.Cfg.Limits.TeamNameMin), "max", render.Num(h.Cfg.Limits.TeamNameMax)), h.cancelKB(c))
}

func (h *H) onCreateName(c *Ctx) error {
	name, err := h.Onboarding.ValidateName(c.Text())
	if err != nil {
		return err
	}
	c.Sess.Set("name", name)
	if err := c.SaveSess(); err != nil {
		return err
	}
	return c.Reply(c.T("create.name_preview", "name", name), kb.Inline([]kb.Btn{
		kb.B(c.T("common.confirm"), cb.Encode(cb.CreateName, "ok")),
		kb.B(c.T("common.change"), cb.Encode(cb.CreateName, "change")),
	}))
}

func (h *H) onCreateNameCB(c *Ctx) error {
	if c.Sess.State != stCreateName || c.Sess.Get("name") == "" {
		return h.expired(c)
	}
	if c.Data().Arg(1) == "change" {
		_ = c.Edit(c.T("create.ask_name_again"), "")
		return nil
	}
	_ = c.Edit(c.T("create.name_preview", "name", c.Sess.Get("name"))+" ✅", "")
	return c.Reply(c.T("create.ask_participate"), kb.Inline(
		[]kb.Btn{kb.B(c.T("create.part_yes"), cb.Encode(cb.CreatePart, "1"))},
		[]kb.Btn{kb.B(c.T("create.part_no"), cb.Encode(cb.CreatePart, "0"))},
	))
}

// expired tells the user that the button belongs to a finished conversation.
func (h *H) expired(c *Ctx) error {
	c.Answer(c.T("common.expired"), true)
	return nil
}

func (h *H) onCreatePart(c *Ctx) error {
	if c.Sess.State != stCreateName || c.Sess.Get("name") == "" {
		return h.expired(c)
	}
	c.Sess.Set("part", c.Data().Arg(1))
	h.setDraft(c, onboarding.DefaultSchedule(h.Cfg))
	c.Sess.Set("flow", flowCreate)
	if err := c.Enter(stCreateDefs); err != nil {
		return err
	}
	_ = c.Edit(c.T("create.part_saved"), "")
	text := c.T("create.defaults", "schedule", h.V.ScheduleText(h.draft(c)))
	return c.Reply(text, kb.Reply([][]string{{c.T("create.defaults_ok")}, {c.T("create.defaults_custom")}, {c.T("common.cancel")}}, false, ""))
}

func (h *H) onCreateDefaults(c *Ctx) error {
	switch c.Text() {
	case c.T("create.defaults_ok"):
		return h.finishCreate(c)
	case c.T("create.defaults_custom"):
		return h.askDays(c)
	}
	return c.Reply(c.T("common.use_buttons"), "")
}

// ---- schedule draft in session

func (h *H) draft(c *Ctx) team.Schedule {
	var s team.Schedule
	_ = json.Unmarshal([]byte(c.Sess.Get("sched")), &s)
	return s
}

func (h *H) setDraft(c *Ctx, s team.Schedule) {
	raw, _ := json.Marshal(s)
	c.Sess.Set("sched", string(raw))
}

// ---- work days

func (h *H) askDays(c *Ctx) error {
	var labels []string
	for _, p := range h.Cfg.Options.WorkDayPresets {
		labels = append(labels, c.T("days."+p.Key))
	}
	labels = append(labels, c.T("days.manual"))
	rows := kb.ReplyGrid(labels, 2)
	rows = append(rows, []string{c.T("common.cancel")})
	if err := c.Enter(stDays); err != nil {
		return err
	}
	return c.Reply(c.T("days.ask"), kb.Reply(rows, false, ""))
}

func (h *H) onDaysPick(c *Ctx) error {
	text := c.Text()
	for _, p := range h.Cfg.Options.WorkDayPresets {
		if text == c.T("days."+p.Key) {
			s := h.draft(c)
			s.WorkDays = append([]int(nil), p.Days...)
			h.setDraft(c, s)
			return h.afterField(c, "days")
		}
	}
	if text == c.T("days.manual") {
		if err := c.Enter(stDaysManual); err != nil {
			return err
		}
		_ = c.Reply(c.T("days.manual_hint"), kb.Remove())
		return c.Reply(c.T("days.manual_title"), h.daysKB(c, h.draft(c).WorkDays))
	}
	return c.Reply(c.T("common.use_buttons"), "")
}

func (h *H) daysKB(c *Ctx, days []int) string {
	var btns []kb.Btn
	for _, d := range []int{6, 0, 1, 2, 3, 4, 5} {
		label := render.WeekdayName(d)
		if slices.Contains(days, d) {
			label = "✅ " + label
		}
		btns = append(btns, kb.B(label, cb.Encode(cb.DayToggle, fmt.Sprint(d))))
	}
	rows := kb.Grid(btns, 3)
	rows = append(rows, []kb.Btn{kb.B(c.T("common.confirm"), cb.Encode(cb.DayToggle, "ok")), kb.B(c.T("common.cancel"), cb.Encode(cb.Cancel))})
	return kb.Inline(rows...)
}

func (h *H) onDayToggle(c *Ctx) error {
	if c.Sess.State != stDaysManual {
		return h.expired(c)
	}
	s := h.draft(c)
	arg := c.Data().Arg(1)
	if arg == "ok" {
		if len(s.WorkDays) == 0 {
			c.Answer(c.T("errors.no_work_days"), true)
			return nil
		}
		_ = c.Edit(c.T("days.saved", "days", h.daysText(s.WorkDays)), "")
		return h.afterField(c, "days")
	}
	d := c.Data().Int(1)
	if i := slices.Index(s.WorkDays, d); i >= 0 {
		s.WorkDays = slices.Delete(s.WorkDays, i, i+1)
	} else {
		s.WorkDays = append(s.WorkDays, d)
	}
	h.setDraft(c, s)
	if err := c.SaveSess(); err != nil {
		return err
	}
	return c.Edit(c.T("days.manual_title"), h.daysKB(c, s.WorkDays))
}

func (h *H) daysText(days []int) string {
	var names []string
	for _, d := range views.OrderWeek(days) {
		names = append(names, render.WeekdayName(d))
	}
	return strings.Join(names, "، ")
}

// ---- times

func (h *H) options(field string) []string {
	o := h.Cfg.Options
	switch field {
	case "morning":
		return o.Morning
	case "deadline":
		return o.Deadline
	case "evening":
		return o.Evening
	case "summary":
		return o.Summary
	case "weekly":
		return o.Weekly
	}
	return nil
}

func setField(s *team.Schedule, field string, v team.TimeOfDay) {
	switch field {
	case "morning":
		s.Morning = v
	case "deadline":
		s.Deadline = v
	case "evening":
		s.Evening = v
	case "summary":
		s.Summary = v
		if s.Weekly < v {
			s.Weekly = v + 15
		}
	case "weekly":
		s.Weekly = v
	}
}

// valid reports whether setting field=v keeps the schedule consistent.
func (h *H) valid(s team.Schedule, field string, v team.TimeOfDay) error {
	setField(&s, field, v)
	return s.Validate(h.Cfg.Limits.MinGapMinutes)
}

func (h *H) askTime(c *Ctx, field, note string) error {
	if field == "checkins" {
		return h.askCheckins(c)
	}
	s := h.draft(c)
	var labels []string
	for _, o := range h.options(field) {
		v := team.MustTime(o)
		if h.valid(s, field, v) == nil {
			labels = append(labels, render.Time(v))
		}
	}
	rows := kb.ReplyGrid(labels, 3)
	rows = append(rows, []string{c.T("time.custom")}, []string{c.T("common.cancel")})
	c.Sess.Set("field", field)
	if err := c.Enter(stTime); err != nil {
		return err
	}
	text := c.T("time.ask_" + field)
	if note != "" {
		text = note + "\n" + text
	}
	return c.Reply(text, kb.Reply(rows, false, ""))
}

func (h *H) onTimePick(c *Ctx) error {
	if c.Text() == c.T("time.custom") {
		if err := c.Enter(stTimeCustom); err != nil {
			return err
		}
		return c.Reply(c.T("time.custom_ask"), h.cancelKB(c))
	}
	return h.applyTime(c, c.Text())
}

func (h *H) onTimeCustom(c *Ctx) error { return h.applyTime(c, c.Text()) }

func (h *H) applyTime(c *Ctx, input string) error {
	field := c.Sess.Get("field")
	v, err := team.ParseTimeOfDay(input)
	if err != nil {
		return c.Reply(c.T("errors.time_format"), "")
	}
	s := h.draft(c)
	if err := h.valid(s, field, v); err != nil {
		return h.askTime(c, field, c.T("errors."+domain.Code(err)))
	}
	setField(&s, field, v)
	h.setDraft(c, s)
	return h.afterField(c, field)
}

func (h *H) askCheckins(c *Ctx) error {
	var labels []string
	for _, p := range h.Cfg.Options.Checkins {
		labels = append(labels, c.T("checkins."+p.Key))
	}
	rows := kb.ReplyGrid(labels, 2)
	rows = append(rows, []string{c.T("common.cancel")})
	c.Sess.Set("field", "checkins")
	if err := c.Enter(stCheckins); err != nil {
		return err
	}
	return c.Reply(c.T("time.ask_checkins"), kb.Reply(rows, false, ""))
}

func (h *H) onCheckinsPick(c *Ctx) error {
	s := h.draft(c)
	for _, p := range h.Cfg.Options.Checkins {
		if c.Text() != c.T("checkins."+p.Key) {
			continue
		}
		if p.EveryHours > 0 {
			s.Checkins = team.EveryHours(s.Deadline, s.Evening, p.EveryHours)
		} else {
			s.Checkins = nil
			for _, t := range p.Times {
				s.Checkins = append(s.Checkins, team.MustTime(t))
			}
		}
		h.setDraft(c, s)
		return h.afterField(c, "checkins")
	}
	return c.Reply(c.T("common.use_buttons"), "")
}

// afterField continues the create flow, or saves in the settings flow.
func (h *H) afterField(c *Ctx, field string) error {
	if c.Sess.Get("flow") == flowSettings {
		return h.saveScheduleSetting(c)
	}
	next := ""
	if field == "days" {
		next = createOrder[0]
	} else if i := slices.Index(createOrder, field); i >= 0 && i+1 < len(createOrder) {
		next = createOrder[i+1]
	}
	if next == "" {
		return h.finishCreate(c)
	}
	if err := c.SaveSess(); err != nil {
		return err
	}
	return h.askTime(c, next, "")
}

func (h *H) finishCreate(c *Ctx) error {
	in := onboarding.Input{
		OwnerID:      c.U.From.ID,
		OwnerName:    displayName(c),
		Name:         c.Sess.Get("name"),
		Participates: c.Sess.Get("part") == "1",
		Schedule:     h.draft(c),
	}
	t, _, err := h.Onboarding.CreateTeam(c, in)
	if err != nil {
		return err
	}
	_ = c.Reset()
	c.User.ActiveTeam = t.ID
	if err := c.Reply(c.T("create.done", "team", t.Name)+"\n"+h.V.ScheduleText(t.Schedule), kb.Remove()); err != nil {
		return err
	}
	if err := h.sendInvite(c, t); err != nil {
		return err
	}
	if err := c.Reply(c.T("group.ask"), kb.Inline(
		[]kb.Btn{kb.U(c.T("group.btn_add"), h.V.GroupLink(t.ID))},
		[]kb.Btn{kb.B(c.T("group.btn_skip"), cb.Encode(cb.GroupSkip))},
	)); err != nil {
		return err
	}
	return h.mainMenu(c, c.T("create.menu_ready"))
}

func displayName(c *Ctx) string {
	n := strings.TrimSpace(c.U.From.FirstName + " " + c.U.From.LastName)
	if n == "" {
		n = c.U.From.Username
	}
	if n == "" {
		n = fmt.Sprint(c.U.From.ID)
	}
	return render.Truncate(n, 32)
}

// sendInvite shows the key, the one-tap link and the forwardable message button.
func (h *H) sendInvite(c *Ctx, t *team.Team) error {
	text := c.T("invite.info", "key", t.InviteKey, "link", h.V.InviteLink(t.InviteKey))
	return c.Reply(text, kb.Inline(
		[]kb.Btn{kb.B(c.T("invite.btn_forward"), cb.Encode(cb.InviteForward, t.ID))},
	))
}

// onInviteForward sends a ready-made invitation the owner can forward to members.
func (h *H) onInviteForward(c *Ctx) error {
	tm, err := h.Members.OwnerContext(c, c.U.From.ID, c.Data().Arg(1))
	if err != nil {
		return err
	}
	c.Answer(c.T("invite.forward_hint"), false)
	t := tm.Team
	return c.Reply(c.T("invite.message", "team", t.Name, "key", t.InviteKey, "link", h.V.InviteLink(t.InviteKey)),
		kb.Inline([]kb.Btn{kb.U(c.T("invite.btn_join"), h.V.InviteLink(t.InviteKey))}))
}

func (h *H) onGroupSkip(c *Ctx) error {
	return c.Edit(c.T("group.skipped"), "")
}
