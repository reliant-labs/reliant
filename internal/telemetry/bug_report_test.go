// Copyright (c) 2025 Reliant Labs
package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	anthropicKey = "sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	reliantToken = "rlat_0123456789abcdefghij"

	chatID     = "3f2a8c1e-9b7d-4e2f-8a6b-1c2d3e4f5a6b"
	toolCallID = "toolu_01AbCdEfGhIjKlMn"
	threadID   = "thr_7c9e6679"
	userID     = "user-8d7f"
)

func sampleBugReport() BugReport {
	return BugReport{
		Product:  "reliant",
		Severity: "high",
		Title:    "User message delivered to the model as [content trimmed]",
		Summary:  "The newest user message reached the model as the literal [content trimmed].",
		Expected: "The model receives the user's message intact.",
		Actual:   "The model replied that the message was truncated.",
		Evidence: "chat 5ffd6bb4: messages.token_count alternates 520k / 696k\nmodel window is 272000",

		ChatID:     chatID,
		ThreadID:   threadID,
		ToolCallID: toolCallID,
		ProjectID:  "d70701a6-8aa9-4fb8-8d13-9c41b708201d",
		UserID:     userID,
		WorktreeID: "1ddeaaf3-ffa8-43e0-a99e-08f219670a2c",
		Workflow:   "builtin://agent",
		Model:      "gpt-5.6-terra",
		DaemonID:   "daemon-2aab1465",
		DaemonType: "managed",
		Pod:        "ws-ws-2aab1465",

		ReliantVersion: "v0.9.3",
		ForgeVersion:   "v0.1.28-0.20261009201500-abcdef123456",
	}
}

// fileToJSON files report through a JSON logger and returns the one record it
// wrote, as the collector would read it.
func fileToJSON(t *testing.T, report BugReport) (string, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	id := FileBugReport(context.Background(), logger, report)
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 1, "one report is one record")
	var record map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &record))
	return id, record
}

func TestFileBugReport_WritesOneGroupedIssueEvent(t *testing.T) {
	id, record := fileToJSON(t, sampleBugReport())

	require.NotEmpty(t, id, "the model is handed the report's id")
	assert.Equal(t, id, record[AttrBugReportID])
	assert.Equal(t, "WARN", record["level"],
		"a report is an issue event, never an ERROR: it must not count as an error or fail a release gate")
	assert.Equal(t, "[reliant] User message delivered to the model as [content trimmed]", record["msg"])
	assert.Equal(t, BugReportSource, record[AttrIssueSource], "the issues worker groups records by issue source")
	assert.Equal(t, "llm_bug_report/"+id, record["error.group"], "a report is grouped by its own id")

	for key, want := range map[string]string{
		AttrBugReportProduct:  "reliant",
		AttrBugReportSeverity: "high",
		AttrBugReportTitle:    "User message delivered to the model as [content trimmed]",
		AttrBugReportSummary:  "The newest user message reached the model as the literal [content trimmed].",
		AttrBugReportExpected: "The model receives the user's message intact.",
		AttrBugReportActual:   "The model replied that the message was truncated.",
		AttrBugReportEvidence: "chat 5ffd6bb4: messages.token_count alternates 520k / 696k\nmodel window is 272000",
		"chat_id":             chatID,
		"thread_id":           threadID,
		"tool_call_id":        toolCallID,
		"project_id":          "d70701a6-8aa9-4fb8-8d13-9c41b708201d",
		AttrUserID:            userID,
		"workflow":            "builtin://agent",
		"model":               "gpt-5.6-terra",
		"daemon_id":           "daemon-2aab1465",
		"daemon_type":         "managed",
		// The machine, and the build, are what make a report actionable:
		// without them a workspace defect cannot be tied to a pod.
		"worktree_id":     "1ddeaaf3-ffa8-43e0-a99e-08f219670a2c",
		"pod":             "ws-ws-2aab1465",
		"reliant_version": "v0.9.3",
		"forge_version":   "v0.1.28-0.20261009201500-abcdef123456",
	} {
		assert.Equal(t, want, record[key], "attribute %s", key)
	}
}

