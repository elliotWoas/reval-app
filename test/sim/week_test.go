package sim

import (
	"strings"
	"testing"
	"time"

	"taskbot/internal/testkit"
)

// TestFullWeek runs Saturday→Wednesday: mahdi takes Monday off through the leave flow,
// everyone else plans and reports, and the weekly report arrives in three versions.
func TestFullWeek(t *testing.T) {
	e := testkit.New(t, testkit.Tehran(2026, 11, 14, 8, 0)) // Saturday
	setupTeam(t, e)

	// mahdi requests Monday (16 Nov) off, full day, without a note
	e.Text(mahdi, e.Tr("menu.leave"))
	e.ClickWhere(mahdi, func(b testkit.Btn) bool { return b.Data == "lp|t|20261116" })
	e.Click(mahdi, e.Tr("leave.continue"))
	e.Click(mahdi, e.Tr("leave.full"))
	e.Text(mahdi, e.Tr("leave.no_note"))
	e.Click(mahdi, e.Tr("leave.btn_submit"))
	if e.Client.Count(owner, "مرخصی کامل گرفت") != 1 {
		t.Fatalf("owner not told about the leave: %q", e.Client.Last(owner).Text)
	}

	days := []int{14, 15, 16, 17, 18} // Sat..Wed
	for _, d := range days {
		at := func(hh, mm int) time.Time { return testkit.Tehran(2026, 11, d, hh, mm) }
		e.RunUntil(at(9, 5))
		for _, id := range []int64{owner, sara, mahdi, ali} {
			if id == mahdi && d == 16 {
				continue
			}
			if id == ali && d == 17 {
				continue // ali skips Tuesday entirely
			}
			e.Text(id, "کار اول\nکار دوم")
			e.Click(id, e.Tr("plan.btn_save"))
		}
		e.RunUntil(at(21, 1))
		for _, id := range []int64{owner, sara, mahdi, ali} {
			if (id == mahdi && d == 16) || (id == ali && d == 17) {
				continue
			}
			e.ClickWhere(id, func(b testkit.Btn) bool {
				return strings.HasPrefix(b.Data, "ev|f|") && strings.HasSuffix(b.Data, "|1|done")
			})
			final := "|2|done"
			if id == sara {
				final = "|2|partial"
			}
			e.ClickWhere(id, func(b testkit.Btn) bool {
				return strings.HasPrefix(b.Data, "ev|f|") && strings.HasSuffix(b.Data, final)
			})
			e.Click(id, e.Tr("evening.btn_send"))
			e.Text(id, e.Tr("evening.no_note"))
		}
		e.RunUntil(at(23, 0))
		if d == 16 {
			if e.Client.Count(mahdi, "۲۵ آبان") != 0 && e.Client.Count(mahdi, "صبح بخیر") > 2 {
				t.Fatal("mahdi got messages on his leave day")
			}
			if !strings.Contains(lastWith(e, group, "جمع‌بندی"), "مهدی 🏖") {
				t.Fatalf("summary should show mahdi on leave: %q", lastWith(e, group, "جمع‌بندی"))
			}
		}
	}
	if n := e.Client.Count(mahdi, "صبح بخیر"); n != 4 {
		t.Fatalf("mahdi morning requests = %d, want 4 (no message on leave day)", n)
	}

	g := lastWith(e, group, "گزارش هفته")
	if g == "" || !strings.Contains(g, "نرخ انجام") || !strings.Contains(g, "منظم‌ترین") {
		t.Fatalf("group weekly: %q", g)
	}
	o := lastWith(e, owner, "گزارش هفته")
	if !strings.Contains(o, "سارا") || !strings.Contains(o, "علی") {
		t.Fatalf("owner weekly: %q", o)
	}
	m := lastWith(e, sara, "هفته")
	if !strings.Contains(m, "نرخ انجام") || strings.Contains(m, "علی") {
		t.Fatalf("member weekly should only show own stats: %q", m)
	}
	// stored stats: ali missed one day, sara has partials
	w, _ := e.Store.FindWeekly(t.Context(), teamID(t, e), "2026-11-14", "")
	if w == nil || w.Counts.Total != 2*(5*4-2) {
		t.Fatalf("team weekly counts: %+v", w)
	}
	// on-demand weekly for a member
	e.Text(sara, e.Tr("menu.weekly"))
	if !strings.Contains(e.Client.Last(sara).Text, "نرخ انجام") {
		t.Fatal("on-demand weekly not confirmed")
	}
}

func lastWith(e *testkit.Env, chat int64, substr string) string {
	ms := e.Client.To(chat)
	for i := len(ms) - 1; i >= 0; i-- {
		if strings.Contains(ms[i].Text, substr) {
			return ms[i].Text
		}
	}
	return ""
}

func teamID(t *testing.T, e *testkit.Env) string {
	tms, err := e.Svc.Members.MyTeams(t.Context(), owner)
	if err != nil || len(tms) == 0 {
		t.Fatal("no team")
	}
	return tms[0].Team.ID
}
