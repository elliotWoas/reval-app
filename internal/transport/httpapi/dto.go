package httpapi

import (
	"time"

	"taskbot/internal/domain/leave"
	"taskbot/internal/domain/member"
	"taskbot/internal/domain/plan"
	"taskbot/internal/domain/stats"
	"taskbot/internal/domain/task"
	"taskbot/internal/domain/team"
	"taskbot/internal/usecase/overview"
	"taskbot/internal/usecase/ports"
)

// The JSON contract of the Mini App API. web/src/api/types.ts mirrors these types.

type userDTO struct {
	ID        int64  `json:"id"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName,omitempty"`
	Username  string `json:"username,omitempty"`
	Language  string `json:"languageCode,omitempty"`
	PhotoURL  string `json:"photoUrl,omitempty"`
}

type scheduleDTO struct {
	WorkDays []int    `json:"workDays"` // 0=Sunday ... 6=Saturday
	Morning  string   `json:"morning"`
	Deadline string   `json:"deadline"`
	Checkins []string `json:"checkins"`
	Evening  string   `json:"evening"`
	Summary  string   `json:"summary"`
	Weekly   string   `json:"weekly"`
}

type teamDTO struct {
	ID             string      `json:"id"`
	Name           string      `json:"name"`
	IsOwner        bool        `json:"isOwner"`
	Role           string      `json:"role"`
	DisplayName    string      `json:"displayName"`
	Participates   bool        `json:"participates"`
	MembershipID   string      `json:"membershipId"`
	StartDate      string      `json:"startDate"`
	Timezone       string      `json:"timezone"`
	Today          string      `json:"today"`
	Schedule       scheduleDTO `json:"schedule"`
	LeaveApproval  bool        `json:"leaveApproval"`
	HasGroup       bool        `json:"hasGroup"`
	GroupTitle     string      `json:"groupTitle,omitempty"`
	InviteKey      string      `json:"inviteKey,omitempty"` // owner only
	ManualApproval bool        `json:"manualApproval"`
}

type limitsDTO struct {
	MaxTasks       int      `json:"maxTasks"`
	TaskMaxLen     int      `json:"taskMaxLen"`
	NoteMaxLen     int      `json:"noteMaxLen"`
	DisplayNameMax int      `json:"displayNameMax"`
	LeaveDaysAhead int      `json:"leaveDaysAhead"`
	FailReasons    []string `json:"failReasons"`
	Progress       []int    `json:"progress"`
	StuckCarryDays int      `json:"stuckCarryDays"`
}

type meDTO struct {
	User        userDTO   `json:"user"`
	Teams       []teamDTO `json:"teams"`
	ActiveTeam  string    `json:"activeTeam,omitempty"`
	BotUsername string    `json:"botUsername,omitempty"`
	Limits      limitsDTO `json:"limits"`
}

type taskDTO struct {
	ID             int        `json:"id"`
	Text           string     `json:"text"`
	Important      bool       `json:"important"`
	Progress       *int       `json:"progress"` // null when never reported
	Final          string     `json:"final,omitempty"`
	EffectiveFinal string     `json:"effectiveFinal,omitempty"`
	Reason         string     `json:"reason,omitempty"`
	Source         string     `json:"source"`
	CarriedDays    int        `json:"carriedDays"`
	AddedLate      bool       `json:"addedLate"`
	DoneAt         *time.Time `json:"doneAt,omitempty"`
	OwnerNote      string     `json:"ownerNote,omitempty"`
}

type countsDTO struct {
	Total   int     `json:"total"`
	Done    int     `json:"done"`
	Partial int     `json:"partial"`
	NotDone int     `json:"notDone"`
	Unknown int     `json:"unknown"`
	Rate    float64 `json:"rate"` // (done + partial/2) / total
}

type planDTO struct {
	Date         string     `json:"date"`
	Status       string     `json:"status"`
	SubmittedAt  *time.Time `json:"submittedAt,omitempty"`
	ReportStatus string     `json:"reportStatus,omitempty"`
	ReportedAt   *time.Time `json:"reportedAt,omitempty"`
	Note         string     `json:"note,omitempty"`
	Stuck        bool       `json:"stuck"`
	HalfLeave    bool       `json:"halfLeave"`
	Attachments  int        `json:"attachments"`
	Tasks        []taskDTO  `json:"tasks"`
	Counts       countsDTO  `json:"counts"`
}

type windowDTO struct {
	Phase          string    `json:"phase"` // off | leave | before | planning | working | evening | late_report | closed
	PlanWindowOpen bool      `json:"planWindowOpen"`
	ReportAccepted bool      `json:"reportAccepted"`
	ReportOnTime   bool      `json:"reportOnTime"`
	MorningAt      time.Time `json:"morningAt"`
	DeadlineAt     time.Time `json:"deadlineAt"`
	EveningAt      time.Time `json:"eveningAt"`
	SummaryAt      time.Time `json:"summaryAt"`
	CloseAt        time.Time `json:"closeAt"` // late reports are accepted until then
}

