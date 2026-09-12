// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// oversizeParams is the (empty) parameter struct for the fixture tools below.
type oversizeParams struct {
	Note string `json:"note,omitempty" jsonschema:"description=ignored"`
}

// oversizeMetadata stands in for a real tool's typed output struct — the thing
// WithResponseMetadata marshals into ToolResponse.Metadata.
type oversizeMetadata struct {
	AttachmentID string `json:"attachment_id"`
	Width        int    `json:"width"`
}

// oversizeTool returns a response whose Content exceeds the output ceiling and
// whose Metadata is a JSON document. This is the combination that the
// truncation path used to corrupt.
type oversizeTool struct {
	name     string
	metadata any
	rawMeta  string
}

func (t *oversizeTool) Name() string        { return t.name }
func (t *oversizeTool) Description() string { return "oversize test tool" }

func (t *oversizeTool) RequiresPermission(oversizeParams) (bool, error) { return false, nil }

func (t *oversizeTool) Execute(_ *rctx.ToolContext, _ oversizeParams) (ToolResponse, error) {
	resp := NewTextResponse(strings.Repeat("x", MaxOutputSize+5000))
	if t.rawMeta != "" {
		resp.Metadata = t.rawMeta
		return resp, nil
	}
	if t.metadata != nil {
		return WithResponseMetadata(resp, t.metadata), nil
	}
	return resp, nil
}

func runOversizeTool(t *testing.T, inner *oversizeTool) ToolResponse {
	t.Helper()
	wrapper := NewToolWrapper[oversizeParams, ToolResponse](inner)
	resp, err := wrapper.Run(createTestContext(t, "truncation-chat"), ToolCall{ID: "call-1", Input: `{}`})
	require.NoError(t, err)
	return resp
}

// TestTruncation_MetadataStaysParseableJSON is the core claim: a tool whose
// output is oversize AND carries typed metadata must still produce a Metadata
// string that json.Unmarshal accepts, because execute_tools.go parses it into
// response_data and silently drops it on a parse error.
func TestTruncation_MetadataStaysParseableJSON(t *testing.T) {
	inner := &oversizeTool{
		name:     "oversize_typed_tool",
		metadata: oversizeMetadata{AttachmentID: "att_123", Width: 512},
	}
	resp := runOversizeTool(t, inner)

	require.LessOrEqual(t, len(resp.Content), MaxOutputSize, "content should have been truncated")
	require.NotEmpty(t, resp.Metadata, "typed metadata must survive truncation")

	// This is the round trip execute_tools.go performs to build response_data.
	var data map[string]any
	err := json.Unmarshal([]byte(resp.Metadata), &data)
	require.NoError(t, err, "Metadata must remain valid JSON after truncation; got: %s", resp.Metadata)

	assert.Equal(t, "att_123", data["attachment_id"], "the tool's own fields must be preserved")
	assert.Equal(t, float64(512), data["width"])

	// Truncation info is carried structurally, under a reserved key.
	truncation, ok := data[truncationMetadataKey].(map[string]any)
	require.True(t, ok, "expected structured truncation info under %q, got: %s", truncationMetadataKey, resp.Metadata)
	assert.Equal(t, float64(MaxOutputSize+5000), truncation["original_bytes"])
	assert.Equal(t, float64(len(resp.Content)), truncation["delivered_bytes"])
}

// TestTruncation_EmptyMetadataStaysEmpty pins the deliberate choice not to
// start populating Metadata where nothing did before. execute_tools.go keys
// response_data off `r.Metadata != ""`, so inventing a document here would
// create response_data entries for tools that have no structured output.
func TestTruncation_EmptyMetadataStaysEmpty(t *testing.T) {
	inner := &oversizeTool{name: "oversize_plain_tool"}
	resp := runOversizeTool(t, inner)

	require.LessOrEqual(t, len(resp.Content), MaxOutputSize, "content should have been truncated")
	assert.Empty(t, resp.Metadata, "a tool with no typed metadata must not acquire one from truncation")
}

// TestTruncation_NonObjectMetadataIsLeftAlone covers metadata that is valid
// JSON but not an object. There is nowhere to put the truncation fields
// without changing the document's shape, so the document wins.
func TestTruncation_NonObjectMetadataIsLeftAlone(t *testing.T) {
	inner := &oversizeTool{name: "oversize_array_tool", rawMeta: `["a","b"]`}
	resp := runOversizeTool(t, inner)

	assert.Equal(t, `["a","b"]`, resp.Metadata)
	var data any
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &data))
}

// TestTruncation_UnparseableMetadataIsNotWorsened covers metadata that was
// already not JSON. Truncation must not append to it — that is exactly the
// corruption this fix removes.
func TestTruncation_UnparseableMetadataIsNotWorsened(t *testing.T) {
	inner := &oversizeTool{name: "oversize_broken_tool", rawMeta: "not json at all"}
	resp := runOversizeTool(t, inner)

	assert.Equal(t, "not json at all", resp.Metadata,
		"truncation must not append prose to metadata it could not parse")
}

// TestTruncation_ReservedKeyIsNotClobbered: if a tool genuinely emits a field
// with the reserved name, its value is authoritative and must survive.
func TestTruncation_ReservedKeyIsNotClobbered(t *testing.T) {
	inner := &oversizeTool{
		name:    "oversize_collision_tool",
		rawMeta: `{"attachment_id":"att_9","` + truncationMetadataKey + `":"mine"}`,
	}
	resp := runOversizeTool(t, inner)

	var data map[string]any
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &data))
	assert.Equal(t, "mine", data[truncationMetadataKey], "the tool's own value must win")
	assert.Equal(t, "att_9", data["attachment_id"])
}
