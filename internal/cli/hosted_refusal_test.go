package cli

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
)

// TestPromoteRefusalOf_ReachesTheCLIIntact is the end-to-end of F0's "typed
// errors off the wire": a refusal's STRUCTURED payload must survive the trip
// from the server's Connect error detail into forge, because that is what
// lets a pipeline print "prod is on v1.9.1, promoted by alice" instead of
// "someone else moved prod" and sending a human to a dashboard.
//
// This is the part the old string flattening destroyed outright: the detail
// was discarded and only a sentence survived.
func TestPromoteRefusalOf_ReachesTheCLIIntact(t *testing.T) {
	refusal := map[string]any{
		"reason":                     reasonPromotionConflict,
		"expectedCurrentPromotionId": "pr_expected",
		"detail":                     "prod is on v1.9.1, promoted by alice 4 minutes ago",
		"actualCurrent": map[string]any{
			"id": "pr_actual", "releaseVersion": "v1.9.1",
			"kind": wireKindPromote, "promotedByUserId": "usr_alice",
		},
	}
	debug, err := json.Marshal(refusal)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(cloud.ReasonHeader, reasonPromotionConflict)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPreconditionFailed)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":    "failed_precondition",
			"message": "the environment's current promotion is not the one you expected",
			"details": []map[string]any{{
				"type":  promoteRefusalType,
				"value": base64.RawStdEncoding.EncodeToString([]byte("proto-bytes")),
				"debug": json.RawMessage(debug),
			}},
		})
	}))
	defer srv.Close()

	ep, err := cloud.ResolveEndpoint("prod", &cloud.Declaration{Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	client := cloud.NewClient(ep, cloud.Credential{Token: "t"})
	callErr := client.Call(t.Context(), procPromote, map[string]any{"environmentId": "env_prod"}, &struct{}{})
	if callErr == nil {
		t.Fatal("expected a refusal")
	}

	// The reason chooses the exit code — reasons, not message text.
	if got := hostedErrorReason(callErr); got != reasonPromotionConflict {
		t.Errorf("reason = %q, want %q", got, reasonPromotionConflict)
	}
	if got := exitCodeForRefusal(hostedErrorReason(callErr)); got != exitConflict {
		t.Errorf("exit code = %d, want %d", got, exitConflict)
	}

	// And the structured payload survived, so the message can name what
	// is actually there.
	got, ok := promoteRefusalOf(callErr)
	if !ok {
		t.Fatal("the refusal detail must reach the CLI")
	}
	if got.Reason != reasonPromotionConflict {
		t.Errorf("refusal reason = %q", got.Reason)
	}
	if got.ExpectedCurrentPromotionID != "pr_expected" {
		t.Errorf("the echoed expectation = %q", got.ExpectedCurrentPromotionID)
	}
	if got.ActualCurrent == nil || got.ActualCurrent.ReleaseVersion != "v1.9.1" {
		t.Fatalf("actualCurrent = %+v", got.ActualCurrent)
	}
	if got.ActualCurrent.PromotedByUserID != "usr_alice" {
		t.Errorf("the refusal must name WHO promoted; got %+v", got.ActualCurrent)
	}

	// It survives the context wrapping a caller adds.
	if _, ok := promoteRefusalOf(fmt.Errorf("promote v2 to prod: %w", callErr)); !ok {
		t.Error("the detail must survive %w wrapping")
	}
}

// TestPromoteRefusalOf_MissingDetailIsNotAFailure: the reason header alone
// is enough to choose an exit code, so a refusal with no decodable detail
// must still read as a refusal. Treating a missing detail as an error would
// make forge refuse to report a refusal.
func TestPromoteRefusalOf_MissingDetailIsNotAFailure(t *testing.T) {
	t.Parallel()
	bare := &cloud.Error{
		Code:    cloud.CodeFailedPrecondition,
		Reason:  reasonRolloutInFlight,
		Message: "a rollout is in flight",
	}
	if _, ok := promoteRefusalOf(bare); ok {
		t.Error("there is no detail to decode")
	}
	// The classification still works, which is the point.
	if got := exitCodeForRefusal(hostedErrorReason(bare)); got != exitRefused {
		t.Errorf("exit code = %d, want %d", got, exitRefused)
	}

	// A plain error is not a refusal at all.
	if _, ok := promoteRefusalOf(errors.New("boom")); ok {
		t.Error("a plain error is not a refusal")
	}
}

// TestPromoteRefusalOf_FallsBackToTheHeaderReason: a detail that states no
// reason still carries the useful part (actual_current), and the header is
// authoritative for the classification.
func TestPromoteRefusalOf_FallsBackToTheHeaderReason(t *testing.T) {
	t.Parallel()
	debug, _ := json.Marshal(map[string]any{
		"detail":        "prod is pinned",
		"actualCurrent": map[string]any{"id": "pr_actual", "releaseVersion": "v1.9.1"},
	})
	err := &cloud.Error{
		Code:   cloud.CodeFailedPrecondition,
		Reason: reasonEnvironmentPinned,
		Details: []cloud.ErrorDetail{{
			Type:  promoteRefusalType,
			Debug: debug,
		}},
	}
	got, ok := promoteRefusalOf(err)
	if !ok {
		t.Fatal("the detail should decode")
	}
	if got.Reason != reasonEnvironmentPinned {
		t.Errorf("a detail with no reason should inherit the header's; got %q", got.Reason)
	}
	if got.ActualCurrent == nil || got.ActualCurrent.ReleaseVersion != "v1.9.1" {
		t.Errorf("the useful part must survive: %+v", got.ActualCurrent)
	}
}
