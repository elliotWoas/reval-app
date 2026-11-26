// Package notify implements ports.Notifier: it renders messages and queues them in the outbox.
package notify

import (
	"context"
	"fmt"
	"strings"

	"taskbot/internal/config"
	"taskbot/internal/domain/leave"
	"taskbot/internal/domain/member"
	"taskbot/internal/domain/plan"
	"taskbot/internal/domain/team"
	"taskbot/internal/outbox"
	"taskbot/internal/platform/clock"
	"taskbot/internal/render"
	cb "taskbot/internal/transport/telegram/callbackdata"
	kb "taskbot/internal/transport/telegram/keyboards"
	"taskbot/internal/transport/telegram/views"
	"taskbot/internal/usecase/ports"
)

// Report kinds stored for later edits.
const (
	ReportPlan    = "plan"
	ReportSummary = "summary"
)

// Notifier queues outgoing messages.
type Notifier struct {
	ob    outbox.Store
	st    ports.Store
	v     *views.V
	clock clock.Clock
	cfg   *config.Config
}

// New creates the notifier.
func New(ob outbox.Store, st ports.Store, v *views.V, c clock.Clock, cfg *config.Config) *Notifier {
	return &Notifier{ob: ob, st: st, v: v, clock: c, cfg: cfg}
}

func (n *Notifier) t(key string, kv ...any) string { return n.v.T.T(key, kv...) }

func (n *Notifier) item(chatID int64, group bool, text, markup, dedup string) outbox.Item {
	now := n.clock.Now()
	return outbox.Item{ChatID: chatID, IsGroup: group, Text: text, Markup: markup, Dedup: dedup, NotBefore: now, CreatedAt: now}
}

// send queues a message (split when too long; the keyboard goes on the last part).
func (n *Notifier) send(ctx context.Context, chatID int64, group bool, msg views.Msg, dedup string) error {
	parts := render.Split(msg.Text, n.cfg.Limits.MessageMaxLen)
	items := make([]outbox.Item, 0, len(parts))
	for i, p := range parts {
		mk, dd := "", ""
		if i == len(parts)-1 {
			mk = msg.Markup
		}
		if dedup != "" {
			dd = fmt.Sprintf("%s#%d", dedup, i)
		}
		items = append(items, n.item(chatID, group, p, mk, dd))
	}
	return n.ob.Enqueue(ctx, items...)
}

func (n *Notifier) multi(ctx context.Context, userID int64) bool {
	ms, err := n.st.UserMemberships(ctx, userID)
	return err == nil && len(ms) > 1
}

func key(parts ...any) string {
	s := make([]string, len(parts))
	for i, p := range parts {
		s[i] = fmt.Sprint(p)
	}
	return strings.Join(s, ":")
}

// report sends or edits a report message (team plan / summary) in one chat.
// dedup is the key of the first send; an edit that finds no stored message (the first send is
// still queued or failed) reuses it, so the report is never posted twice.
func (n *Notifier) report(ctx context.Context, t *team.Team, date, kind string, chatID int64, group bool, msg views.Msg, edit, pin bool, dedup string) error {
	parts := render.Split(msg.Text, n.cfg.Limits.MessageMaxLen)
	if edit {
		rm, err := n.st.FindReportMsg(ctx, t.ID, date, kind, chatID)
		if err != nil {
			return err
		}
		if rm != nil {
			it := n.item(chatID, group, parts[0], msg.Markup, "")
			it.EditMessageID = rm.MessageID
			return n.ob.Enqueue(ctx, it)
		}
	}
	items := make([]outbox.Item, 0, len(parts))
	for i, p := range parts {
		it := n.item(chatID, group, p, "", "")
		if dedup != "" {
			it.Dedup = fmt.Sprintf("%s#%d", dedup, i)
		}
		if i == 0 {
			it.Markup = msg.Markup
			it.ReportTeam, it.ReportDate, it.ReportKind = t.ID, date, kind
			it.Pin = pin
			it.TeamID = t.ID
		}
		items = append(items, it)
	}
	return n.ob.Enqueue(ctx, items...)
}

// ---- scheduled messages

func (n *Notifier) MorningRequest(ctx context.Context, t *team.Team, m *member.Membership, date string, carry int) error {
	return n.send(ctx, m.UserID, false, n.v.MorningRequest(t, m, date, carry, n.multi(ctx, m.UserID)), key("mr", t.ID, date, m.UserID))
}

