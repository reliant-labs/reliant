// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/llm/drivers/openai"
	"github.com/reliant-labs/reliant/internal/llm/drivers/openrouter"
	"github.com/reliant-labs/reliant/internal/llm/drivers/reliant"
)

const openAIDriverEnvSentinel = "PROCESS_CREDENTIAL_SENTINEL_openai_drivers"

// TestOpenAIDialectDriversSendNoProcessCredential builds every driver whose
// client is an openai-go client with no credential of its own, under a process
// environment holding every OPENAI_* credential and redirect the SDK reads, and
// asserts no request carries one. These drivers run on the multi-tenant server
// roles, so a credential of the process would serve another tenant.
func TestOpenAIDialectDriversSendNoProcessCredential(t *testing.T) {
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("request %s reached OPENAI_BASE_URL", r.URL)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(trap.Close)
	t.Setenv("OPENAI_BASE_URL", trap.URL)
	t.Setenv("OPENAI_API_KEY", openAIDriverEnvSentinel)
	t.Setenv("OPENAI_ADMIN_KEY", openAIDriverEnvSentinel)
	t.Setenv("OPENAI_ORG_ID", openAIDriverEnvSentinel)
	t.Setenv("OPENAI_PROJECT_ID", openAIDriverEnvSentinel)
	t.Setenv("OPENAI_CUSTOM_HEADERS", "X-Process-Credential: "+openAIDriverEnvSentinel)

	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var rec strings.Builder
		rec.WriteString(r.URL.String() + "\n" + string(b))
		for name, values := range r.Header {
			rec.WriteString("\n" + name + ": " + strings.Join(values, ","))
		}
		mu.Lock()
		seen = append(seen, rec.String())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
	}))
	t.Cleanup(srv.Close)

	opts := llm.DriverOptions{BaseURL: srv.URL}
	for name, client := range map[string]openaisdk.Client{
		"openai":     openai.NewClient(opts).Client,
		"openrouter": openrouter.NewClient(opts).Client,
		"reliant":    reliant.NewClient(opts).Client,
		"local":      local.NewClient(opts).Client,
	} {
		t.Run(name, func(t *testing.T) {
			mu.Lock()
			before := len(seen)
			mu.Unlock()
			if _, err := client.Models.List(context.Background()); err != nil {
				t.Fatalf("Models.List: %v", err)
			}
			mu.Lock()
			reqs := append([]string(nil), seen[before:]...)
			mu.Unlock()
			if len(reqs) == 0 {
				t.Fatal("no request reached the account's endpoint")
			}
			for _, r := range reqs {
				if strings.Contains(r, openAIDriverEnvSentinel) {
					t.Errorf("a request carried a credential of the server process:\n%s", r)
				}
			}
		})
	}
}