type carryDTO struct {
	Text        string `json:"text"`
	Important   bool   `json:"important"`
	CarriedDays int    `json:"carriedDays"`
}

type todayDTO struct {
	Date         string     `json:"date"`
	Now          time.Time  `json:"now"`
	WorkDay      bool       `json:"workDay"`
	OnLeave      bool       `json:"onLeave"`
	Participates bool       `json:"participates"`
	Window       windowDTO  `json:"window"`
	Plan         *planDTO   `json:"plan"`
	Carry        []carryDTO `json:"carry"`
}

type leaveDTO struct {
	Date      string     `json:"date"`
	Half      bool       `json:"half"`
	Note      string     `json:"note,omitempty"`
	Status    string     `json:"status"`
	CreatedAt time.Time  `json:"createdAt"`
	DecidedAt *time.Time `json:"decidedAt,omitempty"`
}

type dayDTO struct {
	Date    string    `json:"date"`
	WorkDay bool      `json:"workDay"`
	Leave   *leaveDTO `json:"leave,omitempty"`
	Plan    *planDTO  `json:"plan,omitempty"`
}

type historyDTO struct {
	From  string   `json:"from"`
	To    string   `json:"to"`
	Today string   `json:"today"`
	Days  []dayDTO `json:"days"`
}

type weeklyDTO struct {
	Counts       countsDTO      `json:"counts"`
	ActiveDays   int            `json:"activeDays"`
	PunctualDays int            `json:"punctualDays"`
	Punctuality  float64        `json:"punctuality"`
	Streak       int            `json:"streak"`
	LeaveDays    int            `json:"leaveDays"`
	TopReason    string         `json:"topReason,omitempty"`
	ReasonCounts map[string]int `json:"reasonCounts"`
	Stuck        []string       `json:"stuck"`
	BestDay      string         `json:"bestDay,omitempty"`
	BestDayRate  float64        `json:"bestDayRate"`
}

type dayStatDTO struct {
	Date    string    `json:"date"`
	WorkDay bool      `json:"workDay"`
	OnLeave bool      `json:"onLeave"`
	Planned bool      `json:"planned"`
	PlanOn  bool      `json:"planOnTime"`
	Report  bool      `json:"reported"`
	Counts  countsDTO `json:"counts"`
}

type weekDTO struct {
	WeekStart string       `json:"weekStart"`
	WeekEnd   string       `json:"weekEnd"`
	UpTo      string       `json:"upTo"`
	This      *weeklyDTO   `json:"this"`
	Prev      *weeklyDTO   `json:"prev"` // the stored report of the previous week, when one was sent
	Days      []dayStatDTO `json:"days"`
}

type pulseDTO struct {
	Date      string    `json:"date"`
	WorkDay   bool      `json:"workDay"`
	Members   int       `json:"members"`
	Submitted int       `json:"submitted"`
	Reported  int       `json:"reported"`
	OnLeave   int       `json:"onLeave"`
	Counts    countsDTO `json:"counts"`
}

type leavesDTO struct {
	Upcoming         []leaveDTO `json:"upcoming"`
	Selectable       []string   `json:"selectable"`
	ApprovalRequired bool       `json:"approvalRequired"`
}

// ---- owner views

type memberDTO struct {
	MembershipID string    `json:"membershipId"`
	DisplayName  string    `json:"displayName"`
	Role         string    `json:"role"`
	Participates bool      `json:"participates"`
	JoinedAt     time.Time `json:"joinedAt"`
	StartDate    string    `json:"startDate"`
}

type teamRowDTO struct {
	Member  memberDTO `json:"member"`
	Plan    *planDTO  `json:"plan"`
	Leave   *leaveDTO `json:"leave,omitempty"`
	Blocked bool      `json:"blocked"`
}

type teamTodayDTO struct {
	Date    string       `json:"date"`
	WorkDay bool         `json:"workDay"`
	Window  windowDTO    `json:"window"`
	Rows    []teamRowDTO `json:"rows"`
	Counts  countsDTO    `json:"counts"`
}

type memberWeekDTO struct {
	Member memberDTO  `json:"member"`
	This   weeklyDTO  `json:"this"`
	Prev   *weeklyDTO `json:"prev"`
}

type teamWeekDTO struct {
	WeekStart        string          `json:"weekStart"`
	WeekEnd          string          `json:"weekEnd"`
	UpTo             string          `json:"upTo"`
	Team             weeklyDTO       `json:"team"`
	PrevTeam         *weeklyDTO      `json:"prevTeam"`
	MostPunctual     string          `json:"mostPunctual,omitempty"`
	MostPunctualDays int             `json:"mostPunctualDays"`
	Members          []memberWeekDTO `json:"members"`
}

