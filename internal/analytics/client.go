// Copyright (c) 2025 Reliant Labs
//
//forge:exclude-contract: the product-event emitter: one concrete Client over the process logger, installed once at startup; tests read what it writes through a slog handler, so there is no collaborator to fake
package analytics

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/version"
)

// Product events ride the telemetry the process already ships: each one is a
// single INFO record through the process logger, named product.<event>. On a
// server that line is collected from stdout into ClickStack's otel_logs (and,
// where OTLP log export is switched on, leaves as an OTel log record instead),
// so a funnel is a ClickHouse query over LogAttributes['event.name'] and
// LogAttributes['user.id']. dev-docs/product-events.md has the shape, the
// reasons for it and an example query.
const (
	// EventNamePrefix starts every product event's name.
	EventNamePrefix = "product."

	// AppClientBackend is the app.client value of every event written here;
	// the web runtime and Electron stamp their own on the events they emit.
	AppClientBackend = "backend"

	// AttrEventName, AttrUserID, AttrAppClient and AttrAppVersion are the attributes every
	// event carries. Typed fields are AttrFieldPrefix + their snake_case name.
	AttrEventName   = "event.name"
	AttrUserID      = "user.id"
	AttrAppVersion  = "app.version"
	AttrAppClient   = "app.client"
	AttrFieldPrefix = "product."
)

// dedupWindow is how long an identical event for the same user is suppressed:
// long enough to absorb a double-fired effect or a retried RPC, short enough
// never to hide a real repeat.
const dedupWindow = 5 * time.Second

// Settings is a server role's AnalyticsConfig.
type Settings struct {
	// Disabled turns every product event off (RELIANT_ANALYTICS_DISABLED).
	Disabled bool
}

// Client emits product events. A process has at most one, installed by
// Start; without one every Tracker is silent.
type Client struct {
	consent PrivacyChecker
	// logger is where events are written; nil means slog.Default() at the
	// moment of each event, so the process logger may be installed (or
	// replaced) after Start.
	logger *slog.Logger
	now    func() time.Time

	mu     sync.Mutex
	recent map[string]time.Time // dedup key -> when it was last emitted
}

// NewClient returns a Client that reads each user's analytics consent from
// consent and writes events to logger (nil: the process logger).
func NewClient(consent PrivacyChecker, logger *slog.Logger) *Client {
	return &Client{consent: consent, logger: logger, now: time.Now, recent: make(map[string]time.Time)}
}

var installed atomic.Pointer[Client]

// Start installs the process's product-event client. With s.Disabled nothing
// is installed and every event is dropped.
func Start(s Settings, consent PrivacyChecker) {
	if s.Disabled {
		logging.Info("[Analytics] Product events disabled by RELIANT_ANALYTICS_DISABLED")
		installed.Store(nil)
		return
	}
	installed.Store(NewClient(consent, nil))
}

// ForUser is the installed client's Tracker for userID (Client.ForUser).
func ForUser(ctx context.Context, userID string) Tracker {
	return installed.Load().ForUser(ctx, userID)
}

func (c *Client) log() *slog.Logger {
	if c.logger != nil {
		return c.logger
	}
	return slog.Default()
}

// emit writes one event unless an identical one (same name, user and
// identity) was written within dedupWindow. identity names the event
// instance; empty means every field.
func (c *Client) emit(ctx context.Context, userID string, event EventType, identity string, fields []slog.Attr) {
	name := EventNamePrefix + string(event)
	if identity == "" {
		identity = attrsKey(fields)
	}
	if c.duplicate(name + "\x00" + userID + "\x00" + identity) {
		return
	}

	attrs := make([]slog.Attr, 0, len(fields)+4)
	attrs = append(attrs, slog.String(AttrEventName, name))
	if userID != "" {
		attrs = append(attrs, slog.String(AttrUserID, userID))
	}
	attrs = append(attrs, slog.String(AttrAppClient, AppClientBackend), slog.String(AttrAppVersion, version.Version))
	for _, f := range fields {
		f.Key = AttrFieldPrefix + f.Key
		attrs = append(attrs, f)
	}
	c.log().LogAttrs(ctx, slog.LevelInfo, name, attrs...)
}

func (c *Client) duplicate(key string) bool {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.recent) > 1000 {
		for k, at := range c.recent {
			if now.Sub(at) >= dedupWindow {
				delete(c.recent, k)
			}
		}
	}
	if at, ok := c.recent[key]; ok && now.Sub(at) < dedupWindow {
		return true
	}
	c.recent[key] = now
	return false
}

