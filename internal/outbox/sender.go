package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"taskbot/internal/platform/clock"
)

// Error kinds returned by an API implementation.
const (
	KindRateLimited = "rate_limited"
	KindForbidden   = "forbidden" // user blocked the bot / bot removed from group
	KindMigrated    = "migrated"  // group became a supergroup
	KindNotModified = "not_modified"
	KindNotFound    = "not_found" // message to edit no longer exists
	KindBadRequest  = "bad_request"
	KindTemporary   = "temporary"
)

// APIError is the classified error of a Telegram call.
type APIError struct {
	Kind       string
	RetryAfter time.Duration
	MigrateTo  int64
	Msg        string
}

func (e *APIError) Error() string { return e.Kind + ": " + e.Msg }

// AsAPIError extracts an APIError, treating unknown errors as temporary.
func AsAPIError(err error) *APIError {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae
	}
	return &APIError{Kind: KindTemporary, Msg: err.Error()}
}

// API is the part of the Telegram client the outbox needs.
type API interface {
	Send(ctx context.Context, chatID int64, text, markup string) (int, error)
	Edit(ctx context.Context, chatID int64, messageID int, text, markup string) error
	Pin(ctx context.Context, chatID int64, messageID int) error
}

// Hooks react to delivery results.
type Hooks struct {
	// Sent is called after a successful send (report messages store their id here).
	Sent func(ctx context.Context, it Item, messageID int)
	// Forbidden is called when a user blocked the bot or the bot lost the group.
	Forbidden func(ctx context.Context, it Item)
	// Migrated is called when a group got a new id.
	Migrated func(ctx context.Context, from, to int64)
}

// Sender drains the queue.
type Sender struct {
	store       Store
	api         API
	limiter     *Limiter
	clock       clock.Clock
	log         *slog.Logger
	hooks       Hooks
	batch       int
	maxAttempts int
	backoff     time.Duration
	poll        time.Duration
}

// NewSender creates a sender.
func NewSender(store Store, api API, lim *Limiter, c clock.Clock, log *slog.Logger, h Hooks, batch, maxAttempts int, backoff, poll time.Duration) *Sender {
	return &Sender{store: store, api: api, limiter: lim, clock: c, log: log, hooks: h, batch: batch, maxAttempts: maxAttempts, backoff: backoff, poll: poll}
}

// Loop drains the queue until ctx ends.
func (s *Sender) Loop(ctx context.Context, isLeader func() bool) {
	for ctx.Err() == nil {
		n := 0
		if isLeader == nil || isLeader() {
			var err error
			if n, err = s.Drain(ctx); err != nil && ctx.Err() == nil {
				s.log.Error("outbox drain", "err", err)
			}
		}
		if n == 0 {
			select {
			case <-ctx.Done():
			case <-time.After(s.poll):
			}
		}
	}
}

// Drain sends what the rate limits allow right now and returns how many items were handled.
// Items of a chat that must wait are skipped together so per-chat order is kept.
func (s *Sender) Drain(ctx context.Context) (int, error) {
	items, err := s.store.Due(ctx, s.clock.Now(), s.batch)
	if err != nil {
		return 0, err
	}
	handled := 0
	blocked := map[int64]bool{}
	for _, it := range items {
		if ctx.Err() != nil {
			return handled, ctx.Err()
		}
		if blocked[it.ChatID] {
			continue
		}
		ok, wait := s.limiter.TryReserve(time.Now(), it.ChatID, it.IsGroup)
		if !ok {
			blocked[it.ChatID] = true
			if wait > 0 && wait < 50*time.Millisecond {
				time.Sleep(wait)
			}
			continue
		}
		s.deliver(ctx, it)
		handled++
	}
	return handled, nil
}

func (s *Sender) deliver(ctx context.Context, it Item) {
	var msgID int
	var err error
	if it.EditMessageID != 0 {
		err = s.api.Edit(ctx, it.ChatID, it.EditMessageID, it.Text, it.Markup)
		msgID = it.EditMessageID
	} else {
		msgID, err = s.api.Send(ctx, it.ChatID, it.Text, it.Markup)
	}
	now := s.clock.Now()
	if err == nil {
		if it.Pin && it.EditMessageID == 0 {
			if perr := s.api.Pin(ctx, it.ChatID, msgID); perr != nil {
				s.log.Debug("pin failed", "chat", it.ChatID, "err", perr)
				it.Pin = false
			}
		}
		_ = s.store.MarkSent(ctx, it.ID, msgID, now)
		if s.hooks.Sent != nil {
			s.hooks.Sent(ctx, it, msgID)
		}
		return
	}
	ae := AsAPIError(err)
	switch ae.Kind {
	case KindNotModified:
		_ = s.store.MarkSent(ctx, it.ID, msgID, now)
	case KindRateLimited:
		s.limiter.Pause(time.Now(), ae.RetryAfter)
		_ = s.store.MarkRetry(ctx, it.ID, now.Add(ae.RetryAfter), ae.Error())
	case KindForbidden:
		_ = s.store.MarkFailed(ctx, it.ID, ae.Error())
		if s.hooks.Forbidden != nil {
			s.hooks.Forbidden(ctx, it)
		}
	case KindMigrated:
		_ = s.store.RetargetChat(ctx, it.ChatID, ae.MigrateTo)
		if s.hooks.Migrated != nil {
			s.hooks.Migrated(ctx, it.ChatID, ae.MigrateTo)
		}
	case KindNotFound:
		if it.EditMessageID != 0 { // the message was deleted: send a fresh one instead
			it.EditMessageID = 0
			_ = s.store.MarkFailed(ctx, it.ID, ae.Error())
			it.ID, it.Dedup, it.Attempts, it.Status = "", "", 0, StatusPending
			_ = s.store.Enqueue(ctx, it)
			return
		}
		_ = s.store.MarkFailed(ctx, it.ID, ae.Error())
	case KindBadRequest:
		_ = s.store.MarkFailed(ctx, it.ID, ae.Error())
		s.log.Error("outbox bad request", "chat", it.ChatID, "err", ae.Msg)
	default:
		if it.Attempts+1 >= s.maxAttempts {
			_ = s.store.MarkFailed(ctx, it.ID, ae.Error())
			s.log.Error("outbox gave up", "chat", it.ChatID, "err", ae.Msg)
			return
		}
		back := s.backoff * time.Duration(1<<it.Attempts)
		_ = s.store.MarkRetry(ctx, it.ID, now.Add(back), ae.Error())
		s.log.Warn("outbox retry", "chat", it.ChatID, "in", back.String(), "err", ae.Msg)
	}
}

// Describe is a debug helper.
func (it Item) Describe() string {
	return fmt.Sprintf("chat=%d edit=%d text=%q", it.ChatID, it.EditMessageID, it.Text)
}
