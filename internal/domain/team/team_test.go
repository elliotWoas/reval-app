package team

import (
	"errors"
	"testing"
	"time"

	"taskbot/internal/domain"
)

func sched() Schedule {
	return Schedule{
		WorkDays: []int{6, 0, 1, 2, 3},
		Morning:  MustTime("09:00"), Deadline: MustTime("10:30"),
		Checkins: []TimeOfDay{MustTime("13:00"), MustTime("17:30")},
		Evening:  MustTime("21:00"), Summary: MustTime("22:30"), Weekly: MustTime("22:45"),
		ReminderBefore: 30,
	}
}

func TestParseTimeOfDay(t *testing.T) {
	cases := map[string]string{"9:45": "9:45", "۹:۴۵": "9:45", "09.05": "9:05", "21": "21:00", "٢١:٣٠": "21:30"}
	for in, want := range cases {
		got, err := ParseTimeOfDay(in)
		if err != nil || got.String() != want {
			t.Errorf("%q: got %v %v, want %s", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "25:00", "9:61", "abc"} {
		if _, err := ParseTimeOfDay(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestValidate(t *testing.T) {
	s := sched()
	if err := s.Validate(60); err != nil {
		t.Fatal(err)
	}
	s.Deadline = MustTime("08:00")
	if !errors.Is(s.Validate(60), domain.ErrTimeOrder) {
		t.Fatal("expected order error")
	}
	s = sched()
	s.Evening, s.Summary, s.Deadline = MustTime("11:00"), MustTime("11:15"), MustTime("10:45")
	if !errors.Is(s.Validate(60), domain.ErrTimeGap) {
		t.Fatal("expected gap error")
	}
}

func TestWeekStartAndEvents(t *testing.T) {
	if got := WeekStart("2026-11-18"); got != "2026-11-14" { // Wednesday → Saturday
		t.Fatalf("week start %s", got)
	}
	tm := &Team{Timezone: "Asia/Tehran", Schedule: sched()}
	sat := tm.EventsOn("2026-11-14")
	if sat[0].Kind != EvLeaveDigest || sat[1].Kind != EvMorning {
		t.Fatalf("saturday should start with leave digest then morning: %+v", sat)
	}
	wed := tm.EventsOn("2026-11-18")
	if wed[len(wed)-1].Kind != EvWeekly {
		t.Fatalf("wednesday should end with weekly: %+v", wed)
	}
	if len(tm.EventsOn("2026-11-19")) != 0 { // Thursday off
		t.Fatal("thursday has events")
	}
	var kinds []string
	for _, e := range wed {
		kinds = append(kinds, e.Key())
	}
	want := []string{"morning", "remind_plan", "deadline", "checkin1", "checkin2", "evening", "remind_report", "summary", "weekly"}
	if len(kinds) != len(want) {
		t.Fatalf("kinds %v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds %v", kinds)
		}
	}
	// 09:00 Tehran (UTC+3:30) = 05:30 UTC
	if got := wed[0].At; !got.Equal(time.Date(2026, 11, 18, 5, 30, 0, 0, time.UTC)) {
		t.Fatalf("morning at %v", got)
	}
	tm.OffDays = []string{"2026-11-18"}
	if len(tm.EventsOn("2026-11-18")) != 0 {
		t.Fatal("one-off holiday still has events")
	}
	// weekly moves to the last real work day
	tue := tm.EventsOn("2026-11-17")
	if tue[len(tue)-1].Kind != EvWeekly {
		t.Fatal("weekly should move to tuesday when wednesday is off")
	}
}

func TestNextEventAndMeaningful(t *testing.T) {
	tm := &Team{Timezone: "Asia/Tehran", Schedule: sched()}
	// Wednesday 23:00 Tehran → next is Saturday leave digest 09:00
	at := time.Date(2026, 11, 18, 19, 30, 0, 0, time.UTC)
	ev, ok := tm.NextEventAfter(at)
	if !ok || ev.Date != "2026-11-21" || ev.Kind != EvLeaveDigest {
		t.Fatalf("next %+v", ev)
	}
	morning := tm.EventsOn("2026-11-18")[0]
	if !tm.StillMeaningful(morning, morning.At.Add(time.Hour)) {
		t.Fatal("morning still meaningful before deadline")
	}
	if tm.StillMeaningful(morning, morning.At.Add(2*time.Hour)) {
		t.Fatal("morning after deadline should be skipped")
	}
}

func TestEveryHours(t *testing.T) {
	got := EveryHours(MustTime("10:30"), MustTime("21:00"), 5)
	if len(got) != 2 || got[0] != MustTime("15:30") || got[1] != MustTime("20:30") {
		t.Fatalf("%v", got)
	}
}
