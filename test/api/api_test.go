// Package api drives the Mini App HTTP API on a fully wired bot (memory store, fake clock and
// fake Telegram) and checks authentication, permissions and the main workflows.
package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"taskbot/internal/app"
	"taskbot/internal/domain/team"
	"taskbot/internal/testkit"
	"taskbot/internal/transport/httpapi"
	"taskbot/internal/usecase/onboarding"
)

const (
	owner    int64 = 1
	sara     int64 = 2
	mahdi    int64 = 3
	stranger int64 = 99
)

type env struct {
	*testkit.Env
	t    *testing.T
	h    http.Handler
	team *team.Team
}

func setup(t *testing.T, start time.Time) *env {
	t.Helper()
	e := testkit.New(t, start)
	e.Cfg.WebApp.AuthMaxAge = time.Hour
	e.Text(owner, "/start")
	e.Click(owner, e.Tr("menu.create"))
	e.Text(owner, "تیم آزمایشی")
	e.Click(owner, e.Tr("common.confirm"))
	e.Click(owner, e.Tr("create.part_yes"))
	e.Text(owner, e.Tr("create.defaults_ok"))
	tms, err := e.Svc.Members.MyTeams(t.Context(), owner)
	if err != nil || len(tms) != 1 {
		t.Fatalf("team not created: %v", err)
	}
	tm := tms[0].Team
	for id, name := range map[int64]string{sara: "سارا", mahdi: "مهدی"} {
		e.Text(id, "/start "+tm.InviteKey)
		e.Click(id, e.Tr("common.yes"))
		e.Text(id, name)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &env{Env: e, t: t, team: tm, h: app.NewWebApp(e.Cfg, log, e.Clock, "testbot", e.Svc)}
}

func (e *env) initData(userID int64) string {
	return httpapi.SignInitData(e.Cfg.Telegram.Token, map[string]string{
		"auth_date": strconv.FormatInt(e.Clock.Now().Unix(), 10),
		"user":      fmt.Sprintf(`{"id":%d,"first_name":"U%d"}`, userID, userID),
	})
}

// call performs a request as userID (0 = no auth) and decodes the JSON answer into out.
func (e *env) call(userID int64, method, path string, body any, out any) int {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	if userID != 0 {
		req.Header.Set("Authorization", "tma "+e.initData(userID))
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	e.Flush()
	if out != nil && rec.Code < 300 {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			e.t.Fatalf("%s %s: decode %q: %v", method, path, rec.Body.String(), err)
		}
	}
	if rec.Code >= 300 && out != nil {
		if m, ok := out.(*map[string]any); ok {
			_ = json.Unmarshal(rec.Body.Bytes(), m)
		}
	}
	return rec.Code
}

func (e *env) url(format string, a ...any) string {
	return "/api/v1/teams/" + e.team.ID + fmt.Sprintf(format, a...)
}

type today struct {
	Date   string `json:"date"`
	Window struct {
		Phase string `json:"phase"`
	} `json:"window"`
	Plan *struct {
		Status       string `json:"status"`
		ReportStatus string `json:"reportStatus"`
		Note         string `json:"note"`
		Stuck        bool   `json:"stuck"`
		Tasks        []struct {
			ID             int    `json:"id"`
			Text           string `json:"text"`
			Important      bool   `json:"important"`
			Progress       *int   `json:"progress"`
			EffectiveFinal string `json:"effectiveFinal"`
			Reason         string `json:"reason"`
			Source         string `json:"source"`
			OwnerNote      string `json:"ownerNote"`
		} `json:"tasks"`
		Counts struct {
			Total, Done, Partial, NotDone int
			Rate                          float64
		} `json:"counts"`
	} `json:"plan"`
	Carry []struct {
		Text string `json:"text"`
	} `json:"carry"`
}

func errCode(m map[string]any) string {
	if e, ok := m["error"].(map[string]any); ok {
		s, _ := e["code"].(string)
		return s
	}
	return ""
}

func TestAuthentication(t *testing.T) {
	e := setup(t, testkit.Tehran(2026, 11, 14, 8, 0))
	if code := e.call(0, "GET", "/api/v1/me", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("no auth: %d", code)
	}
	// forged init data
	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	req.Header.Set("Authorization", "tma "+strings.Replace(e.initData(sara), "U2", "U1", 1))
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("forged init data accepted: %d", rec.Code)
	}
	// stale init data
	stale := e.initData(sara)
	e.Clock.Advance(2 * time.Hour)
	req = httptest.NewRequest("GET", "/api/v1/me", nil)
	req.Header.Set("Authorization", "tma "+stale)
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "auth_expired") {
		t.Fatalf("stale init data: %d %s", rec.Code, rec.Body.String())
	}

	var me struct {
		User  struct{ ID int64 }
		Teams []struct {
			ID, Name, InviteKey string
			IsOwner             bool
		}
	}
	if code := e.call(sara, "GET", "/api/v1/me", nil, &me); code != 200 || me.User.ID != sara || len(me.Teams) != 1 {
		t.Fatalf("me: %d %+v", code, me)
	}
	if me.Teams[0].IsOwner || me.Teams[0].InviteKey != "" {
		t.Fatalf("member must not see owner data: %+v", me.Teams[0])
	}
	if code := e.call(owner, "GET", "/api/v1/me", nil, &me); code != 200 || !me.Teams[0].IsOwner || me.Teams[0].InviteKey == "" {
		t.Fatalf("owner me: %+v", me)
	}
	var empty struct{ Teams []any }
	if code := e.call(stranger, "GET", "/api/v1/me", nil, &empty); code != 200 || len(empty.Teams) != 0 {
		t.Fatalf("stranger me: %d %+v", code, empty)
	}
}

