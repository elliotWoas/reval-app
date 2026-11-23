package app

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"taskbot/internal/transport/telegram/tgapi"
)

// Alerter sends error alerts to an admin chat, at most one per minute.
type Alerter struct {
	client tgapi.Client
	chatID int64
	log    *slog.Logger
	mu     sync.Mutex
	last   time.Time
	muted  int
}

// NewAlerter creates an alerter; chatID 0 disables Telegram alerts (errors are still logged).
func NewAlerter(c tgapi.Client, chatID int64, log *slog.Logger) *Alerter {
	return &Alerter{client: c, chatID: chatID, log: log}
}

// Alert reports an error.
func (a *Alerter) Alert(err error) {
	if a.chatID == 0 || err == nil {
		return
	}
	a.mu.Lock()
	if time.Since(a.last) < time.Minute {
		a.muted++
		a.mu.Unlock()
		return
	}
	muted := a.muted
	a.last, a.muted = time.Now(), 0
	a.mu.Unlock()
	text := "⚠️ taskbot: " + err.Error()
	if muted > 0 {
		text += " (+" + strconv.Itoa(muted) + " more)"
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, e := a.client.Send(ctx, a.chatID, text, ""); e != nil {
			a.log.Warn("alert send failed", "err", e)
		}
	}()
}