type teamLeaveDTO struct {
	Member memberDTO `json:"member"`
	Leave  leaveDTO  `json:"leave"`
}

// ---- mapping

func toSchedule(s team.Schedule) scheduleDTO {
	out := scheduleDTO{
		WorkDays: append([]int{}, s.WorkDays...), Morning: hhmm(s.Morning), Deadline: hhmm(s.Deadline),
		Evening: hhmm(s.Evening), Summary: hhmm(s.Summary), Weekly: hhmm(s.Weekly), Checkins: []string{},
	}
	for _, c := range s.ActiveCheckins() {
		out.Checkins = append(out.Checkins, hhmm(c))
	}
	return out
}

func hhmm(t team.TimeOfDay) string {
	h, m := int(t)/60, int(t)%60
	return string([]byte{byte('0' + h/10), byte('0' + h%10), ':', byte('0' + m/10), byte('0' + m%10)})
}

func toTeam(t *team.Team, m *member.Membership, today string) teamDTO {
	d := teamDTO{
		ID: t.ID, Name: t.Name, IsOwner: t.OwnerID == m.UserID, Role: m.Role, DisplayName: m.DisplayName,
		Participates: m.Participates, MembershipID: m.ID, StartDate: m.StartDate, Timezone: t.Timezone, Today: today,
		Schedule: toSchedule(t.Schedule), LeaveApproval: t.LeaveApproval, HasGroup: t.HasGroup(),
		ManualApproval: t.ManualApproval,
	}
	if d.IsOwner {
		d.InviteKey = t.InviteKey
		d.GroupTitle = t.GroupTitle
	}
	return d
}

func toCounts(c plan.Counts) countsDTO {
	return countsDTO{Total: c.Total, Done: c.Done, Partial: c.Partial, NotDone: c.NotDone, Unknown: c.Unknown, Rate: c.Rate()}
}

func toTask(t task.Task) taskDTO {
	d := taskDTO{
		ID: t.ID, Text: t.Text, Important: t.Important, Final: t.Final, EffectiveFinal: t.EffectiveFinal(),
		Reason: t.Reason, Source: t.Source, CarriedDays: t.CarriedDays, AddedLate: t.AddedLate, DoneAt: t.DoneAt,
		OwnerNote: t.OwnerNote,
	}
	if t.Progress >= 0 {
		p := t.Progress
		d.Progress = &p
	}
	return d
}

func toPlan(p *plan.Plan) *planDTO {
	if p == nil {
		return nil
	}
	d := &planDTO{
		Date: p.Date, Status: p.Status, SubmittedAt: p.SubmittedAt, ReportStatus: p.ReportStatus, ReportedAt: p.ReportedAt,
		Note: p.Note, Stuck: p.Stuck, HalfLeave: p.HalfLeave, Attachments: len(p.Attachments),
		Tasks: make([]taskDTO, 0, len(p.Tasks)), Counts: toCounts(p.Count()),
	}
	for _, t := range p.Tasks {
		d.Tasks = append(d.Tasks, toTask(t))
	}
	return d
}

func toLeave(l *leave.Leave) *leaveDTO {
	if l == nil {
		return nil
	}
	return &leaveDTO{Date: l.Date, Half: l.Half, Note: l.Note, Status: l.Status, CreatedAt: l.CreatedAt, DecidedAt: l.DecidedAt}
}

func toDay(d overview.Day) dayDTO {
	return dayDTO{Date: d.Date, WorkDay: d.WorkDay, Leave: toLeave(d.Leave), Plan: toPlan(d.Plan)}
}

func toWeekly(w *stats.Weekly) *weeklyDTO {
	if w == nil {
		return nil
	}
	d := &weeklyDTO{
		Counts: toCounts(w.Counts), ActiveDays: w.ActiveDays, PunctualDays: w.PunctualDays, Punctuality: w.Punctuality,
		Streak: w.Streak, LeaveDays: w.LeaveDays, TopReason: w.TopReason, ReasonCounts: w.ReasonCounts,
		Stuck: append([]string{}, w.Stuck...), BestDay: w.BestDay, BestDayRate: w.BestDayRate,
	}
	if d.ReasonCounts == nil {
		d.ReasonCounts = map[string]int{}
	}
	return d
}

func toMember(m *member.Membership) memberDTO {
	return memberDTO{MembershipID: m.ID, DisplayName: m.DisplayName, Role: m.Role, Participates: m.Participates, JoinedAt: m.JoinedAt, StartDate: m.StartDate}
}

func toTeamRow(r ports.PlanRow) teamRowDTO {
	return teamRowDTO{Member: toMember(r.Member), Plan: toPlan(r.Plan), Leave: toLeave(r.Leave), Blocked: r.Blocked}
}
