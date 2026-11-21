// Copyright (c) 2025 Reliant Labs
package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	anthropicopt "github.com/anthropics/anthropic-sdk-go/option"
)

// processCredentialSentinel stands in for a credential of the server process.
// It must never appear in a request a driver sends.
const processCredentialSentinel = "PROCESS_CREDENTIAL_SENTINEL_must_not_leave_the_process"

// recordedRequest is what a test provider server saw of one request.
type recordedRequest struct {
	header http.Header
	url    string
	body   string
}

// recordingServer is a provider stand-in that records every request and
// answers each with body.
func recordingServer(t *testing.T, contentType, body string) (*httptest.Server, func() []recordedRequest) {
	t.Helper()
	var mu sync.Mutex
	var seen []recordedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, recordedRequest{header: r.Header.Clone(), url: r.URL.String(), body: string(b)})
		mu.Unlock()
		w.Header().Set("Content-Type", contentType)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []recordedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedRequest(nil), seen...)
	}
}

// trapServer fails the test if any request reaches it: it is where the
// process environment points, and nothing may follow it there.
func trapServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("request %s %s reached the endpoint named only by the process environment", r.Method, r.URL)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// assertNoSentinel fails when any recorded request carries the sentinel in a
// header, the URL or the body.
func assertNoSentinel(t *testing.T, reqs []recordedRequest) {
	t.Helper()
	if len(reqs) == 0 {
		t.Fatal("the provider server received no request")
	}
	for _, r := range reqs {
		for name, values := range r.header {
			for _, v := range values {
				if strings.Contains(v, processCredentialSentinel) {
					t.Errorf("header %s carries the process credential: %q", name, v)
				}
			}
		}
		if strings.Contains(r.url, processCredentialSentinel) {
			t.Errorf("URL carries the process credential: %s", r.url)
		}
		if strings.Contains(r.body, processCredentialSentinel) {
			t.Errorf("body carries the process credential")
		}
	}
}

const anthropicMessageJSON = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-test",` +
	`"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`

// TestAnthropicSDKClientIgnoresProcessEnvironment pins that a client built
// with no credential of its own sends nothing the server process's environment
// holds: no ANTHROPIC_API_KEY / ANTHROPIC_AUTH_TOKEN credential, no
// ANTHROPIC_CUSTOM_HEADERS header, no ANTHROPIC_BASE_URL redirect, and no
// ANTHROPIC_PROFILE lookup. Drivers run on the multi-tenant server roles.
func TestAnthropicSDKClientIgnoresProcessEnvironment(t *testing.T) {
	trap := trapServer(t)
	t.Setenv("ANTHROPIC_API_KEY", processCredentialSentinel)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", processCredentialSentinel)
	t.Setenv("ANTHROPIC_CUSTOM_HEADERS", "X-Process-Credential: "+processCredentialSentinel)
	t.Setenv("ANTHROPIC_BASE_URL", trap.URL)
	// A profile the SDK would fail to load: with environment defaults on, the
	// request below errors naming it.
	t.Setenv("ANTHROPIC_PROFILE", "process-profile-must-not-load")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	srv, seen := recordingServer(t, "application/json", anthropicMessageJSON)

	call := func(t *testing.T, opts ...anthropicopt.RequestOption) []recordedRequest {
		t.Helper()
		before := len(seen())
		client := NewAnthropicSDKClient(append([]anthropicopt.RequestOption{anthropicopt.WithBaseURL(srv.URL), anthropicopt.WithMaxRetries(0)}, opts...)...)
		_, err := client.Messages.New(context.Background(), anthropicsdk.MessageNewParams{
			Model:     "claude-test",
			MaxTokens: 1,
			Messages:  []anthropicsdk.MessageParam{anthropicsdk.NewUserMessage(anthropicsdk.NewTextBlock("hi"))},
		})
		if err != nil {
			t.Fatalf("Messages.New: %v", err)
		}
		return seen()[before:]
	}

	t.Run("no credential", func(t *testing.T) {
		reqs := call(t)
		assertNoSentinel(t, reqs)
		for _, r := range reqs {
			if v := r.header.Get("X-Api-Key"); v != "" {
				t.Errorf("x-api-key = %q, want none", v)
			}
			if v := r.header.Get("Authorization"); v != "" {
				t.Errorf("authorization = %q, want none", v)
			}
		}
	})

	t.Run("empty credential", func(t *testing.T) {
		reqs := call(t, anthropicopt.WithAPIKey(""))
		assertNoSentinel(t, reqs)
		if v := reqs[0].header.Get("X-Api-Key"); v != "" {
			t.Errorf("x-api-key = %q, want none", v)
		}
	})

	t.Run("caller credential still applies", func(t *testing.T) {
		reqs := call(t, anthropicopt.WithAPIKey("sk-ant-account"))
		assertNoSentinel(t, reqs)
		if v := reqs[0].header.Get("X-Api-Key"); v != "sk-ant-account" {
			t.Errorf("x-api-key = %q, want the account's key", v)
		}
	})
}
