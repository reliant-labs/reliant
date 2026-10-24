// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/ctxkeys"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/version"
)

// report_bug lets an agent file a defect in Reliant itself, or in forge, with
// engineering. Before it existed an agent that hit one — a user message
// arriving as "[content trimmed]", a workspace that vanished, a chat that
// could not resume — could only tell the user, and nothing reached anyone who
// could fix it.
//
// Each report becomes one issue event in forge's ClickStack
// (telemetry.FileBugReport): a structured log record with
// issue.source=llm_bug_report, grouped into issues by product and normalized
// title, and listed by `forge obs issues --source llm_bug_report`. The tool is
// server-placed: it needs no filesystem, and the worker's log pipeline is
// ours. Filing is a log write, so it cannot fail.

const (
	// ToolReportBug is in names.AllToolNames too, as the workflow validator's
	// copy.
	ToolReportBug = "report_bug"

	// reportBugWindow is how long a filed report answers a repeat of itself
	// in the same chat, and the period the per-chat cap counts over.
	reportBugWindow = 30 * time.Minute
	// reportBugPerChat is how many distinct reports one chat may file per
	// window. A real session hits one or two defects; more is an agent
	// cycling through titles.
	reportBugPerChat = 5
	// maxBugTitleRunes bounds the title. It is the issue's title, and ids
	// belong in evidence.
	maxBugTitleRunes = 200
)

const reportBugDescription = `Report a defect in Reliant itself, or in forge, to the Reliant engineering team. One call files it; there is nothing to follow up.

Use it when the product misbehaves, not the user's code:
- reliant: a tool returns wrong or broken output, a message reaches you truncated or garbled (e.g. "[content trimmed]"), the daemon, workspace or working directory vanishes, a chat or sub-agent is stuck or cannot resume, the UI shows something wrong.
- forge: a forge command fails, hangs or generates something wrong, or a forge lint, skill or doc is wrong.

Do not use it for bugs in the user's own application, for test or build failures in their code, or for your own mistakes.

File each defect once: a repeat in this chat returns the first report's id. Put what you observed in evidence — exact commands, error text, file paths, ids — never credentials or the user's file contents. Then carry on with your task, and still tell the user when the defect blocks them.`

// ReportBugParams is report_bug's input.
type ReportBugParams struct {
	Product  string `json:"product" jsonschema:"enum=reliant,enum=forge,description=Which product has the defect. reliant: the Reliant app — harness, tools, daemon, workspace, UI, message delivery. forge: the forge CLI and framework."`
	Severity string `json:"severity" jsonschema:"enum=low,enum=medium,enum=high,enum=critical,description=critical: data loss, or nothing can proceed. high: it blocks this task. medium: there is a workaround. low: cosmetic."`
	Title    string `json:"title" jsonschema:"description=One line naming the defect, e.g. 'User message delivered to the model as [content trimmed]'. Ids and numbers go in evidence."`
	Summary  string `json:"summary" jsonschema:"description=What happened, in a few sentences."`
	Expected string `json:"expected" jsonschema:"description=What should have happened."`
	Actual   string `json:"actual" jsonschema:"description=What happened instead, with the exact error text."`
	Evidence string `json:"evidence,omitempty" jsonschema:"description=How to reproduce it and what you observed: commands run, their output, file paths, chat or run ids."`
}

// ReportBugMetadata is report_bug's structured result.
type ReportBugMetadata struct {
	// ReportID is the filed report's bug_report.id (this call's, or the
	// earlier one a duplicate was answered with); empty when nothing was
	// filed.
	ReportID string `json:"report_id,omitempty"`
	// Duplicate: this chat already reported the defect within the window;
	// nothing new was sent.
	Duplicate bool `json:"duplicate,omitempty"`
	// RateLimited: this chat reached its cap; nothing was sent.
	RateLimited bool `json:"rate_limited,omitempty"`
}

// processBugReportLimiter is the process's one limiter. It has to outlive the
// tool, because the executor builds a fresh tool for every call.
var processBugReportLimiter = newBugReportLimiter(reportBugWindow, reportBugPerChat)

type reportBugTool struct {
	// lookup is nil where there is no database (the daemon runtime); the
	// report is then described by the tool context alone.
	lookup  bugReportLookup
	file    fileBugReport
	limiter *bugReportLimiter
	now     func() time.Time
}

// fileBugReport files a report and returns its id.
type fileBugReport func(ctx context.Context, report telemetry.BugReport) (reportID string)

// NewReportBugTool builds report_bug on the process logger.
func NewReportBugTool(lookup bugReportLookup) Tool {
	return newReportBugTool(lookup, func(ctx context.Context, report telemetry.BugReport) string {
		return telemetry.FileBugReport(ctx, nil, report)
	}, processBugReportLimiter, time.Now)
}