func (n *Notifier) PlanReminder(ctx context.Context, t *team.Team, m *member.Membership, date string) error {
	text := n.v.Header(t, n.multi(ctx, m.UserID)) + n.t("cycle.plan_reminder", "minutes", render.Num(t.Schedule.ReminderBefore), "deadline", render.Time(t.Schedule.Deadline))
	return n.send(ctx, m.UserID, false, views.Msg{Text: text}, key("rp", t.ID, date, m.UserID))
}

func (n *Notifier) TeamPlan(ctx context.Context, v ports.DayView, edit bool) error {
	t := v.Team
	var first error
	if t.HasGroup() {
		first = n.report(ctx, t, v.Date, ReportPlan, t.GroupChatID, true, n.v.TeamPlan(v, false), edit, true, key("tpg", t.ID, v.Date))
	}
	if err := n.report(ctx, t, v.Date, ReportPlan, t.OwnerID, false, n.v.TeamPlan(v, true), edit, false, key("tpo", t.ID, v.Date)); err != nil && first == nil {
		first = err
	}
	return first
}

func (n *Notifier) LatePlan(ctx context.Context, t *team.Team, m *member.Membership, p *plan.Plan) error {
	if !t.HasGroup() {
		return nil
	}
	text := n.t("report.late_plan", "name", m.DisplayName, "n", render.Num(len(p.Tasks)))
	return n.send(ctx, t.GroupChatID, true, views.Msg{Text: text}, key("lp", t.ID, p.Date, m.ID))
}

func (n *Notifier) Checkin(ctx context.Context, t *team.Team, m *member.Membership, p *plan.Plan, idx int) error {
	return n.send(ctx, m.UserID, false, n.v.Checkin(t, p, n.multi(ctx, m.UserID)), key("ck", t.ID, p.Date, m.UserID, idx))
}

func (n *Notifier) EveningRequest(ctx context.Context, t *team.Team, m *member.Membership, p *plan.Plan) error {
	msg := n.v.Evening(t, p, n.multi(ctx, m.UserID))
	msg.Text = n.t("evening.intro") + "\n" + msg.Text
	return n.send(ctx, m.UserID, false, msg, key("ev", t.ID, p.Date, m.UserID))
}

func (n *Notifier) ReportReminder(ctx context.Context, t *team.Team, m *member.Membership, date string) error {
	text := n.v.Header(t, n.multi(ctx, m.UserID)) + n.t("evening.reminder", "minutes", render.Num(t.Schedule.ReminderBefore))
	mk := ""
	if p, err := n.st.FindPlan(ctx, m.ID, date); err == nil && p != nil {
		mk = kb.Inline([]kb.Btn{kb.B(n.t("evening.btn_open"), cb.Encode(cb.Evening, "open", p.ID))})
	}
	return n.send(ctx, m.UserID, false, views.Msg{Text: text, Markup: mk}, key("rr", t.ID, date, m.UserID))
}

func (n *Notifier) DaySummary(ctx context.Context, v ports.DayView, edit bool) error {
	t := v.Team
	var first error
	if t.HasGroup() {
		first = n.report(ctx, t, v.Date, ReportSummary, t.GroupChatID, true, n.v.Summary(v, false), edit, false, key("sg", t.ID, v.Date))
	}
	if err := n.report(ctx, t, v.Date, ReportSummary, t.OwnerID, false, n.v.Summary(v, true), edit, false, key("so", t.ID, v.Date)); err != nil && first == nil {
		first = err
	}
	return first
}

func (n *Notifier) WeeklyReport(ctx context.Context, w ports.WeekView, toGroup bool) error {
	t := w.Team
	var first error
	keep := func(err error) {
		if err != nil && first == nil {
			first = err
		}
	}
	if toGroup && t.HasGroup() {
		keep(n.send(ctx, t.GroupChatID, true, n.v.WeeklyGroup(w), key("wg", t.ID, w.WeekStart)))
	}
	keep(n.send(ctx, t.OwnerID, false, n.v.WeeklyOwner(w), key("wo", t.ID, w.WeekStart)))
	for _, mw := range w.Members {
		if mw.Member.UserID == t.OwnerID {
			continue
		}
		keep(n.send(ctx, mw.Member.UserID, false, n.v.WeeklyMember(w, mw), key("wm", t.ID, w.WeekStart, mw.Member.UserID)))
	}
	return first
}

