// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mediaRouter answers every daemon call with resp, after a JSON round trip:
// the gateway-to-worker hop is JSON over NATS.
type mediaRouter struct {
	routerStub
	resp *ToolExecutionResponse
}

func (r *mediaRouter) SendToolRequestSync(context.Context, string, *ToolExecutionRequest) (*ToolExecutionResponse, error) {
	raw, err := json.Marshal(r.resp)
	if err != nil {
		return nil, err
	}
	var out ToolExecutionResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// A daemon-placed tool's images (an MCP screenshot) reach the worker as the
// tool result's BinaryParts, where execute_tools persists them for the model.
// They used to be dropped on the daemon, so the model saw only the caption.
func TestExecuteOnDaemon_CarriesTheResultsMediaAcrossEveryHop(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nfake")
	parts := []message.BinaryContent{{MIMEType: "image/png", Data: png, Path: "/w/shot.png"}}

	// Daemon -> gateway: the proto ToolResponse.
	wire := MediaToProto(parts)
	require.Len(t, wire, 1)

	// Gateway -> worker: ToolExecutionResponse, as routeToolResponse builds it.
	resp := &ToolExecutionResponse{Success: true, Content: "[Image: image/png, 12B]", Media: MediaFromProto(wire)}
	executor := &RemoteExecutor{router: &mediaRouter{resp: resp}}
	req := &ToolRequest{UserID: "u", ChatID: "c", ProjectID: "p", ToolName: "mcp__chrome-devtools__take_screenshot", ToolCallID: "tc"}
	res, err := executor.executeOnDaemon(context.Background(), req, time.Now())
	require.NoError(t, err)
	assert.Equal(t, parts, res.BinaryParts)
}

func TestToolResponseMedia_SkipsEmptyParts(t *testing.T) {
	assert.Nil(t, MediaToProto(nil))
	assert.Empty(t, MediaToProto([]message.BinaryContent{{MIMEType: "image/png"}}), "a part with no bytes is not sent")
	assert.Nil(t, MediaFromProto(nil))
	resp := &ToolExecutionResponse{}
	assert.Nil(t, resp.binaryParts(), "a response from a daemon that predates media has none")
}
