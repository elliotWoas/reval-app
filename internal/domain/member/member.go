// Package member holds users and their memberships in teams.
package member

import (
	"strings"
	"time"
	"unicode/utf8"

	"taskbot/internal/domain"
)

// User is a Telegram user known to the bot.
type User struct {
	ID         int64     `bson:"_id"` // Telegram user id
	FirstName  string    `bson:"firstName"`
	LastName   string    `bson:"lastName,omitempty"`
	Username   string    `bson:"username,omitempty"`
	Blocked    bool      `bson:"blocked"` // the user blocked the bot
	ActiveTeam string    `bson:"activeTeam,omitempty"`
	CreatedAt  time.Time `bson:"createdAt"`
	SeenAt     time.Time `bson:"seenAt"`
}

// FullName returns first + last name.
func (u *User) FullName() string {
	return strings.TrimSpace(u.FirstName + " " + u.LastName)
}

// Roles.
const (
	RoleOwner  = "owner"
	RoleMember = "member"
)

// Membership statuses.
const (
	StatusActive   = "active"
	StatusPending  = "pending" // waiting for owner approval
	StatusLeft     = "left"
	StatusRemoved  = "removed"
	StatusRejected = "rejected"
)

// Membership links a user to a team.
type Membership struct {
	ID           string    `bson:"_id"`
	TeamID       string    `bson:"teamId"`
	UserID       int64     `bson:"userId"`
	Role         string    `bson:"role"`
	DisplayName  string    `bson:"displayName"`
	Status       string    `bson:"status"`
	Participates bool      `bson:"participates"` // receives daily requests
	JoinedAt     time.Time `bson:"joinedAt"`
	StartDate    string    `bson:"startDate"` // first work date counted in the cycle and stats
}

// Active reports whether the membership is active.
func (m *Membership) Active() bool { return m.Status == StatusActive }

// IsOwner reports whether the membership is the team owner.
func (m *Membership) IsOwner() bool { return m.Role == RoleOwner }

// InCycle reports whether the member gets daily messages on date.
func (m *Membership) InCycle(date string) bool {
	return m.Active() && m.Participates && date >= m.StartDate
}

// ValidateDisplayName trims and checks a display name.
func ValidateDisplayName(name string, max int) (string, error) {
	name = strings.TrimSpace(name)
	n := utf8.RuneCountInString(name)
	if n < 1 || n > max {
		return "", domain.ErrNameLength
	}
	return name, nil
}
