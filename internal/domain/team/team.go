// Package team holds the team entity, its schedule and the rules that derive daily events.
package team

import (
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"taskbot/internal/domain"
)

// Schedule is the per-team daily routine. All times are local to the team timezone.
type Schedule struct {
	WorkDays       []int       `bson:"workDays"`
	Morning        TimeOfDay   `bson:"morning"`
	Deadline       TimeOfDay   `bson:"deadline"`
	Checkins       []TimeOfDay `bson:"checkins"`
	Evening        TimeOfDay   `bson:"evening"`
	Summary        TimeOfDay   `bson:"summary"`
	Weekly         TimeOfDay   `bson:"weekly"`
	ReminderBefore int         `bson:"reminderBefore"` // minutes
}

// Validate checks order: morning < deadline < evening < summary <= weekly, and the
// minimum gap between deadline and summary.
func (s Schedule) Validate(minGapMinutes int) error {
	if len(s.WorkDays) == 0 {
		return domain.ErrNoWorkDays
	}
	if !(s.Morning < s.Deadline && s.Deadline < s.Evening && s.Evening < s.Summary && s.Summary <= s.Weekly) {
		return domain.ErrTimeOrder
	}
	if int(s.Summary-s.Deadline) < minGapMinutes {
		return domain.ErrTimeGap
	}
	return nil
}

