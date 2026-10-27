// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This driver measures each submitted request, rather than succeeding after a
// scripted refusal count. Dense unicode content has a deliberately different
// token density from ASCII; counts, when provided, are not byte counts.
type fitSummarizer struct {
	mockLLMDriverForIdempotency
	limit      int
	withCounts bool
	got        [][]message.Message
	refused    int
	accepted   []message.Message
}

func (d *fitSummarizer) StreamResponse(_ context.Context, prompts []string, history []message.Message, _ []tools.Tool) <-chan llm.DriverEvent {
	d.got = append(d.got, append([]message.Message(nil), history...))
	cost := func(s string) int {
		n := 0
		for _, r := range s {
			if r > 127 {
				n += 5
			} else {
				n++
			}
		}
		return n
	}
	size := 0
	for _, p := range prompts {
		size += cost(p)
	}
	for _, m := range history {
		size += cost(m.Content().Text)
	}
	ch := make(chan llm.DriverEvent, 2)
	defer close(ch)
	if size > d.limit {
		d.refused++
		err := errors.New("Your input exceeds the context window of this model")
		if d.withCounts {
			err = fmt.Errorf("prompt is too long: %d tokens > %d maximum", size, d.limit)
		}
		ch <- llm.DriverEvent{Type: llm.EventError, Error: err}
		return ch
	}
	d.accepted = append([]message.Message(nil), history...)
	// A deterministic semantic summary: keep only facts actually seen. This
	// cannot invent the user instruction or result that lossy retries omit.
	summary := "Processed prefix."
	for _, m := range history {
		for _, fact := range preservationFacts {
			if strings.Contains(m.Content().Text, fact) {
				summary += "\n" + fact
			}
		}
	}
	ch <- llm.DriverEvent{Type: llm.EventContentDelta, Content: summary}
	ch <- llm.DriverEvent{Type: llm.EventComplete, Response: &llm.DriverResponse{Content: summary}}
	return ch
}

func (d *fitSummarizer) resolver() drivers.DriverResolver {
	return func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
		return d, nil
	}
}

var preservationFacts = []string{
	"OPENING_TASK_KEEP", "MIDDLE_USER_REQUIREMENT_DO_NOT_DEPLOY", "MIDDLE_RESULT_TESTS_PASS_827",
	"LATEST_USER_ONLY_COMMIT_LOCALLY", "LATEST_RESULT_PATCH_abc123", "PENDING_COMMAND_REVIEW_PATCH",
}

func preservationHistory() []message.Message {
	history := []message.Message{textMsg(message.User, preservationFacts[0])}
	for i := range 80 {
		history = append(history, textMsg(message.Assistant, strings.Repeat("x", 2000)))
		if i == 30 {
			history = append(history, textMsg(message.User, preservationFacts[1]),
				message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: "middle-call", Name: "shell", Input: `{}`}}},
				message.Message{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: "middle-call", Name: "shell", Content: strings.Repeat("r", 15000) + preservationFacts[2] + strings.Repeat("s", 15000)}}})
		}
	}
	// The second half has a different token/byte ratio. Never truncate a rune.
	history = append(history, textMsg(message.Assistant, strings.Repeat("界", 8000)),
		textMsg(message.User, preservationFacts[3]),
		message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: "latest-call", Name: "shell", Input: `{}`}}},
		message.Message{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: "latest-call", Name: "shell", Content: preservationFacts[4]}}},
		message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: "pending-call", Name: "shell", Input: preservationFacts[5]}}})
	return history
}

func TestCompactionOverflow_PreservesUnprocessedContinuation(t *testing.T) {
	t.Parallel()
	for _, counts := range []bool{false, true} {
		t.Run(fmt.Sprintf("counts=%t", counts), func(t *testing.T) {
			driver := &fitSummarizer{limit: 6000, withCounts: counts}
			activity := NewCompactActivity(nil, driver.resolver())
			history := preservationHistory()
			summary, err := activity.generateCompactionSummary(context.Background(), &db.Chat{ID: "chat", UserID: "user"}, "thread", "", types.FailoverPolicy{}, history, nil)
			require.NoError(t, err)
			if !counts {
				assert.Greater(t, driver.refused, 2, "recover after more than two real count-free overflows")
			}
			for _, fact := range preservationFacts {
				assert.Contains(t, summary, fact, "must survive even when outside the accepted prefix")
			}
			assert.Contains(t, summary, "pending-call", "pending tool identity must survive in the continuation")
			require.NotEmpty(t, driver.accepted)
			acceptedText := ""
			for _, m := range driver.accepted {
				acceptedText += m.Content().Text
			}
			assert.NotContains(t, acceptedText, preservationFacts[1], "fixture really puts the middle outside the processed prefix")
			// Input ownership: neither the normal preparation nor retries may trim the
			// original tool results, mutate parts, or append into the caller's slice.
			assert.Equal(t, preservationHistory(), history)
			for _, request := range driver.got {
				for _, m := range request {
					assert.True(t, strings.ToValidUTF8(m.Content().Text, "") == m.Content().Text)
				}
			}
		})
	}
}

