package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/cloudcred"
)

// TestErrorFactsOf pins what a failed --json document says in structure. A
// host routes on error_reason (sign in again vs ask for a grant); the text
// in `error` is unchanged and is for a person.
func TestErrorFactsOf(t *testing.T) {
	t.Parallel()
	cpRefusal := &cloud.Error{HTTPStatus: 403, Code: "permission_denied", Reason: "scope_missing",
		Scopes: []string{"deploy:write"}, Message: "your organization permissions do not include deploy:write"}
	helperDenied := fmt.Errorf("%w for https://cp: %w", cloud.ErrNoCredential,
		&cloudcred.HelperError{Code: cloudcred.CodeDenied, Message: "no session can authorize",
			Reason: "scope_missing", Scopes: []string{"deploy:write", "secret:read"}})
	helperDeniedNoReason := fmt.Errorf("%w for https://cp: %w", cloud.ErrNoCredential,
		&cloudcred.HelperError{Code: cloudcred.CodeDenied, Message: "no session can authorize"})

	cases := []struct {
		name string
		err  error
		want jsonErrorFacts
	}{
		{"nil", nil, jsonErrorFacts{}},
		{"plain", errors.New("boom"), jsonErrorFacts{}},
		{"control plane refusal", fmt.Errorf("capacity: %w", cpRefusal),
			jsonErrorFacts{ErrorCode: "permission_denied", ErrorReason: "scope_missing", ErrorScopes: []string{"deploy:write"}}},
		{"helper denial with the server's reason", helperDenied,
			jsonErrorFacts{ErrorCode: "permission_denied", ErrorReason: "scope_missing", ErrorScopes: []string{"deploy:write", "secret:read"}}},
		{"helper denial without a reason is still no credential", helperDeniedNoReason,
			jsonErrorFacts{ErrorCode: "unauthenticated", ErrorReason: ReasonNoControlPlaneCredential}},
		{"no credential at all", fmt.Errorf("%w for https://cp", cloud.ErrNoCredential),
			jsonErrorFacts{ErrorCode: "unauthenticated", ErrorReason: ReasonNoControlPlaneCredential}},
	}
	for _, tc := range cases {
		got := errorFactsOf(tc.err)
		if got.ErrorCode != tc.want.ErrorCode || got.ErrorReason != tc.want.ErrorReason ||
			strings.Join(got.ErrorScopes, ",") != strings.Join(tc.want.ErrorScopes, ",") {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// TestJSONEnvelope_ErrorFactsOnTheWire: the keys a consumer reads, and that
// success carries none of them.
func TestJSONEnvelope_ErrorFactsOnTheWire(t *testing.T) {
	t.Parallel()
	var env jsonEnvelope
	env.stamp(fmt.Errorf("%w for https://cp", cloud.ErrNoCredential))
	raw, _ := json.Marshal(env)
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	if doc["error_reason"] != ReasonNoControlPlaneCredential || doc["error_code"] != "unauthenticated" {
		t.Fatalf("document = %s", raw)
	}
	if doc["error"] != "no control-plane credential for https://cp" {
		t.Fatalf("error text changed: %s", raw)
	}

	env.stamp(nil)
	raw, _ = json.Marshal(env)
	for _, k := range []string{"error", "error_code", "error_reason", "error_scopes"} {
		if strings.Contains(string(raw), `"`+k+`"`) {
			t.Fatalf("success carries %q: %s", k, raw)
		}
	}

	r := newDeployReport("prod", true)
	r.finish(&cloud.Error{Code: "permission_denied", Reason: "scope_missing", Scopes: []string{"deploy:write"}, Message: "m"}, 0)
	raw, _ = json.Marshal(r.document())
	if !strings.Contains(string(raw), `"error_reason":"scope_missing"`) || !strings.Contains(string(raw), `"error_scopes":["deploy:write"]`) {
		t.Fatalf("deploy document = %s", raw)
	}
}