func TestFileBugReport_OmitsUnsetIdentifiers(t *testing.T) {
	report := sampleBugReport()
	report.Pod, report.DaemonID, report.Evidence = "", "", ""
	_, record := fileToJSON(t, report)
	for _, key := range []string{"pod", "daemon_id", AttrBugReportEvidence} {
		assert.NotContains(t, record, key)
	}
}

// Every report is its own issue, even one identical to another: deduplicating
// is the person triaging them's call.
func TestFileBugReport_EveryReportIsItsOwnIssue(t *testing.T) {
	first, a := fileToJSON(t, sampleBugReport())
	second, b := fileToJSON(t, sampleBugReport())
	assert.NotEqual(t, first, second)
	assert.NotEqual(t, a["error.group"], b["error.group"], "two identical reports must not group into one issue")
	assert.Equal(t, "llm_bug_report/"+first, a["error.group"])
	assert.Equal(t, "llm_bug_report/"+second, b["error.group"])
}

func TestFileBugReport_RedactsCredentialsAndBoundsText(t *testing.T) {
	report := sampleBugReport()
	report.Title = "forge env deploy leaks " + anthropicKey
	report.Evidence = strings.Join([]string{
		"$ curl -H 'Authorization: Bearer " + reliantToken + "' https://api.reliantapi.com/x",
		"DATABASE_URL=postgres://reliant:hunter2pass@10.0.0.4:5432/reliant_cp",
		"ANTHROPIC_API_KEY=" + anthropicKey,
		"error: context deadline exceeded",
	}, "\n")
	// Prose, not one long run: a 40+ character token is an opaque blob and
	// is redacted outright, which is a different rule.
	report.Summary = strings.Repeat("the daemon lost its mount ", maxBugReportFieldRunes/10)

	var buf bytes.Buffer
	FileBugReport(context.Background(), slog.New(slog.NewJSONHandler(&buf, nil)), report)
	wire := buf.String()
	for _, secret := range []string{anthropicKey, reliantToken, "hunter2pass"} {
		assert.NotContains(t, wire, secret, "a credential in an authored report left the process")
	}

	var record map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &record))
	evidence, _ := record[AttrBugReportEvidence].(string)
	assert.Contains(t, evidence, "error: context deadline exceeded", "the diagnostic text itself is kept")
	assert.Contains(t, evidence, "https://api.reliantapi.com/x")
	assert.Equal(t, 4, strings.Count(evidence, "\n")+1, "every line of evidence survives redaction")

	summary, _ := record[AttrBugReportSummary].(string)
	assert.True(t, strings.HasSuffix(summary, truncatedMarker), "an overlong field is cut and says so")
	assert.LessOrEqual(t, len([]rune(summary)), maxBugReportFieldRunes+len(truncatedMarker))
}

func TestBugReportFingerprint_GroupsRewordingsOfOneDefect(t *testing.T) {
	a := BugReportFingerprint("reliant", "Workspace /home/user vanished after 3 retries (chat 5ffd6bb4)")
	b := BugReportFingerprint("reliant", "workspace /home/user VANISHED after 7 retries, chat 9a1c0e2f")
	assert.Equal(t, a, b, "case, punctuation and numbers must not split one defect into two issues")
	assert.NotEqual(t, a, BugReportFingerprint("forge", "Workspace /home/user vanished after 3 retries"),
		"the same title in another product is another defect")
	assert.Equal(t, []string{BugReportSource, "reliant", "untitled"}, BugReportFingerprint("reliant", "1234 5678"))
	assert.NotContains(t, strings.Join(BugReportFingerprint("forge", "leaks "+anthropicKey), " "), "sk-ant")
}
