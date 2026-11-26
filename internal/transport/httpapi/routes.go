package httpapi

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"taskbot/internal/domain"
	"taskbot/internal/domain/leave"
	"taskbot/internal/domain/plan"
	"taskbot/internal/domain/task"
	"taskbot/internal/domain/team"
	"taskbot/internal/usecase/dayview"
	"taskbot/internal/usecase/membership"
	"taskbot/internal/usecase/overview"
)

func (s *Server) routes(handle func(string, apiHandler)) {
	handle("GET /api/v1/me", s.me)

	// the caller's own day, plan and history in one team
	handle("GET /api/v1/teams/{team}/today", s.today)
	handle("POST /api/v1/teams/{team}/plan/tasks", s.addTasks)
	handle("POST /api/v1/teams/{team}/plan/carry", s.carry)
	handle("POST /api/v1/teams/{team}/plans/{date}/tasks/{task}/progress", s.setProgress)
	handle("POST /api/v1/teams/{team}/plans/{date}/tasks/{task}/status", s.setStatus)
	handle("POST /api/v1/teams/{team}/plans/{date}/report", s.submitReport)
	handle("POST /api/v1/teams/{team}/plans/{date}/stuck", s.stuck)
	handle("GET /api/v1/teams/{team}/history", s.history)
	handle("GET /api/v1/teams/{team}/week", s.week)
	handle("GET /api/v1/teams/{team}/pulse", s.pulse)
	handle("GET /api/v1/teams/{team}/leaves", s.leaves)
	handle("POST /api/v1/teams/{team}/leaves", s.requestLeave)
	handle("POST /api/v1/teams/{team}/leaves/{date}/cancel", s.cancelLeave)
	handle("PUT /api/v1/teams/{team}/profile", s.updateProfile)

	// owner tools; every handler checks ownership through membership.OwnerContext
	handle("GET /api/v1/teams/{team}/admin/today", s.adminToday)
	handle("GET /api/v1/teams/{team}/admin/week", s.adminWeek)
	handle("GET /api/v1/teams/{team}/admin/members", s.adminMembers)
	handle("GET /api/v1/teams/{team}/admin/leaves", s.adminLeaves)
	handle("POST /api/v1/teams/{team}/admin/leaves/{member}/{date}/decide", s.adminDecideLeave)
	handle("POST /api/v1/teams/{team}/admin/members/{member}/remind", s.adminRemind)
	handle("POST /api/v1/teams/{team}/admin/plans/{member}/{date}/tasks/{task}/note", s.adminNote)
}

// ---- helpers

func (s *Server) member(r *http.Request) (membership.TeamMember, error) {
	return s.Members.Context(r.Context(), caller(r).ID, r.PathValue("team"))
}

func (s *Server) owner(r *http.Request) (membership.TeamMember, error) {
	return s.Members.OwnerContext(r.Context(), caller(r).ID, r.PathValue("team"))
}

func pathDate(r *http.Request) (string, error) {
	d := r.PathValue("date")
	if _, err := time.Parse(team.DateLayout, d); err != nil {
		return "", errBadRequest
	}
	return d, nil
}

func pathInt(r *http.Request, name string) (int, error) {
	n, err := strconv.Atoi(r.PathValue(name))
	if err != nil || n <= 0 {
		return 0, errBadRequest
	}
	return n, nil
}

// ownPlanID builds the caller's plan id of a date; the use cases also check ownership.
func ownPlanID(tm membership.TeamMember, date string) string { return plan.ID(tm.Member.ID, date) }

func runes(s string) int { return utf8.RuneCountInString(s) }

// window describes where the team day stands for the caller.
func window(t *team.Team, date string, now time.Time, workDay, onLeave bool) windowDTO {
	sch := t.Schedule
	w := windowDTO{
		PlanWindowOpen: t.PlanWindowOpen(date, now),
		ReportAccepted: t.ReportAccepted(date, now),
		ReportOnTime:   t.ReportOnTime(date, now),
		MorningAt:      t.At(date, sch.Morning),
		DeadlineAt:     t.At(date, sch.Deadline),
		EveningAt:      t.At(date, sch.Evening),
		SummaryAt:      t.At(date, sch.Summary),
		CloseAt:        t.At(team.AddDays(date, 1), sch.Morning),
	}
	switch {
	case !workDay:
		w.Phase = "off"
	case onLeave:
		w.Phase = "leave"
	case now.Before(w.MorningAt):
		w.Phase = "before"
	case now.Before(w.DeadlineAt):
		w.Phase = "planning"
	case now.Before(w.EveningAt):
		w.Phase = "working"
	case !now.After(w.SummaryAt):
		w.Phase = "evening"
	case w.ReportAccepted:
		w.Phase = "late_report"
	default:
		w.Phase = "closed"
	}
	return w
}