func TestPermissions(t *testing.T) {
	e := setup(t, testkit.Tehran(2026, 11, 14, 8, 0))
	var m map[string]any
	if code := e.call(stranger, "GET", e.url("/today"), nil, &m); code != http.StatusForbidden {
		t.Fatalf("stranger today: %d", code)
	}
	for _, p := range []string{"/admin/today", "/admin/week", "/admin/members", "/admin/leaves"} {
		if code := e.call(sara, "GET", e.url("%s", p), nil, &m); code != http.StatusForbidden || errCode(m) != "not_owner" {
			t.Fatalf("member reached %s: %d %v", p, code, m)
		}
	}
	saraM, _ := e.Store.FindMembership(t.Context(), e.team.ID, sara)
	mahdiM, _ := e.Store.FindMembership(t.Context(), e.team.ID, mahdi)
	if code := e.call(mahdi, "POST", e.url("/admin/members/%s/remind", saraM.ID), struct{}{}, &m); code != http.StatusForbidden {
		t.Fatalf("member reminded someone: %d", code)
	}
	if code := e.call(mahdi, "POST", e.url("/admin/plans/%s/2026-11-14/tasks/1/note", saraM.ID), map[string]string{"note": "x"}, &m); code != http.StatusForbidden {
		t.Fatalf("member wrote an owner note: %d", code)
	}
	// a second team owned by mahdi cannot be used to reach the first team's data
	t2, _, err := e.Svc.Onboarding.CreateTeam(t.Context(), onboarding.Input{
		OwnerID: mahdi, OwnerName: "مهدی", Name: "تیم دوم", Participates: true, Schedule: onboarding.DefaultSchedule(e.Cfg),
	})
	if err != nil {
		t.Fatalf("second team: %v", err)
	}
	tms := []*team.Team{t2}
	other := "/api/v1/teams/" + tms[0].ID
	if code := e.call(mahdi, "POST", other+"/admin/plans/"+saraM.ID+"/2026-11-14/tasks/1/note", map[string]string{"note": "x"}, &m); code < 400 {
		t.Fatalf("cross-team owner note accepted: %d", code)
	}
	if code := e.call(mahdi, "POST", other+"/admin/leaves/"+saraM.ID+"/2026-11-15/decide", map[string]bool{"approve": true}, &m); code != http.StatusNotFound {
		t.Fatalf("cross-team leave decision: %d", code)
	}
	if code := e.call(mahdi, "POST", other+"/admin/members/"+saraM.ID+"/remind", struct{}{}, &m); code != http.StatusForbidden {
		t.Fatalf("cross-team remind: %d", code)
	}
	_ = mahdiM
	// unknown fields and bad input
	if code := e.call(sara, "POST", e.url("/plan/tasks"), map[string]any{"text": "a", "userId": owner}, &m); code != http.StatusBadRequest {
		t.Fatalf("unknown field accepted: %d", code)
	}
	if code := e.call(sara, "GET", e.url("/history?from=2026-01-01&to=2026-12-31"), nil, &m); code != http.StatusBadRequest {
		t.Fatalf("huge range accepted: %d", code)
	}
}

