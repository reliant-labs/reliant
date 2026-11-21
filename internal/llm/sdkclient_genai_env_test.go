// Copyright (c) 2025 Reliant Labs
package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"cloud.google.com/go/auth"
	"google.golang.org/genai"
)

// genAIEnvSentinel stands in for a credential of the server process. It must
// never appear in a request a driver sends.
const genAIEnvSentinel = "PROCESS_CREDENTIAL_SENTINEL_genai_env"

// setGenAIProcessEnvironment sets every variable genai's environment provider
// reads, with both base URLs at a server that fails the test if anything
// reaches it.
func setGenAIProcessEnvironment(t *testing.T) {
	t.Helper()
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("request %s %s reached a base URL named only by the process environment", r.Method, r.URL)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(trap.Close)
	t.Setenv("GOOGLE_API_KEY", genAIEnvSentinel)
	t.Setenv("GEMINI_API_KEY", genAIEnvSentinel)
	t.Setenv("GOOGLE_GENAI_USE_VERTEXAI", "true")
	t.Setenv("GOOGLE_GENAI_USE_ENTERPRISE", "true")
	t.Setenv("GOOGLE_CLOUD_PROJECT", genAIEnvSentinel)
	t.Setenv("GOOGLE_CLOUD_LOCATION", genAIEnvSentinel)
	t.Setenv("GOOGLE_CLOUD_REGION", genAIEnvSentinel)
	t.Setenv("GOOGLE_GEMINI_BASE_URL", trap.URL)
	t.Setenv("GOOGLE_VERTEX_BASE_URL", trap.URL)
}

