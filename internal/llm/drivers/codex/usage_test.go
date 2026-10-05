// Copyright (c) 2025 Reliant Labs
package codex

import "testing"

// Captured shape of a Codex response.completed event's usage block. Before the
// fix only TotalTokens was copied, so every Codex turn billed 0 output tokens.
const completedWithUsage = `{"type":"response.completed","sequence_number":2,"response":{"id":"resp_1","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hi","annotations":[]}]}],"usage":{"input_tokens":1200,"input_tokens_details":{"cached_tokens":1024},"output_tokens":87,"output_tokens_details":{"reasoning_tokens":64},"total_tokens":1287}}}`

func TestStreamResponse_PopulatesTokenUsageBreakdown(t *testing.T) {
	srv := sseResponsesServer(t,
		sseEvent("response.created", `{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress"}}`),
		sseEvent("response.completed", completedWithUsage),
	)
	u := streamOnce(t, srv).Usage
	if u.TokenCount != 1287 || u.InputTokens != 1200 || u.OutputTokens != 87 || u.CachedInputTokens != 1024 {
		t.Errorf("usage = total %d in %d out %d cached %d, want 1287/1200/87/1024",
			u.TokenCount, u.InputTokens, u.OutputTokens, u.CachedInputTokens)
	}
}