// ---- me

func (s *Server) me(w http.ResponseWriter, r *http.Request) error {
	u := caller(r)
	tms, err := s.Members.MyTeams(r.Context(), u.ID)
	if err != nil {
		return err
	}
	l := s.Cfg.Limits
	out := meDTO{
		User:        userDTO{ID: u.ID, FirstName: u.FirstName, LastName: u.LastName, Username: u.Username, Language: u.LanguageCode, PhotoURL: u.PhotoURL},
		Teams:       []teamDTO{},
		ActiveTeam:  s.Members.ActiveTeam(r.Context(), u.ID),
		BotUsername: s.BotUsername,
		Limits: limitsDTO{
			MaxTasks: l.MaxTasks, TaskMaxLen: l.TaskMaxLen, NoteMaxLen: l.NoteMaxLen, DisplayNameMax: l.DisplayNameMax,
			LeaveDaysAhead: l.LeaveDaysAhead, FailReasons: s.Cfg.Options.FailReasons, Progress: s.Cfg.Options.Progress,
			StuckCarryDays: l.StuckCarryDays,
		},
	}
	for _, tm := range tms {
		out.Teams = append(out.Teams, toTeam(tm.Team, tm.Member, s.Overview.Today(tm.Team)))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// ---- today and plan

func (s *Server) today(w http.ResponseWriter, r *http.Request) error {
	tm, err := s.member(r)
	if err != nil {
		return err
	}
	return s.respondToday(w, r, tm)
}

func (s *Server) todayView(r *http.Request, tm membership.TeamMember) (todayDTO, error) {
	st, err := s.Cycle.State(r.Context(), tm.Team, tm.Member)
	if err != nil {
		return todayDTO{}, err
	}
	out := todayDTO{
		Date: st.Date, Now: s.Clock.Now(), WorkDay: st.WorkDay, OnLeave: st.OnLeave, Participates: tm.Member.Participates,
		Window: window(tm.Team, st.Date, s.Clock.Now(), st.WorkDay, st.OnLeave), Plan: toPlan(st.Plan), Carry: []carryDTO{},
	}
	if st.WorkDay && !st.OnLeave && tm.Member.Participates {
		if carry, err := s.Cycle.CarryTasks(r.Context(), tm.Member, st.Date); err == nil {
			for _, c := range carry {
				out.Carry = append(out.Carry, carryDTO{Text: c.Text, Important: c.Important, CarriedDays: c.CarriedDays})
			}
		}
	}
	return out, nil
}

// respondToday answers with the fresh "today" view, so a write needs no second request.
func (s *Server) respondToday(w http.ResponseWriter, r *http.Request, tm membership.TeamMember) error {
	v, err := s.todayView(r, tm)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, v)
	return nil
}

type addTasksReq struct {
	Text    string `json:"text"`    // one task per line; a leading "!" marks it important (same as the bot)
	Replace bool   `json:"replace"` // replace the whole plan (only while the plan window is open)
}

func (s *Server) addTasks(w http.ResponseWriter, r *http.Request) error {
	tm, err := s.member(r)
	if err != nil {
		return err
	}
	var req addTasksReq
	if err := decode(r, &req); err != nil {
		return err
	}
	l := s.Cfg.Limits
	if runes(req.Text) > l.MaxTasks*(l.TaskMaxLen+8) {
		return domain.ErrTooManyTasks
	}
	tasks, err := task.Parse(req.Text, l.MaxTasks, l.TaskMaxLen)
	if err != nil {
		return err
	}
	if _, err := s.Cycle.SubmitTasks(r.Context(), tm.Team, tm.Member, tasks, req.Replace); err != nil {
		return err
	}
	return s.respondToday(w, r, tm)
}

func (s *Server) carry(w http.ResponseWriter, r *http.Request) error {
	tm, err := s.member(r)
	if err != nil {
		return err
	}
	if err := decode(r, &struct{}{}); err != nil {
		return err
	}
	tasks, err := s.Cycle.CarryTasks(r.Context(), tm.Member, s.Cycle.Today(tm.Team))
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		return domain.ErrNoTasks
	}
	if _, err := s.Cycle.SubmitTasks(r.Context(), tm.Team, tm.Member, tasks, false); err != nil {
		return err
	}
	return s.respondToday(w, r, tm)
}