func newReportBugTool(lookup bugReportLookup, file fileBugReport, limiter *bugReportLimiter, now func() time.Time) Tool {
	return NewToolWrapper[ReportBugParams, ToolResponse](&reportBugTool{
		lookup:  lookup,
		file:    file,
		limiter: limiter,
		now:     now,
	})
}

func (t *reportBugTool) Name() string        { return ToolReportBug }
func (t *reportBugTool) Description() string { return reportBugDescription }

func (t *reportBugTool) RequiresPermission(ReportBugParams) (bool, error) {
	return false, nil
}

func (t *reportBugTool) Execute(tc *rctx.ToolContext, params ReportBugParams) (ToolResponse, error) {
	report, problem := params.toReport()
	if problem != "" {
		return NewTextErrorResponse(problem), nil
	}
	t.describeCaller(tc, &report)

	key := strings.Join(telemetry.BugReportFingerprint(report.Product, report.Title), "\x00")
	outcome := t.limiter.file(report.ChatID, key, t.now(), func() string {
		return t.file(tc.Context, report)
	})

	if outcome.Limited {
		logging.Info("LLM bug report not filed: chat reached its cap",
			"chat_id", report.ChatID, "product", report.Product, "cap", t.limiter.perChat)
	}
	return outcome.response(t.limiter), nil
}

// toReport validates the input and returns the report it describes, or why it
// was refused — every problem at once, so one retry is enough.
func (p ReportBugParams) toReport() (telemetry.BugReport, string) {
	report := telemetry.BugReport{
		Product:  strings.ToLower(strings.TrimSpace(p.Product)),
		Severity: strings.ToLower(strings.TrimSpace(p.Severity)),
		Title:    oneLine(p.Title, maxBugTitleRunes),
		Summary:  strings.TrimSpace(p.Summary),
		Expected: strings.TrimSpace(p.Expected),
		Actual:   strings.TrimSpace(p.Actual),
		Evidence: strings.TrimSpace(p.Evidence),
	}

	var problems []string
	if report.Product != "reliant" && report.Product != "forge" {
		problems = append(problems, fmt.Sprintf("product must be reliant or forge, not %q", p.Product))
	}
	switch report.Severity {
	case "low", "medium", "high", "critical":
	default:
		problems = append(problems, fmt.Sprintf("severity must be low, medium, high or critical, not %q", p.Severity))
	}
	var blank []string
	for _, field := range []struct{ name, value string }{
		{"title", report.Title}, {"summary", report.Summary}, {"expected", report.Expected}, {"actual", report.Actual},
	} {
		if field.value == "" {
			blank = append(blank, field.name)
		}
	}
	if len(blank) > 0 {
		problems = append(problems, "empty: "+strings.Join(blank, ", "))
	}
	if len(problems) > 0 {
		return report, "Bug report not filed (" + strings.Join(problems, "; ") + "). Fix these and call report_bug again."
	}
	return report, ""
}

// oneLine collapses s onto one line and bounds it.
func oneLine(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxRunes {
		s = strings.TrimSpace(string([]rune(s)[:maxRunes]))
	}
	return s
}

// describeCaller fills in where the report came from. Every lookup is best
// effort: a report that cannot say which daemon it ran on is still a report,
// and one lost to a failed read is not.
func (t *reportBugTool) describeCaller(tc *rctx.ToolContext, report *telemetry.BugReport) {
	report.ReliantVersion = version.Version
	report.ForgeVersion = version.Forge()
	report.ChatID = tc.ChatID
	report.ThreadID = tc.Thread
	if call, ok := tc.Value(ctxkeys.ToolCallContextKey).(*ctxkeys.ToolCallContext); ok && call != nil {
		report.ToolCallID = call.CurrentToolCallID
	}
	if tc.Project != nil {
		report.ProjectID = tc.Project.ID
	}
	if userID, ok := auth.GetUserIDFromContext(tc.Context); ok {
		report.UserID = userID
	}
	// The worktree's daemon is the one that holds its checkout and ran the
	// run's tools; the chat's active daemon is the fallback.
	if tc.Worktree != nil {
		report.WorktreeID = tc.Worktree.ID
		report.DaemonID = tc.Worktree.DaemonID
	}
	if t.lookup == nil {
		return
	}

	if tc.ChatID != "" {
		if chat, err := t.lookup.GetChat(tc.Context, tc.ChatID); err == nil && chat != nil {
			if report.UserID == "" {
				report.UserID = chat.UserID
			}
			if report.ProjectID == "" {
				report.ProjectID = chat.ProjectID
			}
			report.Workflow = stringOrEmpty(chat.WorkflowName)
			if report.DaemonID == "" {
				report.DaemonID = stringOrEmpty(chat.ActiveDaemonID)
			}
		}
	}
	// The thread's newest message is the assistant turn that made this call:
	// tool results are saved only after the batch finishes.
	if tc.Thread != "" {
		if msg, err := t.lookup.GetLatestMessageInThread(tc.Context, tc.Thread); err == nil && msg != nil {
			report.Model = stringOrEmpty(msg.Model)
		}
	}
	// Nothing pinned the run to a machine, so its tools went wherever default
	// resolution sent them. When the user has exactly one machine, that is
	// the one; with several, naming any of them would be a guess.
	if report.DaemonID == "" && report.UserID != "" {
		if daemons, err := t.lookup.ListDaemonsByUserID(tc.Context, report.UserID); err == nil && len(daemons) == 1 {
			report.DaemonID = daemons[0].ID
		}
	}
	if report.DaemonID != "" {
		if daemon, err := t.lookup.GetDaemon(tc.Context, report.DaemonID); err == nil && daemon != nil {
			report.DaemonType = stringOrEmpty(daemon.DaemonType)
			// A managed machine's hostname is its workspace pod's name.
			if report.DaemonType == daemonTypeManaged {
				report.Pod = stringOrEmpty(daemon.Hostname)
			}
		}
	}
}

