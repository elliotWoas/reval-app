// Package app wires dependencies and owns the process lifecycle.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"taskbot/internal/config"
	"taskbot/internal/domain/team"
	"taskbot/internal/outbox"
	"taskbot/internal/platform/clock"
	"taskbot/internal/platform/i18n"
	mongorepo "taskbot/internal/repository/mongo"
	"taskbot/internal/scheduler"
	"taskbot/internal/transport/telegram/dispatcher"
	"taskbot/internal/transport/telegram/fsm"
	"taskbot/internal/transport/telegram/handlers"
	"taskbot/internal/transport/telegram/middleware"
	"taskbot/internal/transport/telegram/notify"
	"taskbot/internal/transport/telegram/tgapi"
	"taskbot/internal/transport/telegram/views"
	"taskbot/internal/usecase/checkin"
	"taskbot/internal/usecase/dailycycle"
	"taskbot/internal/usecase/leave"
	"taskbot/internal/usecase/membership"
	"taskbot/internal/usecase/onboarding"
	"taskbot/internal/usecase/ports"
	"taskbot/internal/usecase/reporting"
	"taskbot/internal/usecase/settings"
)

const leaderLock = "leader"

// Services groups the use cases (also used by tests and simulations).
type Services struct {
	Onboarding *onboarding.Service
	Members    *membership.Service
	Settings   *settings.Service
	Cycle      *dailycycle.Service
	Checkin    *checkin.Service
	Leaves     *leave.Service
	Reports    *reporting.Service
}

// NewServices builds all use cases on a store, clock and notifier.
func NewServices(st ports.Store, c clock.Clock, cfg *config.Config, n ports.Notifier) *Services {
	return &Services{
		Onboarding: onboarding.New(st, c, cfg),
		Members:    membership.New(st, c, cfg, n),
		Settings:   settings.New(st, c, cfg),
		Cycle:      dailycycle.New(st, c, cfg, n),
		Checkin:    checkin.New(st, c, n),
		Leaves:     leave.New(st, c, cfg, n),
		Reports:    reporting.New(st, c, cfg, n),
	}
}

// Runner dispatches a scheduler event to the owning use case.
func (s *Services) Runner() scheduler.Runner {
	return scheduler.RunnerFunc(func(ctx context.Context, t *team.Team, ev team.Event) error {
		switch {
		case dailycycle.Handles(ev.Kind):
			return s.Cycle.Run(ctx, t, ev)
		case ev.Kind == team.EvCheckin:
			return s.Checkin.Run(ctx, t, ev)
		case ev.Kind == team.EvLeaveDigest:
			return s.Leaves.Digest(ctx, t, ev)
		case ev.Kind == team.EvWeekly:
			return s.Reports.Run(ctx, t, ev)
		}
		return fmt.Errorf("unknown event %s", ev.Kind)
	})
}

// Hooks builds the outbox delivery hooks: save report message ids, mark blocked users,
// detach lost groups and follow supergroup migrations.
func Hooks(st ports.Store, members *membership.Service, log *slog.Logger) outbox.Hooks {
	return outbox.Hooks{
		Sent: func(ctx context.Context, it outbox.Item, msgID int) {
			if it.ReportTeam == "" {
				return
			}
			err := st.SaveReportMsg(ctx, ports.ReportMsg{TeamID: it.ReportTeam, Date: it.ReportDate, Kind: it.ReportKind, ChatID: it.ChatID, MessageID: msgID, Pinned: it.Pin})
			if err != nil {
				log.Error("save report msg", "err", err)
			}
		},
		Forbidden: func(ctx context.Context, it outbox.Item) {
			if it.IsGroup || it.ChatID < 0 {
				if err := members.GroupLost(ctx, it.ChatID); err != nil {
					log.Error("group lost", "err", err)
				}
				return
			}
			_ = st.SetUserBlocked(ctx, it.ChatID, true)
		},
		Migrated: func(ctx context.Context, from, to int64) {
			if err := members.GroupMigrated(ctx, from, to); err != nil {
				log.Error("group migrated", "err", err)
			}
		},
	}
}

