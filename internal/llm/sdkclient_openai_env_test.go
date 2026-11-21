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

	openaiopt "github.com/openai/openai-go/v3/option"
)

// openAIEnvSentinel stands in for a credential of the server process. It must
// never appear in a request a driver sends.
const openAIEnvSentinel = "PROCESS_CREDENTIAL_SENTINEL_openai_env"

// setOpenAIProcessEnvironment sets every variable openai.DefaultClientOptions
// reads to a sentinel, with OPENAI_BASE_URL at a server that fails the test if
// anything reaches it.
func setOpenAIProcessEnvironment(t *testing.T) {
	t.Helper()
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("request %s %s reached OPENAI_BASE_URL", r.Method, r.URL)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(trap.Close)
	t.Setenv("OPENAI_BASE_URL", trap.URL)
	t.Setenv("OPENAI_API_KEY", openAIEnvSentinel)
	t.Setenv("OPENAI_ADMIN_KEY", openAIEnvSentinel)
	t.Setenv("OPENAI_ORG_ID", openAIEnvSentinel)
	t.Setenv("OPENAI_PROJECT_ID", openAIEnvSentinel)
	t.Setenv("OPENAI_WEBHOOK_SECRET", openAIEnvSentinel)
	t.Setenv("OPENAI_CUSTOM_HEADERS", "X-Process-Credential: "+openAIEnvSentinel+"\nX-Other-Process-Header: "+openAIEnvSentinel)
}

// TestOpenAISDKClientIgnoresProcessEnvironment pins that a client built with
// no credential of its own sends nothing the server process's environment
// holds, while a caller's key, endpoint and headers still apply. Drivers run
// on the multi-tenant server roles.
func TestOpenAISDKClientIgnoresProcessEnvironment(t *testing.T) {
	setOpenAIProcessEnvironment(t)

	var mu sync.Mutex
	var seen []*http.Request
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, r.Clone(context.Background()))
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
	}))
	t.Cleanup(srv.Close)

	call := func(t *testing.T, opts ...openaiopt.RequestOption) *http.Request {
		t.Helper()
		mu.Lock()
		before := len(seen)
		mu.Unlock()
		client := NewOpenAISDKClient(append([]openaiopt.RequestOption{openaiopt.WithBaseURL(srv.URL), openaiopt.WithMaxRetries(0)}, opts...)...)
		if _, err := client.Models.List(context.Background()); err != nil {
			t.Fatalf("Models.List: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(seen) != before+1 {
			t.Fatalf("the account's endpoint received %d requests, want 1", len(seen)-before)
		}
		r := seen[before]
		for name, values := range r.Header {
			for _, v := range values {
				if strings.Contains(v, openAIEnvSentinel) {
					t.Errorf("header %s carries the process credential: %q", name, v)
				}
			}
		}
		if strings.Contains(r.URL.String(), openAIEnvSentinel) || strings.Contains(bodies[before], openAIEnvSentinel) {
			t.Errorf("URL or body carries the process credential")
		}
		return r
	}

	t.Run("no credential", func(t *testing.T) {
		r := call(t)
		for _, h := range []string{"Authorization", "OpenAI-Organization", "OpenAI-Project", "X-Process-Credential", "X-Other-Process-Header"} {
			if v := r.Header.Get(h); v != "" {
				t.Errorf("%s = %q, want none", h, v)
			}
		}
	})

	t.Run("caller credential and headers still apply", func(t *testing.T) {
		r := call(t, openaiopt.WithAPIKey("sk-account"), openaiopt.WithHeader("X-Process-Credential", "the-account's"))
		if got := r.Header.Get("Authorization"); got != "Bearer sk-account" {
			t.Errorf("Authorization = %q, want the account's key", got)
		}
		if got := r.Header.Get("X-Process-Credential"); got != "the-account's" {
			t.Errorf("a caller header of the same name = %q, want the caller's value", got)
		}
	})

	t.Run("caller authorization header still applies", func(t *testing.T) {
		r := call(t, openaiopt.WithHeader("Authorization", "Bearer gho_account"))
		if got := r.Header.Get("Authorization"); got != "Bearer gho_account" {
			t.Errorf("Authorization = %q, want the caller's header", got)
		}
	})
}

// TestOpenAISDKClientDefaultsToTheProductionEndpoint pins that, with no base
// URL of its own, a client goes to api.openai.com rather than OPENAI_BASE_URL.
func TestOpenAISDKClientDefaultsToTheProductionEndpoint(t *testing.T) {
	setOpenAIProcessEnvironment(t)

	var host string
	client := NewOpenAISDKClient(openaiopt.WithMaxRetries(0), openaiopt.WithMiddleware(
		func(r *http.Request, _ openaiopt.MiddlewareNext) (*http.Response, error) {
			host = r.URL.Host
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(`{"object":"list","data":[]}`)), Request: r}, nil
		}))
	if _, err := client.Models.List(context.Background()); err != nil {
		t.Fatalf("Models.List: %v", err)
	}
	if host != "api.openai.com" {
		t.Fatalf("request host = %q, want api.openai.com", host)
	}
}
