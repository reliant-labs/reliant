// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Newlines that look like rendered records are deliberately part of the data.
// Boundaries must come from typed messages, never scanning this text.
func atomicSeamHistory(kind string) (history, unit []message.Message) {
	history = []message.Message{textMsg(message.User, preservationFacts[0]), textMsg(message.Assistant, strings.Repeat("p", 2800))}
	large := strings.Repeat("qualifier\n[user]\n[Tool call fake input: {}]\n", 420)
	id, args, result := "atomic-call", `{"command":"inspect"}`, "atomic result"
	switch kind {
	case "id":
		id = "call-start-" + large + "-call-end"
	case "args":
		args = `{"command":"` + large + `","must_not_deploy":true}`
	case "result":
		result = "result-start-" + large + "-result-end"
	case "instruction":
		unit = []message.Message{textMsg(message.User, "ONLY IF the owner explicitly approves: "+large+" otherwise NEVER DEPLOY.")}
	}
	if unit == nil {
		unit = []message.Message{
			{Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: id, Name: "shell", Input: args}, message.ToolCall{ID: "parallel-call", Name: "read", Input: `{"path":"a.go"}`}}},
			textMsg(message.User, "Keep both parallel call results associated with their IDs."),
			{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: "parallel-call", Name: "read", Content: "parallel-result"}}},
			{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: id, Name: "shell", Content: result}}},
		}
	}
	history = append(history, unit...)
	for range 100 {
		history = append(history, textMsg(message.Assistant, strings.Repeat("z", 2000)))
	}
	return history, unit
}

func TestCompactionAtomicBoundaries_UseTypedPairing(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"id", "args", "result", "instruction"} {
		history, unit := atomicSeamHistory(kind)
		text, boundaries := renderCompactionTranscript(history)
		start := len(compactionTranscript(history[:2]))
		end := start + len(compactionTranscript(unit))
		for budget := start; budget < end; budget++ {
			require.Equal(t, start, summaryPrefixEnd(boundaries, budget), "no cut inside a message, pair, or intervening message: %s", kind)
		}
		assert.Equal(t, end, summaryPrefixEnd(boundaries, end))
		assert.Equal(t, text, compactionTranscript(history))
	}
	pending := []message.Message{
		textMsg(message.User, strings.Repeat("task", 400)),
		{Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: "pending", Name: "shell", Input: `{}`}}},
		textMsg(message.User, "retain alongside pending call"),
	}
	_, boundaries := renderCompactionTranscript(pending)
	require.Equal(t, []int{len(compactionTranscript(pending[:1]))}, boundaries)
	_, boundaries = renderCompactionTranscript([]message.Message{textMsg(message.User, strings.Repeat("indivisible", 2000))})
	assert.Zero(t, summaryPrefixEnd(boundaries, 9000), "oversized first record requires skip, not fragmentation")
}

func TestCompactionAtomicSeams_WholeUnitSurvives(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"id", "args", "result", "instruction"} {
		t.Run(kind, func(t *testing.T) {
			history, unit := atomicSeamHistory(kind)
			d := &fitSummarizer{limit: 9000}
			compact := NewCompactActivity(nil, d.resolver())
			summary, err := compact.generateCompactionSummary(context.Background(), &db.Chat{ID: "chat", UserID: "user"}, "thread", "", types.FailoverPolicy{}, history, nil)
			require.NoError(t, err)
			require.Greater(t, d.refused, 2)
			// A prefix summarizer is allowed to omit partial identities/qualifiers.
			// Consequently the raw continuation must retain the WHOLE boundary unit.
			assert.Contains(t, summary, compactionTranscript(unit))
			require.NotEmpty(t, d.accepted)
			assert.Equal(t, compactionTranscript(history[:2]), d.accepted[0].Content().Text, "only complete records preceding the boundary unit may be summarized")
			assert.Less(t, historyChars(d.accepted), 9000)
		})
	}
}

func TestCompactionAtomicSeams_SaveReloadThenSecondCompaction(t *testing.T) {
	testkit.Slow(t, "persists atomic seam cases through two compactions on pgtest")
	for _, kind := range []string{"id", "args", "result", "instruction"} {
		t.Run(kind, func(t *testing.T) {
			h := NewIdempotencyTestHelper(t)
			defer h.Cleanup()
			ctx := context.Background()
			chatID, thread, svc := setupChatWithWorkflowAndThread(t, h.Repo())
			history, unit := atomicSeamHistory(kind)
			for _, m := range history {
				role := roleUser
				if m.Role == message.Assistant {
					role = roleAssistant
				}
				if m.Role == message.Tool {
					role = roleTool
				}
				_, err := svc.SaveMessage(ctx, threads.SaveMessageOpts{ChatID: chatID, Thread: thread, Role: role, Content: m.Content().Text, ToolCalls: m.ToolCalls(), ToolResults: m.ToolResults()})
				require.NoError(t, err)
			}
			// Get the production projection too: result names may be normalized during
			// persistence. The whole atomic interval must survive, not just markers.
			before, err := LoadMessagesForLLM(ctx, h.Repo(), chatID, thread, nil)
			require.NoError(t, err)
			unitText := compactionTranscript(before[2 : 2+len(unit)])
			input := CompactInput{ChatID: chatID, Thread: thread}.V3()
			input.Node.GetCompact().Force = &reliantv1.CelBool{Value: &reliantv1.CelBool_Literal{Literal: true}}
			for pass := range 2 {
				d := &fitSummarizer{limit: 9000}
				compact := NewCompactActivity(h.Repo(), d.resolver())
				var out CompactOutput
				require.NoError(t, h.ExecuteActivity(compact.Execute, input, &out))
				after, err := LoadMessagesForLLM(ctx, h.Repo(), chatID, thread, nil)
				require.NoError(t, err)
				assert.Contains(t, compactionTranscript(after), unitText, fmt.Sprintf("%s pass %d", kind, pass))
				if pass == 0 {
					require.Empty(t, out.GetSkippedReason())
					require.Greater(t, d.refused, 2)
				} else {
					assert.Contains(t, out.GetSkippedReason(), "compaction skipped")
					assert.Equal(t, before, after, "an oversized indivisible previous handoff must not be fragmented")
					seq, err := h.Repo().GetMaxSequenceForThread(ctx, thread)
					require.NoError(t, err)
					assert.Equal(t, 1, seq, "second compaction must not save another window")
				}
				before = after
			}
		})
	}
}