func TestMemberDay(t *testing.T) {
	// Saturday 14 Nov 2026, 09:30 Tehran: plan window open until 10:30
	e := setup(t, testkit.Tehran(2026, 11, 14, 9, 30))
	var d today
	if code := e.call(sara, "GET", e.url("/today"), nil, &d); code != 200 || d.Window.Phase != "planning" || d.Plan != nil {
		t.Fatalf("today: %d %+v", code, d)
	}
	if code := e.call(sara, "POST", e.url("/plan/tasks"), map[string]any{"text": "طراحی صفحه\n!رفع باگ ورود\nنوشتن تست"}, &d); code != 200 {
		t.Fatalf("add tasks: %d", code)
	}
	if d.Plan == nil || len(d.Plan.Tasks) != 3 || d.Plan.Status != "submitted" || !d.Plan.Tasks[1].Important {
		t.Fatalf("plan: %+v", d.Plan)
	}
	// the owner sees the plan in the team view and the bot's team status
	var tt struct {
		Rows []struct {
			Member struct{ DisplayName string }
			Plan   *struct{ Status string }
		}
	}
	if code := e.call(owner, "GET", e.url("/admin/today"), nil, &tt); code != 200 || len(tt.Rows) != 3 {
		t.Fatalf("admin today: %d %+v", code, tt)
	}

	e.Clock.Set(testkit.Tehran(2026, 11, 14, 13, 0))
	date := d.Date
	if code := e.call(sara, "POST", e.url("/plans/%s/tasks/1/progress", date), map[string]int{"progress": 50}, &d); code != 200 || *d.Plan.Tasks[0].Progress != 50 {
		t.Fatalf("progress: %d %+v", code, d.Plan)
	}
	if code := e.call(sara, "POST", e.url("/plans/%s/tasks/2/status", date), map[string]string{"status": "done"}, &d); code != 200 || d.Plan.Tasks[1].EffectiveFinal != "done" {
		t.Fatalf("done: %d %+v", code, d.Plan)
	}
	var m map[string]any
	if code := e.call(sara, "POST", e.url("/plans/%s/tasks/3/status", date), map[string]string{"status": "not_done", "reason": "hack"}, &m); code != http.StatusBadRequest {
		t.Fatalf("unknown reason: %d", code)
	}
	if code := e.call(sara, "POST", e.url("/plans/%s/tasks/3/status", date), map[string]string{"status": "not_done", "reason": "no_time"}, &d); code != 200 || d.Plan.Tasks[2].Reason != "no_time" {
		t.Fatalf("not done: %d %+v", code, d.Plan)
	}
	// owner note reaches sara in the bot
	saraM, _ := e.Store.FindMembership(t.Context(), e.team.ID, sara)
	if code := e.call(owner, "POST", e.url("/admin/plans/%s/%s/tasks/1/note", saraM.ID, date), map[string]string{"note": "عالیه"}, nil); code != 200 {
		t.Fatalf("owner note: %d", code)
	}
	if e.Client.Count(sara, "عالیه") == 0 {
		t.Fatal("owner note was not delivered by the bot")
	}
	// stuck alerts the owner
	before := len(e.Client.To(owner))
	if code := e.call(sara, "POST", e.url("/plans/%s/stuck", date), map[string]int{"taskId": 1}, &d); code != 200 || !d.Plan.Stuck {
		t.Fatalf("stuck: %d", code)
	}
	if len(e.Client.To(owner)) == before {
		t.Fatal("stuck alert not sent to the owner")
	}
	// evening report
	e.Clock.Set(testkit.Tehran(2026, 11, 14, 21, 30))
	if code := e.call(sara, "POST", e.url("/plans/%s/report", date), map[string]string{"note": "روز خوبی بود"}, &d); code != 200 {
		t.Fatalf("report: %d", code)
	}
	if d.Plan.ReportStatus != "on_time" || d.Plan.Note != "روز خوبی بود" || d.Plan.Counts.Partial != 1 || d.Plan.Counts.Done != 1 {
		t.Fatalf("report state: %+v", d.Plan)
	}
	// member team pulse is aggregate only
	var pulse map[string]any
	if code := e.call(sara, "GET", e.url("/pulse"), nil, &pulse); code != 200 || pulse["members"].(float64) != 3 || pulse["submitted"].(float64) != 1 {
		t.Fatalf("pulse: %d %v", code, pulse)
	}
	if _, named := pulse["rows"]; named {
		t.Fatal("pulse must not list members")
	}

	// next day: carry over the unfinished tasks
	e.Clock.Set(testkit.Tehran(2026, 11, 15, 9, 0))
	if code := e.call(sara, "GET", e.url("/today"), nil, &d); code != 200 || len(d.Carry) != 2 {
		t.Fatalf("carry candidates: %+v", d.Carry)
	}
	if code := e.call(sara, "POST", e.url("/plan/carry"), struct{}{}, &d); code != 200 || len(d.Plan.Tasks) != 2 || d.Plan.Tasks[0].Source != "carried" {
		t.Fatalf("carry: %d %+v", code, d.Plan)
	}
	if code := e.call(sara, "POST", e.url("/plan/carry"), struct{}{}, &m); code != http.StatusUnprocessableEntity || errCode(m) != "no_tasks" {
		t.Fatalf("second carry must find nothing: %d %v", code, m)
	}

	// history and week
	var h struct {
		Days []struct {
			Date    string
			WorkDay bool
			Plan    *struct{ ReportStatus string }
		}
	}
	if code := e.call(sara, "GET", e.url("/history?from=2026-11-14&to=2026-11-20"), nil, &h); code != 200 || len(h.Days) != 7 {
		t.Fatalf("history: %d %+v", code, h)
	}
	if h.Days[0].Plan == nil || h.Days[0].Plan.ReportStatus != "on_time" || !h.Days[0].WorkDay {
		t.Fatalf("history day: %+v", h.Days[0])
	}
	var wk struct {
		WeekStart string
		This      *struct {
			Counts       struct{ Total, Done int }
			PunctualDays int
		}
		Days []struct{ Date string }
	}
	if code := e.call(sara, "GET", e.url("/week"), nil, &wk); code != 200 || wk.WeekStart != "2026-11-14" || wk.This == nil || len(wk.Days) != 7 {
		t.Fatalf("week: %d %+v", code, wk)
	}
	if wk.This.PunctualDays != 1 || wk.This.Counts.Done != 1 {
		t.Fatalf("week stats: %+v", wk.This)
	}
	if ws, _ := e.Store.FindWeekly(t.Context(), e.team.ID, "2026-11-14", saraM.ID); ws != nil {
		t.Fatal("the read-only week view must not store weekly stats")
	}
	if code := e.call(sara, "GET", e.url("/week?date=2026-12-30"), nil, &m); code != http.StatusBadRequest {
		t.Fatalf("future week: %d", code)
	}
}