// Run starts the bot and blocks until ctx is cancelled, then shuts down gracefully.
func Run(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	bundle, err := i18n.Load(cfg.App.LocalePath)
	if err != nil {
		return err
	}
	clk := clock.Real{}

	store, err := mongorepo.Connect(ctx, cfg.Mongo.URI, cfg.Mongo.Database, cfg.Mongo.MaxPool, cfg.Mongo.ConnectTimeout, cfg.Mongo.OpTimeout)
	if err != nil {
		return err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = store.Close(cctx)
	}()
	if err := store.EnsureIndexes(ctx, cfg.Scheduler.JobRunsTTL); err != nil {
		return err
	}
	log.Info("mongo ready", "db", cfg.Mongo.Database)

	client, err := tgapi.New(ctx, cfg.Telegram.Token, cfg.Telegram.PollTimeout, cfg.Telegram.APITimeout, log)
	if err != nil {
		return err
	}
	log.Info("telegram ready", "bot", client.Username())

	v := &views.V{T: bundle, Cfg: cfg, BotName: client.Username()}
	notifier := notify.New(store, store, v, clk, cfg)
	svc := NewServices(store, clk, cfg, notifier)
	limiter := outbox.NewLimiter(cfg.Outbox.GlobalPerSec, cfg.Outbox.ChatPerSec, cfg.Outbox.GroupPerMin)

	alerter := NewAlerter(client, cfg.App.AdminChatID, log)
	h := handlers.New(handlers.Deps{
		Cfg: cfg, T: bundle, V: v, Client: client, Limiter: limiter, Store: store,
		FSM: fsm.NewManager(store, clk, cfg.Limits.SessionTTL), Clock: clk, Log: log,
		Onboarding: svc.Onboarding, Members: svc.Members, Settings: svc.Settings, Cycle: svc.Cycle,
		Checkin: svc.Checkin, Leaves: svc.Leaves, Reports: svc.Reports,
	})

	// Only one instance may poll Telegram and run the scheduler: take the leader lock first.
	leader := &atomic.Bool{}
	if err := waitLeader(ctx, store, cfg, leader, log); err != nil {
		return err
	}

	workCtx, stopWork := context.WithCancel(context.Background())
	defer stopWork()
	disp := dispatcher.New(cfg.App.Workers, cfg.App.WorkerQueue,
		middleware.Chain(h.Handle, log, cfg.App.HandlerTimeout, alerter.Alert))
	disp.Start(workCtx)

	sender := outbox.NewSender(store, client, limiter, clk, log, Hooks(store, svc.Members, log),
		cfg.Outbox.Batch, cfg.Outbox.MaxAttempts, cfg.Outbox.RetryBackoff, cfg.Outbox.PollInterval)
	sched := scheduler.New(store, clk, svc.Runner(), log, cfg.Scheduler.BatchTeams)
	sched.OnError = alerter.Alert

	var wg sync.WaitGroup
	run := func(f func()) { wg.Add(1); go func() { defer wg.Done(); f() }() }

	run(func() { keepLeader(ctx, store, cfg, leader, log) })
	run(func() { sched.Loop(ctx, cfg.Scheduler.Tick, leader.Load) })
	run(func() { sender.Loop(workCtx, leader.Load) })
	run(func() { purgeLoop(ctx, store, cfg, log) })
	if cfg.App.HealthAddr != "" {
		run(func() { serveHealth(ctx, cfg.App.HealthAddr, store, leader, log) })
	}

	log.Info("bot started", "workers", cfg.App.Workers)
	// Long polling blocks until ctx is cancelled.
	client.Run(ctx, func(u tgapi.Update) { disp.Dispatch(workCtx, u) })

	log.Info("shutting down")
	disp.Close() // finish in-flight updates
	// give the sender a moment to flush replies queued by the last updates
	flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for flushCtx.Err() == nil {
		n, _ := store.CountPending(flushCtx)
		if n == 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	stopWork()
	wg.Wait()
	rctx, rcancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer rcancel()
	_ = store.ReleaseLock(rctx, leaderLock, cfg.App.InstanceID)
	log.Info("stopped")
	return nil
}

func waitLeader(ctx context.Context, l ports.Locker, cfg *config.Config, leader *atomic.Bool, log *slog.Logger) error {
	for {
		ok, err := l.AcquireLock(ctx, leaderLock, cfg.App.InstanceID, cfg.Scheduler.LockTTL)
		if err != nil {
			return fmt.Errorf("leader lock: %w", err)
		}
		if ok {
			leader.Store(true)
			log.Info("leader lock acquired", "instance", cfg.App.InstanceID)
			return nil
		}
		log.Warn("another instance holds the leader lock; waiting")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(cfg.Scheduler.LockTTL / 2):
		}
	}
}

func keepLeader(ctx context.Context, l ports.Locker, cfg *config.Config, leader *atomic.Bool, log *slog.Logger) {
	t := time.NewTicker(cfg.Scheduler.LockTTL / 3)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		ok, err := l.AcquireLock(ctx, leaderLock, cfg.App.InstanceID, cfg.Scheduler.LockTTL)
		if err != nil && ctx.Err() == nil {
			log.Error("renew leader lock", "err", err)
			continue
		}
		if leader.Load() != ok {
			log.Warn("leader status changed", "leader", ok)
		}
		leader.Store(ok)
	}
}

func purgeLoop(ctx context.Context, ob outbox.Store, cfg *config.Config, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if err := ob.PurgeSent(ctx, time.Now().Add(-cfg.Outbox.SentRetention)); err != nil && ctx.Err() == nil {
			log.Warn("purge outbox", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type pinger interface {
	Ping(ctx context.Context) error
	CountPending(ctx context.Context) (int64, error)
}

func serveHealth(ctx context.Context, addr string, st pinger, leader *atomic.Bool, log *slog.Logger) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		c, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		status := map[string]any{"leader": leader.Load()}
		code := http.StatusOK
		if err := st.Ping(c); err != nil {
			status["mongo"] = err.Error()
			code = http.StatusServiceUnavailable
		} else {
			status["mongo"] = "ok"
		}
		if n, err := st.CountPending(c); err == nil {
			status["outbox_pending"] = n
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(status)
	})
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Info("health endpoint", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("health server", "err", err)
	}
}