func attrsKey(fields []slog.Attr) string {
	var b strings.Builder
	for _, f := range fields {
		fmt.Fprintf(&b, "%s=%v;", f.Key, f.Value.Any())
	}
	return b.String()
}

// Tracker records product events about one user. The zero Tracker — no
// client installed, analytics disabled, or a user who opted out — records
// nothing, so callers never check.
type Tracker struct {
	c *Client
	// ctx is the request the event happened in. It is only read for the
	// trace and span ids the logger stamps on the record, which join a
	// backend event to the browser request that caused it.
	ctx    context.Context
	userID string
}

func (t Tracker) emit(event EventType, identity string, fields ...slog.Attr) {
	if t.c == nil {
		return
	}
	ctx := t.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	t.c.emit(ctx, t.userID, event, identity, fields)
}

// TrackSessionStart records the first authenticated request after a user was
// idle (see the auth interceptor's session tracker).
func (t Tracker) TrackSessionStart() {
	t.emit(EventSessionStart, "")
}

// TrackMessageSent records a message the user sent in a chat.
func (t Tracker) TrackMessageSent(m MessageSentMetrics) {
	fields := []slog.Attr{
		slog.String("message_id", m.MessageID),
		slog.String("chat_id", m.ChatID),
		slog.String("project_id", m.ProjectID),
		slog.Bool("has_attachments", m.HasAttachments),
		slog.Int("content_length", m.ContentLength),
		slog.Bool("is_first_in_chat", m.IsFirstInChat),
	}
	fields = appendNonEmpty(fields, "workflow_id", m.WorkflowID)
	fields = appendNonEmpty(fields, "thread_id", m.ThreadID)
	fields = appendNonEmpty(fields, "workflow_name", m.WorkflowName)
	fields = appendNonEmpty(fields, "workflow_type", m.WorkflowType)
	t.emit(EventMessageSent, m.MessageID, fields...)
}

// TrackProjectOpened records the user opening a project.
func (t Tracker) TrackProjectOpened(m ProjectOpenedMetrics) {
	t.emit(EventProjectOpened, m.ProjectID,
		slog.String("project_id", m.ProjectID),
		slog.Bool("is_git_repo", m.IsGitRepo))
}

// TrackWorkflowStarted records a workflow execution starting.
func (t Tracker) TrackWorkflowStarted(m WorkflowMetrics) {
	t.emit(EventWorkflowStarted, m.WorkflowID, workflowFields(m)...)
}

// TrackWorkflowEnded records a workflow execution finishing.
func (t Tracker) TrackWorkflowEnded(m WorkflowMetrics) {
	fields := append(workflowFields(m),
		slog.Int64("duration_ms", m.Duration.Milliseconds()),
		slog.Bool("success", m.Success))
	fields = appendNonEmpty(fields, "error_message", m.ErrorMessage)
	if m.Iterations > 0 {
		fields = append(fields, slog.Int("iterations", m.Iterations))
	}
	t.emit(EventWorkflowEnded, fmt.Sprintf("%s:%t", m.WorkflowID, m.Success), fields...)
}

func workflowFields(m WorkflowMetrics) []slog.Attr {
	fields := []slog.Attr{
		slog.String("workflow_id", m.WorkflowID),
		slog.String("workflow_name", m.WorkflowName),
		slog.String("workflow_type", m.WorkflowType),
		slog.String("chat_id", m.ChatID),
		slog.Bool("is_workspace", m.IsWorkspace),
		slog.Bool("is_child", m.IsChild),
	}
	fields = appendNonEmpty(fields, "project_id", m.ProjectID)
	fields = appendNonEmpty(fields, "model_id", m.ModelID)
	fields = appendList(fields, "presets", m.Presets)
	fields = appendList(fields, "preset_types", m.PresetTypes)
	fields = appendList(fields, "providers", m.Providers)
	return fields
}

// TrackWorkflowDraftSaved records a workflow draft being saved.
func (t Tracker) TrackWorkflowDraftSaved(m WorkflowDraftSavedMetrics) {
	t.emit(EventWorkflowDraftSaved, m.WorkflowSlug,
		slog.String("workflow_slug", m.WorkflowSlug),
		slog.String("workflow_name", m.WorkflowName),
		slog.Bool("is_new", m.IsNew),
		slog.Bool("is_valid", m.IsValid),
		slog.String("status", m.Status))
}

// TrackWorkflowDraftCreated records a new workflow draft.
func (t Tracker) TrackWorkflowDraftCreated(m WorkflowDraftCreatedMetrics) {
	fields := []slog.Attr{
		slog.String("workflow_slug", m.WorkflowSlug),
		slog.String("workflow_name", m.WorkflowName),
	}
	fields = appendNonEmpty(fields, "project_id", m.ProjectID)
	t.emit(EventWorkflowDraftCreated, m.WorkflowSlug, fields...)
}

