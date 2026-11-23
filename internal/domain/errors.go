// Package domain holds typed business errors shared by all domain packages.
// Transport maps each code to a localized message (locales key "errors.<code>").
package domain

import "errors"

// Error is a typed business error.
type Error struct {
	Code string
}

func (e *Error) Error() string { return e.Code }

// E creates a typed error.
func E(code string) *Error { return &Error{Code: code} }

// Code extracts the code of a typed error, or "" when err is not one.
func Code(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

// Shared error codes.
var (
	ErrNotFound         = E("not_found")
	ErrForbidden        = E("forbidden")
	ErrTeamNameLength   = E("team_name_length")
	ErrTimeFormat       = E("time_format")
	ErrTimeOrder        = E("time_order")
	ErrTimeGap          = E("time_gap")
	ErrNoWorkDays       = E("no_work_days")
	ErrKeyNotFound      = E("key_not_found")
	ErrTooManyAttempts  = E("too_many_attempts")
	ErrAlreadyMember    = E("already_member")
	ErrPendingApproval  = E("pending_approval")
	ErrNotOwner         = E("not_owner")
	ErrOwnerCannotLeave = E("owner_cannot_leave")
	ErrNoTasks          = E("no_tasks")
	ErrTooManyTasks     = E("too_many_tasks")
	ErrNotWorkDay       = E("not_work_day")
	ErrOnLeave          = E("on_leave")
	ErrNoPlan           = E("no_plan")
	ErrWindowClosed     = E("window_closed")
	ErrLeaveExists      = E("leave_exists")
	ErrLeaveTooFar      = E("leave_too_far")
	ErrNotGroupAdmin    = E("not_group_admin")
	ErrNameLength       = E("name_length")
	ErrSelf             = E("self")
	ErrDuplicate        = E("duplicate")
)