func TestLeavesAndProfile(t *testing.T) {
	e := setup(t, testkit.Tehran(2026, 11, 14, 8, 0))
	// require approval
	tm, _ := e.Svc.Settings.SetFlag(t.Context(), owner, e.team.ID, "leave_approval", true)
	_ = tm
	var lv struct {
		Upcoming []struct {
			Date, Status string
		}
		Selectable       []string
		ApprovalRequired bool
	}
	if code := e.call(sara, "GET", e.url("/leaves"), nil, &lv); code != 200 || !lv.ApprovalRequired || len(lv.Selectable) == 0 {
		t.Fatalf("leaves: %d %+v", code, lv)
	}
	day := lv.Selectable[1]
	if code := e.call(sara, "POST", e.url("/leaves"), map[string]any{"dates": []string{day}, "note": "دکتر"}, &lv); code != 200 || len(lv.Upcoming) != 1 || lv.Upcoming[0].Status != "registered" {
		t.Fatalf("request leave: %d %+v", code, lv)
	}
	var m map[string]any
	if code := e.call(sara, "POST", e.url("/leaves"), map[string]any{"dates": []string{"2027-06-01"}}, &m); code != http.StatusUnprocessableEntity || errCode(m) != "leave_too_far" {
		t.Fatalf("far leave: %d %v", code, m)
	}
	var tl []struct {
		Member struct{ MembershipID string }
		Leave  struct{ Date, Status string }
	}
	if code := e.call(owner, "GET", e.url("/admin/leaves"), nil, &tl); code != 200 || len(tl) != 1 {
		t.Fatalf("admin leaves: %d %+v", code, tl)
	}
	if code := e.call(owner, "POST", e.url("/admin/leaves/%s/%s/decide", tl[0].Member.MembershipID, day), map[string]bool{"approve": true}, &tl); code != 200 || tl[0].Leave.Status != "approved" {
		t.Fatalf("decide: %d %+v", code, tl)
	}
	if code := e.call(sara, "POST", e.url("/leaves/%s/cancel", day), struct{}{}, &lv); code != 200 || len(lv.Upcoming) != 0 {
		t.Fatalf("cancel: %d %+v", code, lv)
	}
	// mahdi cannot cancel sara's leave: the id is always built from the caller's membership
	if code := e.call(mahdi, "POST", e.url("/leaves/%s/cancel", day), struct{}{}, &m); code < 400 {
		t.Fatalf("cancel someone else's leave: %d", code)
	}

	var team struct{ DisplayName string }
	if code := e.call(sara, "PUT", e.url("/profile"), map[string]string{"displayName": "سارا ک"}, &team); code != 200 || team.DisplayName != "سارا ک" {
		t.Fatalf("rename: %d %+v", code, team)
	}
	if code := e.call(sara, "PUT", e.url("/profile"), map[string]string{"displayName": strings.Repeat("x", 100)}, &m); code != http.StatusUnprocessableEntity || errCode(m) != "name_length" {
		t.Fatalf("long name: %d %v", code, m)
	}
}

