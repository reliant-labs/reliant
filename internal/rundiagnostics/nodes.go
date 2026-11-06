// Copyright (c) 2025 Reliant Labs
// Package rundiagnostics projects persisted node attempts for every run surface.
// It never exposes resolved inputs or arbitrary activity output.
//
//forge:exclude-contract: pure projection helpers, no service or outbound I/O
package rundiagnostics

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/reliant-labs/reliant/internal/ptr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

const (
	RecordLimit = 200
	// The entire response's diagnostic text budget, not a per-node multiplier.
	TextBudget = 128 << 10
	TailBytes  = 8 << 10
	TailLines  = 50
	// Matches the SQL read cap. Never parse a partial JSON/secret value.
	ScanLimit = 16 << 10
)

// Nodes preserves attempt and loop identities instead of collapsing a failed
// iteration behind a later successful one. Rows arrive newest first.
func Nodes(rows []*core.StepExecution) []*pb.RunNodeExecution {
	if len(rows) > RecordLimit {
		rows = rows[:RecordLimit]
	}
	out := make([]*pb.RunNodeExecution, 0, len(rows))
	budget := TextBudget
	scanBudget := TextBudget
	for _, row := range rows {
		n := &pb.RunNodeExecution{Id: row.ID, WorkflowId: row.WorkflowID,
			NodeId: row.StepID, NodePath: ptr.From(row.NodePath), Activity: row.ActivityName,
			Status: "completed", Attempt: ptr.From(row.Attempt),
			CompletedAt: row.CreatedAt.Format(time.RFC3339Nano)}
		if row.DurationMs != nil {
			n.DurationMs = row.DurationMs
			n.StartedAt = row.CreatedAt.Add(-time.Duration(*row.DurationMs) * time.Millisecond).Format(time.RFC3339Nano)
		}
		if row.LoopNodeID != nil {
			n.LoopNodeId = *row.LoopNodeID
		}
		if row.LoopIteration != nil {
			iteration := int32(*row.LoopIteration)
			n.LoopIteration = &iteration
		}
		var output struct {
			Skipped bool   `json:"skipped"`
			Stdout  string `json:"stdout"`
			Stderr  string `json:"stderr"`
			// Message is the text a save_message node wrote. Shown only on a
			// failed node: a failure-outcome node's text is why the run failed.
			Message string `json:"message"`
			Omitted bool   `json:"_diagnostic_omitted"`
		}
		outputJSON := ptr.From(row.OutputJSON)
		if len(outputJSON) > ScanLimit || len(outputJSON) > scanBudget {
			output.Omitted = true
		} else {
			scanBudget -= len(outputJSON)
			_ = json.Unmarshal([]byte(outputJSON), &output)
		}
		if row.ExitCode != nil {
			code := int32(*row.ExitCode)
			n.ExitCode = &code
		}
		switch {
		case row.ActivityName == "SkippedStep" || output.Skipped:
			n.Status = "skipped"
		case ptr.From(row.ErrorMessage) != "" || row.Success != nil && !*row.Success || row.ExitCode != nil && *row.ExitCode != 0:
			n.Status = "failed"
		}
		if strings.HasSuffix(row.ActivityName, "ExecuteRunStep") || n.ExitCode != nil {
			n.StdoutTail = bounded(output.Stdout, TailBytes, TailLines)
			n.StderrTail = bounded(output.Stderr, TailBytes, TailLines)
			if output.Omitted {
				n.StdoutTail = "[output omitted: safe diagnostic limit]"
			}
		}
		errorText := ptr.From(row.ErrorMessage)
		if len(errorText) > ScanLimit || len(errorText) > scanBudget {
			errorText = "[error omitted: safe diagnostic limit]"
		} else {
			scanBudget -= len(errorText)
		}
		if n.Status == "failed" && row.ActivityName == "SaveMessage" && strings.TrimSpace(output.Message) != "" &&
			!strings.HasPrefix(errorText, "[error omitted") {
			if errorText != "" {
				errorText += ": "
			}
			errorText += output.Message
		}
		if errorText == "" && n.Status == "failed" {
			if n.ExitCode != nil {
				errorText = fmt.Sprintf("command exited with code %d", *n.ExitCode)
			} else {
				errorText = "node reported failure"
			}
			if n.StderrTail != "" {
				errorText += ": " + n.StderrTail
			}
		}
		n.Error = bounded(errorText, 4096, TailLines)
		for _, text := range []*string{&n.Error, &n.StderrTail, &n.StdoutTail} {
			if len(*text) > budget {
				*text = "[diagnostic text omitted: response limit]"
				continue
			}
			budget -= len(*text)
		}
		out = append(out, n)
	}
	return out
}

// bounded redacts the complete stream BEFORE either presentation cut, so a
// cut can never hide the credential prefix a masking pattern depends on.
func bounded(s string, maxBytes, lines int) string {
	if len(s) > ScanLimit {
		return "[text omitted: safe diagnostic limit]"
	}
	s = logging.RedactDiagnosticText(s)
	s = strings.TrimRight(s, "\n")
	parts := strings.Split(s, "\n")
	truncated := len(parts) > lines
	if truncated {
		s = strings.Join(parts[len(parts)-lines:], "\n")
	}
	if len(s) > maxBytes {
		cut := len(s) - maxBytes
		for cut < len(s) && !utf8.RuneStart(s[cut]) {
			cut++
		}
		s = s[cut:]
		truncated = true
	}
	if truncated {
		s = "[earlier output omitted]\n" + s
	}
	return strings.ToValidUTF8(s, "")
}

// Failure describes the newest failing attempt, using precisely the projection
// a user sees. It is safe to persist as a fallback assistant message.
func Failure(rows []*core.StepExecution) string {
	for _, node := range Nodes(rows) {
		if node.Status == "failed" {
			id := node.NodePath
			if id == "" {
				id = node.NodeId
			}
			return fmt.Sprintf("Node %s failed: %s", logging.RedactText(id), node.Error)
		}
	}
	return ""
}

// ErrorText is a bounded, log-redacted error for failures before any node ran.
func ErrorText(text string) string { return bounded(text, 4096, TailLines) }
