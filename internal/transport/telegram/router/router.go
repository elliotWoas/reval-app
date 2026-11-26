// Package router picks the handler of an update: membership changes, group messages,
// callbacks by prefix, commands, menu buttons, conversation state, then a fallback.
package router

import (
	"context"
	"strings"

	"taskbot/internal/transport/telegram/callbackdata"
	"taskbot/internal/transport/telegram/tgapi"
)

// Request is what a handler receives.
type Request struct {
	Ctx    context.Context
	Update tgapi.Update
	State  string // current conversation state of the user
	Extra  any    // handler-specific context
}

// Handler handles a request.
type Handler func(r *Request) error

// Router holds the routing tables.
type Router struct {
	commands   map[string]Handler
	callbacks  map[string]Handler
	menu       map[string]Handler
	states     map[string]Handler
	statePref  map[string]Handler
	membership Handler
	group      Handler
	fallback   Handler
}

// New creates an empty router.
func New() *Router {
	return &Router{commands: map[string]Handler{}, callbacks: map[string]Handler{}, menu: map[string]Handler{}, states: map[string]Handler{}, statePref: map[string]Handler{}}
}

// Command registers "/name".
func (r *Router) Command(name string, h Handler) { r.commands[name] = h }

// Callback registers a callback prefix.
func (r *Router) Callback(prefix string, h Handler) { r.callbacks[prefix] = h }

// Menu registers an exact reply-keyboard label.
func (r *Router) Menu(label string, h Handler) { r.menu[label] = h }

// State registers a conversation state.
func (r *Router) State(state string, h Handler) { r.states[state] = h }

// StatePrefix registers every state starting with prefix.
func (r *Router) StatePrefix(prefix string, h Handler) { r.statePref[prefix] = h }

// Membership registers the my_chat_member handler.
func (r *Router) Membership(h Handler) { r.membership = h }

// Group registers the handler for messages in groups.
func (r *Router) Group(h Handler) { r.group = h }

// Fallback registers the handler for private messages nothing else matched.
func (r *Router) Fallback(h Handler) { r.fallback = h }

// IsMenu reports whether text is a registered menu label.
func (r *Router) IsMenu(text string) bool { _, ok := r.menu[strings.TrimSpace(text)]; return ok }

// Route finds the handler. ok=false means nothing matched.
func (r *Router) Route(req *Request) (Handler, bool) {
	u := req.Update
	switch u.Kind {
	case tgapi.KindMembership:
		return r.membership, r.membership != nil
	case tgapi.KindCallback:
		h, ok := r.callbacks[callbackdata.Decode(u.Data).Prefix()]
		return h, ok
	}
	if !u.Private() {
		return r.group, r.group != nil
	}
	text := strings.TrimSpace(u.Text)
	if strings.HasPrefix(text, "/") {
		name := strings.Fields(text)[0]
		if i := strings.Index(name, "@"); i > 0 {
			name = name[:i]
		}
		if h, ok := r.commands[name]; ok {
			return h, true
		}
	}
	if h, ok := r.menu[text]; ok && text != "" {
		return h, true
	}
	if req.State != "" {
		if h, ok := r.states[req.State]; ok {
			return h, true
		}
		for p, h := range r.statePref {
			if strings.HasPrefix(req.State, p) {
				return h, true
			}
		}
	}
	return r.fallback, r.fallback != nil
}
