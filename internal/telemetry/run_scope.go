// Copyright (c) 2025 Reliant Labs
package telemetry

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// The attributes a record logged while a run is in scope carries. user.id is
// AttrUserID (request_user.go); the three below name the run it happened in.
// They are what an error group's "affected" is counted over when a record has
// no user: the worker and reconciler logs that make up most of the error volume
// run on behalf of a chat, not of a request.
const (
	AttrChatID     = "reliant.chat.id"
	AttrThreadID   = "thread.id"
	AttrWorkflowID = "workflow.id"
)

// RunScope is the run a piece of work belongs to. Every field is optional: an
// activity knows its chat, thread and workflow; the reconciler knows a
// workflow's chat; a thread reaper knows only the thread.
type RunScope struct {
	UserID     string
	ChatID     string
	ThreadID   string
	WorkflowID string
}

type runScopeKey struct{}

// WithRunScope returns ctx carrying s. A field s leaves empty keeps the value
// the context already had, so a layer that learns the thread can add it without
// forgetting the chat an outer layer bound.
func WithRunScope(ctx context.Context, s RunScope) context.Context {
	prev := RunScopeFromContext(ctx)
	if s.UserID == "" {
		s.UserID = prev.UserID
	}
	if s.ChatID == "" {
		s.ChatID = prev.ChatID
	}
	if s.ThreadID == "" {
		s.ThreadID = prev.ThreadID
	}
	if s.WorkflowID == "" {
		s.WorkflowID = prev.WorkflowID
	}
	return context.WithValue(ctx, runScopeKey{}, s)
}

// RunScopeFromContext is the scope WithRunScope bound, or the zero scope.
func RunScopeFromContext(ctx context.Context) RunScope {
	if ctx == nil {
		return RunScope{}
	}
	s, _ := ctx.Value(runScopeKey{}).(RunScope)
	return s
}

// Attrs lists the scope's non-empty fields as log attributes, in a fixed order.
func (s RunScope) Attrs() []slog.Attr {
	var out []slog.Attr
	for _, kv := range s.keyvals() {
		out = append(out, slog.String(kv[0], kv[1]))
	}
	return out
}

// Keyvals is Attrs in the alternating key, value form the Temporal SDK's
// log.With takes.
func (s RunScope) Keyvals() []any {
	var out []any
	for _, kv := range s.keyvals() {
		out = append(out, kv[0], kv[1])
	}
	return out
}

func (s RunScope) keyvals() [][2]string {
	var out [][2]string
	for _, f := range [...][2]string{
		{AttrUserID, s.UserID},
		{AttrChatID, s.ChatID},
		{AttrThreadID, s.ThreadID},
		{AttrWorkflowID, s.WorkflowID},
	} {
		if f[1] != "" {
			out = append(out, f)
		}
	}
	return out
}

// ChatOwnerCache answers "whose chat is this" for the code that stamps a run
// scope. A chat's owner never changes, so an answer is kept for ttl. A failed or
// empty lookup is kept as "unknown" for a much shorter time (failTTL): it stamps
// no user rather than failing the work it was describing, and a database that is
// down is asked once per interval, not once per activity.
type ChatOwnerCache struct {
	lookup  func(ctx context.Context, chatID string) (string, error)
	ttl     time.Duration
	failTTL time.Duration
	max     int
	now     func() time.Time

	mu      sync.Mutex
	entries map[string]ownerEntry
}

type ownerEntry struct {
	user    string
	expires time.Time
}

// NewChatOwnerCache wraps lookup. At most max chats are kept; past that the
// table is emptied, which costs one lookup per live chat and bounds memory
// without an eviction order to keep correct.
func NewChatOwnerCache(lookup func(ctx context.Context, chatID string) (string, error), ttl time.Duration, max int) *ChatOwnerCache {
	return &ChatOwnerCache{lookup: lookup, ttl: ttl, failTTL: 30 * time.Second, max: max, now: time.Now, entries: map[string]ownerEntry{}}
}

// Owner is chatID's user, or "" when it has none or cannot be read. A nil cache
// answers "".
func (c *ChatOwnerCache) Owner(ctx context.Context, chatID string) string {
	if c == nil || c.lookup == nil || chatID == "" {
		return ""
	}
	now := c.now()
	c.mu.Lock()
	e, ok := c.entries[chatID]
	c.mu.Unlock()
	if ok && now.Before(e.expires) {
		return e.user
	}
	user, err := c.lookup(ctx, chatID)
	keep := c.ttl
	if err != nil || user == "" {
		user, keep = "", c.failTTL
	}
	c.mu.Lock()
	if len(c.entries) >= c.max {
		c.entries = map[string]ownerEntry{}
	}
	c.entries[chatID] = ownerEntry{user: user, expires: now.Add(keep)}
	c.mu.Unlock()
	return user
}
