// Package leave holds day-off requests.
package leave

import "time"

// Statuses.
const (
	StatusRegistered = "registered"
	StatusApproved   = "approved"
	StatusRejected   = "rejected"
	StatusCancelled  = "cancelled"
)

// Leave is one day off of one member.
type Leave struct {
	ID           string     `bson:"_id"` // membershipID:date
	MembershipID string     `bson:"membershipId"`
	TeamID       string     `bson:"teamId"`
	UserID       int64      `bson:"userId"`
	Date         string     `bson:"date"`
	Half         bool       `bson:"half"`
	Note         string     `bson:"note,omitempty"`
	Status       string     `bson:"status"`
	CreatedAt    time.Time  `bson:"createdAt"`
	DecidedAt    *time.Time `bson:"decidedAt,omitempty"`
}

// ID builds the deterministic leave id.
func ID(membershipID, date string) string { return membershipID + ":" + date }

// Active reports whether the leave counts (registered or approved).
// With owner approval required, a registered leave is still pending but already silences messages
// only after approval; see Effective.
func (l *Leave) Active() bool {
	return l != nil && (l.Status == StatusRegistered || l.Status == StatusApproved)
}

// Effective reports whether the leave should silence messages, given the team approval mode.
func (l *Leave) Effective(needsApproval bool) bool {
	if l == nil {
		return false
	}
	if needsApproval {
		return l.Status == StatusApproved
	}
	return l.Active()
}

// FullDay reports whether the leave silences the whole day.
func (l *Leave) FullDay(needsApproval bool) bool { return l.Effective(needsApproval) && !l.Half }
