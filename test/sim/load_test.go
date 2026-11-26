package sim

import (
	"fmt"
	"testing"
	"time"

	"taskbot/internal/outbox"
	"taskbot/internal/testkit"
	"taskbot/internal/usecase/onboarding"
)

// TestLoadManyTeams simulates several hundred teams through a whole day.
// Every team fires at 09:00 at the same time; nothing may be lost or duplicated.
func TestLoadManyTeams(t *testing.T) {
	if testing.Short() {
		t.Skip("load test")
	}
	const teams, perTeam = 300, 8
	e := testkit.New(t, testkit.Tehran(2026, 11, 14, 7, 0))
	ctx := t.Context()
	var users int64 = 1000
	for i := 0; i < teams; i++ {
		users++
		owner := users
		tm, _, err := e.Svc.Onboarding.CreateTeam(ctx, onboarding.Input{OwnerID: owner, OwnerName: "owner", Name: fmt.Sprintf("team %d", i),
			Participates: false, Schedule: onboarding.DefaultSchedule(e.Cfg)})
		if err != nil {
			t.Fatal(err)
		}
		for j := 0; j < perTeam; j++ {
			users++
			if _, err := e.Svc.Members.Join(ctx, users, tm.ID, fmt.Sprintf("m%d", j)); err != nil {
				t.Fatal(err)
			}
		}
	}
	e.Flush()
	start := time.Now()
	e.RunUntil(testkit.Tehran(2026, 11, 14, 9, 1))
	morning := e.Client.Count(0, "صبح بخیر")
	if morning != teams*perTeam {
		t.Fatalf("morning messages = %d, want %d", morning, teams*perTeam)
	}
	e.RunUntil(testkit.Tehran(2026, 11, 14, 23, 0))
	elapsed := time.Since(start)
	// nobody sent a plan: deadline reminder + team plan for owners, no check-ins/evening
	if n := e.Client.Count(0, "دقیقه تا مهلت"); n != teams*perTeam {
		t.Fatalf("reminders = %d", n)
	}
	if n := e.Client.Count(0, "برنامه امروز تیم"); n != teams {
		t.Fatalf("team plans = %d, want %d (owners only, no groups)", n, teams)
	}
	if n := e.Client.Count(0, "کارها چطور"); n != 0 {
		t.Fatalf("check-ins sent to members without tasks: %d", n)
	}
	t.Logf("simulated %d teams / %d members for a day in %v (%d messages)", teams, teams*perTeam, elapsed, len(e.Client.Msgs))
}

// TestLimiterRates checks the token buckets: global, per chat and per group.
func TestLimiterRates(t *testing.T) {
	l := outbox.NewLimiter(25, 1, 20)
	now := time.Unix(0, 0)
	sent := 0
	for i := int64(0); i < 100; i++ {
		if ok, _ := l.TryReserve(now, i, false); ok {
			sent++
		}
	}
	if sent != 25 {
		t.Fatalf("global burst = %d, want 25", sent)
	}
	now = now.Add(time.Second)
	l = outbox.NewLimiter(1000, 1, 20)
	groupSent := 0
	for i := 0; i < 100; i++ {
		if ok, _ := l.TryReserve(now, -5, true); ok {
			groupSent++
		}
	}
	if groupSent != 20 {
		t.Fatalf("group burst = %d, want 20 per minute", groupSent)
	}
	if ok, w := l.TryReserve(now, -5, true); ok || w < 2*time.Second {
		t.Fatalf("group should wait ~3s, got ok=%v wait=%v", ok, w)
	}
}
