// Package task holds a single planned task and parsing of the morning message.
package task

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"taskbot/internal/domain"
	"taskbot/internal/platform/digits"
)

// Final statuses set in the evening report.
const (
	Done    = "done"
	Partial = "partial"
	NotDone = "not_done"
)

// Sources.
const (
	SourceNew     = "new"
	SourceCarried = "carried"
)

// Task is one line of a member's daily plan.
type Task struct {
	ID          int        `bson:"id"`
	Text        string     `bson:"text"`
	Important   bool       `bson:"important"`
	Progress    int        `bson:"progress"` // last check-in percent, -1 when unknown
	Final       string     `bson:"final,omitempty"`
	Reason      string     `bson:"reason,omitempty"`
	Source      string     `bson:"source"`
	CarriedDays int        `bson:"carriedDays"` // consecutive days carried over
	AddedLate   bool       `bson:"addedLate"`
	DoneAt      *time.Time `bson:"doneAt,omitempty"`
	OwnerNote   string     `bson:"ownerNote,omitempty"`
}

// Open reports whether the task is not finished (candidate for carry-over).
func (t *Task) Open() bool { return t.Final != Done }

// EffectiveFinal returns the final status, or one derived from check-in progress.
func (t *Task) EffectiveFinal() string {
	if t.Final != "" {
		return t.Final
	}
	switch {
	case t.Progress >= 100:
		return Done
	case t.Progress > 0:
		return Partial
	}
	return ""
}

var bullet = regexp.MustCompile(`^\s*(?:[-*•▪◦–—]+\s*|\(?[0-9]+[.)]\s*|[0-9]+\s*-\s+)`)

// Parse splits a message into tasks: one per line, numbering/bullets removed,
// a leading '!' marks the task important. Empty lines are skipped.
func Parse(text string, maxTasks, maxLen int) ([]Task, error) {
	var out []Task
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(bullet.ReplaceAllString(strings.TrimSpace(line), ""))
		line = stripLeadingNumber(line)
		if line == "" {
			continue
		}
		important := false
		if strings.HasPrefix(line, "!") || strings.HasPrefix(line, "！") {
			important = true
			line = strings.TrimSpace(strings.TrimLeft(line, "!！"))
		}
		if line == "" {
			continue
		}
		if utf8.RuneCountInString(line) > maxLen {
			line = string([]rune(line)[:maxLen])
		}
		out = append(out, Task{Text: line, Important: important, Progress: -1, Source: SourceNew})
	}
	if len(out) == 0 {
		return nil, domain.ErrNoTasks
	}
	if len(out) > maxTasks {
		return nil, domain.ErrTooManyTasks
	}
	return out, nil
}

var persianNumbering = regexp.MustCompile(`^\s*\(?[۰-۹٠-٩]+\s*[.)]\s*`)

func stripLeadingNumber(s string) string {
	return strings.TrimSpace(persianNumbering.ReplaceAllString(s, ""))
}

// Normalize maps digits so callers can compare user input.
func Normalize(s string) string { return digits.ToLatin(strings.TrimSpace(s)) }