// daemonTypeManaged is db.Daemon.DaemonType for a cloud workspace.
const daemonTypeManaged = "managed"

func stringOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// bugReportLimiter answers a repeat of a report a chat already filed with the
// first report, and caps how many distinct reports a chat files per window, so
// a looping agent cannot flood the issues.
//
// It is per process. With N worker replicas, a defect reported in a loop is
// filed at most N times per window, and the issue grouping folds those into
// the one issue regardless.
type bugReportLimiter struct {
	mu      sync.Mutex
	window  time.Duration
	perChat int
	filed   map[string][]filedBugReport // by chat id
}

type filedBugReport struct {
	key      string
	at       time.Time
	reportID string
}

// bugReportOutcome is what became of one report_bug call.
type bugReportOutcome struct {
	// ReportID is the report filed, or the earlier one a duplicate is
	// answered with; empty when nothing was filed.
	ReportID  string
	Duplicate bool
	Limited   bool
	// FiledAt is when the report the outcome describes was filed.
	FiledAt time.Time
}

func newBugReportLimiter(window time.Duration, perChat int) *bugReportLimiter {
	return &bugReportLimiter{window: window, perChat: perChat, filed: map[string][]filedBugReport{}}
}

// file runs send unless chatID already filed key within the window, or has
// reached its cap. The lock is held across send so two parallel calls in one
// batch cannot both file the same defect; send is a log write, so it does not
// wait on the network.
func (l *bugReportLimiter) file(chatID, key string, now time.Time, send func() string) bugReportOutcome {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.forgetBefore(now.Add(-l.window))
	filed := l.filed[chatID]
	for _, report := range filed {
		if report.key == key {
			return bugReportOutcome{ReportID: report.reportID, Duplicate: true, FiledAt: report.at}
		}
	}
	if len(filed) >= l.perChat {
		return bugReportOutcome{Limited: true}
	}

	reportID := send()
	l.filed[chatID] = append(filed, filedBugReport{key: key, at: now, reportID: reportID})
	return bugReportOutcome{ReportID: reportID, FiledAt: now}
}

func (l *bugReportLimiter) forgetBefore(cutoff time.Time) {
	for chatID, filed := range l.filed {
		kept := filed[:0]
		for _, report := range filed {
			if report.at.After(cutoff) {
				kept = append(kept, report)
			}
		}
		if len(kept) == 0 {
			delete(l.filed, chatID)
		} else {
			l.filed[chatID] = kept
		}
	}
}

// response is what the model is told. Nothing here is an error: a duplicate or
// a capped report is answered so the agent moves on rather than retrying.
func (o bugReportOutcome) response(l *bugReportLimiter) ToolResponse {
	const carryOn = "Continue with your task, and tell the user if this defect blocks them."
	var text string
	switch {
	case o.Limited:
		text = fmt.Sprintf("Not filed: this chat has already filed %d bug reports in the last %d minutes, which is the limit. Do not retry; tell the user about this defect instead, and continue with your task.",
			l.perChat, int(l.window.Minutes()))
	case o.Duplicate:
		text = fmt.Sprintf("Already reported from this chat at %s (report %s), so it was not filed again. %s",
			o.FiledAt.UTC().Format("15:04 UTC"), o.ReportID, carryOn)
	default:
		text = fmt.Sprintf("Filed with Reliant engineering as report %s. Nothing more is needed from you. %s", o.ReportID, carryOn)
	}
	return WithResponseMetadata(NewTextResponse(text), ReportBugMetadata{
		ReportID:    o.ReportID,
		Duplicate:   o.Duplicate,
		RateLimited: o.Limited,
	})
}
