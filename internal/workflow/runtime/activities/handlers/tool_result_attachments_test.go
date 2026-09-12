// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// attachmentIDsFromMetadata is how a tool tells SaveMessage "I persisted this
// image, render it". It reads the tool's own structured metadata — the value
// that already feeds response_data — rather than adding a second channel
// through every executor between the tool and here.
func TestAttachmentIDsFromMetadata(t *testing.T) {
	cases := []struct {
		name     string
		metadata string
		want     []string
	}{
		{
			name:     "single attachment_id, the generate_image shape",
			metadata: `{"attachment_id":"att-1","filename":"generated-abc.png","mime_type":"image/png"}`,
			want:     []string{"att-1"},
		},
		{
			name:     "plural attachment_ids",
			metadata: `{"attachment_ids":["att-1","att-2"]}`,
			want:     []string{"att-1", "att-2"},
		},
		{
			name:     "both forms, without duplicating the shared id",
			metadata: `{"attachment_id":"att-1","attachment_ids":["att-1","att-2"]}`,
			want:     []string{"att-1", "att-2"},
		},
		{
			name:     "empty ids are dropped rather than becoming blank blocks",
			metadata: `{"attachment_id":"","attachment_ids":["","att-2"]}`,
			want:     []string{"att-2"},
		},
		{
			name:     "a tool with metadata but no attachments",
			metadata: `{"exit_code":0,"stdout":"hello"}`,
			want:     nil,
		},
		{
			name:     "no metadata at all — every tool that never opts in",
			metadata: "",
			want:     nil,
		},
		{
			name:     "metadata that is not a JSON object is not an error",
			metadata: `["not","an","object"]`,
			want:     nil,
		},
		{
			name:     "malformed JSON is not an error",
			metadata: `{"attachment_id":`,
			want:     nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, attachmentIDsFromMetadata(tc.metadata))
		})
	}
}

// The attachment ids must survive the proto round trip in both directions, or
// they are dropped somewhere between the activity that produces them and the
// SaveMessage that renders them.
func TestToolResultAttachmentIDsSurviveProtoRoundTrip(t *testing.T) {
	original := []ToolResult{{
		ToolCallID:    "toolu_1",
		Name:          "generate_image",
		Content:       "Generated an image.",
		AttachmentIDs: []string{"att-1", "att-2"},
	}}

	protoResults := messageToolResultsToProto(original)
	require.Len(t, protoResults, 1)
	assert.Equal(t, []string{"att-1", "att-2"}, protoResults[0].GetAttachmentIds(),
		"attachment ids must be carried on the wire to the SaveMessage activity")

	roundTripped := protoToolResultsToMessage(protoResults)
	require.Len(t, roundTripped, 1)
	assert.Equal(t, []string{"att-1", "att-2"}, roundTripped[0].AttachmentIDs,
		"attachment ids must survive back into the domain type SaveMessage reads")
}
