// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const toolExecutorManifest = `
id: signer
version: 1
display_name: Signer
connection:
  base_url: https://api.signer.example.com
  auth:
    - api_key: { in: header, name: X-Key }
actions:
  - id: sign
    placement: server
    executor: go:signer.tool_sign
    tool: { expose: true }
    params:
      type: object
      required: [text]
      properties: { text: { type: string } }
`

type toolCred struct{ secret string }

func (c toolCred) ConnectionID() string        { return "conn_tool" }
func (c toolCred) Apply(r *http.Request) error { r.Header.Set("X-Key", c.secret); return nil }
func (c toolCred) Scrub(s string) string       { return strings.ReplaceAll(s, c.secret, "[redacted]") }
func (c toolCred) Params() map[string]string   { return nil }

type toolCredSource struct {
	got []httpaction.CredentialRequest
}

func (s *toolCredSource) Credential(_ context.Context, req httpaction.CredentialRequest) (httpaction.Credential, error) {
	s.got = append(s.got, req)
	return toolCred{secret: "tool-secret-1"}, nil
}

// The agent tool form of a go: action dispatches to the same registered
// function, resolving the connection for the run the tool executes in.
func TestIntegrationToolDispatchesGoExecutor(t *testing.T) {
	m, err := manifest.Parse([]byte(toolExecutorManifest), manifest.TrustCurated)
	require.NoError(t, err)
	reg := httpaction.NewExecutorRegistry()
	require.NoError(t, reg.Register("signer.tool_sign", func(_ context.Context, call httpaction.ExecutorCall) (*httpaction.Result, error) {
		return &httpaction.Result{StatusCode: 200, Data: map[string]any{"signed": call.Params["text"].(string) + ":tool-secret-1"}}, nil
	}))
	restore := UseIntegrationRunner(httpaction.NewRunner(netguard.New()).WithExecutors(reg))
	defer restore()

	src := &toolCredSource{}
	tool := newIntegrationTool(m, m.GetActions()[0], src)
	input, _ := json.Marshal(map[string]any{"text": "abc"})
	resp, err := tool.Run(&rctx.ToolContext{Context: context.Background(), ChatID: "run-7", Thread: "run-7"}, ToolCall{ID: "tc-1", Name: tool.Name(), Input: string(input)})
	require.NoError(t, err)
	assert.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, `abc:[redacted]`)
	assert.NotContains(t, resp.Content+resp.Metadata, "tool-secret-1")
	require.Len(t, src.got, 1)
	assert.Equal(t, "run-7", src.got[0].RunID)
	assert.Equal(t, "tc-1", src.got[0].ToolCallID)
	assert.Equal(t, "signer", src.got[0].IntegrationID)
}