func (s *Server) setProgress(w http.ResponseWriter, r *http.Request) error {
	tm, err := s.member(r)
	if err != nil {
		return err
	}
	date, err := pathDate(r)
	if err != nil {
		return err
	}
	id, err := pathInt(r, "task")
	if err != nil {
		return err
	}
	var req struct {
		Progress *int `json:"progress"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	if req.Progress == nil || *req.Progress < 0 || *req.Progress > 100 {
		return errBadRequest
	}
	if _, err := s.Checkin.SetProgress(r.Context(), caller(r).ID, ownPlanID(tm, date), id, *req.Progress); err != nil {
		return err
	}
	return s.respondToday(w, r, tm)
}

func (s *Server) setStatus(w http.ResponseWriter, r *http.Request) error {
	tm, err := s.member(r)
	if err != nil {
		return err
	}
	date, err := pathDate(r)
	if err != nil {
		return err
	}
	id, err := pathInt(r, "task")
	if err != nil {
		return err
	}
	var req struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	switch req.Status {
	case task.Done, task.Partial:
		req.Reason = ""
	case task.NotDone:
		if req.Reason != "" && !slices.Contains(s.Cfg.Options.FailReasons, req.Reason) {
			return errBadRequest
		}
	default:
		return errBadRequest
	}
	if _, err := s.Cycle.SetFinal(r.Context(), caller(r).ID, ownPlanID(tm, date), id, req.Status, req.Reason); err != nil {
		return err
	}
	return s.respondToday(w, r, tm)
}

func (s *Server) submitReport(w http.ResponseWriter, r *http.Request) error {
	tm, err := s.member(r)
	if err != nil {
		return err
	}
	date, err := pathDate(r)
	if err != nil {
		return err
	}
	var req struct {
		Note string `json:"note"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	req.Note = strings.TrimSpace(req.Note)
	if runes(req.Note) > s.Cfg.Limits.NoteMaxLen {
		return errBadRequest
	}
	if _, err := s.Cycle.SubmitReport(r.Context(), caller(r).ID, ownPlanID(tm, date), req.Note, nil); err != nil {
		return err
	}
	return s.respondToday(w, r, tm)
}

func (s *Server) stuck(w http.ResponseWriter, r *http.Request) error {
	tm, err := s.member(r)
	if err != nil {
		return err
	}
	date, err := pathDate(r)
	if err != nil {
		return err
	}
	var req struct {
		TaskID int `json:"taskId"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	if req.TaskID < 0 {
		return errBadRequest
	}
	if err := s.Checkin.Stuck(r.Context(), caller(r).ID, ownPlanID(tm, date), req.TaskID); err != nil {
		return err
	}
	return s.respondToday(w, r, tm)
}

// ---- history and stats

func (s *Server) history(w http.ResponseWriter, r *http.Request) error {
	tm, err := s.member(r)
	if err != nil {
		return err
	}
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	days, err := s.Overview.History(r.Context(), tm.Team, tm.Member, from, to)
	if err != nil {
		return err
	}
	out := historyDTO{From: from, To: to, Today: s.Overview.Today(tm.Team), Days: make([]dayDTO, 0, len(days))}
	for _, d := range days {
		out.Days = append(out.Days, toDay(d))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// weekRange resolves ?date= to the week containing it, counted up to today.
func (s *Server) weekRange(r *http.Request, t *team.Team) (date, upTo string, err error) {
	today := s.Overview.Today(t)
	date = r.URL.Query().Get("date")
	if date == "" {
		date = today
	}
	if _, perr := time.Parse(team.DateLayout, date); perr != nil {
		return "", "", errBadRequest
	}
	if team.WeekStart(date) > today {
		return "", "", overview.ErrBadRange
	}
	upTo = min(today, team.AddDays(team.WeekStart(date), 6))
	return date, upTo, nil
}

func (s *Server) week(w http.ResponseWriter, r *http.Request) error {
	tm, err := s.member(r)
	if err != nil {
		return err
	}
	date, upTo, err := s.weekRange(r, tm.Team)
	if err != nil {
		return err
	}
	v, err := s.Reports.Preview(r.Context(), tm.Team, date, upTo)
	if err != nil {
		return err
	}
	out := weekDTO{WeekStart: v.WeekStart, WeekEnd: v.WeekEnd, UpTo: upTo, Days: []dayStatDTO{}}
	for _, mw := range v.Members {
		if mw.Member.ID == tm.Member.ID {
			out.This, out.Prev = toWeekly(&mw.This), toWeekly(mw.Prev)
		}
	}
	days, err := s.Overview.History(r.Context(), tm.Team, tm.Member, v.WeekStart, v.WeekEnd)
	if err != nil {
		return err
	}
	for _, d := range days {
		ds := dayStatDTO{Date: d.Date, WorkDay: d.WorkDay && d.Date >= tm.Member.StartDate, OnLeave: d.Leave.FullDay(tm.Team.LeaveApproval)}
		if d.Plan != nil {
			ds.Planned, ds.PlanOn, ds.Report = d.Plan.Submitted(), d.Plan.Status == plan.StatusSubmitted, d.Plan.Reported()
			ds.Counts = toCounts(d.Plan.Count())
		}
		out.Days = append(out.Days, ds)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) pulse(w http.ResponseWriter, r *http.Request) error {
	tm, err := s.member(r)
	if err != nil {
		return err
	}
	p, err := s.Overview.TeamPulse(r.Context(), tm.Team)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, pulseDTO{Date: p.Date, WorkDay: p.WorkDay, Members: p.Members, Submitted: p.Submitted,
		Reported: p.Reported, OnLeave: p.OnLeave, Counts: toCounts(p.Counts)})
	return nil
}

// ---- leaves and profile

func (s *Server) leavesView(r *http.Request, tm membership.TeamMember) (leavesDTO, error) {
	ls, err := s.Leaves.Upcoming(r.Context(), tm.Team, tm.Member)
	if err != nil {
		return leavesDTO{}, err
	}
	out := leavesDTO{Upcoming: []leaveDTO{}, Selectable: s.Leaves.Selectable(tm.Team), ApprovalRequired: tm.Team.LeaveApproval}
	for _, l := range ls {
		out.Upcoming = append(out.Upcoming, *toLeave(l))
	}
	return out, nil
}

func (s *Server) leaves(w http.ResponseWriter, r *http.Request) error {
	tm, err := s.member(r)
	if err != nil {
		return err
	}
	out, err := s.leavesView(r, tm)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) requestLeave(w http.ResponseWriter, r *http.Request) error {
	tm, err := s.member(r)
	if err != nil {
		return err
	}
	var req struct {
		Dates []string `json:"dates"`
		Half  bool     `json:"half"`
		Note  string   `json:"note"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	req.Note = strings.TrimSpace(req.Note)
	if len(req.Dates) == 0 || len(req.Dates) > s.Cfg.Limits.LeaveDaysAhead+1 || runes(req.Note) > s.Cfg.Limits.NoteMaxLen {
		return errBadRequest
	}
	slices.Sort(req.Dates)
	req.Dates = slices.Compact(req.Dates)
	if _, err := s.Leaves.Request(r.Context(), tm.Team, tm.Member, req.Dates, req.Half, req.Note); err != nil {
		return err
	}
	out, err := s.leavesView(r, tm)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) cancelLeave(w http.ResponseWriter, r *http.Request) error {
	tm, err := s.member(r)
	if err != nil {
		return err
	}
	date, err := pathDate(r)
	if err != nil {
		return err
	}
	if err := decode(r, &struct{}{}); err != nil {
		return err
	}
	if _, err := s.Leaves.Cancel(r.Context(), caller(r).ID, leave.ID(tm.Member.ID, date)); err != nil {
		return err
	}
	out, err := s.leavesView(r, tm)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) updateProfile(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		DisplayName string `json:"displayName"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	teamID := r.PathValue("team")
	if err := s.Members.SetDisplayName(r.Context(), caller(r).ID, teamID, req.DisplayName); err != nil {
		return err
	}
	tm, err := s.member(r)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toTeam(tm.Team, tm.Member, s.Overview.Today(tm.Team)))
	return nil
}

// ---- owner

func (s *Server) adminToday(w http.ResponseWriter, r *http.Request) error {
	tm, err := s.owner(r)
	if err != nil {
		return err
	}
	v, err := s.Cycle.TodayView(r.Context(), tm.Team)
	if err != nil {
		return err
	}
	workDay := tm.Team.IsWorkDay(v.Date)
	out := teamTodayDTO{Date: v.Date, WorkDay: workDay, Window: window(tm.Team, v.Date, s.Clock.Now(), workDay, false),
		Rows: make([]teamRowDTO, 0, len(v.Rows)), Counts: toCounts(dayview.Counts(v))}
	for _, row := range v.Rows {
		out.Rows = append(out.Rows, toTeamRow(row))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) adminWeek(w http.ResponseWriter, r *http.Request) error {
	tm, err := s.owner(r)
	if err != nil {
		return err
	}
	date, upTo, err := s.weekRange(r, tm.Team)
	if err != nil {
		return err
	}
	v, err := s.Reports.Preview(r.Context(), tm.Team, date, upTo)
	if err != nil {
		return err
	}
	out := teamWeekDTO{WeekStart: v.WeekStart, WeekEnd: v.WeekEnd, UpTo: upTo, Team: *toWeekly(&v.TeamWeek),
		PrevTeam: toWeekly(v.PrevTeam), MostPunctual: v.TeamWeek.MostPunctual, MostPunctualDays: v.TeamWeek.MostPunctualDays,
		Members: make([]memberWeekDTO, 0, len(v.Members))}
	// members stay in join order: the view supports the owner, it is not a ranking
	for _, mw := range v.Members {
		out.Members = append(out.Members, memberWeekDTO{Member: toMember(mw.Member), This: *toWeekly(&mw.This), Prev: toWeekly(mw.Prev)})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) adminMembers(w http.ResponseWriter, r *http.Request) error {
	tm, err := s.owner(r)
	if err != nil {
		return err
	}
	ms, err := s.Members.Members(r.Context(), caller(r).ID, tm.Team.ID)
	if err != nil {
		return err
	}
	out := make([]memberDTO, 0, len(ms))
	for _, m := range ms {
		out = append(out, toMember(m))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) adminLeavesView(r *http.Request, tm membership.TeamMember) ([]teamLeaveDTO, error) {
	rows, err := s.Overview.TeamLeaves(r.Context(), tm.Team)
	if err != nil {
		return nil, err
	}
	out := make([]teamLeaveDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, teamLeaveDTO{Member: toMember(row.Member), Leave: *toLeave(row.Leave)})
	}
	return out, nil
}

func (s *Server) adminLeaves(w http.ResponseWriter, r *http.Request) error {
	tm, err := s.owner(r)
	if err != nil {
		return err
	}
	out, err := s.adminLeavesView(r, tm)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) adminDecideLeave(w http.ResponseWriter, r *http.Request) error {
	tm, err := s.owner(r)
	if err != nil {
		return err
	}
	date, err := pathDate(r)
	if err != nil {
		return err
	}
	var req struct {
		Approve bool `json:"approve"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	l, err := s.Overview.TeamLeave(r.Context(), tm.Team, r.PathValue("member"), date)
	if err != nil {
		return err
	}
	if _, err := s.Leaves.Decide(r.Context(), caller(r).ID, l.ID, req.Approve); err != nil {
		return err
	}
	out, err := s.adminLeavesView(r, tm)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) adminRemind(w http.ResponseWriter, r *http.Request) error {
	tm, err := s.owner(r)
	if err != nil {
		return err
	}
	if err := decode(r, &struct{}{}); err != nil {
		return err
	}
	if err := s.Cycle.ManualRemind(r.Context(), tm.Team, r.PathValue("member")); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return nil
}

func (s *Server) adminNote(w http.ResponseWriter, r *http.Request) error {
	tm, err := s.owner(r)
	if err != nil {
		return err
	}
	date, err := pathDate(r)
	if err != nil {
		return err
	}
	id, err := pathInt(r, "task")
	if err != nil {
		return err
	}
	var req struct {
		Note string `json:"note"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	req.Note = strings.TrimSpace(req.Note)
	if req.Note == "" || runes(req.Note) > s.Cfg.Limits.NoteMaxLen {
		return errBadRequest
	}
	planID := plan.ID(r.PathValue("member"), date)
	p, t, err := s.Cycle.PlanForOwner(r.Context(), caller(r).ID, planID)
	if err != nil {
		return err
	}
	if t.ID != tm.Team.ID {
		return domain.ErrNotFound
	}
	if err := s.Cycle.OwnerNote(r.Context(), caller(r).ID, p.ID, id, req.Note); err != nil {
		return err
	}
	if p, _, err = s.Cycle.PlanForOwner(r.Context(), caller(r).ID, planID); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toPlan(p))
	return nil
}
