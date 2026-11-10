// Copyright (c) 2025 Reliant Labs
package store

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// The retention statements spell enum values and the internal-activity list
// as literals, for the same reason the snapshot heads do: a generic plan
// proves a partial index applies only from what it can see. These pin each
// literal to what it stands for, so a renumbered enum or a moved activity
// fails here rather than deleting the wrong rows.

func intList(t *testing.T, sql, pattern string) []int32 {
	t.Helper()
	m := regexp.MustCompile(pattern).FindStringSubmatch(sql)
	if m == nil {
		t.Fatalf("%q not found in\n%s", pattern, sql)
	}
	var out []int32
	for _, part := range strings.Split(m[1], ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, int32(n))
	}
	slices.Sort(out)
	return out
}

func chatTypes(types ...reliantv1.ChatUpdateType) []int32 {
	out := make([]int32, len(types))
	for i, typ := range types {
		out[i] = int32(typ)
	}
	slices.Sort(out)
	return out
}

func TestChatCompactionClassesMatchTheReaders(t *testing.T) {
	kept := intList(t, CompactChatUpdatesSQL, `c\.update_type NOT IN \(([^)]*)\)`)
	if want := chatTypes(
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_THREAD, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_WORKFLOW_STATUS,
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_ERROR, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_QUESTION,
	); !slices.Equal(kept, want) {
		t.Errorf("kept-whole types %v, want THREAD, WORKFLOW_STATUS, ERROR, QUESTION %v", kept, want)
	}
	deleted := intList(t, CompactChatUpdatesSQL, `c\.update_type IN \(([^)]*)\)`)
	if want := chatTypes(
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_TOOL_CALL,
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_STREAM_FINALIZED, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_AGENT_MESSAGES_DRAINED,
	); !slices.Equal(deleted, want) {
		t.Errorf("deleted types %v, want MESSAGE, TOOL_CALL, STREAM_FINALIZED, AGENT_MESSAGES_DRAINED %v", deleted, want)
	}
	if !strings.Contains(CompactChatUpdatesSQL, "c.update_type = 9 AND") ||
		int32(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_NODE_EXECUTION) != 9 {
		t.Error("the node-heads arm must key NODE_EXECUTION (9)")
	}
	// The per-(type, entity) arm probes idx_chat_updates_snapshot_entity_heads,
	// so its list is the snapshot skip scan's, which that index's predicate is.
	heads := intList(t, CompactChatUpdatesSQL, `c\.update_type NOT IN \(([^)]*)\) AND EXISTS`)
	snapshot := intList(t, SnapshotHeadsSQL, `update_type NOT IN \(([^)]*)\)`)
	if !slices.Equal(heads, snapshot) {
		t.Errorf("per-entity arm excludes %v, the snapshot heads %v: they must probe the same index", heads, snapshot)
	}
}

func TestStepRetentionLiteralsMatchTheEnums(t *testing.T) {
	if int32(reliantv1.WorkflowState_WORKFLOW_STATE_STOPPED) != 3 ||
		int32(reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_COMPLETED) != 1 ||
		int32(reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_CANCELLED) != 4 ||
		int32(reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_PAUSED) != 3 {
		t.Fatal("stepRetentionRootsSQL / pruneRunStepsSQL spell STOPPED as 3, COMPLETED 1, CANCELLED 4, PAUSED 3")
	}
	if !strings.Contains(stepRetentionRootsSQL, "state = 3 AND stop_reason IN (1, 4)") ||
		!strings.Contains(pruneRunStepsSQL, "t.state = 3 AND t.stop_reason <> 3") {
		t.Fatal("the run-state predicates changed: re-check them against the enums above")
	}
	m := regexp.MustCompile(`(?s)se\.activity_name IN \(([^)]*)\)`).FindStringSubmatch(pruneRunStepsSQL)
	if m == nil {
		t.Fatal("pruneRunStepsSQL's prunable activity list not found")
	}
	var got []string
	for _, part := range strings.Split(m[1], ",") {
		got = append(got, strings.Trim(strings.TrimSpace(part), "'"))
	}
	want := slices.Clone(model.InternalActivities)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("prunable activities %v, want model.InternalActivities %v", got, want)
	}
}
