package outbox

import (
	"context"
	"sync"
	"time"
)

// bucket is a token bucket refilled at rate tokens/second up to burst.
type bucket struct {
	tokens float64
	rate   float64
	burst  float64
	last   time.Time
}

func (b *bucket) refill(now time.Time) {
	if b.last.IsZero() {
		b.last = now
		b.tokens = b.burst
		return
	}
	b.tokens += now.Sub(b.last).Seconds() * b.rate
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
	b.last = now
}

// wait returns how long until one token is available (0 = now).
func (b *bucket) wait(now time.Time) time.Duration {
	b.refill(now)
	if b.tokens >= 1 {
		return 0
	}
	return time.Duration((1 - b.tokens) / b.rate * float64(time.Second))
}

func (b *bucket) take(now time.Time) { b.refill(now); b.tokens-- }

// Limiter enforces Telegram limits: a global rate, a per-chat rate and a per-group rate.
// It is shared by the outbox and the direct replies to user actions.
type Limiter struct {
	mu          sync.Mutex
	global      bucket
	chatRate    float64
	groupPerMin float64
	chats       map[int64]*bucket
	pausedUntil time.Time // set after a global 429
}

// NewLimiter creates a limiter.
func NewLimiter(globalPerSec, chatPerSec, groupPerMin float64) *Limiter {
	return &Limiter{
		global:      bucket{rate: globalPerSec, burst: globalPerSec},
		chatRate:    chatPerSec,
		groupPerMin: groupPerMin,
		chats:       map[int64]*bucket{},
	}
}

func (l *Limiter) chat(id int64, group bool) *bucket {
	b, ok := l.chats[id]
	if !ok {
		if group {
			b = &bucket{rate: l.groupPerMin / 60, burst: l.groupPerMin}
		} else {
			b = &bucket{rate: l.chatRate, burst: 1 + l.chatRate}
		}
		l.chats[id] = b
	}
	return b
}

// TryReserve takes a token for chatID when both the global and the chat bucket allow it.
// Otherwise it returns how long to wait and takes nothing.
func (l *Limiter) TryReserve(now time.Time, chatID int64, group bool) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Before(l.pausedUntil) {
		return false, l.pausedUntil.Sub(now)
	}
	gw := l.global.wait(now)
	cb := l.chat(chatID, group)
	cw := cb.wait(now)
	if gw > 0 || cw > 0 {
		return false, max(gw, cw)
	}
	l.global.take(now)
	cb.take(now)
	if len(l.chats) > 50000 { // forget idle chats
		for id, b := range l.chats {
			if now.Sub(b.last) > time.Hour {
				delete(l.chats, id)
			}
		}
	}
	return true, 0
}

// Wait blocks until a token for chatID is available or ctx ends.
func (l *Limiter) Wait(ctx context.Context, chatID int64, group bool) error {
	for {
		ok, w := l.TryReserve(time.Now(), chatID, group)
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(w):
		}
	}
}

// Pause stops all sending until d has passed (after a 429 response).
func (l *Limiter) Pause(now time.Time, d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if u := now.Add(d); u.After(l.pausedUntil) {
		l.pausedUntil = u
	}
}
