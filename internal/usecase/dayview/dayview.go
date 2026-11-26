// Package dayview builds the per-member rows of one team day, shared by the daily cycle,
// the owner "today status" screen and reports.
package dayview

import (
	"context"
	"strings"

	"taskbot/internal/domain"

	"taskbot/internal/domain/leave"
	"taskbot/internal/domain/member"
	"taskbot/internal/domain/plan"
	"taskbot/internal/domain/team"
	"taskbot/internal/usecase/ports"
)

// Build loads plans, leaves and users for the members in the cycle on date.
func Build(ctx context.Context, st ports.Store, t *team.Team, date string) (ports.DayView, error) {
	v := ports.DayView{Team: t, Date: date}
	members, err := st.TeamMembers(ctx, t.ID)
	if err != nil {
		return v, err
	}
	plans, err := st.TeamPlans(ctx, t.ID, date)
	if err != nil {
		return v, err
	}
	leaves, err := st.TeamLeavesRange(ctx, t.ID, date, date)
	if err != nil {
		return v, err
	}
	byMember := map[string]*plan.Plan{}
	for _, p := range plans {
		byMember[p.MembershipID] = p
	}
	leaveBy := map[string]*leave.Leave{}
	for _, l := range leaves {
		if l.Effective(t.LeaveApproval) {
			leaveBy[l.MembershipID] = l
		}
	}
	for _, m := range members {
		if !m.InCycle(date) {
			continue
		}
		row := ports.PlanRow{Member: m, Plan: byMember[m.ID], Leave: leaveBy[m.ID]}
		if u, err := st.GetUser(ctx, m.UserID); err == nil {
			row.Blocked = u.Blocked
		}
		v.Rows = append(v.Rows, row)
	}
	return v, nil
}

// EffectiveLeave returns the member's effective leave on date, or nil.
func EffectiveLeave(ctx context.Context, st ports.Store, t *team.Team, membershipID, date string) (*leave.Leave, error) {
	l, err := st.FindLeave(ctx, membershipID, date)
	if err != nil || l == nil {
		return nil, err
	}
	if !l.Effective(t.LeaveApproval) {
		return nil, nil
	}
	return l, nil
}

// PreviousPlan returns the member's latest plan before date (looking back up to 14 days).
func PreviousPlan(ctx context.Context, st ports.Store, m *member.Membership, date string) (*plan.Plan, error) {
	plans, err := st.MemberPlansRange(ctx, m.ID, team.AddDays(date, -14), team.AddDays(date, -1))
	if err != nil {
		return nil, err
	}
	for i := len(plans) - 1; i >= 0; i-- {
		if plans[i].HasTasks() {
			return plans[i], nil
		}
	}
	return nil, nil
}

// Counts sums the task counts of all rows.
func Counts(v ports.DayView) plan.Counts {
	var c plan.Counts
	for _, r := range v.Rows {
		c = c.Add(r.Plan.Count())
	}
	return c
}

// Owned loads a plan by id and checks it belongs to userID. It returns the plan, its team
// and the membership.
func Owned(ctx context.Context, st ports.Store, userID int64, planID string) (*plan.Plan, *team.Team, *member.Membership, error) {
	i := strings.LastIndex(planID, ":")
	if i <= 0 {
		return nil, nil, nil, domain.ErrNotFound
	}
	p, err := st.FindPlan(ctx, planID[:i], planID[i+1:])
	if err != nil {
		return nil, nil, nil, err
	}
	if p == nil {
		return nil, nil, nil, domain.ErrNoPlan
	}
	if p.UserID != userID {
		return nil, nil, nil, domain.ErrForbidden
	}
	m, err := st.GetMembership(ctx, p.MembershipID)
	if err != nil {
		return nil, nil, nil, err
	}
	if !m.Active() {
		return nil, nil, nil, domain.ErrForbidden
	}
	t, err := st.GetTeam(ctx, p.TeamID)
	if err != nil {
		return nil, nil, nil, err
	}
	return p, t, m, nil
}