// TestGenAISDKClientIgnoresProcessEnvironment pins that a genai client never
// takes a credential, backend, placement or endpoint from the server process's
// environment. Drivers run on the multi-tenant server roles.
func TestGenAISDKClientIgnoresProcessEnvironment(t *testing.T) {
	setGenAIProcessEnvironment(t)

	t.Run("a Gemini API client with no key is refused", func(t *testing.T) {
		_, err := NewGenAISDKClient(context.Background(), &genai.ClientConfig{Backend: genai.BackendGeminiAPI})
		if !errors.Is(err, ErrGeminiAPIKeyRequired) {
			t.Fatalf("err = %v, want ErrGeminiAPIKeyRequired", err)
		}
	})

	t.Run("an unnamed backend is refused", func(t *testing.T) {
		for _, cfg := range []*genai.ClientConfig{nil, {APIKey: "account-key"}} {
			if _, err := NewGenAISDKClient(context.Background(), cfg); !errors.Is(err, errGenAIBackendRequired) {
				t.Errorf("NewGenAISDKClient(%+v): err = %v, want errGenAIBackendRequired", cfg, err)
			}
		}
	})

	t.Run("a Vertex AI client with no placement is refused", func(t *testing.T) {
		_, err := NewGenAISDKClient(context.Background(), &genai.ClientConfig{Backend: genai.BackendVertexAI, Credentials: staticCredentials("tok")})
		if !errors.Is(err, errVertexPlacementRequired) {
			t.Fatalf("err = %v, want errVertexPlacementRequired", err)
		}
	})

	t.Run("a Vertex AI client without the account's credentials is refused", func(t *testing.T) {
		// The server's own Google credentials, where ADC would find them.
		keyFile := filepath.Join(t.TempDir(), "adc.json")
		if err := os.WriteFile(keyFile, []byte(`{"type":"authorized_user","client_id":"x","client_secret":"`+genAIEnvSentinel+`","refresh_token":"`+genAIEnvSentinel+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", keyFile)
		for _, cfg := range []*genai.ClientConfig{
			{Backend: genai.BackendVertexAI, Project: "p", Location: "us-central1"},
			{Backend: genai.BackendVertexAI, Project: "p", Location: "us-central1", APIKey: "express-key"},
		} {
			if _, err := NewGenAISDKClient(context.Background(), cfg); !errors.Is(err, errVertexCredentialsRequired) {
				t.Errorf("NewGenAISDKClient(%+v): err = %v, want errVertexCredentialsRequired", cfg, err)
			}
		}
	})

	t.Run("the Enterprise backend is refused", func(t *testing.T) {
		_, err := NewGenAISDKClient(context.Background(), &genai.ClientConfig{Backend: genai.BackendEnterprise, Project: "p", Location: "us-central1"})
		if !errors.Is(err, errGenAIEnterpriseUnsupported) {
			t.Fatalf("err = %v, want errGenAIEnterpriseUnsupported", err)
		}
	})

	t.Run("the caller's config is not mutated", func(t *testing.T) {
		in := &genai.ClientConfig{Backend: genai.BackendGeminiAPI, APIKey: "account-key"}
		if _, err := NewGenAISDKClient(context.Background(), in); err != nil {
			t.Fatal(err)
		}
		if in.HTTPOptions.BaseURL != "" || in.HTTPClient != nil {
			t.Fatalf("NewGenAISDKClient filled the caller's config: base %q, http client set %v", in.HTTPOptions.BaseURL, in.HTTPClient != nil)
		}
	})

	t.Run("a Vertex AI client with only a project defaults to us-central1", func(t *testing.T) {
		client, err := NewGenAISDKClient(context.Background(), &genai.ClientConfig{Backend: genai.BackendVertexAI, Project: "account-project", Credentials: staticCredentials("tok")})
		if err != nil {
			t.Fatal(err)
		}
		if cc := client.ClientConfig(); cc.Location != DefaultVertexLocation || cc.HTTPOptions.BaseURL != "https://us-central1-aiplatform.googleapis.com/" {
			t.Fatalf("resolved location %q base %q, want us-central1 and its endpoint", cc.Location, cc.HTTPOptions.BaseURL)
		}
	})

	t.Run("a Vertex AI request is signed with the account's token only", func(t *testing.T) {
		var mu sync.Mutex
		var seen []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var rec strings.Builder
			rec.WriteString(r.URL.String())
			for name, values := range r.Header {
				rec.WriteString("\n" + name + ": " + strings.Join(values, ","))
			}
			mu.Lock()
			seen = append(seen, rec.String())
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}`)
		}))
		t.Cleanup(srv.Close)

		client, err := NewGenAISDKClient(context.Background(), &genai.ClientConfig{
			Backend: genai.BackendVertexAI, Project: "account-project", Location: "us-central1",
			Credentials: staticCredentials("account-token"), HTTPOptions: genai.HTTPOptions{BaseURL: srv.URL},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Models.GenerateContent(context.Background(), "gemini-test", genai.Text("hi"), nil); err != nil {
			t.Fatalf("GenerateContent: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(seen) != 1 {
			t.Fatalf("the account's endpoint received %d requests, want 1", len(seen))
		}
		if strings.Contains(seen[0], genAIEnvSentinel) {
			t.Errorf("the request carried a credential or placement of the server process:\n%s", seen[0])
		}
		if !strings.Contains(seen[0], "Authorization: Bearer account-token") || !strings.Contains(seen[0], "projects/account-project/locations/us-central1") {
			t.Errorf("the request is not the account's (token, project, region):\n%s", seen[0])
		}
	})

	t.Run("a Gemini API request carries only the account's key", func(t *testing.T) {
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
			_, _ = io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}`)
		}))
		t.Cleanup(srv.Close)

		client, err := NewGenAISDKClient(context.Background(), &genai.ClientConfig{
			Backend: genai.BackendGeminiAPI, APIKey: "account-key",
			HTTPOptions: genai.HTTPOptions{BaseURL: srv.URL},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Models.GenerateContent(context.Background(), "gemini-test", genai.Text("hi"), nil); err != nil {
			t.Fatalf("GenerateContent: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(seen) != 1 {
			t.Fatalf("the account's endpoint received %d requests, want 1", len(seen))
		}
		if strings.Contains(seen[0], genAIEnvSentinel) {
			t.Errorf("the request carried a credential of the server process:\n%s", seen[0])
		}
		if !strings.Contains(seen[0], "X-Goog-Api-Key: account-key") {
			t.Errorf("the request did not carry the account's key:\n%s", seen[0])
		}
	})

	t.Run("a Gemini API client with no endpoint goes to Google", func(t *testing.T) {
		client, err := NewGenAISDKClient(context.Background(), &genai.ClientConfig{Backend: genai.BackendGeminiAPI, APIKey: "account-key"})
		if err != nil {
			t.Fatal(err)
		}
		if got := client.ClientConfig().HTTPOptions.BaseURL; got != genAIGeminiBaseURL {
			t.Fatalf("base URL = %q, want %q", got, genAIGeminiBaseURL)
		}
	})
}

// staticCredentials are account credentials that always answer token.
func staticCredentials(token string) *auth.Credentials {
	return auth.NewCredentials(&auth.CredentialsOptions{TokenProvider: staticToken(token)})
}

// staticToken is an auth.TokenProvider that always answers its token.
type staticToken string

func (s staticToken) Token(context.Context) (*auth.Token, error) {
	return &auth.Token{Value: string(s), Type: "Bearer"}, nil
}
