// Package dispatcher spreads updates over N workers. Updates of one chat always go to the
// same worker, so they are processed in order; different chats run in parallel.
package dispatcher

import (
	"context"
	"sync"

	"taskbot/internal/transport/telegram/tgapi"
)

// Handler processes one update.
type Handler func(ctx context.Context, u tgapi.Update)

// Dispatcher owns the worker queues.
type Dispatcher struct {
	queues []chan tgapi.Update
	h      Handler
	wg     sync.WaitGroup
}

// New creates a dispatcher with n workers and bounded queues of size q.
func New(n, q int, h Handler) *Dispatcher {
	d := &Dispatcher{queues: make([]chan tgapi.Update, n), h: h}
	for i := range d.queues {
		d.queues[i] = make(chan tgapi.Update, q)
	}
	return d
}

// Start launches the workers. They stop when Close is called and queues are drained.
func (d *Dispatcher) Start(ctx context.Context) {
	for _, q := range d.queues {
		d.wg.Add(1)
		go func(q chan tgapi.Update) {
			defer d.wg.Done()
			for u := range q {
				d.h(ctx, u)
			}
		}(q)
	}
}

// Dispatch queues an update. It blocks when the worker queue is full (backpressure on polling).
func (d *Dispatcher) Dispatch(ctx context.Context, u tgapi.Update) {
	key := u.ChatID
	if key == 0 {
		key = u.From.ID
	}
	if key < 0 {
		key = -key
	}
	q := d.queues[int(uint64(key)%uint64(len(d.queues)))]
	select {
	case q <- u:
	case <-ctx.Done():
	}
}

// Close stops accepting updates and waits for in-flight work.
func (d *Dispatcher) Close() {
	for _, q := range d.queues {
		close(q)
	}
	d.wg.Wait()
}