func (n *Notifier) WeeklyReportTo(ctx context.Context, w ports.WeekView, chatID int64, owner bool, mw *ports.MemberWeek) error {
	if owner {
		return n.send(ctx, chatID, false, n.v.WeeklyOwner(w), "")
	}
	return n.send(ctx, chatID, false, n.v.WeeklyMember(w, *mw), "")
}

func (n *Notifier) LeaveDigest(ctx context.Context, t *team.Team, weekStart string, rows []ports.LeaveRow) error {
	var parts []string
	for _, r := range rows {
		s := r.Member.DisplayName + " " + render.Weekday(r.Leave.Date)
		if r.Leave.Half {
			s += " " + n.t("leave.half_short")
		}
		if r.Leave.Status == leave.StatusRegistered && t.LeaveApproval {
			s += " " + n.t("leave.pending_short")
		}
		parts = append(parts, s)
	}
	text := n.v.Header(t, n.multi(ctx, t.OwnerID)) + n.t("leave.digest", "list", strings.Join(parts, "، "))
	return n.send(ctx, t.OwnerID, false, views.Msg{Text: text}, key("ld", t.ID, weekStart))
}

// ---- event-driven messages

func (n *Notifier) StuckAlert(ctx context.Context, t *team.Team, m *member.Membership, p *plan.Plan, taskID int) error {
	text := n.v.Header(t, n.multi(ctx, t.OwnerID)) + n.t("checkin.stuck_owner", "name", m.DisplayName)
	if tk := p.Task(taskID); tk != nil {
		text += "\n" + tk.Text
	} else if len(p.OpenTasks()) > 0 {
		text += "\n" + n.v.TaskList(p.OpenTasks(), false)
	}
	return n.send(ctx, t.OwnerID, false, views.Msg{Text: text}, "")
}

func (n *Notifier) JoinRequest(ctx context.Context, t *team.Team, m *member.Membership) error {
	text := n.v.Header(t, n.multi(ctx, t.OwnerID)) + n.t("join.request_owner", "name", m.DisplayName, "team", t.Name)
	mk := kb.Inline([]kb.Btn{kb.B(n.t("common.approve"), cb.Encode(cb.JoinDecide, m.ID, "1")), kb.B(n.t("common.reject"), cb.Encode(cb.JoinDecide, m.ID, "0"))})
	return n.send(ctx, t.OwnerID, false, views.Msg{Text: text, Markup: mk}, "")
}

func (n *Notifier) JoinDecision(ctx context.Context, t *team.Team, m *member.Membership, approved bool) error {
	if !approved {
		return n.send(ctx, m.UserID, false, views.Msg{Text: n.t("join.rejected", "team", t.Name)}, "")
	}
	text := n.t("join.approved", "team", t.Name) + "\n" + n.v.RoutineText(t)
	return n.send(ctx, m.UserID, false, views.Msg{Text: text}, "")
}

func (n *Notifier) MemberJoined(ctx context.Context, t *team.Team, m *member.Membership) error {
	text := n.v.Header(t, n.multi(ctx, t.OwnerID)) + n.t("join.joined_owner", "name", m.DisplayName)
	return n.send(ctx, t.OwnerID, false, views.Msg{Text: text}, "")
}

func (n *Notifier) MemberLeft(ctx context.Context, t *team.Team, m *member.Membership) error {
	text := n.v.Header(t, n.multi(ctx, t.OwnerID)) + n.t("members.left_owner", "name", m.DisplayName)
	return n.send(ctx, t.OwnerID, false, views.Msg{Text: text}, "")
}

func (n *Notifier) MemberRemoved(ctx context.Context, t *team.Team, m *member.Membership) error {
	return n.send(ctx, m.UserID, false, views.Msg{Text: n.t("members.removed_member", "team", t.Name)}, "")
}

func (n *Notifier) LeaveRequested(ctx context.Context, t *team.Team, m *member.Membership, ls []*leave.Leave) error {
	var dates []string
	for _, l := range ls {
		dates = append(dates, render.Date(l.Date))
	}
	kind := n.t("leave.full")
	if len(ls) > 0 && ls[0].Half {
		kind = n.t("leave.half")
	}
	text := n.v.Header(t, n.multi(ctx, t.OwnerID)) + n.t("leave.owner_notice", "name", m.DisplayName, "dates", strings.Join(dates, "، "), "kind", kind)
	if len(ls) > 0 && ls[0].Note != "" {
		text += "\n💬 " + ls[0].Note
	}
	mk := ""
	if t.LeaveApproval {
		var rows [][]kb.Btn
		for _, l := range ls {
			label := render.DayMonth(l.Date)
			rows = append(rows, []kb.Btn{
				kb.B(n.t("common.approve")+" "+label, cb.Encode(cb.LeaveDecide, l.ID, "1")),
				kb.B(n.t("common.reject"), cb.Encode(cb.LeaveDecide, l.ID, "0")),
			})
		}
		mk = kb.Inline(rows...)
	}
	return n.send(ctx, t.OwnerID, false, views.Msg{Text: text, Markup: mk}, "")
}

