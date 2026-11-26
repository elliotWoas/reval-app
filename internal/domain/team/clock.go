package team

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"taskbot/internal/domain"
	"taskbot/internal/platform/digits"
)

// TimeOfDay is minutes since local midnight.
type TimeOfDay int

// ParseTimeOfDay accepts "9:45", "09:45", "۹:۴۵", "9.45" or "9".
func ParseTimeOfDay(s string) (TimeOfDay, error) {
	s = strings.TrimSpace(digits.ToLatin(s))
	s = strings.NewReplacer("٫", ":", ".", ":", "：", ":", " ", "").Replace(s)
	if s == "" {
		return 0, domain.ErrTimeFormat
	}
	hs, ms, found := strings.Cut(s, ":")
	h, err := strconv.Atoi(hs)
	if err != nil {
		return 0, domain.ErrTimeFormat
	}
	m := 0
	if found {
		if m, err = strconv.Atoi(ms); err != nil {
			return 0, domain.ErrTimeFormat
		}
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, domain.ErrTimeFormat
	}
	return TimeOfDay(h*60 + m), nil
}

// MustTime parses a config value and panics on error (config is validated at boot).
func MustTime(s string) TimeOfDay {
	t, err := ParseTimeOfDay(s)
	if err != nil {
		panic(fmt.Sprintf("bad time %q", s))
	}
	return t
}

// String formats as H:MM.
func (t TimeOfDay) String() string { return fmt.Sprintf("%d:%02d", int(t)/60, int(t)%60) }

// On returns the instant of t on the local date (a midnight in loc).
func (t TimeOfDay) On(day time.Time) time.Time {
	y, m, d := day.Date()
	return time.Date(y, m, d, int(t)/60, int(t)%60, 0, 0, day.Location())
}

// Of returns the time of day of an instant in its own location.
func Of(ts time.Time) TimeOfDay { return TimeOfDay(ts.Hour()*60 + ts.Minute()) }

// DateLayout is the storage layout of a local work date.
const DateLayout = "2006-01-02"

// ParseDate parses a YYYY-MM-DD string as local midnight in loc.
func ParseDate(s string, loc *time.Location) (time.Time, error) {
	return time.ParseInLocation(DateLayout, s, loc)
}

// AddDays shifts a YYYY-MM-DD date string.
func AddDays(date string, n int) string {
	t, err := time.Parse(DateLayout, date)
	if err != nil {
		return date
	}
	return t.AddDate(0, 0, n).Format(DateLayout)
}

// WeekStart returns the Saturday on or before the date (Iranian week).
func WeekStart(date string) string {
	t, err := time.Parse(DateLayout, date)
	if err != nil {
		return date
	}
	back := (int(t.Weekday()) + 1) % 7 // Saturday=6 → 0, Sunday → 1 ...
	return t.AddDate(0, 0, -back).Format(DateLayout)
}

// Weekday returns the weekday of a YYYY-MM-DD string.
func Weekday(date string) time.Weekday {
	t, err := time.Parse(DateLayout, date)
	if err != nil {
		return time.Saturday
	}
	return t.Weekday()
}
