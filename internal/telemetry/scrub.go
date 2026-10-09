// Copyright (c) 2025 Reliant Labs
package telemetry

// What may reach Sentry, and why this file is an allowlist.
//
// Sentry is told what broke and where: the error type, the stack trace, the
// service and version, and the identifiers needed to find the run (chat,
// workflow, run, thread, step, tool call, user). It is never told what the
// user was doing: prompts, chat messages, LLM request/response bodies, tool
// inputs and outputs, file contents, diffs, command output, environment values
// or credentials.
//
// A scrubber cannot recognise content, so this one does not try. Free-form
// fields are dropped unless their key is known to hold an identifier or a
// small enum AND the value looks like one. The two fields that are free-form by
// nature and too useful to drop — exception values and the event message — are
// cut to their first line and a short prefix with credential shapes redacted,
// because Go error strings routinely wrap tool output and provider responses.
//
// One event class is exempt, narrowly: a bug report an agent deliberately files
// with the report_bug tool keeps its authored text (redacted and bounded),
// because that text is the report. See bug_report.go.
//
// The same policy is implemented for the browser in web/src/lib/sentryScrub.ts
// and for Electron's main process in electron/src/sentry-scrub.js. Keep them in
// step: the three runtimes report into one Sentry organisation.

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/getsentry/sentry-go"
)

const (
	// maxMessageRunes bounds an exception value or event message. Long enough
	// for "activity call_llm failed: anthropic: 400 Bad Request: ...", short
	// enough that a wrapped tool output contributes at most a fragment.
	maxMessageRunes = 160
	truncatedMarker = " [truncated]"
	redactedMarker  = "[redacted]"
)

// standardContexts are populated by the SDK itself (OS, Go runtime, device,
// trace ids) and carry no user data, so they pass through untouched. Every
// other context is caller-supplied and goes through the field allowlist.
var standardContexts = map[string]bool{
	"os": true, "runtime": true, "device": true, "trace": true, "app": true, "culture": true,
}

// messageKeys hold error text. They are kept, bounded like an exception value.
var messageKeys = map[string]bool{"error": true, "err": true, "message": true, "logmessage": true}

// metadataKeys hold small enums worth keeping (keys compared via normalizeKey).
// A key not listed here and not an identifier is dropped whatever its value.
var metadataKeys = map[string]bool{
	"type": true, "kind": true, "component": true, "operation": true, "procedure": true,
	"method": true, "code": true, "status": true, "statuscode": true,
	"errortype": true, "errorcategory": true, "errorcode": true, "errorname": true,
	"activitytype": true, "provider": true, "model": true, "driver": true, "service": true,
	"phase": true, "step": true, "laststep": true, "source": true, "level": true, "category": true,
	"environment": true, "release": true, "version": true,
	"url": true, "baseurl": true, "route": true, "from": true, "to": true,
	"grpcservice": true, "grpcmethod": true, "grpccode": true,
	"durationms": true, "attempt": true, "attemptnumber": true,
	"logsource": true, "loggroup": true, "funnelevent": true, "funnelstep": true,
}

// urlKeys may carry a query string or fragment (OAuth codes, signed URLs).
var urlKeys = map[string]bool{"url": true, "baseurl": true, "from": true, "to": true}

// identifierValue is the shape of an id or enum: one token, no whitespace,
// nothing that reads as prose or code.
var identifierValue = regexp.MustCompile(`^[A-Za-z0-9_.:/@+%\-]{1,128}$`)

// secretPatterns redact credential shapes from the text that is kept.
var secretPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	// URL userinfo: postgres://user:password@host
	{regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.\-]*://)[^\s/@:]+:[^\s/@]+@`), "${1}" + redactedMarker + "@"},
	// key=value / "key": "value" for credential-named keys, including an
	// Authorization header's scheme so the whole credential goes at once.
	{regexp.MustCompile(`(?i)\b(authorization|x-api-key|api[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|client[_-]?secret|secret|password|passwd|token)(["']?\s*[:=]\s*["']?)(?:(?:bearer|basic|token)\s+)?[^\s"'&,;]+`), "${1}${2}" + redactedMarker},
	// A bare authorization scheme outside a key=value pair.
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=\-]+`), "${1} " + redactedMarker},
	// JWTs.
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]*`), redactedMarker},
	// Provider and platform key prefixes.
	{regexp.MustCompile(`\b(sk-ant-[A-Za-z0-9_\-]{8,}|sk-[A-Za-z0-9_\-]{16,}|sk_(?:live|test)_[A-Za-z0-9]{8,}|(?:rlat|dpat|gho|ghp|ghu|ghs|ghr|github_pat|glpat|xox[abprs])[_\-][A-Za-z0-9_\-]{10,}|AIza[0-9A-Za-z_\-]{20,}|ya29\.[0-9A-Za-z_\-]+|AKIA[0-9A-Z]{16})`), redactedMarker},
	// Long opaque runs (digests, base64 blobs). '-', '/' and '.' are excluded so
	// UUIDs, paths and dotted names are not swallowed.
	{regexp.MustCompile(`[A-Za-z0-9+=_]{40,}`), redactedMarker},
}

