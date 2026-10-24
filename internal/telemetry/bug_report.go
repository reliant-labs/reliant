// Copyright (c) 2025 Reliant Labs
package telemetry

import (
	"context"
	"log/slog"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/observe"
)

// Agent-filed bug reports.
//
// An agent that hits a defect in Reliant itself, or in forge, files it with the
// report_bug tool, and each report becomes ONE structured log record
// (FileBugReport). The process's log pipeline ships it to forge's ClickStack
// like every other record; nothing about delivery is special.
//
// The record is an issue event, not an error. It is written at WARN, so it never
// counts toward an error rate, an error alert or a release-health gate, and it
// carries issue.source=llm_bug_report: the control plane's issues worker makes
// each record of that source an issue — one per report, like a GitHub issue,
// because error.group is the report's own id — and `forge obs issues --source
// llm_bug_report` lists them. Deduplicating reports is a person's call, made
// while triaging them.
//
// The report's text was written by the model FOR engineering, as a defect
// report, and is the whole point of the record, so it is kept — redacted of
// credential shapes and bounded. It goes to our own telemetry and never to a
// public tracker: it can hold the user's context.

const (
	// BugReportSource is the issue source every agent-filed report carries,
	// and the first element of its fingerprint.
	BugReportSource = "llm_bug_report"

	// AttrIssueSource marks a record as an issue event of the named source,
	// whatever its level. forge spells it obsquery.LogKeyIssueSource.
	AttrIssueSource = "issue.source"

	// The report's own attributes.
	AttrBugReportID       = "bug_report.id"
	AttrBugReportProduct  = "bug_report.product"
	AttrBugReportSeverity = "bug_report.severity"
	AttrBugReportTitle    = "bug_report.title"
	AttrBugReportSummary  = "bug_report.summary"
	AttrBugReportExpected = "bug_report.expected"
	AttrBugReportActual   = "bug_report.actual"
	AttrBugReportEvidence = "bug_report.evidence"

	// maxBugReportFieldRunes bounds each text field. Generous enough for a
	// command, its error output and a few file paths; a field that is longer
	// is a log dump.
	maxBugReportFieldRunes = 4000

	// maxFingerprintTitleRunes bounds the title part of the fingerprint.
	maxFingerprintTitleRunes = 120
)

// BugReport is one agent-filed defect report and where it was filed from.
type BugReport struct {
	// Product is "reliant" or "forge".
	Product string
	// Severity is "low", "medium", "high" or "critical".
	Severity string
	Title    string
	Summary  string
	Expected string
	Actual   string
	// Evidence is reproduction steps and what was observed: commands, error
	// text, file paths, ids.
	Evidence string

	// Each identifier below becomes an attribute when set.
	ChatID     string
	ThreadID   string
	ToolCallID string
	ProjectID  string
	UserID     string
	WorktreeID string
	Workflow   string
	Model      string
	// DaemonID is the machine the run's tools executed on.
	DaemonID string
	// DaemonType is that machine's kind: "managed" (a cloud workspace) or
	// "self_hosted" (the desktop app, or a daemon the user runs themselves).
	DaemonType string
	// Pod is a managed machine's workspace pod. It is never set for a
	// self-hosted machine, whose hostname names the user's own computer.
	Pod string

	// ReliantVersion and ForgeVersion are the build that filed the report:
	// reliant's version, and the forge it embeds.
	ReliantVersion string
	ForgeVersion   string
}

// FileBugReport writes report to logger (the process logger when nil) as one
// issue event and returns the report's id, which the record carries as
// bug_report.id: the handle the agent is given, and what finds the record in
// ClickStack.
func FileBugReport(ctx context.Context, logger *slog.Logger, report BugReport) string {
	if logger == nil {
		logger = slog.Default()
	}
	id := uuid.NewString()
	logger.LogAttrs(ctx, slog.LevelWarn, BugReportMessage(report.Product, report.Title), bugReportAttrs(ctx, id, report)...)
	return id
}

// BugReportMessage is a report's record body, and so its issue's title:
// "[product] title", the title redacted.
func BugReportMessage(product, title string) string {
	return "[" + product + "] " + redactBugReportText(title)
}

// bugReportAttrs are the record's attributes: the issue source and group, the
// report's text, redacted and bounded, and every identifier that is set.
func bugReportAttrs(ctx context.Context, id string, report BugReport) []slog.Attr {
	attrs := []slog.Attr{
		slog.String(AttrIssueSource, BugReportSource),
		observe.ErrorGroup(ctx, BugReportGroup(id)),
		slog.String(AttrBugReportID, id),
	}
	for _, field := range []struct{ key, value string }{
		{AttrBugReportProduct, report.Product},
		{AttrBugReportSeverity, report.Severity},
		{AttrBugReportTitle, redactBugReportText(report.Title)},
		{AttrBugReportSummary, redactBugReportText(report.Summary)},
		{AttrBugReportExpected, redactBugReportText(report.Expected)},
		{AttrBugReportActual, redactBugReportText(report.Actual)},
		{AttrBugReportEvidence, redactBugReportText(report.Evidence)},
		{"chat_id", report.ChatID},
		{"thread_id", report.ThreadID},
		{"tool_call_id", report.ToolCallID},
		{"project_id", report.ProjectID},
		{AttrUserID, report.UserID},
		{"worktree_id", report.WorktreeID},
		{"workflow", report.Workflow},
		{"model", report.Model},
		{"daemon_id", report.DaemonID},
		{"daemon_type", report.DaemonType},
		{"pod", report.Pod},
		{"reliant_version", report.ReliantVersion},
		{"forge_version", report.ForgeVersion},
	} {
		if field.value != "" {
			attrs = append(attrs, slog.String(field.key, field.value))
		}
	}
	return attrs
}

// BugReportFingerprint names the defect a report describes: the source, the
// product and the normalized title. Two reports from one chat describing one
// defect in different case or punctuation share it, which is how report_bug
// answers a looping agent's repeat with the first report. It does NOT group
// issues: every report is its own (BugReportGroup). The title is redacted
// first, so a credential pasted into one can never become part of the key.
func BugReportFingerprint(product, title string) []string {
	return []string{BugReportSource, product, normalizeBugTitle(redactSecrets(title))}
}

// BugReportGroup is a report's error.group, "llm_bug_report/<report id>": the
// key the issues worker groups by (sfp1 hashes error.group when a record has
// one), so each report is exactly one issue.
func BugReportGroup(reportID string) string {
	return BugReportSource + "/" + reportID
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

// redactBugReportText redacts credential shapes from text and cuts it to
// maxBugReportFieldRunes. Lines are kept: unlike an error string, evidence was
// written to be read whole.
func redactBugReportText(text string) string {
	text = redactSecrets(text)
	if utf8.RuneCountInString(text) > maxBugReportFieldRunes {
		text = string([]rune(text)[:maxBugReportFieldRunes]) + truncatedMarker
	}
	return text
}
