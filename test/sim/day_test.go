// Package sim drives the whole bot through real conversations and full days with a fake clock.
package sim

import (
	"strings"
	"testing"
	"time"

	"taskbot/internal/domain/team"
	"taskbot/internal/testkit"
)

const (
	owner int64 = 1
	sara  int64 = 2
	mahdi int64 = 3
	ali   int64 = 4
	group int64 = -1001
)

// setupTeam creates a team through the real conversation and lets three members join.
func setupTeam(t *testing.T, e *testkit.Env) *team.Team {
	t.Helper()
	e.Text(owner, "/start")
	e.Click(owner, e.Tr("menu.create"))
	e.Text(owner, "تیم مفیست")
	e.Click(owner, e.Tr("common.confirm"))
	e.Click(owner, e.Tr("create.part_yes"))
	e.Text(owner, e.Tr("create.defaults_ok"))
	if e.Client.Count(owner, "ساخته شد") != 1 {
		t.Fatalf("team not created; last: %q", e.Client.Last(owner).Text)
	}
	tms, err := e.Svc.Members.MyTeams(t.Context(), owner)
	if err != nil || len(tms) != 1 {
		t.Fatalf("owner teams: %v %v", tms, err)
	}
	tm := tms[0].Team
	if e.Client.Count(owner, tm.InviteKey) == 0 {
		t.Fatal("invite key not shown")
	}
	for id, name := range map[int64]string{sara: "سارا", mahdi: "مهدی", ali: "علی"} {
		e.Text(id, "/start "+tm.InviteKey)
		e.Click(id, e.Tr("common.yes"))
		e.Text(id, name)
		if e.Client.Count(id, "خوش آمدی") != 1 {
			t.Fatalf("%s did not join: %q", name, e.Client.Last(id).Text)
		}
	}
	// connect the report group
	e.BotAdded(owner, group, "گروه تیم")
	e.Click(owner, e.Tr("group.btn_connect", "team", tm.Name))
	if e.Client.Count(group, "از این به بعد گزارش") != 1 {
		t.Fatal("group test message missing")
	}
	tm, _ = e.Store.GetTeam(t.Context(), tm.ID)
	return tm
}

