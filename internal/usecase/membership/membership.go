// Package membership handles joining, leaving, removing, invite keys, ownership transfer
// and the report group connection.
package membership

import (
	"context"
	"errors"
	"sync"
	"time"

	"taskbot/internal/config"
	"taskbot/internal/domain"
	"taskbot/internal/domain/member"
	"taskbot/internal/domain/team"
	"taskbot/internal/platform/clock"
	"taskbot/internal/platform/idgen"
	"taskbot/internal/usecase/onboarding"
	"taskbot/internal/usecase/ports"
)

// Service manages memberships.
type Service struct {
	st     ports.Store
	clock  clock.Clock
	cfg    *config.Config
	notify ports.Notifier

	mu       sync.Mutex
	attempts map[int64][]time.Time // wrong key attempts per user
}

// New creates the service.
func New(st ports.Store, c clock.Clock, cfg *config.Config, n ports.Notifier) *Service {
	return &Service{st: st, clock: c, cfg: cfg, notify: n, attempts: map[int64][]time.Time{}}
}

// TeamMember pairs a team with the caller's membership.
type TeamMember struct {
	Team   *team.Team
	Member *member.Membership
}

// MyTeams returns the active teams of a user.
func (s *Service) MyTeams(ctx context.Context, userID int64) ([]TeamMember, error) {
	ms, err := s.st.UserMemberships(ctx, userID)
	if err != nil {
		return nil, err
	}
	var out []TeamMember
	for _, m := range ms {
		t, err := s.st.GetTeam(ctx, m.TeamID)
		if err != nil {
			continue
		}
		out = append(out, TeamMember{Team: t, Member: m})
	}
	return out, nil
}

// Context loads a team and the caller's active membership in it.
func (s *Service) Context(ctx context.Context, userID int64, teamID string) (TeamMember, error) {
	t, err := s.st.GetTeam(ctx, teamID)
	if err != nil {
		return TeamMember{}, err
	}
	m, err := s.st.FindMembership(ctx, teamID, userID)
	if err != nil {
		return TeamMember{}, err
	}
	if m == nil || !m.Active() {
		return TeamMember{}, domain.ErrForbidden
	}
	return TeamMember{Team: t, Member: m}, nil
}

// OwnerContext is Context that also requires the owner role.
func (s *Service) OwnerContext(ctx context.Context, userID int64, teamID string) (TeamMember, error) {
	tm, err := s.Context(ctx, userID, teamID)
	if err != nil {
		return tm, err
	}
	if tm.Team.OwnerID != userID {
		return tm, domain.ErrNotOwner
	}
	return tm, nil
}

// ResolveKey finds a team by invite key, limiting wrong attempts per user per hour.
func (s *Service) ResolveKey(ctx context.Context, userID int64, key string) (*team.Team, error) {
	now := s.clock.Now()
	s.mu.Lock()
	var recent []time.Time
	for _, at := range s.attempts[userID] {
		if now.Sub(at) < time.Hour {
			recent = append(recent, at)
		}
	}
	s.attempts[userID] = recent
	blocked := len(recent) >= s.cfg.Limits.JoinAttemptsPerHour
	s.mu.Unlock()
	if blocked {
		return nil, domain.ErrTooManyAttempts
	}
	t, err := s.st.GetTeamByKey(ctx, idgen.NormalizeKey(key))
	if errors.Is(err, domain.ErrNotFound) {
		s.mu.Lock()
		s.attempts[userID] = append(s.attempts[userID], now)
		s.mu.Unlock()
		return nil, domain.ErrKeyNotFound
	}
	return t, err
}

