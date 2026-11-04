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
			NodeId: row.StepID, NodePath: row.NodePath.String, Activity: row.ActivityName,
			Status: "completed", Attempt: row.Attempt.Int32,
			CompletedAt: row.CreatedAt.Format(time.RFC3339Nano)}
		if row.DurationMs.Valid {
			n.DurationMs = &row.DurationMs.Int64
			n.StartedAt = row.CreatedAt.Add(-time.Duration(row.DurationMs.Int64) * time.Millisecond).Format(time.RFC3339Nano)
		}
		if row.LoopNodeID.Valid {
			n.LoopNodeId = row.LoopNodeID.String
		}
		if row.LoopIteration.Valid {
			iteration := int32(row.LoopIteration.Int64)
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
		if len(row.OutputJSON.String) > ScanLimit || len(row.OutputJSON.String) > scanBudget {
			output.Omitted = true
		} else {
			scanBudget -= len(row.OutputJSON.String)
			_ = json.Unmarshal([]byte(row.OutputJSON.String), &output)
		}
		if row.ExitCode.Valid {
			code := int32(row.ExitCode.Int64)
			n.ExitCode = &code
		}
		switch {
		case row.ActivityName == "SkippedStep" || output.Skipped:
			n.Status = "skipped"
		case row.ErrorMessage.String != "" || row.Success.Valid && !row.Success.Bool || row.ExitCode.Valid && row.ExitCode.Int64 != 0:
			n.Status = "failed"
		}
		if strings.HasSuffix(row.ActivityName, "ExecuteRunStep") || n.ExitCode != nil {
			n.StdoutTail = bounded(output.Stdout, TailBytes, TailLines)
			n.StderrTail = bounded(output.Stderr, TailBytes, TailLines)
			if output.Omitted {
				n.StdoutTail = "[output omitted: safe diagnostic limit]"
			}
		}
		errorText := row.ErrorMessage.String
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
