package handlers

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecuteToolsActivity_CapsAggregateToolResultBatch(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()

	firstID := "toolu_" + uuid.New().String()
	secondID := "toolu_" + uuid.New().String()

	firstContent := "first view result\n" + strings.Repeat("a", 40_000)
	secondContent := "second view result\n" + strings.Repeat("b", 40_000)

	executor := newMockToolExecutor()
	executor.SetResult(firstID, &toolexec.ToolResult{Success: true, Content: firstContent})
	executor.SetResult(secondID, &toolexec.ToolResult{Success: true, Content: secondContent})

	compactionThreshold := int64(10_000)
	activityInstance := NewExecuteToolsActivity(f.h.Repo(), executor)
	var output ExecuteToolsOutput
	require.NoError(t, f.h.ExecuteActivity(activityInstance.Execute, ExecuteToolsInput{
		ChatID:              f.chatID,
		Thread:              f.chatID,
		CompactionThreshold: compactionThreshold,
		ToolCalls: []message.ToolCall{
			{ID: firstID, Name: "view", Input: `{"file_path":"first.go"}`},
			{ID: secondID, Name: "view", Input: `{"file_path":"second.go"}`},
		},
	}, &output))

	require.Len(t, output.ToolResults, 2)
	batchLimit := toolResultBatchLimitBytes(int32(compactionThreshold))
	totalBytes := 0
	combined := ""
	for _, result := range output.ToolResults {
		totalBytes += len(result.GetContent())
		combined += result.GetContent()
	}

	assert.LessOrEqual(t, totalBytes, batchLimit)
	assert.Contains(t, combined, "TOOL RESULT BATCH TRUNCATED")
	assert.Contains(t, combined, "offset")
	assert.Contains(t, combined, "limit")

	for _, result := range output.ToolResults {
		durable := getToolCallResult(t, f.h, result.GetToolCallId())
		require.NotNil(t, durable)
		assert.Equal(t, result.GetContent(), durable.Content,
			"durable result content must match the truncated content returned to the LLM")
	}
}
