// Package outbox is the persisted send queue with rate limiting and retries.
package outbox

import (
	"context"
	"time"
)

// Item statuses.
const (
	StatusPending = "pending"
	StatusSent    = "sent"
	StatusFailed  = "failed"
)

// Item is one queued Telegram operation: a new message, or an edit when EditMessageID is set.
type Item struct {
	ID            string    `bson:"_id"`
	Dedup         string    `bson:"dedup,omitempty"` // unique when set: the same logical message is queued once
	ChatID        int64     `bson:"chatId"`
	IsGroup       bool      `bson:"isGroup"`
	Text          string    `bson:"text"`
	Markup        string    `bson:"markup,omitempty"` // JSON reply_markup
	EditMessageID int       `bson:"editMessageId,omitempty"`
	Pin           bool      `bson:"pin"`
	ReportTeam    string    `bson:"reportTeam,omitempty"` // when set, store the sent message id
	ReportDate    string    `bson:"reportDate,omitempty"`
	ReportKind    string    `bson:"reportKind,omitempty"`
	TeamID        string    `bson:"teamId,omitempty"`
	Status        string    `bson:"status"`
	Attempts      int       `bson:"attempts"`
	NotBefore     time.Time `bson:"notBefore"`
	CreatedAt     time.Time `bson:"createdAt"`
	Seq           int64     `bson:"seq"` // insertion order
	SentAt        time.Time `bson:"sentAt,omitempty"`
	MessageID     int       `bson:"messageId,omitempty"`
	Error         string    `bson:"error,omitempty"`
}

// Store persists the queue.
type Store interface {
	// Enqueue inserts items; an item whose Dedup key already exists is skipped silently.
	Enqueue(ctx context.Context, items ...Item) error
	// Due returns pending items with NotBefore <= now, oldest first.
	Due(ctx context.Context, now time.Time, limit int) ([]Item, error)
	MarkSent(ctx context.Context, id string, messageID int, at time.Time) error
	MarkRetry(ctx context.Context, id string, notBefore time.Time, errText string) error
	MarkFailed(ctx context.Context, id string, errText string) error
	// RetargetChat moves pending items of a migrated group to its new chat id.
	RetargetChat(ctx context.Context, from, to int64) error
	// CountPending returns the queue length (for health).
	CountPending(ctx context.Context) (int64, error)
	// PurgeSent deletes sent/failed items older than before.
	PurgeSent(ctx context.Context, before time.Time) error
}