func (n *Notifier) LeaveCancelled(ctx context.Context, t *team.Team, m *member.Membership, l *leave.Leave) error {
	text := n.v.Header(t, n.multi(ctx, t.OwnerID)) + n.t("leave.owner_cancelled", "name", m.DisplayName, "date", render.Date(l.Date))
	return n.send(ctx, t.OwnerID, false, views.Msg{Text: text}, "")
}

func (n *Notifier) LeaveDecision(ctx context.Context, t *team.Team, m *member.Membership, l *leave.Leave) error {
	k := "leave.approved"
	if l.Status == leave.StatusRejected {
		k = "leave.rejected"
	}
	text := n.v.Header(t, n.multi(ctx, m.UserID)) + n.t(k, "date", render.Date(l.Date))
	return n.send(ctx, m.UserID, false, views.Msg{Text: text}, "")
}

func (n *Notifier) OwnerNote(ctx context.Context, t *team.Team, m *member.Membership, p *plan.Plan, taskID int, note string) error {
	tk := p.Task(taskID)
	name := ""
	if tk != nil {
		name = tk.Text
	}
	text := n.v.Header(t, n.multi(ctx, m.UserID)) + n.t("owner.note_member", "task", name, "note", note)
	return n.send(ctx, m.UserID, false, views.Msg{Text: text}, "")
}

func (n *Notifier) TransferRequest(ctx context.Context, t *team.Team, to *member.Membership) error {
	text := n.t("members.transfer_ask", "team", t.Name)
	mk := kb.Inline([]kb.Btn{kb.B(n.t("common.accept"), cb.Encode(cb.TransferAnswer, t.ID, "1")), kb.B(n.t("common.decline"), cb.Encode(cb.TransferAnswer, t.ID, "0"))})
	return n.send(ctx, to.UserID, false, views.Msg{Text: text, Markup: mk}, "")
}

func (n *Notifier) TransferDone(ctx context.Context, t *team.Team, oldOwnerID int64, to *member.Membership) error {
	return n.send(ctx, oldOwnerID, false, views.Msg{Text: n.t("members.transfer_done_old", "name", to.DisplayName, "team", t.Name)}, "")
}

func (n *Notifier) GroupConnected(ctx context.Context, t *team.Team) error {
	return n.send(ctx, t.GroupChatID, true, views.Msg{Text: n.t("group.test_message", "team", t.Name)}, "")
}

func (n *Notifier) GroupLost(ctx context.Context, t *team.Team) error {
	text := n.t("group.lost", "team", t.Name)
	mk := kb.Inline([]kb.Btn{kb.U(n.t("group.btn_add"), n.v.GroupLink(t.ID))})
	return n.send(ctx, t.OwnerID, false, views.Msg{Text: text, Markup: mk}, "")
}

func (n *Notifier) ManualReminder(ctx context.Context, t *team.Team, m *member.Membership, date string) error {
	p, _ := n.st.FindPlan(ctx, m.ID, date)
	text := n.v.Header(t, n.multi(ctx, m.UserID))
	mk := ""
	switch {
	case !p.Submitted():
		text += n.t("owner.reminder_plan")
	case !p.Reported():
		text += n.t("owner.reminder_report")
		mk = kb.Inline([]kb.Btn{kb.B(n.t("evening.btn_open"), cb.Encode(cb.Evening, "open", p.ID))})
	default:
		text += n.t("owner.reminder_generic")
	}
	return n.send(ctx, m.UserID, false, views.Msg{Text: text, Markup: mk}, "")
}

func (n *Notifier) AskPlanNow(ctx context.Context, t *team.Team, m *member.Membership, date string) error {
	carry := 0
	return n.send(ctx, m.UserID, false, n.v.MorningRequest(t, m, date, carry, n.multi(ctx, m.UserID)), "")
}

var _ ports.Notifier = (*Notifier)(nil)