// Exercise the production save/load boundary, not just the summary string.
// Each subtest gets its own pgtest database; never the user's dev database.
func TestCompactionOverflow_SaveReloadAndSkip(t *testing.T) {
	testkit.Slow(t, "persists compaction and reloads continuation through pgtest")
	for _, limit := range []int{6000, 1} {
		t.Run(fmt.Sprintf("limit=%d", limit), func(t *testing.T) {
			h := NewIdempotencyTestHelper(t)
			defer h.Cleanup()
			ctx := context.Background()
			chatID, thread, svc := setupChatWithWorkflowAndThread(t, h.Repo())
			history := preservationHistory()
			for _, m := range history {
				role := roleUser
				if m.Role == message.Assistant {
					role = roleAssistant
				}
				if m.Role == message.Tool {
					role = roleTool
				}
				_, err := svc.SaveMessage(ctx, threads.SaveMessageOpts{
					ChatID: chatID, Thread: thread, Role: role,
					Content: m.Content().Text, ToolCalls: m.ToolCalls(), ToolResults: m.ToolResults(),
				})
				require.NoError(t, err)
			}
			before, err := LoadMessagesForLLM(ctx, h.Repo(), chatID, thread, nil)
			require.NoError(t, err)
			input := CompactInput{ChatID: chatID, Thread: thread}.V3()
			input.Node.GetCompact().Force = &reliantv1.CelBool{Value: &reliantv1.CelBool_Literal{Literal: true}}
			driver := &fitSummarizer{limit: limit}
			compact := NewCompactActivity(h.Repo(), driver.resolver())
			var out CompactOutput
			require.NoError(t, h.ExecuteActivity(compact.Execute, input, &out))
			after, err := LoadMessagesForLLM(ctx, h.Repo(), chatID, thread, nil)
			require.NoError(t, err)
			seq, err := h.Repo().GetMaxSequenceForThread(ctx, thread)
			require.NoError(t, err)
			if limit == 1 {
				assert.Contains(t, out.GetSkippedReason(), "compaction skipped")
				assert.Nil(t, out.GetMessage())
				assert.Zero(t, seq, "no destructive context boundary on overflow exhaustion")
				assert.Equal(t, before, after, "the full original continuation remains loadable")
				return
			}
			assert.Empty(t, out.GetSkippedReason())
			assert.Equal(t, 1, seq)
			var reloaded strings.Builder
			for _, m := range after {
				reloaded.WriteString(m.Content().Text)
			}
			for _, fact := range preservationFacts {
				assert.Contains(t, reloaded.String(), fact)
			}
			assert.Contains(t, reloaded.String(), "pending-call")
			assert.Greater(t, driver.refused, 2)
		})
	}
}

func TestCompactionOverflow_HardMinimumIsNonDestructive(t *testing.T) {
	t.Parallel()
	driver := &fitSummarizer{limit: 1}
	compact := NewCompactActivity(nil, driver.resolver())
	history := preservationHistory()
	summary, err := compact.generateCompactionSummary(context.Background(), &db.Chat{ID: "chat", UserID: "user"}, "thread", "", types.FailoverPolicy{}, history, nil)
	require.Error(t, err)
	assert.Empty(t, summary, "no partial handoff may be committed")
	assert.Equal(t, preservationHistory(), history)
	assert.Contains(t, err.Error(), "compaction skipped", "use a recoverable skip, not a fatal provider failure")
	assert.Less(t, len(driver.got), 20, "monotone retry must terminate at a useful minimum")
	assert.Greater(t, len(driver.got), 3)
	for i := 1; i < len(driver.got); i++ {
		assert.Less(t, historyChars(driver.got[i]), historyChars(driver.got[i-1]), "every refusal must shrink the complete request")
	}
}