// ActiveCheckins returns the check-in times that fall strictly between deadline and evening.
func (s Schedule) ActiveCheckins() []TimeOfDay {
	var out []TimeOfDay
	for _, c := range s.Checkins {
		if c > s.Deadline && c < s.Evening {
			out = append(out, c)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// EveryHours builds check-in times every n hours after the deadline, before evening.
func EveryHours(deadline, evening TimeOfDay, n int) []TimeOfDay {
	var out []TimeOfDay
	if n <= 0 {
		return out
	}
	for t := deadline + TimeOfDay(n*60); t < evening; t += TimeOfDay(n * 60) {
		out = append(out, t)
	}
	return out
}

// Team is one team with its owner, invite key, report group and schedule.
type Team struct {
	ID                 string    `bson:"_id"`
	Name               string    `bson:"name"`
	OwnerID            int64     `bson:"ownerId"`
	InviteKey          string    `bson:"inviteKey"`
	GroupChatID        int64     `bson:"groupChatId,omitempty"`
	GroupTitle         string    `bson:"groupTitle,omitempty"`
	GroupReminders     int       `bson:"groupReminders"`
	Timezone           string    `bson:"timezone"`
	Schedule           Schedule  `bson:"schedule"`
	OffDays            []string  `bson:"offDays,omitempty"` // one-off holidays YYYY-MM-DD
	ManualApproval     bool      `bson:"manualApproval"`
	LeaveApproval      bool      `bson:"leaveApproval"`
	ShowMissingInGroup bool      `bson:"showMissingInGroup"`
	OwnerParticipates  bool      `bson:"ownerParticipates"`
	PendingOwner       string    `bson:"pendingOwner,omitempty"` // membership id offered ownership
	NextRunAt          time.Time `bson:"nextRunAt"`
	Cursor             time.Time `bson:"cursor"` // scheduler processed events up to here
	CreatedAt          time.Time `bson:"createdAt"`
}

// Location returns the team timezone (falls back to UTC).
func (t *Team) Location() *time.Location {
	loc, err := time.LoadLocation(t.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// LocalDate returns the team work date (YYYY-MM-DD) of an instant.
func (t *Team) LocalDate(ts time.Time) string { return ts.In(t.Location()).Format(DateLayout) }

// LocalTime returns the team local time of day of an instant.
func (t *Team) LocalTime(ts time.Time) TimeOfDay { return Of(ts.In(t.Location())) }

// At returns the instant of a local time on a local date.
func (t *Team) At(date string, tod TimeOfDay) time.Time {
	d, err := ParseDate(date, t.Location())
	if err != nil {
		return time.Time{}
	}
	return tod.On(d).UTC()
}

// IsWorkDay reports whether the date is a work day and not a one-off holiday.
func (t *Team) IsWorkDay(date string) bool {
	if slices.Contains(t.OffDays, date) {
		return false
	}
	return slices.Contains(t.Schedule.WorkDays, int(Weekday(date)))
}

// HasGroup reports whether a report group is connected.
func (t *Team) HasGroup() bool { return t.GroupChatID != 0 }

// ValidateName checks the team name length in characters.
func ValidateName(name string, min, max int) (string, error) {
	name = strings.TrimSpace(name)
	n := utf8.RuneCountInString(name)
	if n < min || n > max {
		return "", domain.ErrTeamNameLength
	}
	return name, nil
}

// Event kinds run by the scheduler.
const (
	EvLeaveDigest  = "leave_digest"
	EvMorning      = "morning"
	EvRemindPlan   = "remind_plan"
	EvDeadline     = "deadline"
	EvCheckin      = "checkin"
	EvEvening      = "evening"
	EvRemindReport = "remind_report"
	EvSummary      = "summary"
	EvWeekly       = "weekly"
)

var kindOrder = map[string]int{
	EvLeaveDigest: 0, EvMorning: 1, EvRemindPlan: 2, EvDeadline: 3, EvCheckin: 4,
	EvEvening: 5, EvRemindReport: 6, EvSummary: 7, EvWeekly: 8,
}

// Event is one scheduled action of a team on a work date.
type Event struct {
	Kind  string
	Index int // check-in number (1-based) for EvCheckin
	Date  string
	At    time.Time // UTC
}

// Key identifies the event uniquely within the team and date (for idempotency).
func (e Event) Key() string {
	if e.Kind == EvCheckin {
		return e.Kind + string(rune('0'+e.Index))
	}
	return e.Kind
}

// EventsOn returns the sorted events of a local date. Non-work days have none.
func (t *Team) EventsOn(date string) []Event {
	if !t.IsWorkDay(date) {
		return nil
	}
	s := t.Schedule
	add := func(evs []Event, kind string, idx int, tod TimeOfDay) []Event {
		return append(evs, Event{Kind: kind, Index: idx, Date: date, At: t.At(date, tod)})
	}
	var evs []Event
	if t.isFirstWorkDayOfWeek(date) {
		evs = add(evs, EvLeaveDigest, 0, s.Morning)
	}
	evs = add(evs, EvMorning, 0, s.Morning)
	if r := s.Deadline - TimeOfDay(s.ReminderBefore); r > s.Morning {
		evs = add(evs, EvRemindPlan, 0, r)
	}
	evs = add(evs, EvDeadline, 0, s.Deadline)
	for i, c := range s.ActiveCheckins() {
		evs = add(evs, EvCheckin, i+1, c)
	}
	evs = add(evs, EvEvening, 0, s.Evening)
	if r := s.Summary - TimeOfDay(s.ReminderBefore); r > s.Evening {
		evs = add(evs, EvRemindReport, 0, r)
	}
	evs = add(evs, EvSummary, 0, s.Summary)
	if t.isLastWorkDayOfWeek(date) {
		w := s.Weekly
		if w < s.Summary {
			w = s.Summary
		}
		evs = add(evs, EvWeekly, 0, w)
	}
	sort.SliceStable(evs, func(i, j int) bool {
		if !evs[i].At.Equal(evs[j].At) {
			return evs[i].At.Before(evs[j].At)
		}
		return kindOrder[evs[i].Kind] < kindOrder[evs[j].Kind]
	})
	return evs
}

func (t *Team) weekWorkDays(date string) []string {
	start := WeekStart(date)
	var out []string
	for i := 0; i < 7; i++ {
		d := AddDays(start, i)
		if t.IsWorkDay(d) {
			out = append(out, d)
		}
	}
	return out
}

func (t *Team) isFirstWorkDayOfWeek(date string) bool {
	days := t.weekWorkDays(date)
	return len(days) > 0 && days[0] == date
}

func (t *Team) isLastWorkDayOfWeek(date string) bool {
	days := t.weekWorkDays(date)
	return len(days) > 0 && days[len(days)-1] == date
}

// WeekWorkDays returns the work days of the week containing date.
func (t *Team) WeekWorkDays(date string) []string { return t.weekWorkDays(date) }

// EventsBetween returns all events with from < At <= to, across local dates.
func (t *Team) EventsBetween(from, to time.Time) []Event {
	var out []Event
	day := t.LocalDate(from)
	last := t.LocalDate(to)
	for i := 0; i < 60 && day <= last; i++ {
		for _, e := range t.EventsOn(day) {
			if e.At.After(from) && !e.At.After(to) {
				out = append(out, e)
			}
		}
		day = AddDays(day, 1)
	}
	return out
}

// NextEventAfter returns the first event strictly after ts (searching up to 30 days).
func (t *Team) NextEventAfter(ts time.Time) (Event, bool) {
	day := t.LocalDate(ts)
	for i := 0; i < 30; i++ {
		for _, e := range t.EventsOn(day) {
			if e.At.After(ts) {
				return e, true
			}
		}
		day = AddDays(day, 1)
	}
	return Event{}, false
}

// StillMeaningful decides whether a missed event should still run at now.
func (t *Team) StillMeaningful(e Event, now time.Time) bool {
	s := t.Schedule
	switch e.Kind {
	case EvMorning, EvRemindPlan, EvLeaveDigest:
		return now.Before(t.At(e.Date, s.Deadline))
	case EvDeadline:
		return now.Before(t.At(e.Date, s.Evening))
	case EvCheckin:
		return now.Before(t.At(e.Date, s.Evening))
	case EvEvening, EvRemindReport:
		return now.Before(t.At(e.Date, s.Summary))
	case EvSummary:
		return now.Before(t.At(AddDays(e.Date, 1), s.Morning))
	case EvWeekly:
		return now.Sub(e.At) < 24*time.Hour
	}
	return false
}

// PlanWindowOpen reports whether on-time plan submission is still open at now for date.
func (t *Team) PlanWindowOpen(date string, now time.Time) bool {
	return now.Before(t.At(date, t.Schedule.Deadline))
}

// ReportOnTime reports whether an evening report at now counts as on time for date.
func (t *Team) ReportOnTime(date string, now time.Time) bool {
	return !now.After(t.At(date, t.Schedule.Summary))
}

// ReportAccepted reports whether a (possibly late) evening report is still accepted at now.
func (t *Team) ReportAccepted(date string, now time.Time) bool {
	return now.Before(t.At(AddDays(date, 1), t.Schedule.Morning))
}
