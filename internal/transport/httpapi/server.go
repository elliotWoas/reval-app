// Package httpapi serves the Telegram Mini App: a JSON API over the existing use cases and the
// built frontend. It shares no state with the long-polling bot except the use cases and the store.
package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"taskbot/internal/config"
	"taskbot/internal/domain"
	"taskbot/internal/platform/clock"
	"taskbot/internal/usecase/checkin"
	"taskbot/internal/usecase/dailycycle"
	"taskbot/internal/usecase/leave"
	"taskbot/internal/usecase/membership"
	"taskbot/internal/usecase/overview"
	"taskbot/internal/usecase/reporting"
)

// Deps are the use cases the API calls. Handlers never touch the store directly.
type Deps struct {
	Cfg         *config.Config
	Log         *slog.Logger
	Clock       clock.Clock
	BotUsername string
	Members     *membership.Service
	Cycle       *dailycycle.Service
	Checkin     *checkin.Service
	Leaves      *leave.Service
	Reports     *reporting.Service
	Overview    *overview.Service
}

// Server is the Mini App HTTP handler.
type Server struct {
	Deps
	limiter *userLimiter
}

// New creates the server.
func New(d Deps) *Server {
	return &Server{Deps: d, limiter: newUserLimiter(120, time.Minute)}
}

const maxBody = 64 << 10

type ctxKey struct{}

// caller returns the authenticated Telegram user of a request.
func caller(r *http.Request) TGUser { return r.Context().Value(ctxKey{}).(TGUser) }

type apiHandler func(w http.ResponseWriter, r *http.Request) error

// Handler returns the full HTTP handler: /api/v1/* and the static frontend.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routes(func(pattern string, h apiHandler) { mux.Handle(pattern, s.api(h)) })
	mux.HandleFunc("GET /api/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found")
	}))
	if dir := s.Cfg.WebApp.StaticDir; dir != "" {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			mux.Handle("/", spa(os.DirFS(dir)))
		} else {
			s.Log.Warn("webapp static dir missing; serving the API only", "dir", dir)
		}
	}
	return s.secure(mux)
}

// api wraps a handler with CORS, authentication, rate limiting, body limits and error mapping.
func (s *Server) api(h apiHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		u, err := s.authenticate(r)
		if err != nil {
			s.Log.Debug("api auth rejected", "path", r.URL.Path, "reason", err.Error(), "header", r.Header.Get("Authorization") != "")
			code := "unauthorized"
			if errors.Is(err, ErrInitExpired) {
				code = "auth_expired"
			}
			writeError(w, http.StatusUnauthorized, code)
			return
		}
		if !s.limiter.allow(u.ID, s.Clock.Now()) {
			writeError(w, http.StatusTooManyRequests, "rate_limited")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, u))
		defer func() {
			if p := recover(); p != nil {
				s.Log.Error("api panic", "path", r.URL.Path, "panic", p)
				writeError(w, http.StatusInternalServerError, "internal")
			}
		}()
		if err := h(w, r); err != nil {
			s.fail(w, r, err)
		}
	})
}

// authenticate verifies "Authorization: tma <initData>". With WEBAPP_DEV_USER_ID set, requests
// without the header act as that user (local development only).
func (s *Server) authenticate(r *http.Request) (TGUser, error) {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "tma ")
	if !ok || raw == "" {
		if id := s.Cfg.WebApp.DevUserID; id != 0 {
			return TGUser{ID: id, FirstName: "Dev"}, nil
		}
		return TGUser{}, ErrInitMissing
	}
	return ValidateInitData(raw, s.Cfg.Telegram.Token, s.Cfg.WebApp.AuthMaxAge, s.Clock.Now())
}

// secure adds security headers and CORS for configured origins.
func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if origin := r.Header.Get("Origin"); origin != "" && slices.Contains(s.Cfg.WebApp.CORSOrigins, origin) {
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Vary", "Origin")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
			h.Set("Access-Control-Max-Age", "600")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		if s.Log.Enabled(r.Context(), slog.LevelDebug) {
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			start := time.Now()
			next.ServeHTTP(rec, r)
			s.Log.Debug("http", "method", r.Method, "path", r.URL.Path, "status", rec.status,
				"ms", time.Since(start).Milliseconds(), "ua", r.UserAgent())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the response status for the debug access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// ---- errors

// errBadRequest marks malformed input detected by the API itself.
var errBadRequest = domain.E("bad_request")

var statusByCode = map[string]int{
	"bad_request":        http.StatusBadRequest,
	"bad_range":          http.StatusBadRequest,
	"not_found":          http.StatusNotFound,
	"no_plan":            http.StatusNotFound,
	"forbidden":          http.StatusForbidden,
	"not_owner":          http.StatusForbidden,
	"not_work_day":       http.StatusConflict,
	"on_leave":           http.StatusConflict,
	"window_closed":      http.StatusConflict,
	"leave_exists":       http.StatusConflict,
	"owner_cannot_leave": http.StatusConflict,
	"duplicate":          http.StatusConflict,
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large")
		return
	}
	code := domain.Code(err)
	if code == "" {
		s.Log.Error("api error", "path", r.URL.Path, "user", caller(r).ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	status, ok := statusByCode[code]
	if !ok {
		status = http.StatusUnprocessableEntity
	}
	writeError(w, status, code)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code}})
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return err
		}
		return errBadRequest
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return errBadRequest
	}
	return nil
}

// ---- rate limit

// userLimiter is a fixed-window request counter per Telegram user.
type userLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	start  time.Time
	counts map[int64]int
}

func newUserLimiter(max int, window time.Duration) *userLimiter {
	return &userLimiter{max: max, window: window, counts: map[int64]int{}}
}

func (l *userLimiter) allow(userID int64, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.start) >= l.window {
		l.start = now
		clear(l.counts)
	}
	l.counts[userID]++
	return l.counts[userID] <= l.max
}

// ---- static frontend

// spa serves the built frontend. Unknown paths without a file extension fall back to
// index.html so client-side routes survive a reload.
func spa(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)
	gz := &gzipCache{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" {
			if _, err := fs.Stat(fsys, name); err == nil {
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				if gz.serve(w, r, fsys, name) {
					return
				}
				files.ServeHTTP(w, r)
				return
			}
			if path.Ext(name) != "" {
				http.NotFound(w, r)
				return
			}
		}
		index, err := fs.ReadFile(fsys, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
			"img-src 'self' data: https:; font-src 'self' data:; connect-src 'self'; "+
			"frame-ancestors 'self' https://web.telegram.org https://*.telegram.org")
		_, _ = w.Write(index)
	})
}

// gzipCache serves text assets gzip-compressed, compressing each file once. Built assets are
// immutable, so the cache never needs invalidation; a reverse proxy may still compress on its own.
type gzipCache struct{ m sync.Map }

var compressible = map[string]bool{".js": true, ".css": true, ".svg": true, ".json": true, ".html": true, ".txt": true}

func (c *gzipCache) serve(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) bool {
	ext := path.Ext(name)
	if !compressible[ext] || !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		return false
	}
	v, ok := c.m.Load(name)
	if !ok {
		raw, err := fs.ReadFile(fsys, name)
		if err != nil {
			return false
		}
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		_, _ = zw.Write(raw)
		_ = zw.Close()
		v, _ = c.m.LoadOrStore(name, buf.Bytes())
	}
	h := w.Header()
	if ct := mime.TypeByExtension(ext); ct != "" {
		h.Set("Content-Type", ct)
	}
	h.Set("Content-Encoding", "gzip")
	h.Add("Vary", "Accept-Encoding")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(v.([]byte)))
	return true
}
