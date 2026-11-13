package reliant

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/chatmarkers"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

// policyGateway is a fake Reliant gateway that refuses every call as
// control-plane's org policy enforcement does (org-policies.md §4.7): a 403,
// x-forge-error-reason naming the policy, and an OpenAI-shaped body whose
// error.code is the same reason. header=false drops the header, as a proxy
// in between might.
func policyGateway(t *testing.T, reason string, header bool) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if header {
			w.Header().Set(svcerr.ReasonHeader, reason)
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"Your organization's spend cap applies; an org admin sets it.","type":"permission_error","code":"` + reason + `"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// Every policy reason is a verdict the managed driver hands back typed, at
// once: one request (no retry), the reason and the chat marker on the error,
// a user error class, and never a rejected credential.
func TestPolicyRefusalIsAVerdict(t *testing.T) {
	for _, reason := range []string{
		orgpolicy.ReasonModelNotAllowed, orgpolicy.ReasonMemberSpendCapReached,
		orgpolicy.ReasonOrgSpendCapReached, orgpolicy.ReasonProviderNotAllowed,
	} {
		for _, header := range []bool{true, false} {
			srv, calls := policyGateway(t, reason, header)
			client := NewClient(llm.DriverOptions{ApiKey: "rlk-test", BaseURL: srv.URL, Model: models.Model{ID: "claude-opus-5"}})
			_, err := client.SendMessages(context.Background(), nil, nil, nil)

			got, ok := orgpolicy.RefusalOf(err)
			if !ok || got.Reason != reason {
				t.Fatalf("%s (header %v): err = %v, want a refusal for %s", reason, header, err, reason)
			}
			if calls.Load() != 1 {
				t.Errorf("%s: %d requests, want 1 (a verdict is never retried)", reason, calls.Load())
			}
			if kind, payload, found := chatmarkers.Extract(err.Error()); !found || kind != chatmarkers.KindOrgPolicyRefused || payload != reason {
				t.Errorf("%s: message %q lacks the policy marker", reason, err)
			}
			if !strings.Contains(err.Error(), "spend cap applies") {
				t.Errorf("%s: the gateway's own explanation is lost: %q", reason, err)
			}
			if svcerr.Classify(err) != svcerr.ClassUser {
				t.Errorf("%s: class = %v, want user", reason, svcerr.Classify(err))
			}
			// It does not unwrap to the gateway's 403, so nothing that reads a
			// provider 403 as a dead key can see one (drivers.CredentialRejected
			// also refuses a refusal outright; credential_rejected_test.go).
			var apierr *openai.Error
			if errors.As(err, &apierr) {
				t.Errorf("%s: the refusal unwraps to the gateway's 403", reason)
			}
		}
	}
}

// The stream path hands the same verdict back as its error event.
func TestPolicyRefusalOnTheStream(t *testing.T) {
	srv, calls := policyGateway(t, orgpolicy.ReasonModelNotAllowed, true)
	client := NewClient(llm.DriverOptions{ApiKey: "rlk-test", BaseURL: srv.URL, Model: models.Model{ID: "claude-opus-5"}})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var refused error
	for ev := range client.StreamResponse(ctx, nil, nil, nil) {
		if ev.Type == llm.EventRetryWait {
			t.Fatal("a policy refusal announced a retry")
		}
		if ev.Type == llm.EventError {
			refused = ev.Error
		}
	}
	if r, ok := orgpolicy.RefusalOf(refused); !ok || r.Reason != orgpolicy.ReasonModelNotAllowed || calls.Load() != 1 {
		t.Fatalf("stream error = %v after %d requests, want one model_not_allowed refusal", refused, calls.Load())
	}
}

// A 403 that names no policy reason is not a policy verdict: it keeps today's
// handling.
func TestA403WithoutAPolicyReasonIsUnchanged(t *testing.T) {
	ok, reason := shouldRetryReliantAPIError(newOpenAIErrorForTest(t, http.StatusForbidden, `{"error":{"message":"forbidden","type":"permission_error","code":"forbidden"}}`))
	if ok || reason != "terminal_auth_config_error" {
		t.Fatalf("plain 403 = %v %q, want terminal_auth_config_error", ok, reason)
	}
	byo := newOpenAIErrorForTest(t, http.StatusForbidden, `{"error":{"message":"invalid key","code":"invalid_api_key"}}`)
	if orgpolicy.IsRefusal(byo) {
		t.Fatal("a BYO 403 was read as a policy refusal")
	}
	// A 429 naming a policy reason is the wallet's status, never a policy's.
	if r := orgPolicyRefusalReason(newOpenAIErrorForTest(t, http.StatusTooManyRequests, `{"error":{"code":"model_not_allowed"}}`)); r != "" {
		t.Fatalf("429 read as a policy refusal: %q", r)
	}
}
