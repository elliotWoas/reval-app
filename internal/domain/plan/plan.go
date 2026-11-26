// Package plan holds a member's plan for one work day.
package plan

import (
	"time"

	"taskbot/internal/domain/task"
)

// Plan statuses.
const (
	StatusSubmitted = "submitted"
	StatusLate      = "late"
	StatusMissing   = "missing"
	StatusLeave     = "leave"
)

// Report statuses.
const (
	ReportNone   = ""
	ReportOnTime = "on_time"
	ReportLate   = "late"
)

// Plan is one member's plan for one date. Tasks live inside the document.
type Plan struct {
	ID           string      `bson:"_id"` // membershipID:date
	MembershipID string      `bson:"membershipId"`
	TeamID       string      `bson:"teamId"`
	UserID       int64       `bson:"userId"`
	Date         string      `bson:"date"`
	Status       string      `bson:"status"`
	SubmittedAt  *time.Time  `bson:"submittedAt,omitempty"`
	Tasks        []task.Task `bson:"tasks"`
	ReportStatus string      `bson:"reportStatus"`
	ReportedAt   *time.Time  `bson:"reportedAt,omitempty"`
	Note         string      `bson:"note,omitempty"`
	Attachments  []string    `bson:"attachments,omitempty"` // Telegram file ids
	HalfLeave    bool        `bson:"halfLeave"`
	Stuck        bool        `bson:"stuck"`
}

// ID builds the deterministic plan id.
func ID(membershipID, date string) string { return membershipID + ":" + date }

// New creates an empty plan.
func New(membershipID, teamID string, userID int64, date string) *Plan {
	return &Plan{ID: ID(membershipID, date), MembershipID: membershipID, TeamID: teamID, UserID: userID, Date: date, Status: StatusMissing}
}

// HasTasks reports whether the member submitted at least one task.
func (p *Plan) HasTasks() bool { return p != nil && len(p.Tasks) > 0 }

// Submitted reports whether a plan was sent (on time or late).
func (p *Plan) Submitted() bool {
	return p != nil && (p.Status == StatusSubmitted || p.Status == StatusLate)
}

// Reported reports whether the evening report was sent.
func (p *Plan) Reported() bool { return p != nil && p.ReportStatus != ReportNone }

// NextTaskID returns the id for a new task.
func (p *Plan) NextTaskID() int {
	max := 0
	for _, t := range p.Tasks {
		if t.ID > max {
			max = t.ID
		}
	}
	return max + 1
}

// Append adds tasks with fresh ids. addedLate marks tasks added after the deadline.
func (p *Plan) Append(tasks []task.Task, addedLate bool) {
	id := p.NextTaskID()
	for _, t := range tasks {
		t.ID = id
		t.AddedLate = addedLate
		if t.Source == "" {
			t.Source = task.SourceNew
		}
		p.Tasks = append(p.Tasks, t)
		id++
	}
}

// Task returns a pointer to the task with id, or nil.
func (p *Plan) Task(id int) *task.Task {
	for i := range p.Tasks {
		if p.Tasks[i].ID == id {
			return &p.Tasks[i]
		}
	}
	return nil
}

// OpenTasks returns tasks that are not done (for carry-over).
func (p *Plan) OpenTasks() []task.Task {
	if p == nil {
		return nil
	}
	var out []task.Task
	for _, t := range p.Tasks {
		if t.EffectiveFinal() != task.Done {
			out = append(out, t)
		}
	}
	return out
}

// Counts summarizes final statuses. Unknown final counts as not done once reported.
type Counts struct {
	Total, Done, Partial, NotDone, Unknown int
}

// Rate is (done + partial/2) / total, 0 when no tasks.
func (c Counts) Rate() float64 {
	if c.Total == 0 {
		return 0
	}
	return (float64(c.Done) + float64(c.Partial)/2) / float64(c.Total)
}

// Add merges two counts.
func (c Counts) Add(o Counts) Counts {
	return Counts{c.Total + o.Total, c.Done + o.Done, c.Partial + o.Partial, c.NotDone + o.NotDone, c.Unknown + o.Unknown}
}

// Count returns the counts of the plan's tasks.
func (p *Plan) Count() Counts {
	var c Counts
	if p == nil {
		return c
	}
	for _, t := range p.Tasks {
		c.Total++
		switch t.EffectiveFinal() {
		case task.Done:
			c.Done++
		case task.Partial:
			c.Partial++
		case task.NotDone:
			c.NotDone++
		default:
			c.Unknown++
		}
	}
	return c
}

// FinalizeUnknown marks tasks without any status as not done (used when the report is submitted).
func (p *Plan) FinalizeUnknown() {
	for i := range p.Tasks {
		if p.Tasks[i].Final == "" {
			p.Tasks[i].Final = p.Tasks[i].EffectiveFinal()
			if p.Tasks[i].Final == "" {
				p.Tasks[i].Final = task.NotDone
			}
		}
	}
}

// CarryFrom builds carried tasks from yesterday's open tasks.
func CarryFrom(prev *Plan) []task.Task {
	var out []task.Task
	for _, t := range prev.OpenTasks() {
		out = append(out, task.Task{
			Text:        t.Text,
			Important:   t.Important,
			Progress:    -1,
			Source:      task.SourceCarried,
			CarriedDays: t.CarriedDays + 1,
		})
	}
	return out
}
