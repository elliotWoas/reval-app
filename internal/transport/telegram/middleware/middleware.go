// Package middleware wraps update handling with panic recovery, timeouts and logging.
package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"taskbot/internal/transport/telegram/tgapi"
)

// Func handles one update.
type Func func(ctx context.Context, u tgapi.Update) error

// Chain wraps f with recover, timeout and log. onPanic receives panics for alerting.
func Chain(f Func, log *slog.Logger, timeout time.Duration, onPanic func(error)) func(ctx context.Context, u tgapi.Update) {
	return func(ctx context.Context, u tgapi.Update) {
		start := time.Now()
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		defer func() {
			if rec := recover(); rec != nil {
				err := fmt.Errorf("panic: %v", rec)
				log.Error("handler panic", "err", err, "update", u.ID, "stack", string(debug.Stack()))
				if onPanic != nil {
					onPanic(err)
				}
			}
		}()
		err := f(ctx, u)
		attrs := []any{"update", u.ID, "kind", u.Kind, "chat", u.ChatID, "user", u.From.ID, "ms", time.Since(start).Milliseconds()}
		if err != nil {
			log.Error("handler", append(attrs, "err", err)...)
			return
		}
		log.Debug("handled", attrs...)
	}
}
