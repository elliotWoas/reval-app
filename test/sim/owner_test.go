package sim

import (
	"strings"
	"testing"

	"taskbot/internal/domain/member"
	"taskbot/internal/domain/team"
	"taskbot/internal/testkit"
)

func TestOwnerTools(t *testing.T) {
	e := testkit.New(t, testkit.Tehran(2026, 11, 14, 8, 0))
	tm := setupTeam(t, e)

	// change the morning time: invalid custom time is refused, a valid one is saved
	e.Text(owner, e.Tr("menu.settings"))
	e.Click(owner, e.Tr("settings.morning"))
	e.Text(owner, e.Tr("time.custom"))
	e.Text(owner, "۱۱:۰۰") // after the 10:30 deadline → order error
	if !strings.Contains(strings.Join(texts(e, owner, 3), "\n"), e.Tr("errors.time_order")) {
		t.Fatalf("expected order error, got %q", e.Client.Last(owner).Text)
	}
	e.Text(owner, "۸:۳۰")
	got, _ := e.Store.GetTeam(t.Context(), tm.ID)
	if got.Schedule.Morning != team.MustTime("08:30") {
		t.Fatalf("morning = %v", got.Schedule.Morning)
	}

	// toggle manual approval, then a new member needs approval
	e.Text(owner, e.Tr("menu.settings"))
	e.Click(owner, e.Tr("settings.approval", "v", e.Tr("settings.off")))
	got, _ = e.Store.GetTeam(t.Context(), tm.ID)
	if !got.ManualApproval {
		t.Fatal("manual approval not enabled")
	}
	const neda int64 = 5
	e.Text(neda, "/start "+got.InviteKey)
	e.Click(neda, e.Tr("common.yes"))
	e.Text(neda, "ندا")
	if e.Client.Count(neda, "برای مالک تیم") != 1 {
		t.Fatal("join should be pending")
	}
	e.Click(owner, e.Tr("common.approve"))
	if e.Client.Count(neda, "تأیید شد") != 1 {
		t.Fatal("neda not told about approval")
	}

	// rotate the key: old key stops working, members stay
	oldKey := got.InviteKey
	e.Text(owner, e.Tr("menu.invite"))
	e.Click(owner, e.Tr("invite.btn_rotate"))
	e.Click(owner, e.Tr("common.yes"))
	got, _ = e.Store.GetTeam(t.Context(), tm.ID)
	if got.InviteKey == oldKey {
		t.Fatal("key not rotated")
	}
	const x int64 = 9
	e.Text(x, "/start "+oldKey)
	if !strings.Contains(e.Client.Last(x).Text, e.Tr("errors.key_not_found")) {
		t.Fatal("old key still works")
	}

	// remove a member
	e.Text(owner, e.Tr("menu.members"))
	e.Click(owner, "❌ علی")
	e.Click(owner, e.Tr("common.yes"))
	m, _ := e.Store.FindMembership(t.Context(), tm.ID, ali)
	if m.Status != member.StatusRemoved {
		t.Fatal("ali not removed")
	}

	// status screen and manual reminder
	e.RunUntil(testkit.Tehran(2026, 11, 14, 9, 0))
	e.Text(owner, e.Tr("menu.status"))
	if !strings.Contains(e.Client.Last(owner).Text, "هنوز نفرستاده") {
		t.Fatalf("status: %q", e.Client.Last(owner).Text)
	}
	e.Click(owner, e.Tr("status.btn_remind", "name", "سارا"))
	if e.Client.Count(sara, "مالک تیم یادآوری کرد") != 1 {
		t.Fatal("manual reminder not delivered")
	}

	// ownership transfer to sara
	e.Text(owner, e.Tr("menu.members"))
	sm, _ := e.Store.FindMembership(t.Context(), tm.ID, sara)
	e.ClickWhere(owner, func(b testkit.Btn) bool { return b.Data == "ma|tr|"+sm.ID })
	e.Click(owner, e.Tr("common.yes"))
	e.Click(sara, e.Tr("common.accept"))
	got, _ = e.Store.GetTeam(t.Context(), tm.ID)
	if got.OwnerID != sara {
		t.Fatal("ownership not transferred")
	}

	// a member leaves; the owner cannot
	e.Text(mahdi, e.Tr("menu.more"))
	e.Click(mahdi, e.Tr("more.leave_team"))
	e.Click(mahdi, e.Tr("common.yes"))
	m, _ = e.Store.FindMembership(t.Context(), tm.ID, mahdi)
	if m.Status != member.StatusLeft {
		t.Fatal("mahdi did not leave")
	}
	if e.Client.Count(sara, "از تیم خارج شد") != 1 {
		t.Fatal("new owner not told")
	}
}

func TestMultiTeamHeaderAndPicker(t *testing.T) {
	e := testkit.New(t, testkit.Tehran(2026, 11, 14, 8, 0))
	setupTeam(t, e)
	// sara creates her own team too
	e.Text(sara, e.Tr("menu.more"))
	e.Click(sara, e.Tr("menu.create"))
	e.Text(sara, "تیم دوم")
	e.Click(sara, e.Tr("common.confirm"))
	e.Click(sara, e.Tr("create.part_yes"))
	e.Text(sara, e.Tr("create.defaults_ok"))
	e.RunUntil(testkit.Tehran(2026, 11, 14, 9, 0))
	if n := e.Client.Count(sara, "🏷"); n < 2 {
		t.Fatalf("morning messages should carry team headers, got %d", n)
	}
	e.Text(sara, "کار مشترک")
	e.Click(sara, "تیم دوم")
	e.Click(sara, e.Tr("plan.btn_save"))
	if !strings.Contains(e.Client.Last(sara).Text, "تیم دوم") {
		t.Fatalf("plan should be saved for the picked team: %q", e.Client.Last(sara).Text)
	}
}

func texts(e *testkit.Env, chat int64, n int) []string {
	ms := e.Client.To(chat)
	var out []string
	for i := len(ms) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, ms[i].Text)
	}
	return out
}