func TestFullDay(t *testing.T) {
	// Saturday 14 Nov 2026, 08:00 Tehran
	e := testkit.New(t, testkit.Tehran(2026, 11, 14, 8, 0))
	tm := setupTeam(t, e)
	day := func(hh, mm int) time.Time { return testkit.Tehran(2026, 11, 14, hh, mm) }

	e.RunUntil(day(9, 0))
	if n := e.Client.Count(0, "صبح بخیر"); n != 4 {
		t.Fatalf("morning requests = %d, want 4", n)
	}

	// plans: sara 3 tasks, mahdi 1, owner 2; ali nothing
	e.Text(sara, "طراحی صفحه فرود\n!جلسه با مشتری\nبستن گزارش ماهانه")
	if !strings.Contains(e.Client.Last(sara).Text, "۳ کار") {
		t.Fatalf("preview: %q", e.Client.Last(sara).Text)
	}
	e.Click(sara, e.Tr("plan.btn_save"))
	e.Text(mahdi, "پیگیری سرور")
	e.Click(mahdi, e.Tr("plan.btn_save"))
	e.Text(owner, "بررسی قرارداد\nجلسه سرمایه‌گذار")
	e.Click(owner, e.Tr("plan.btn_save"))

	e.RunUntil(day(10, 0))
	if e.Client.Count(ali, "دقیقه تا مهلت") != 1 || e.Client.Count(sara, "دقیقه تا مهلت") != 0 {
		t.Fatal("deadline reminder should reach only ali")
	}

	e.RunUntil(day(10, 30))
	plan := e.Client.Last(group).Text
	if !strings.Contains(plan, "برنامه امروز تیم") || !strings.Contains(plan, "سارا") || !strings.Contains(plan, "هنوز نفرستاده: علی") {
		t.Fatalf("group plan: %q", plan)
	}
	if e.Client.Pins == 0 {
		t.Fatal("team plan not pinned")
	}

	// late plan from ali: group gets a short note and the pinned plan is edited
	e.RunUntil(day(11, 0))
	e.Text(ali, "کار دیرهنگام")
	e.Click(ali, e.Tr("plan.btn_save"))
	if e.Client.Count(group, "با تأخیر فرستاد") != 1 {
		t.Fatal("late plan notice missing")
	}
	edited := false
	for _, m := range e.Client.To(group) {
		if m.Edited && strings.Contains(m.Text, "علی") {
			edited = true
		}
	}
	if !edited {
		t.Fatal("team plan was not edited with the late plan")
	}

	// check-in at 13:00, mahdi is stuck
	e.RunUntil(day(13, 0))
	if n := e.Client.Count(0, e.Tr("checkin.ask")); n != 4 {
		t.Fatalf("check-ins = %d, want 4", n)
	}
	e.Click(mahdi, e.Tr("checkin.btn_stuck"))
	if e.Client.Count(owner, "گیر کرده") != 1 {
		t.Fatal("stuck alert not sent to owner")
	}
	// sara marks her first task done during the day
	e.Click(sara, e.Tr("checkin.btn_each"))
	e.ClickWhere(sara, func(b testkit.Btn) bool {
		return strings.HasPrefix(b.Data, "ck|p|") && strings.HasSuffix(b.Data, "|1|100")
	})

	// evening
	e.RunUntil(day(21, 0))
	if n := e.Client.Count(0, e.Tr("evening.intro")); n != 4 {
		t.Fatalf("evening requests = %d", n)
	}
	e.ClickWhere(sara, func(b testkit.Btn) bool {
		return strings.HasPrefix(b.Data, "ev|f|") && strings.HasSuffix(b.Data, "|2|not_done")
	})
	e.Click(sara, e.Tr("reasons.no_time"))
	e.ClickWhere(sara, func(b testkit.Btn) bool {
		return strings.HasPrefix(b.Data, "ev|f|") && strings.HasSuffix(b.Data, "|3|partial")
	})
	e.Click(sara, e.Tr("evening.btn_send"))
	e.Text(sara, e.Tr("evening.no_note"))

	e.RunUntil(day(22, 0))
	if e.Client.Count(sara, "تا جمع‌بندی") != 0 || e.Client.Count(ali, "تا جمع‌بندی") != 1 {
		t.Fatal("summary reminder should skip sara and reach ali")
	}

	e.RunUntil(day(22, 30))
	sum := e.Client.Last(group).Text
	if !strings.Contains(sum, "جمع‌بندی امروز") || !strings.Contains(sum, "سارا ✅❌◑") {
		t.Fatalf("group summary: %q", sum)
	}
	if strings.Contains(sum, e.Tr("reasons.no_time")) {
		t.Fatal("fail reasons must not appear in the group")
	}
	ownerSum := e.Client.Last(owner).Text
	if !strings.Contains(ownerSum, e.Tr("reasons.no_time")) {
		t.Fatalf("owner summary lacks reasons: %q", ownerSum)
	}

	// restart: rewind the cursor and tick again; nothing may be sent twice
	before := len(e.Client.Msgs)
	if err := e.Store.SetTeamRun(t.Context(), tm.ID, day(8, 0), day(8, 0)); err != nil {
		t.Fatal(err)
	}
	e.Tick()
	if len(e.Client.Msgs) != before {
		t.Fatalf("restart re-sent %d messages", len(e.Client.Msgs)-before)
	}
}

func TestBlockedUserAndRetry(t *testing.T) {
	e := testkit.New(t, testkit.Tehran(2026, 11, 14, 8, 0))
	setupTeam(t, e)
	e.Client.Blocked[ali] = true
	e.Client.Failures = 1 // first send fails temporarily and is retried
	e.RunUntil(testkit.Tehran(2026, 11, 14, 9, 0))
	e.Clock.Advance(10 * time.Second)
	e.Flush()
	u, _ := e.Store.GetUser(t.Context(), ali)
	if !u.Blocked {
		t.Fatal("ali should be marked blocked")
	}
	if n := e.Client.Count(0, "صبح بخیر"); n != 3 {
		t.Fatalf("morning messages = %d, want 3 (retry must deliver, blocked user skipped)", n)
	}
	e.RunUntil(testkit.Tehran(2026, 11, 14, 10, 30))
	if !strings.Contains(e.Client.Last(owner).Text, "دسترسی قطع") {
		t.Fatalf("owner plan should show blocked user: %q", e.Client.Last(owner).Text)
	}
}

func TestGroupMigrationAndLoss(t *testing.T) {
	e := testkit.New(t, testkit.Tehran(2026, 11, 14, 8, 0))
	tm := setupTeam(t, e)
	if err := e.Svc.Members.GroupMigrated(t.Context(), group, -2002); err != nil {
		t.Fatal(err)
	}
	got, _ := e.Store.GetTeam(t.Context(), tm.ID)
	if got.GroupChatID != -2002 {
		t.Fatalf("group id not migrated: %d", got.GroupChatID)
	}
	e.Client.Blocked[-2002] = true
	e.RunUntil(testkit.Tehran(2026, 11, 14, 10, 30))
	got, _ = e.Store.GetTeam(t.Context(), tm.ID)
	if got.HasGroup() {
		t.Fatal("lost group should be detached")
	}
	if e.Client.Count(owner, "دسترسی من به گروه") != 1 {
		t.Fatal("owner not told about lost group")
	}
}