// TrackPreferencesUpdated records the user changing their preferences.
func (t Tracker) TrackPreferencesUpdated(m PreferencesUpdatedMetrics) {
	fields := appendList(nil, "changed_keys", m.ChangedKeys)
	fields = append(fields, slog.Bool("model_provider_settings_changed", m.ModelProviderSettingsChanged))
	t.emit(EventPreferencesUpdated, "", fields...)
}

// TrackProviderSettingsUpdated records a provider credential being
// connected, changed or removed.
func (t Tracker) TrackProviderSettingsUpdated(m ProviderSettingsUpdatedMetrics) {
	fields := []slog.Attr{
		slog.String("provider", m.Provider),
		slog.String("action", m.Action),
	}
	fields = appendNonEmpty(fields, "auth_method", m.AuthMethod)
	t.emit(EventProviderSettingsUpdate, m.Provider+":"+m.Action, fields...)
}

// TrackPageVisited records the user navigating to a page or panel.
func (t Tracker) TrackPageVisited(m PageVisitedMetrics) {
	fields := []slog.Attr{slog.String("page_name", m.PageName)}
	fields = appendNonEmpty(fields, "previous_page", m.PreviousPage)
	t.emit(EventPageVisited, m.PageName, fields...)
}

// TrackLLMCallCompleted records one model call finishing.
func (t Tracker) TrackLLMCallCompleted(m LLMCallMetrics) {
	fields := []slog.Attr{
		slog.String("provider", m.Provider),
		slog.String("model", m.Model),
		slog.Int("input_tokens", m.InputTokens),
		slog.Int("output_tokens", m.OutputTokens),
		slog.Int64("latency_ms", m.LatencyMs),
		slog.Bool("success", m.Success),
		slog.Bool("is_streaming", m.IsStreaming),
	}
	fields = appendNonEmpty(fields, "error_type", m.ErrorType)
	fields = appendNonEmpty(fields, "workflow_id", m.WorkflowID)
	fields = appendNonEmpty(fields, "chat_id", m.ChatID)
	fields = appendNonEmpty(fields, "step_name", m.StepName)
	if m.CacheReadTokens > 0 {
		fields = append(fields, slog.Int("cache_read_tokens", m.CacheReadTokens))
	}
	if m.CacheWriteTokens > 0 {
		fields = append(fields, slog.Int("cache_write_tokens", m.CacheWriteTokens))
	}
	t.emit(EventLLMCallCompleted, "", fields...)
}

// TrackAPIKeyConfigured records a provider API key being configured.
func (t Tracker) TrackAPIKeyConfigured(m APIKeyConfiguredMetrics) {
	t.emit(EventAPIKeyConfigured, "",
		slog.String("provider", m.Provider),
		slog.String("auth_method", m.AuthMethod),
		slog.Bool("is_first_key", m.IsFirstKey),
		slog.Int("total_providers", m.TotalProviders))
}

// TrackFirstMessageSent records the user's first message ever.
func (t Tracker) TrackFirstMessageSent(m FirstMessageSentMetrics) {
	fields := []slog.Attr{
		slog.String("chat_id", m.ChatID),
		slog.String("project_id", m.ProjectID),
		slog.String("workflow_name", m.WorkflowName),
		slog.String("workflow_type", m.WorkflowType),
	}
	fields = appendNonEmpty(fields, "provider", m.Provider)
	t.emit(EventFirstMessageSent, "", fields...)
}

// TrackOnboardingEvent records an onboarding step; eventType is one of the
// EventOnboarding* events.
func (t Tracker) TrackOnboardingEvent(eventType EventType, m OnboardingMetrics) {
	fields := []slog.Attr{
		slog.Int("total_steps", m.TotalSteps),
		slog.Int("steps_completed", m.StepsCompleted),
		slog.Int("steps_skipped", m.StepsSkipped),
	}
	fields = appendNonEmpty(fields, "step_id", m.StepID)
	fields = appendNonEmpty(fields, "step_name", m.StepName)
	t.emit(eventType, "", fields...)
}

func appendNonEmpty(fields []slog.Attr, key, value string) []slog.Attr {
	if value == "" {
		return fields
	}
	return append(fields, slog.String(key, value))
}

func appendList(fields []slog.Attr, key string, values []string) []slog.Attr {
	if len(values) == 0 {
		return fields
	}
	return append(fields, slog.Any(key, values))
}
