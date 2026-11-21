// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/require"
)

const processEnvSentinel = "PROCESS_CREDENTIAL_SENTINEL_anthropic_driver"

// TestDriversSendNoProcessCredential builds each Anthropic-dialect driver with
// no credential of its own, under a process environment that holds every
// ANTHROPIC_* credential and redirect the SDK knows, and asserts no request
// carries one. These drivers run on the multi-tenant server roles.
func TestDriversSendNoProcessCredential(t *testing.T) {
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("request %s reached ANTHROPIC_BASE_URL", r.URL)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(trap.Close)
	t.Setenv("ANTHROPIC_API_KEY", processEnvSentinel)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", processEnvSentinel)
	t.Setenv("ANTHROPIC_CUSTOM_HEADERS", "X-Process-Credential: "+processEnvSentinel)
	t.Setenv("ANTHROPIC_BASE_URL", trap.URL)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var b strings.Builder
		b.WriteString(r.URL.String())
		for name, values := range r.Header {
			b.WriteString("\n" + name + ": " + strings.Join(values, ","))
		}
		b.WriteString("\n" + string(body))
		mu.Lock()
		seen = append(seen, b.String())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m",`+
			`"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)

	opts := llm.DriverOptions{
		Model:     models.Model{APIModel: "claude-test"},
		MaxTokens: 1024,
		BaseURL:   srv.URL,
	}
	turn := []message.Message{{ID: "u-1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}}

	for name, send := range map[string]func() error{
		"api key": func() error {
			_, err := NewAnthropicClient(opts).SendMessages(context.Background(), nil, turn, nil)
			return err
		},
		"api key with options": func() error {
			_, err := NewAnthropicClientWithOptions(opts).SendMessages(context.Background(), nil, turn, nil)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			mu.Lock()
			before := len(seen)
			mu.Unlock()
			require.NoError(t, send())
			mu.Lock()
			reqs := append([]string(nil), seen[before:]...)
			mu.Unlock()
			require.NotEmpty(t, reqs, "no request reached the account's endpoint")
			for _, r := range reqs {
				require.NotContains(t, r, processEnvSentinel, "a request carried a credential of the server process")
			}
		})
	}

	// The Claude Code driver writes an exact-cased host header net/http's
	// server refuses, so it is checked against the raw versionFloorServer.
	t.Run("claude code", func(t *testing.T) {
		raw := newVersionFloorServer(t)
		ccOpts := opts
		ccOpts.BaseURL = raw.url()
		_, err := NewClaudeCodeClient(ccOpts).SendMessages(context.Background(), nil, turn, nil)
		require.NoError(t, err)
		req := raw.last()
		for name, values := range req.header {
			require.NotContains(t, name+": "+strings.Join(values, ","), processEnvSentinel, "a header carried a credential of the server process")
		}
		require.NotContains(t, string(req.body), processEnvSentinel)
	})
}
