package handlers

import (
	"context"
	"fmt"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

const (
	toolResultBatchLimitPercent      = 7
	estimatedToolResultBytesPerToken = 4
)

func resolvedExecuteToolsCompactionThreshold(args *reliantv1.ExecuteToolsArgs) int32 {
	if args == nil || !model.CelIntIsSet(args.GetCompactionThreshold()) || model.CelIntIsExpr(args.GetCompactionThreshold()) {
		return int32(DefaultCompactionThreshold)
	}
	threshold := model.CelIntValue(args.GetCompactionThreshold())
	if threshold <= 0 {
		return int32(DefaultCompactionThreshold)
	}
	return int32(threshold)
}

func toolResultBatchLimitBytes(compactionThreshold int32) int {
	if compactionThreshold <= 0 {
		compactionThreshold = int32(DefaultCompactionThreshold)
	}
	return int(compactionThreshold) * toolResultBatchLimitPercent / 100 * estimatedToolResultBytesPerToken
}

func totalToolResultContentBytes(results []message.ToolResult) int {
	total := 0
	for _, result := range results {
		total += len(result.Content)
	}
	return total
}

func (a *ExecuteToolsActivity) capToolResultBatch(ctx context.Context, results []message.ToolResult, compactionThreshold int32) ([]message.ToolResult, int, bool) {
	originalTotal := totalToolResultContentBytes(results)
	limit := toolResultBatchLimitBytes(compactionThreshold)
	if originalTotal <= limit || len(results) == 0 {
		return results, originalTotal, false
	}

	capped := make([]message.ToolResult, len(results))
	copy(capped, results)

	remaining := limit
	usedDetailedNotice := false
	changed := false
	for i := range capped {
		reserveForLater := 0
		for j := i + 1; j < len(capped); j++ {
			reserveForLater += len(batchShortTruncationNotice(capped[j], limit))
		}

		available := remaining - reserveForLater
		if available < 0 {
			available = 0
		}

		if len(capped[i].Content) <= available {
			remaining -= len(capped[i].Content)
			continue
		}

		var notice string
		if !usedDetailedNotice {
			notice = batchDetailedTruncationNotice(capped[i], originalTotal, limit, compactionThreshold)
			usedDetailedNotice = true
		} else {
			notice = batchShortTruncationNotice(capped[i], limit)
		}
		capped[i].Content = fitToolResultContentToBudget(capped[i].Content, available, notice)
		remaining -= len(capped[i].Content)
		changed = true
	}

	if !changed {
		return results, originalTotal, false
	}

	for i := range capped {
		if capped[i].Content != results[i].Content {
			a.persistCappedToolResult(ctx, capped[i])
		}
	}

	return capped, totalToolResultContentBytes(capped), true
}

func (a *ExecuteToolsActivity) persistCappedToolResult(ctx context.Context, result message.ToolResult) {
	if result.ToolCallID == "" {
		return
	}

	readCtx, cancel := detachedForTerminalWrite(ctx)
	defer cancel()
	call, err := a.repo.GetToolCall(readCtx, result.ToolCallID)
	if err != nil || call == nil || !call.Status.IsTerminal() {
		return
	}
	a.upsertToolCallResult(ctx, result.ToolCallID, result.Content, result.IsError)
}

func batchDetailedTruncationNotice(result message.ToolResult, originalTotal, limit int, compactionThreshold int32) string {
	return fmt.Sprintf(`

=== TOOL RESULT BATCH TRUNCATED ===
The tool-result batch was %d bytes, above the %d-byte batch budget (%d%% of compaction threshold %d tokens, estimated at %d bytes/token).
This %s result was cut from %d bytes so one tool batch cannot consume the next LLM turn. Re-run or paginate the tool with offset/limit, tail, regex, or narrower filters to read omitted content.
=== END TOOL RESULT BATCH TRUNCATION NOTICE ===
`, originalTotal, limit, toolResultBatchLimitPercent, compactionThreshold, estimatedToolResultBytesPerToken, toolResultLabel(result), len(result.Content))
}

func batchShortTruncationNotice(result message.ToolResult, limit int) string {
	return fmt.Sprintf("\n\n[TOOL RESULT BATCH TRUNCATED: the %s result was omitted to keep this batch under %d bytes. Re-run or paginate this tool with offset/limit or narrower filters.]\n", toolResultLabel(result), limit)
}

func toolResultLabel(result message.ToolResult) string {
	if result.Name != "" && result.ToolCallID != "" {
		return fmt.Sprintf("%s (%s)", result.Name, result.ToolCallID)
	}
	if result.Name != "" {
		return result.Name
	}
	if result.ToolCallID != "" {
		return result.ToolCallID
	}
	return "tool"
}

func fitToolResultContentToBudget(content string, budget int, notice string) string {
	if budget <= 0 {
		return ""
	}
	if len(notice) >= budget {
		return notice[:budget]
	}
	keep := budget - len(notice)
	if keep <= 0 {
		return notice
	}
	head := content[:keep]
	if idx := strings.LastIndexByte(head, '\n'); idx > keep/2 {
		head = head[:idx+1]
	}
	return head + notice
}