// Join adds a user to a team, or files a request when the team needs manual approval.
func (s *Service) Join(ctx context.Context, userID int64, teamID, displayName string) (*member.Membership, error) {
	name, err := member.ValidateDisplayName(displayName, s.cfg.Limits.DisplayNameMax)
	if err != nil {
		return nil, err
	}
	t, err := s.st.GetTeam(ctx, teamID)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	m, err := s.st.FindMembership(ctx, teamID, userID)
	if err != nil {
		return nil, err
	}
	switch {
	case m != nil && m.Active():
		return m, domain.ErrAlreadyMember
	case m != nil && m.Status == member.StatusPending:
		return m, domain.ErrPendingApproval
	case m == nil:
		m = &member.Membership{ID: idgen.ID(), TeamID: teamID, UserID: userID, Role: member.RoleMember}
	}
	m.DisplayName = name
	m.Participates = true
	m.JoinedAt = now
	m.StartDate = onboarding.StartDate(t, now)
	if t.ManualApproval {
		m.Status = member.StatusPending
	} else {
		m.Status = member.StatusActive
	}
	if err := s.st.SaveMembership(ctx, m); err != nil {
		return nil, err
	}
	if m.Status == member.StatusPending {
		return m, s.notify.JoinRequest(ctx, t, m)
	}
	_ = s.st.SetActiveTeam(ctx, userID, teamID)
	return m, s.notify.MemberJoined(ctx, t, m)
}

// Decide approves or rejects a pending join request.
func (s *Service) Decide(ctx context.Context, ownerID int64, membershipID string, approve bool) (*member.Membership, error) {
	m, err := s.st.GetMembership(ctx, membershipID)
	if err != nil {
		return nil, err
	}
	if _, err := s.OwnerContext(ctx, ownerID, m.TeamID); err != nil {
		return nil, err
	}
	if m.Status != member.StatusPending {
		return m, nil
	}
	t, _ := s.st.GetTeam(ctx, m.TeamID)
	now := s.clock.Now()
	if approve {
		m.Status = member.StatusActive
		m.JoinedAt = now
		m.StartDate = onboarding.StartDate(t, now)
		_ = s.st.SetActiveTeam(ctx, m.UserID, m.TeamID)
	} else {
		m.Status = member.StatusRejected
	}
	if err := s.st.SaveMembership(ctx, m); err != nil {
		return nil, err
	}
	return m, s.notify.JoinDecision(ctx, t, m, approve)
}

// Leave lets a member leave a team. The owner must transfer ownership first.
func (s *Service) Leave(ctx context.Context, userID int64, teamID string) error {
	tm, err := s.Context(ctx, userID, teamID)
	if err != nil {
		return err
	}
	if tm.Member.IsOwner() {
		return domain.ErrOwnerCannotLeave
	}
	tm.Member.Status = member.StatusLeft
	if err := s.st.SaveMembership(ctx, tm.Member); err != nil {
		return err
	}
	return s.notify.MemberLeft(ctx, tm.Team, tm.Member)
}

// Remove lets the owner remove a member.
func (s *Service) Remove(ctx context.Context, ownerID int64, membershipID string) (*member.Membership, error) {
	m, err := s.st.GetMembership(ctx, membershipID)
	if err != nil {
		return nil, err
	}
	tm, err := s.OwnerContext(ctx, ownerID, m.TeamID)
	if err != nil {
		return nil, err
	}
	if m.UserID == ownerID {
		return nil, domain.ErrSelf
	}
	m.Status = member.StatusRemoved
	if err := s.st.SaveMembership(ctx, m); err != nil {
		return nil, err
	}
	return m, s.notify.MemberRemoved(ctx, tm.Team, m)
}

// RotateKey replaces the invite key; existing members stay.
func (s *Service) RotateKey(ctx context.Context, ownerID int64, teamID string) (*team.Team, error) {
	tm, err := s.OwnerContext(ctx, ownerID, teamID)
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		tm.Team.InviteKey = idgen.InviteKey(s.cfg.Limits.InviteKeyGroups, s.cfg.Limits.InviteKeyGroupLen)
		err = s.st.SaveTeam(ctx, tm.Team)
		if !errors.Is(err, domain.ErrDuplicate) || attempt > 5 {
			break
		}
	}
	return tm.Team, err
}

// SetDisplayName changes the caller's name in a team.
func (s *Service) SetDisplayName(ctx context.Context, userID int64, teamID, name string) error {
	tm, err := s.Context(ctx, userID, teamID)
	if err != nil {
		return err
	}
	if tm.Member.DisplayName, err = member.ValidateDisplayName(name, s.cfg.Limits.DisplayNameMax); err != nil {
		return err
	}
	return s.st.SaveMembership(ctx, tm.Member)
}

