package streaming

import (
	"encoding/json"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/messageproto"
	"github.com/stretchr/testify/require"
)

func TestFormatChatUpdateDataJSON_MessageUpdateWrappedUnderMessageField(t *testing.T) {
	raw := json.RawMessage(`{
		"id":"msg-1",
		"role":"assistant",
		"thread":"thread-1",
		"context_window_id":"cw-1",
		"context_sequence":3,
		"streaming_state":"complete",
		"content_blocks":[],
		"attachments":[]
	}`)

	formatted := formatChatUpdateDataJSON(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE, raw)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(formatted), &decoded))

	message, ok := decoded["message"].(map[string]any)
	require.True(t, ok, "message updates must be wrapped in {\"message\": {...}} for frontend contract")
	require.Equal(t, "msg-1", message["id"])
	require.Equal(t, "assistant", message["role"])
	require.Equal(t, "thread-1", message["thread"])
	require.Equal(t, "cw-1", message["context_window_id"])
	require.Equal(t, float64(3), message["context_sequence"])
	require.Equal(t, "complete", message["streaming_state"])
	require.Contains(t, message, "content_blocks")
	require.Contains(t, message, "attachments")
}

func TestFormatChatUpdateDataJSON_MessageUpdateAlreadyWrappedUnchanged(t *testing.T) {
	raw := json.RawMessage(`{"message":{"id":"msg-1","role":"assistant"}}`)

	formatted := formatChatUpdateDataJSON(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE, raw)

	require.JSONEq(t, string(raw), formatted)
}

func TestFormatChatUpdateDataJSON_NonMessageUpdateUnchanged(t *testing.T) {
	raw := json.RawMessage(`{"id":"approval-1","status":"pending"}`)

	formatted := formatChatUpdateDataJSON(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_APPROVAL, raw)

	require.Equal(t, string(raw), formatted)
}

func TestFormatChatUpdateDataJSON_StreamingDeltaPayloadRemainsFlat(t *testing.T) {
	raw := json.RawMessage(`{"id":"msg-1","delta":"hel","stream_state":"streaming"}`)

	formatted := formatChatUpdateDataJSON(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_STREAMING_DELTA, raw)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(formatted), &decoded))
	require.Equal(t, "msg-1", decoded["id"])
	require.Equal(t, "hel", decoded["delta"])
	require.NotContains(t, decoded, "message", "streaming_delta payloads must stay flat for frontend delta reducer")
}

func TestFormatChatUpdateDataJSON_MessageUpdateDoesNotLeakTopLevelFields(t *testing.T) {
	raw := json.RawMessage(`{"id":"msg-1","role":"assistant"}`)

	formatted := formatChatUpdateDataJSON(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE, raw)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(formatted), &decoded))
	require.Contains(t, decoded, "message")
	require.NotContains(t, decoded, "id", "frontend expects message updates under payload.message only")
	require.NotContains(t, decoded, "role", "frontend expects message updates under payload.message only")
}

func TestFormatChatUpdateDataJSON_InvalidJSONFallsBackToRaw(t *testing.T) {
	raw := json.RawMessage(`{"id":"msg-1"`)

	formatted := formatChatUpdateDataJSON(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE, raw)

	require.Equal(t, string(raw), formatted)
}

func TestFormatChatUpdateDataJSON_LiveToolResultIsPreviewed(t *testing.T) {
	// A live message update carries the same tool-result preview the
	// snapshot and ListMessages send, not the whole output.
	big := strings.Repeat("output line\n", 4000)
	raw, err := json.Marshal(map[string]any{
		"id": "msg-1",
		"content_blocks": []any{map[string]any{
			"id": "b1", "type": int(reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT),
			"tool_call_id": "c1", "tool_name": "shell", "content": big,
		}},
	})
	require.NoError(t, err)

	formatted := formatChatUpdateDataJSON(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE, raw)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(formatted), &decoded))
	message := decoded["message"].(map[string]any)
	block := message["content_blocks"].([]any)[0].(map[string]any)
	require.Equal(t, true, block["truncated"])
	require.Equal(t, float64(len(big)), block["original_size"])
	require.LessOrEqual(t, len(block["content"].(string)), messageproto.ToolResultPreviewBytes)
}
