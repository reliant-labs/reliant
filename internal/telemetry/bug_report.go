// Copyright (c) 2025 Reliant Labs
package telemetry

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/getsentry/sentry-go"
	"github.com/reliant-labs/reliant/internal/version"
)

// Agent-filed bug reports.
//
// An agent that hits a defect in Reliant itself, or in forge, files it with the
// report_bug tool, and each report becomes one Sentry event. These events are
// the one deliberate exception to the scrub policy in scrub.go: the report's
// text was written by the model FOR engineering, as a defect report, and is the
// whole point of the event, so it is kept — redacted of credential shapes and
// bounded — instead of dropped. Nothing else about the policy changes; see
// scrubEvent and TestScrubEvent_BugReportExceptionIsNarrow.

const (
	// BugReportSource is the "source" tag every agent-filed report carries,
	// and the first element of its fingerprint. Filter on it in Sentry.
	BugReportSource = "llm_bug_report"

	// bugReportContext is the event context holding the report's text.
	bugReportContext = "llm_bug_report"

	// maxBugReportFieldRunes bounds each text field. Generous enough for a
	// command, its error output and a few file paths; a field that is longer
	// is a log dump, and Sentry trims long context values anyway.
	maxBugReportFieldRunes = 4000

	// maxFingerprintTitleRunes bounds the title part of the fingerprint.
	maxFingerprintTitleRunes = 120
)

// BugReport is one agent-filed defect report and where it was filed from.
type BugReport struct {
	// Product is "reliant" or "forge".
	Product string
	// Severity is "low", "medium", "high" or "critical"; it sets the level.
	Severity string
	Title    string
	Summary  string
	Expected string
	Actual   string
	// Evidence is reproduction steps and what was observed: commands, error
	// text, file paths, ids.
	Evidence string

	// Each identifier below becomes a tag when set.
	ChatID     string
	ThreadID   string
	ToolCallID string
	ProjectID  string
	UserID     string
	Workflow   string
	Model      string
	DaemonID   string
	DaemonType string
}

// BugReportCapturer is a reporter that can deliver an agent-filed bug report.
// Only a live Sentry reporter implements it, which is how CaptureBugReport
// tells "sent" from "Sentry is off here".
type BugReportCapturer interface {
	CaptureBugReport(report BugReport) string
}

// CaptureBugReport files report with the process reporter. live is false when
// that reporter cannot deliver one — Sentry is disabled or has no DSN — and the
// caller must then record the report some other way; eventID is "" whenever
// nothing was sent.
func CaptureBugReport(report BugReport) (eventID string, live bool) {
	capturer, ok := GetReporter().(BugReportCapturer)
	if !ok {
		return "", false
	}
	return capturer.CaptureBugReport(report), true
}

// CaptureBugReport sends report as one Sentry event and returns its id.
func (r *SentryReporter) CaptureBugReport(report BugReport) string {
	r.mu.RLock()
	initialized := r.initialized
	hub := r.hub
	r.mu.RUnlock()

	if !initialized {
		return ""
	}
	if hub == nil {
		hub = sentry.CurrentHub()
	}
	if eventID := hub.CaptureEvent(bugReportEvent(report)); eventID != nil {
		return string(*eventID)
	}
	return ""
}

// bugReportEvent is the event for report, before BeforeSend scrubs it.
func bugReportEvent(report BugReport) *sentry.Event {
	event := sentry.NewEvent()
	event.Level = bugReportLevel(report.Severity)
	event.Logger = BugReportSource
	event.Message = "[" + report.Product + "] " + report.Title
	event.Fingerprint = BugReportFingerprint(report.Product, report.Title)
	event.User = sentry.User{ID: report.UserID}

	tags := map[string]string{
		"source":       BugReportSource,
		"product":      report.Product,
		"severity":     report.Severity,
		"chat_id":      report.ChatID,
		"thread_id":    report.ThreadID,
		"tool_call_id": report.ToolCallID,
		"project_id":   report.ProjectID,
		"user_id":      report.UserID,
		"workflow":     report.Workflow,
		"model":        report.Model,
		"daemon_id":    report.DaemonID,
		"daemon_type":  report.DaemonType,
		"app_version":  version.Version,
	}
	for key, value := range tags {
		if value != "" {
			event.Tags[key] = value
		}
	}

	event.Contexts[bugReportContext] = sentry.Context{
		"title":    report.Title,
		"summary":  report.Summary,
		"expected": report.Expected,
		"actual":   report.Actual,
		"evidence": report.Evidence,
	}
	return event
}

