// Copyright (c) 2025 Reliant Labs
package telemetry

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
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

	// The rest name the run for AI monitoring (ai_attrs.go). They go on spans
	// only, never on log records: SpanAttributes, GenAIAttributes.

	// OrgID is the org the chat belongs to.
	OrgID string
	// WorkflowName, WorkflowRef and Trigger are the workflow the run
	// executes, the reference it was started with, and what started it.
	WorkflowName string
	WorkflowRef  string
	Trigger      string
	// RunID, ParentRunID and RootRunID are AttrAIRunID and its spawn chain.
	RunID       string
	ParentRunID string
	RootRunID   string
	// Preset is the agent's preset (AttrGenAIAgentName), when it has one.
	Preset string
	// NodeID and NodeType are the workflow node the work runs in; when it runs
	// inside a loop, InLoop is set and NodeIteration is the iteration.
	NodeID        string
	NodeType      string
	NodeIteration int
	InLoop        bool
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
	if s.OrgID == "" {
		s.OrgID = prev.OrgID
	}
	if s.WorkflowName == "" {
		s.WorkflowName, s.WorkflowRef, s.Trigger = prev.WorkflowName, prev.WorkflowRef, prev.Trigger
	}
	if s.RunID == "" {
		s.RunID, s.ParentRunID, s.RootRunID = prev.RunID, prev.ParentRunID, prev.RootRunID
	}
	if s.Preset == "" {
		s.Preset = prev.Preset
	}
	// A node's fields describe one node together: a layer that names none
	// keeps the node an outer layer bound.
	if s.NodeID == "" {
		s.NodeID, s.NodeType, s.NodeIteration, s.InLoop = prev.NodeID, prev.NodeType, prev.NodeIteration, prev.InLoop
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

// SpanAttributes lists the scope as span attributes, in a fixed order: the
// ids log records carry (Attrs) and the run's AI-monitoring identity
// (ai_attrs.go). Empty fields are left out.
func (s RunScope) SpanAttributes() []attribute.KeyValue {
	var out []attribute.KeyValue
	for _, kv := range s.keyvals() {
		out = append(out, attribute.String(kv[0], kv[1]))
	}
	root := s.RootRunID
	if root == "" {
		root = s.RunID // a top-level run is its own root
	}
	for _, f := range [...][2]string{
		{AttrOrgID, s.OrgID},
		{AttrAIWorkflowName, s.WorkflowName},
		{AttrAIWorkflowRef, s.WorkflowRef},
		{AttrAIWorkflowTrigger, s.Trigger},
		{AttrAIRunID, s.RunID},
		{AttrAIRootRunID, root},
		{AttrAIParentRunID, s.ParentRunID},
		{AttrAINodeID, s.NodeID},
		{AttrAINodeType, s.NodeType},
	} {
		if f[1] != "" {
			out = append(out, attribute.String(f[0], f[1]))
		}
	}
	if s.NodeID != "" {
		iteration := 0
		if s.InLoop {
			iteration = s.NodeIteration
		}
		out = append(out, attribute.Int(AttrAINodeIteration, iteration))
	}
	return out
}

// GenAIAttributes is SpanAttributes plus the GenAI conventions' names for the
// same facts, for the spans the AI dashboards read (an agent turn, an LLM
// call, a tool call): the chat is the conversation, the thread is the agent,
// and its preset — else its workflow — is the agent's name.
func (s RunScope) GenAIAttributes() []attribute.KeyValue {
	out := s.SpanAttributes()
	if s.ChatID != "" {
		out = append(out, attribute.String(AttrGenAIConversationID, s.ChatID))
	}
	if s.ThreadID != "" {
		out = append(out, attribute.String(AttrGenAIAgentID, s.ThreadID))
	}
	if name := s.AgentName(); name != "" {
		out = append(out, attribute.String(AttrGenAIAgentName, name))
	}
	return out
}

// AgentName is the agent's name: its preset, else the workflow it runs.
func (s RunScope) AgentName() string {
	if s.Preset != "" {
		return s.Preset
	}
	return s.WorkflowName
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

// ChatOwner is whose a chat is: its user and, when it belongs to one, its org.
type ChatOwner struct {
	UserID string
	OrgID  string
}

// ChatOwnerCache answers "whose chat is this" for the code that stamps a run
// scope. A chat's owner never changes, so an answer is kept for ttl. A failed or
// empty lookup is kept as "unknown" for a much shorter time (failTTL): it stamps
// no user rather than failing the work it was describing, and a database that is
// down is asked once per interval, not once per activity.
type ChatOwnerCache struct {
	lookup  func(ctx context.Context, chatID string) (ChatOwner, error)
	ttl     time.Duration
	failTTL time.Duration
	max     int
	now     func() time.Time

	mu      sync.Mutex
	entries map[string]ownerEntry
}

type ownerEntry struct {
	owner   ChatOwner
	expires time.Time
}

// NewChatOwnerCache wraps lookup, which answers a chat's user. At most max
// chats are kept; past that the table is emptied, which costs one lookup per
// live chat and bounds memory without an eviction order to keep correct.
func NewChatOwnerCache(lookup func(ctx context.Context, chatID string) (string, error), ttl time.Duration, max int) *ChatOwnerCache {
	if lookup == nil {
		return NewChatOwnerOrgCache(nil, ttl, max)
	}
	return NewChatOwnerOrgCache(func(ctx context.Context, chatID string) (ChatOwner, error) {
		user, err := lookup(ctx, chatID)
		return ChatOwner{UserID: user}, err
	}, ttl, max)
}

// NewChatOwnerOrgCache is NewChatOwnerCache for a lookup that also answers
// the chat's org (ChatOf).
func NewChatOwnerOrgCache(lookup func(ctx context.Context, chatID string) (ChatOwner, error), ttl time.Duration, max int) *ChatOwnerCache {
	return &ChatOwnerCache{lookup: lookup, ttl: ttl, failTTL: 30 * time.Second, max: max, now: time.Now, entries: map[string]ownerEntry{}}
}

// Owner is chatID's user, or "" when it has none or cannot be read. A nil cache
// answers "".
func (c *ChatOwnerCache) Owner(ctx context.Context, chatID string) string {
	return c.ChatOf(ctx, chatID).UserID
}

// ChatOf is chatID's user and org; the zero ChatOwner when it has no user or
// cannot be read. A nil cache answers the zero ChatOwner.
func (c *ChatOwnerCache) ChatOf(ctx context.Context, chatID string) ChatOwner {
	if c == nil || c.lookup == nil || chatID == "" {
		return ChatOwner{}
	}
	now := c.now()
	c.mu.Lock()
	e, ok := c.entries[chatID]
	c.mu.Unlock()
	if ok && now.Before(e.expires) {
		return e.owner
	}
	owner, err := c.lookup(ctx, chatID)
	keep := c.ttl
	if err != nil || owner.UserID == "" {
		owner, keep = ChatOwner{}, c.failTTL
	}
	c.mu.Lock()
	if len(c.entries) >= c.max {
		c.entries = map[string]ownerEntry{}
	}
	c.entries[chatID] = ownerEntry{owner: owner, expires: now.Add(keep)}
	c.mu.Unlock()
	return owner
}
