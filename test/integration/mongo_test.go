// Package integration runs the Mongo store against a real database.
// Set MONGO_TEST_URI (e.g. an Atlas test cluster); the test uses a throw-away database.
package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"taskbot/internal/domain"
	"taskbot/internal/domain/member"
	"taskbot/internal/domain/plan"
	"taskbot/internal/domain/task"
	"taskbot/internal/domain/team"
	"taskbot/internal/outbox"
	mongorepo "taskbot/internal/repository/mongo"
)

func store(t *testing.T) *mongorepo.Store {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set")
	}
	ctx := context.Background()
	db := fmt.Sprintf("taskbot_test_%d", time.Now().UnixNano())
	s, err := mongorepo.Connect(ctx, uri, db, 10, 15*time.Second, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = s.DropDatabase(context.Background())
		_ = s.Close(context.Background())
	})
	if err := s.EnsureIndexes(ctx, 90*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureIndexes(ctx, 90*24*time.Hour); err != nil { // idempotent
		t.Fatal(err)
	}
	return s
}

func TestMongoStore(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	tm := &team.Team{ID: "t1", Name: "x", OwnerID: 1, InviteKey: "AAAA-BBBB", Timezone: "Asia/Tehran", NextRunAt: now}
	if err := s.CreateTeam(ctx, tm); err != nil {
		t.Fatal(err)
	}
	dup := *tm
	dup.ID = "t2"
	if err := s.CreateTeam(ctx, &dup); err != domain.ErrDuplicate {
		t.Fatalf("duplicate invite key: %v", err)
	}
	due, err := s.DueTeams(ctx, now.Add(time.Second), 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("due teams: %v %v", due, err)
	}

	m := &member.Membership{ID: "m1", TeamID: "t1", UserID: 7, Status: member.StatusActive, JoinedAt: now}
	if err := s.CreateMembership(ctx, m); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.FindMembership(ctx, "t1", 7); got == nil || got.ID != "m1" {
		t.Fatal("membership not found")
	}

	p := plan.New("m1", "t1", 7, "2026-11-18")
	p.Append([]task.Task{{Text: "a"}, {Text: "b"}}, false)
	if err := s.SavePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	tk := p.Tasks[1]
	tk.Final = task.Done
	if err := s.UpdateTask(ctx, p.ID, tk); err != nil {
		t.Fatal(err)
	}
	got, _ := s.FindPlan(ctx, "m1", "2026-11-18")
	if got.Tasks[1].Final != task.Done || got.Tasks[0].Final != "" {
		t.Fatalf("atomic task update: %+v", got.Tasks)
	}

	ok, _ := s.ClaimJob(ctx, "t1", "morning", "2026-11-18")
	ok2, _ := s.ClaimJob(ctx, "t1", "morning", "2026-11-18")
	if !ok || !ok2 {
		t.Fatal("unfinished job should stay claimable")
	}
	_ = s.FinishJob(ctx, "t1", "morning", "2026-11-18")
	if ok, _ := s.ClaimJob(ctx, "t1", "morning", "2026-11-18"); ok {
		t.Fatal("finished job claimed again")
	}

	items := []outbox.Item{{ChatID: 1, Text: "x", Dedup: "k1", NotBefore: now, CreatedAt: now}, {ChatID: 1, Text: "y", NotBefore: now, CreatedAt: now}}
	if err := s.Enqueue(ctx, items...); err != nil {
		t.Fatal(err)
	}
	if err := s.Enqueue(ctx, outbox.Item{ChatID: 1, Text: "x again", Dedup: "k1", NotBefore: now, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	dueItems, _ := s.Due(ctx, now.Add(time.Second), 10)
	if len(dueItems) != 2 || dueItems[0].Text != "x" {
		t.Fatalf("outbox: %+v", dueItems)
	}

	a, _ := s.AcquireLock(ctx, "leader", "A", time.Minute)
	b, _ := s.AcquireLock(ctx, "leader", "B", time.Minute)
	a2, _ := s.AcquireLock(ctx, "leader", "A", time.Minute)
	if !a || b || !a2 {
		t.Fatalf("lock: a=%v b=%v renew=%v", a, b, a2)
	}
}