// bugReportLevel maps a report's severity onto a Sentry level, so an alert
// rule on level sees a critical report the way it sees a crash.
func bugReportLevel(severity string) sentry.Level {
	switch severity {
	case "critical":
		return sentry.LevelFatal
	case "high":
		return sentry.LevelError
	case "low":
		return sentry.LevelInfo
	default:
		return sentry.LevelWarning
	}
}

// BugReportFingerprint is the grouping key of a report: the source, the
// product and the normalized title. Two agents describing one defect in
// different case or punctuation, or naming a different chat while doing it,
// land in one Sentry issue rather than two. The title is redacted first, so a
// credential pasted into one can never become part of the key.
func BugReportFingerprint(product, title string) []string {
	return []string{BugReportSource, product, normalizeBugTitle(redactSecrets(title))}
}

// normalizeBugTitle lowercases title and keeps its words, dropping every word
// that contains a digit — ids, hashes, counts and ports vary between two
// sightings of the same defect, and are exactly what split one issue in two.
func normalizeBugTitle(title string) string {
	words := strings.FieldsFunc(strings.ToLower(title), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	kept := words[:0]
	for _, word := range words {
		if !strings.ContainsFunc(word, unicode.IsDigit) {
			kept = append(kept, word)
		}
	}
	normalized := strings.Join(kept, " ")
	if utf8.RuneCountInString(normalized) > maxFingerprintTitleRunes {
		normalized = strings.TrimSpace(string([]rune(normalized)[:maxFingerprintTitleRunes]))
	}
	if normalized == "" {
		return "untitled"
	}
	return normalized
}

// bugReportTagKeys are the tags only a bug report carries. They are small
// enums and names, but they are not in the general allowlist (metadataKeys),
// which the browser and Electron scrubbers mirror; a report keeps them by
// being a report.
var bugReportTagKeys = []string{"product", "severity", "workflow", "daemon_type", "app_version"}

// bugReportFields are the text fields of the report context.
var bugReportFields = map[string]bool{"title": true, "summary": true, "expected": true, "actual": true, "evidence": true}

// isBugReport reports whether event is an agent-filed bug report: both the
// source tag and the report context, which only bugReportEvent sets together.
func isBugReport(event *sentry.Event) bool {
	if event.Tags["source"] != BugReportSource {
		return false
	}
	_, ok := event.Contexts[bugReportContext]
	return ok
}

// bugReportTags adds the report-only tags in original to the already-scrubbed
// tags, each still subject to the identifier shape.
func bugReportTags(scrubbed, original map[string]string) map[string]string {
	for _, key := range bugReportTagKeys {
		value, ok := original[key]
		if !ok || !identifierValue.MatchString(value) {
			continue
		}
		if scrubbed == nil {
			scrubbed = map[string]string{}
		}
		scrubbed[key] = value
	}
	return scrubbed
}

// scrubBugReportFields keeps the report's text fields, each with credential
// shapes redacted and cut to maxBugReportFieldRunes. Lines are kept: unlike an
// error string, evidence was written to be read whole. Anything else in the
// context is dropped.
func scrubBugReportFields(in sentry.Context) sentry.Context {
	out := sentry.Context{}
	for key, value := range in {
		text, ok := value.(string)
		if !ok || !bugReportFields[key] || text == "" {
			continue
		}
		out[key] = redactBugReportText(text)
	}
	return out
}

// Redacted is the report with its text put through the same redaction and
// bound the Sentry event gets, for recording it anywhere else — the server log
// a report falls back to when Sentry is off.
func (r BugReport) Redacted() BugReport {
	r.Title = redactBugReportText(r.Title)
	r.Summary = redactBugReportText(r.Summary)
	r.Expected = redactBugReportText(r.Expected)
	r.Actual = redactBugReportText(r.Actual)
	r.Evidence = redactBugReportText(r.Evidence)
	return r
}

func redactBugReportText(text string) string {
	text = redactSecrets(text)
	if utf8.RuneCountInString(text) > maxBugReportFieldRunes {
		text = string([]rune(text)[:maxBugReportFieldRunes]) + truncatedMarker
	}
	return text
}
