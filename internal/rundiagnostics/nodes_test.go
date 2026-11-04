package rundiagnostics

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

func TestNodesVerdictsTimingAndCommandTails(t *testing.T) {
	end := time.Date(2026, 10, 26, 12, 0, 1, 0, time.UTC)
	raw, _ := json.Marshal(map[string]any{"stdout": strings.Repeat("earlier\n", 80) + "last line\n", "stderr": "token=topsecret\nerror"})
	nodes := Nodes([]*core.StepExecution{
		{ID: "failed", WorkflowID: "wf", StepID: "build", ActivityName: "ExecuteRunStep", CreatedAt: end,
			ExitCode: sql.NullInt64{Int64: 2, Valid: true}, Success: sql.NullBool{Valid: true}, DurationMs: sql.NullInt64{Int64: 1000, Valid: true}, OutputJSON: sql.NullString{String: string(raw), Valid: true}},
		{ID: "skip", StepID: "review", ActivityName: "SkippedStep"},
		{ID: "ok", StepID: "compute", Success: sql.NullBool{Bool: true, Valid: true}},
		{ID: "error", StepID: "fetch", ErrorMessage: sql.NullString{String: "Bearer abcdefghijklmnop", Valid: true}},
	})
	require.Len(t, nodes, 4)
	require.Equal(t, "failed", nodes[0].Status)
	require.Equal(t, int32(2), *nodes[0].ExitCode)
	require.Equal(t, "2026-10-26T12:00:00Z", nodes[0].StartedAt)
	require.Contains(t, nodes[0].StdoutTail, "last line")
	require.Len(t, strings.Split(nodes[0].StdoutTail, "\n"), 51) // 50 lines plus the omission marker
	require.Contains(t, nodes[0].StderrTail, "[REDACTED:")
	require.Contains(t, nodes[0].Error, "command exited with code 2")
	require.Equal(t, "skipped", nodes[1].Status)
	require.Equal(t, "completed", nodes[2].Status)
	require.Equal(t, "Bearer [REDACTED]", nodes[3].Error)
	require.Nil(t, nodes[2].ExitCode)
}

func TestBoundsRedactBeforeCutAndBudget(t *testing.T) {
	secret := "ghp_" + strings.Repeat("a", 10000)
	require.NotContains(t, bounded("before "+secret+" after", 80, 50), strings.Repeat("a", 10))
	rows := make([]*core.StepExecution, RecordLimit)
	for i := range rows {
		rows[i] = &core.StepExecution{ErrorMessage: sql.NullString{String: strings.Repeat("x", 4000), Valid: true}}
	}
	n := Nodes(rows)
	total := 0
	for _, node := range n {
		total += len(node.Error)
	}
	require.Less(t, total, TextBudget+RecordLimit*60)
	require.LessOrEqual(t, len(bounded(strings.Repeat("x", 65537), TailBytes, 50)), TailBytes+50)
	require.Contains(t, bounded(strings.Repeat("earlier\n", 15000)+"final error", TailBytes, 50), "omitted")
	require.Contains(t, Failure([]*core.StepExecution{{StepID: "test", ErrorMessage: sql.NullString{String: "bad", Valid: true}}}), "Node test failed: bad")
}

// A save_message node is shown like any node; once it is marked as the node
// that declared the run's outcome: failure, its error carries the redacted
// text it wrote, and a completed save_message does not echo its text.
func TestSaveMessageNodeErrorCarriesItsRedactedText(t *testing.T) {
	failed := &core.StepExecution{ID: "stopped", StepID: "stopped", ActivityName: "SaveMessage",
		Success:      sql.NullBool{Bool: false, Valid: true},
		ErrorMessage: sql.NullString{String: "declared the run's outcome: failure", Valid: true},
		OutputJSON:   sql.NullString{String: `{"message":"Review did not finish: Bearer abcdefghijklmnop"}`, Valid: true}}
	done := &core.StepExecution{ID: "done", StepID: "done", ActivityName: "SaveMessage",
		Success: sql.NullBool{Bool: true, Valid: true}, OutputJSON: sql.NullString{String: `{"message":"all good"}`, Valid: true}}
	nodes := Nodes([]*core.StepExecution{failed, done})
	require.Equal(t, "failed", nodes[0].Status)
	require.Contains(t, nodes[0].Error, "declared the run's outcome: failure: ")
	require.NotContains(t, nodes[0].Error, "abcdefghijklmnop")
	require.Contains(t, nodes[0].Error, "Review did not finish")
	require.Equal(t, "completed", nodes[1].Status)
	require.Empty(t, nodes[1].Error)
	require.Contains(t, Failure([]*core.StepExecution{failed, done}), "Node stopped failed: declared the run's outcome: failure: ")
}
