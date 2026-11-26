// Package stats computes weekly statistics from plans and leaves. Pure functions, no I/O.
package stats

import (
	"sort"
	"time"

	"taskbot/internal/domain/plan"
	"taskbot/internal/domain/task"
)

// Day is one work day of one member.
type Day struct {
	Date    string
	Plan    *plan.Plan // nil when nothing was sent
	OnLeave bool       // full-day effective leave
	Half    bool       // half-day leave
}

// Counted reports whether the day enters the statistics.
func (d Day) Counted() bool { return !d.OnLeave }

// PlanOnTime reports a plan sent before the deadline.
func (d Day) PlanOnTime() bool { return d.Plan != nil && d.Plan.Status == plan.StatusSubmitted }

// ReportOnTime reports an evening report sent before the summary.
func (d Day) ReportOnTime() bool { return d.Plan != nil && d.Plan.ReportStatus == plan.ReportOnTime }

// Punctual reports both plan and report on time.
func (d Day) Punctual() bool { return d.PlanOnTime() && d.ReportOnTime() }

// Weekly is the stored weekly statistic of a member, or of the team when MembershipID is "".
type Weekly struct {
	ID               string         `bson:"_id"`
	TeamID           string         `bson:"teamId"`
	WeekStart        string         `bson:"weekStart"`
	MembershipID     string         `bson:"membershipId"`
	Name             string         `bson:"name,omitempty"`
	Counts           plan.Counts    `bson:"counts"`
	Rate             float64        `bson:"rate"`
	ActiveDays       int            `bson:"activeDays"`
	PunctualDays     int            `bson:"punctualDays"`
	Punctuality      float64        `bson:"punctuality"`
	Streak           int            `bson:"streak"`
	LeaveDays        int            `bson:"leaveDays"`
	TopReason        string         `bson:"topReason,omitempty"`
	ReasonCounts     map[string]int `bson:"reasonCounts,omitempty"`
	Stuck            []string       `bson:"stuck,omitempty"`
	BestDay          string         `bson:"bestDay,omitempty"`
	BestDayRate      float64        `bson:"bestDayRate"`
	MostPunctual     string         `bson:"mostPunctual,omitempty"`
	MostPunctualDays int            `bson:"mostPunctualDays"`
	ComputedAt       time.Time      `bson:"computedAt"`
}

// ID builds the stored id.
func ID(teamID, weekStart, membershipID string) string {
	return teamID + ":" + weekStart + ":" + membershipID
}

// Member computes one member's week. history is every work day up to the week end
// (oldest first) and is used for the streak; days are the week's work days.
func Member(teamID, weekStart, membershipID, name string, days []Day, history []Day, stuckAfter int) Weekly {
	w := Weekly{ID: ID(teamID, weekStart, membershipID), TeamID: teamID, WeekStart: weekStart, MembershipID: membershipID, Name: name, ReasonCounts: map[string]int{}}
	stuck := map[string]bool{}
	for _, d := range days {
		if d.OnLeave || d.Half {
			w.LeaveDays++
		}
		if !d.Counted() {
			continue
		}
		w.ActiveDays++
		if d.Punctual() {
			w.PunctualDays++
		}
		if d.Plan == nil {
			continue
		}
		w.Counts = w.Counts.Add(d.Plan.Count())
		for _, t := range d.Plan.Tasks {
			if t.EffectiveFinal() == task.NotDone && t.Reason != "" {
				w.ReasonCounts[t.Reason]++
			}
			if t.CarriedDays >= stuckAfter && t.EffectiveFinal() != task.Done {
				stuck[t.Text] = true
			}
		}
	}
	w.Rate = w.Counts.Rate()
	if w.ActiveDays > 0 {
		w.Punctuality = float64(w.PunctualDays) / float64(w.ActiveDays)
	}
	w.TopReason = topKey(w.ReasonCounts)
	for s := range stuck {
		w.Stuck = append(w.Stuck, s)
	}
	sort.Strings(w.Stuck)
	w.Streak = Streak(history)
	return w
}

// Streak counts consecutive punctual days going back from the last day; leave days are skipped.
func Streak(history []Day) int {
	n := 0
	for i := len(history) - 1; i >= 0; i-- {
		d := history[i]
		if d.OnLeave {
			continue
		}
		if !d.Punctual() {
			break
		}
		n++
	}
	return n
}

// Team aggregates member weeks. byDay maps date → counts of all members that day.
func Team(teamID, weekStart string, members []Weekly, byDay map[string]plan.Counts) Weekly {
	w := Weekly{ID: ID(teamID, weekStart, ""), TeamID: teamID, WeekStart: weekStart, ReasonCounts: map[string]int{}}
	punctual := 0
	for _, m := range members {
		w.Counts = w.Counts.Add(m.Counts)
		w.ActiveDays += m.ActiveDays
		punctual += m.PunctualDays
		w.LeaveDays += m.LeaveDays
		for k, v := range m.ReasonCounts {
			w.ReasonCounts[k] += v
		}
		w.Stuck = append(w.Stuck, m.Stuck...)
		if m.ActiveDays > 0 && m.PunctualDays > w.MostPunctualDays {
			w.MostPunctual = m.Name
			w.MostPunctualDays = m.PunctualDays
		}
	}
	w.PunctualDays = punctual
	w.Rate = w.Counts.Rate()
	if w.ActiveDays > 0 {
		w.Punctuality = float64(punctual) / float64(w.ActiveDays)
	}
	w.TopReason = topKey(w.ReasonCounts)
	dates := make([]string, 0, len(byDay))
	for d := range byDay {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	for _, d := range dates {
		c := byDay[d]
		if c.Total == 0 {
			continue
		}
		if r := c.Rate(); w.BestDay == "" || r > w.BestDayRate {
			w.BestDay, w.BestDayRate = d, r
		}
	}
	return w
}

func topKey(m map[string]int) string {
	best, n := "", 0
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if m[k] > n {
			best, n = k, m[k]
		}
	}
	return best
}