// RequestTransfer offers ownership to another active member.
func (s *Service) RequestTransfer(ctx context.Context, ownerID int64, membershipID string) (*member.Membership, error) {
	to, err := s.st.GetMembership(ctx, membershipID)
	if err != nil {
		return nil, err
	}
	tm, err := s.OwnerContext(ctx, ownerID, to.TeamID)
	if err != nil {
		return nil, err
	}
	if to.UserID == ownerID {
		return nil, domain.ErrSelf
	}
	if !to.Active() {
		return nil, domain.ErrNotFound
	}
	tm.Team.PendingOwner = to.ID
	if err := s.st.SaveTeam(ctx, tm.Team); err != nil {
		return nil, err
	}
	return to, s.notify.TransferRequest(ctx, tm.Team, to)
}

// AnswerTransfer is called by the offered member; accepting swaps the roles.
func (s *Service) AnswerTransfer(ctx context.Context, userID int64, teamID string, accept bool) (*team.Team, error) {
	tm, err := s.Context(ctx, userID, teamID)
	if err != nil {
		return nil, err
	}
	t := tm.Team
	if t.PendingOwner != tm.Member.ID {
		return nil, domain.ErrForbidden
	}
	t.PendingOwner = ""
	if !accept {
		return t, s.st.SaveTeam(ctx, t)
	}
	old, err := s.st.FindMembership(ctx, t.ID, t.OwnerID)
	if err != nil {
		return nil, err
	}
	oldOwner := t.OwnerID
	t.OwnerID = userID
	if err := s.st.SaveTeam(ctx, t); err != nil {
		return nil, err
	}
	if old != nil {
		old.Role = member.RoleMember
		if err := s.st.SaveMembership(ctx, old); err != nil {
			return nil, err
		}
	}
	tm.Member.Role = member.RoleOwner
	if err := s.st.SaveMembership(ctx, tm.Member); err != nil {
		return nil, err
	}
	return t, s.notify.TransferDone(ctx, t, oldOwner, tm.Member)
}

// OwnedTeams returns the teams owned by a user.
func (s *Service) OwnedTeams(ctx context.Context, userID int64) ([]*team.Team, error) {
	tms, err := s.MyTeams(ctx, userID)
	if err != nil {
		return nil, err
	}
	var out []*team.Team
	for _, tm := range tms {
		if tm.Team.OwnerID == userID {
			out = append(out, tm.Team)
		}
	}
	return out, nil
}

// ConnectGroup links a report group. The caller must already have checked that the owner
// is an admin of the group.
func (s *Service) ConnectGroup(ctx context.Context, ownerID int64, teamID string, chatID int64, title string) (*team.Team, error) {
	tm, err := s.OwnerContext(ctx, ownerID, teamID)
	if err != nil {
		return nil, err
	}
	if other, err := s.st.GetTeamByGroup(ctx, chatID); err == nil && other.ID != teamID {
		other.GroupChatID, other.GroupTitle = 0, ""
		_ = s.st.SaveTeam(ctx, other)
	}
	tm.Team.GroupChatID = chatID
	tm.Team.GroupTitle = title
	if err := s.st.SaveTeam(ctx, tm.Team); err != nil {
		return nil, err
	}
	return tm.Team, s.notify.GroupConnected(ctx, tm.Team)
}

// GroupLost is called when the bot is removed from a group or cannot post there.
func (s *Service) GroupLost(ctx context.Context, chatID int64) error {
	t, err := s.st.GetTeamByGroup(ctx, chatID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	t.GroupChatID, t.GroupTitle = 0, ""
	if err := s.st.SaveTeam(ctx, t); err != nil {
		return err
	}
	return s.notify.GroupLost(ctx, t)
}

// GroupMigrated stores the new id of a group that became a supergroup.
func (s *Service) GroupMigrated(ctx context.Context, from, to int64) error {
	return s.st.MigrateChat(ctx, from, to)
}

// Members lists active members of a team (owner only).
func (s *Service) Members(ctx context.Context, ownerID int64, teamID string) ([]*member.Membership, error) {
	if _, err := s.OwnerContext(ctx, ownerID, teamID); err != nil {
		return nil, err
	}
	return s.st.TeamMembers(ctx, teamID)
}

// ActiveTeam returns the team the user last chose in the bot, or "" when unknown.
func (s *Service) ActiveTeam(ctx context.Context, userID int64) string {
	u, err := s.st.GetUser(ctx, userID)
	if err != nil {
		return ""
	}
	return u.ActiveTeam
}
