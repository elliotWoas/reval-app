// Package callbackdata encodes inline button data compactly. Telegram allows 64 bytes.
//
// Format: "<prefix>|<arg>|<arg>..." where prefix is a short action code.
package callbackdata

import (
	"strconv"
	"strings"
)

// MaxLen is Telegram's limit for callback_data.
const MaxLen = 64

// Action codes.
const (
	MainCreate     = "mc" // start create-team flow
	MainJoin       = "mj" // start join flow
	MainMore       = "mm" // "more" menu items
	CreateName     = "cn" // confirm/change team name
	CreatePart     = "cp" // owner participates yes/no
	DayToggle      = "dt" // manual work-day toggle
	JoinConfirm    = "jc" // join this team yes/no
	JoinDecide     = "ja" // owner approves join
	InviteForward  = "if" // build forwardable invite message
	GroupConnect   = "gc" // connect group to team
	GroupSkip      = "gs" // skip group step
	TeamPick       = "tp" // choose team for a pending action
	Carry          = "cr" // carry yesterday's open tasks
	PlanPreview    = "pp" // plan preview actions
	PlanView       = "pv" // today plan actions
	Checkin        = "ck"
	Evening        = "ev"
	LeaveStart     = "ls" // open leave flow (today shortcut)
	LeavePick      = "lp" // toggle date / page / continue
	LeaveType      = "lt" // full / half
	LeaveNote      = "ln" // no note
	LeaveConfirm   = "lf"
	LeaveCancel    = "lc"
	LeaveDecide    = "la"
	LeaveTodayPlan = "lx" // move or drop today's open tasks
	OwnerRemind    = "rm"
	MemberAction   = "ma" // remove / transfer
	MemberConfirm  = "mx"
	TransferAnswer = "ta"
	KeyRotate      = "kr"
	Settings       = "st"
	OwnerNote      = "on"
	Cancel         = "xx"
	Noop           = "no"
)

// Encode joins parts. It panics in tests when the result is too long, so a long id is caught early.
func Encode(parts ...string) string {
	s := strings.Join(parts, "|")
	if len(s) > MaxLen {
		panic("callback data too long: " + s)
	}
	return s
}

// Data is decoded callback data.
type Data []string

// Decode splits callback data.
func Decode(s string) Data { return strings.Split(s, "|") }

// Prefix returns the action code.
func (d Data) Prefix() string { return d.Arg(0) }

// Arg returns part i or "".
func (d Data) Arg(i int) string {
	if i < len(d) {
		return d[i]
	}
	return ""
}

// Int returns part i as int (0 when invalid).
func (d Data) Int(i int) int { n, _ := strconv.Atoi(d.Arg(i)); return n }

// Int64 returns part i as int64.
func (d Data) Int64(i int) int64 { n, _ := strconv.ParseInt(d.Arg(i), 10, 64); return n }