func TestOwnerViews(t *testing.T) {
	e := setup(t, testkit.Tehran(2026, 11, 14, 9, 0))
	var d today
	e.call(sara, "POST", e.url("/plan/tasks"), map[string]any{"text": "یک\nدو"}, &d)
	e.Clock.Set(testkit.Tehran(2026, 11, 14, 12, 0))
	var members []struct{ MembershipID, DisplayName, Role string }
	if code := e.call(owner, "GET", e.url("/admin/members"), nil, &members); code != 200 || len(members) != 3 {
		t.Fatalf("members: %d %+v", code, members)
	}
	mahdiID := ""
	for _, m := range members {
		if m.DisplayName == "مهدی" {
			mahdiID = m.MembershipID
		}
	}
	before := len(e.Client.To(mahdi))
	if code := e.call(owner, "POST", e.url("/admin/members/%s/remind", mahdiID), struct{}{}, nil); code != 200 {
		t.Fatalf("remind: %d", code)
	}
	if len(e.Client.To(mahdi)) == before {
		t.Fatal("reminder not delivered")
	}
	var wk struct {
		Team    struct{ Counts struct{ Total int } }
		Members []struct {
			Member struct{ DisplayName string }
		}
	}
	if code := e.call(owner, "GET", e.url("/admin/week"), nil, &wk); code != 200 || wk.Team.Counts.Total != 2 || len(wk.Members) != 3 {
		t.Fatalf("admin week: %d %+v", code, wk)
	}
}

func TestStaticFrontend(t *testing.T) {
	e := setup(t, testkit.Tehran(2026, 11, 14, 8, 0))
	dir := t.TempDir()
	if err := writeFiles(dir, map[string]string{"index.html": "<html>app</html>", "assets/app.js": "console.log(1)"}); err != nil {
		t.Fatal(err)
	}
	e.Cfg.WebApp.StaticDir = dir
	h := app.NewWebApp(e.Cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), e.Clock, "testbot", e.Svc)
	get := func(p string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		return rec
	}
	if r := get("/history"); r.Code != 200 || !strings.Contains(r.Body.String(), "app") || r.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("spa fallback: %d %q", r.Code, r.Body.String())
	}
	if r := get("/assets/app.js"); r.Code != 200 || !strings.Contains(r.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset: %d %v", r.Code, r.Header())
	}
	gzReq := httptest.NewRequest("GET", "/assets/app.js", nil)
	gzReq.Header.Set("Accept-Encoding", "gzip, br")
	gzRec := httptest.NewRecorder()
	h.ServeHTTP(gzRec, gzReq)
	if gzRec.Code != 200 || gzRec.Header().Get("Content-Encoding") != "gzip" || !strings.Contains(gzRec.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("gzip asset: %d %v", gzRec.Code, gzRec.Header())
	}
	if r := get("/assets/missing.js"); r.Code != 404 {
		t.Fatalf("missing asset: %d", r.Code)
	}
	if r := get("/api/v1/nope"); r.Code != 404 {
		t.Fatalf("unknown api path: %d", r.Code)
	}
	if r := get("/../../etc/passwd"); r.Code == 200 && strings.Contains(r.Body.String(), "root:") {
		t.Fatal("path traversal")
	}
}