// scrubEvent applies the policy above to an error or transaction event in
// place and returns it. It never drops the event: the fact that something
// failed, and where, is exactly what Sentry is for.
func scrubEvent(event *sentry.Event) *sentry.Event {
	if event == nil {
		return nil
	}

	event.Message = boundMessage(event.Message)
	for i := range event.Exception {
		ex := &event.Exception[i]
		ex.Value = boundMessage(ex.Value)
		scrubStacktrace(ex.Stacktrace)
		if ex.Mechanism != nil {
			// Type and Handled say how it was caught; the rest is free-form.
			ex.Mechanism.Description = ""
			ex.Mechanism.HelpLink = ""
			ex.Mechanism.Data = nil
		}
	}
	for i := range event.Threads {
		scrubStacktrace(event.Threads[i].Stacktrace)
	}

	// An agent-filed bug report is the one exception to this policy: its
	// report-only tags and authored text are kept, redacted and bounded
	// (bug_report.go). Decided, and those tags read, from the event as it
	// arrived, before scrubTags replaces its tags.
	bugReport := isBugReport(event)

	tags := scrubTags(event.Tags)
	if bugReport {
		tags = bugReportTags(tags, event.Tags)
	}
	event.Tags = tags
	for name, ctx := range event.Contexts {
		if standardContexts[name] {
			continue
		}
		if bugReport && name == bugReportContext {
			event.Contexts[name] = scrubBugReportFields(ctx)
			continue
		}
		if kept := scrubFields(ctx); len(kept) > 0 {
			event.Contexts[name] = kept
		} else {
			delete(event.Contexts, name)
		}
	}

	if event.Request != nil {
		event.Request = &sentry.Request{Method: event.Request.Method, URL: stripQuery(event.Request.URL)}
	}

	// Breadcrumbs keep their place in the timeline, not their payload.
	for i, crumb := range event.Breadcrumbs {
		if crumb == nil {
			continue
		}
		event.Breadcrumbs[i] = &sentry.Breadcrumb{
			Type:      crumb.Type,
			Category:  crumb.Category,
			Level:     crumb.Level,
			Timestamp: crumb.Timestamp,
		}
	}

	event.User = sentry.User{ID: event.User.ID}
	event.Attachments = nil

	for _, span := range event.Spans {
		if span == nil {
			continue
		}
		span.Description = scrubSpanDescription(span.Description)
		span.Data = scrubFields(span.Data)
		span.Tags = scrubTags(span.Tags)
	}

	return event
}

// boundMessage keeps the first line of s, redacts credential shapes, and cuts
// it to maxMessageRunes. Multi-line strings are where tool output, file
// contents and subprocess stderr end up, so everything after the first line
// goes.
func boundMessage(s string) string {
	if s == "" {
		return s
	}
	first, _, multiline := strings.Cut(s, "\n")
	first = strings.TrimRight(first, "\r\t ")
	first = redactSecrets(first)
	cut := multiline
	if utf8.RuneCountInString(first) > maxMessageRunes {
		first = string([]rune(first)[:maxMessageRunes])
		cut = true
	}
	if cut {
		first += truncatedMarker
	}
	return first
}

func redactSecrets(s string) string {
	for _, p := range secretPatterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}

// scrubFields keeps scalar values whose key and shape pass the allowlist.
func scrubFields(in map[string]interface{}) map[string]interface{} {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(in))
	for key, value := range in {
		if kept, ok := scrubValue(key, value); ok {
			out[key] = kept
		}
	}
	return out
}

func scrubTags(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		if kept, ok := scrubString(key, value); ok {
			out[key] = kept
		}
	}
	return out
}

func scrubValue(key string, value interface{}) (interface{}, bool) {
	switch v := value.(type) {
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		// A number or a flag cannot carry a prompt.
		return v, true
	case string:
		return scrubString(key, v)
	default:
		// Maps, slices and structs are exactly where request bodies and tool
		// payloads hide; nothing here inspects them.
		return nil, false
	}
}

func scrubString(key, value string) (string, bool) {
	if messageKeys[normalizeKey(key)] {
		return boundMessage(value), true
	}
	return TagValue(key, value)
}

// TagValue reports whether a field may be sent to Sentry as a tag, and the
// value to send. Only identifiers (chat_id, tool_call_id, ...) and the small
// enums in metadataKeys qualify, and only when the value has the shape of one.
//
// It is exported so logging's slog bridge picks tags with this same allowlist
// at the source, rather than promoting every short attribute and relying on
// BeforeSend to take the content back out. One list, so the two cannot drift.
func TagValue(key, value string) (string, bool) {
	norm := normalizeKey(key)
	if !isIdentifierKey(key) && !metadataKeys[norm] {
		return "", false
	}
	if urlKeys[norm] {
		value = stripQuery(value)
	}
	if !identifierValue.MatchString(value) {
		return "", false
	}
	return value, true
}

// isIdentifierKey matches id, chat_id, tool_call_ids, chatId, activityID.
func isIdentifierKey(key string) bool {
	if normalizeKey(key) == "id" {
		return true
	}
	for _, suffix := range []string{"_id", "_ids", "Id", "Ids", "ID", "IDs"} {
		if strings.HasSuffix(key, suffix) {
			return true
		}
	}
	return false
}

// normalizeKey folds case and separators so log_message, logMessage and
// log-message are one key.
func normalizeKey(key string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '_', '-', '.', ' ':
			return -1
		}
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, key)
}

func stripQuery(raw string) string {
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		return raw[:i]
	}
	return raw
}

// spanDescription is the "GET /path" shape an HTTP span carries. Anything else
// (a SQL statement, a prompt passed as a span name) is free text and dropped.
var spanDescription = regexp.MustCompile(`^(?:[A-Z]+ )?[A-Za-z0-9_.:/@+%\-]{1,200}$`)

func scrubSpanDescription(description string) string {
	description = stripQuery(description)
	if spanDescription.MatchString(description) {
		return description
	}
	return ""
}

func scrubStacktrace(st *sentry.Stacktrace) {
	if st == nil {
		return
	}
	for i := range st.Frames {
		st.Frames[i].Vars = nil
	}
}
