// Copyright (c) 2025 Reliant Labs
package imagegen

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

// The gateway refuses a generation an org's policy forbids with 403 and the
// reason as x-forge-error-reason (control-plane docs/design/org-policies.md
// §5.1). The managed client answers it as the policy's verdict: one attempt,
// no retry, and never an *APIError a caller could read as a dead key.

func refusingGateway(t *testing.T, attempts *int, header, code string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*attempts++
		if header != "" {
			w.Header().Set("X-Forge-Error-Reason", header)
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"model gpt-image-2.5-flare is not allowed in this organization","code":"` + code + `"}}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestGenerateImage_ManagedPolicyRefusalIsAVerdictNotRetried(t *testing.T) {
	for name, tc := range map[string]struct{ header, code, want string }{
		"reason header":                      {header: orgpolicy.ReasonModelNotAllowed, want: orgpolicy.ReasonModelNotAllowed},
		"reason in the body, header dropped": {code: orgpolicy.ReasonMemberSpendCapReached, want: orgpolicy.ReasonMemberSpendCapReached},
	} {
		t.Run(name, func(t *testing.T) {
			attempts := 0
			server := refusingGateway(t, &attempts, tc.header, tc.code)
			client := newTestClient(t, Config{BaseURL: server.URL + "/v1", APIModel: "gpt-image-2.5-flare", Driver: "reliant", RetryBaseDelay: time.Millisecond})

			_, err := client.GenerateImage(context.Background(), Request{Prompt: "x"})

			refusal, ok := orgpolicy.RefusalOf(err)
			if !ok {
				t.Fatalf("want the policy's refusal, got %v", err)
			}
			if refusal.Reason != tc.want {
				t.Errorf("reason = %q, want %q", refusal.Reason, tc.want)
			}
			if refusal.Detail == "" {
				t.Error("the gateway's own explanation is kept as the detail")
			}
			var apiErr *APIError
			if errors.As(err, &apiErr) {
				t.Error("a policy refusal must not be an *APIError: nothing may read it as a rejected key")
			}
			if attempts != 1 {
				t.Errorf("a policy refusal was retried %d times", attempts-1)
			}
		})
	}
}

func TestGenerateImage_A403WithoutAPolicyReasonIsNotARefusal(t *testing.T) {
	attempts := 0
	server := refusingGateway(t, &attempts, "", "forbidden")
	client := newTestClient(t, Config{BaseURL: server.URL + "/v1", APIModel: "gpt-image-2.5-flare", Driver: "reliant", RetryBaseDelay: time.Millisecond})

	_, err := client.GenerateImage(context.Background(), Request{Prompt: "x"})
	if orgpolicy.IsRefusal(err) {
		t.Fatalf("a 403 naming no policy reason is not the policy's: %v", err)
	}
}

func TestGenerateImage_ABYOProvidersReasonHeaderIsNotARefusal(t *testing.T) {
	attempts := 0
	server := refusingGateway(t, &attempts, orgpolicy.ReasonModelNotAllowed, "")
	client := newTestClient(t, Config{BaseURL: server.URL + "/v1", APIModel: "gpt-image-2", Driver: "openai", RetryBaseDelay: time.Millisecond})

	_, err := client.GenerateImage(context.Background(), Request{Prompt: "x"})
	if orgpolicy.IsRefusal(err) {
		t.Fatalf("only the managed driver reaches the gateway; a BYO provider's 403 is its own: %v", err)
	}
}
