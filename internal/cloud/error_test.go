package cloud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// connectErrorServer serves one Connect JSON error envelope.
func connectErrorServer(t *testing.T, status int, body any, reason string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reason != "" {
			w.Header().Set(ReasonHeader, reason)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func callAgainst(t *testing.T, srv *httptest.Server) error {
	t.Helper()
	ep, err := ResolveEndpoint("prod", &Declaration{Endpoint: srv.URL, TokenEnv: "ACME_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(ep, Credential{Token: "t", Source: SourceEnv, From: "ACME_TOKEN"})
	return client.Call(context.Background(), "controlplane.v1.DeployService/Promote", map[string]any{}, &struct{}{})
}

// TestCall_FailureIsATypedError is the headline of F0's "typed errors off the
// wire": a failed call must be classifiable WITHOUT reading its message. The
// previous behaviour flattened everything to a string and callers recovered
// the class with strings.Contains, which breaks the moment a server improves
// its prose — silently, because the branch just stops matching.
func TestCall_FailureIsATypedError(t *testing.T) {
	srv := connectErrorServer(t, http.StatusConflict, map[string]any{
		"code": "already_exists", "message": "that version has already been cut",
	}, "")

	err := callAgainst(t, srv)
	if err == nil {
		t.Fatal("expected an error")
	}
	var cerr *Error
	if !errors.As(err, &cerr) {
		t.Fatalf("a failed call must return *cloud.Error; got %T", err)
	}
	if !cerr.HasCode(CodeAlreadyExists) {
		t.Errorf("Code = %q, want %q", cerr.Code, CodeAlreadyExists)
	}
	if cerr.HTTPStatus != http.StatusConflict {
		t.Errorf("HTTPStatus = %d", cerr.HTTPStatus)
	}
	if cerr.Message != "that version has already been cut" {
		t.Errorf("Message = %q", cerr.Message)
	}
	if cerr.Procedure != "controlplane.v1.DeployService/Promote" {
		t.Errorf("Procedure = %q", cerr.Procedure)
	}
	// The classification must not depend on the message: a server that
	// rewords its prose keeps the same code.
	reworded := connectErrorServer(t, http.StatusConflict, map[string]any{
		"code": "already_exists", "message": "a completely different sentence",
	}, "")
	var second *Error
	if !errors.As(callAgainst(t, reworded), &second) || !second.HasCode(CodeAlreadyExists) {
		t.Error("the code must survive a reworded message")
	}
}

// TestCall_CarriesTheDomainReason: the Connect code is the CATEGORY, and
// three promote refusals share FailedPrecondition. The reason header is what
// says which one, so it must reach the caller as data.
func TestCall_CarriesTheDomainReason(t *testing.T) {
	for _, reason := range []string{"promotion_conflict", "rollout_in_flight", "environment_pinned"} {
		srv := connectErrorServer(t, http.StatusPreconditionFailed, map[string]any{
			"code": "failed_precondition", "message": "refused",
		}, reason)
		var cerr *Error
		if !errors.As(callAgainst(t, srv), &cerr) {
			t.Fatalf("%s: want *cloud.Error", reason)
		}
		if !cerr.HasCode(CodeFailedPrecondition) {
			t.Errorf("%s: Code = %q", reason, cerr.Code)
		}
		if !cerr.HasReason(reason) {
			t.Errorf("Reason = %q, want %q", cerr.Reason, reason)
		}
		// The reason belongs in the human message too: a refusal whose
		// cause is only machine-readable sends the operator to a
		// dashboard.
		if !strings.Contains(cerr.Error(), reason) {
			t.Errorf("message should name the reason; got:\n%s", cerr.Error())
		}
	}
}

// TestCall_PreservesErrorDetails: a promote refusal carries a
// DeployPromoteRefusal naming the promotion that actually landed — which is
// what lets a pipeline print "prod is on v1.9.1, promoted by alice" instead
// of "someone else moved prod". Flattening to a string threw that away.
//
// forge does not decode the Any (that would mean vendoring the control
// plane's protos). It reads Connect's `debug` member, which is the same
// message as protojson.
func TestCall_PreservesErrorDetails(t *testing.T) {
	refusal := map[string]any{
		"reason":                     "promotion_conflict",
		"expectedCurrentPromotionId": "pr_expected",
		"actualCurrent":              map[string]any{"id": "pr_actual", "releaseVersion": "v1.9.1"},
		"detail":                     "prod is on v1.9.1",
	}
	debug, err := json.Marshal(refusal)
	if err != nil {
		t.Fatal(err)
	}
	srv := connectErrorServer(t, http.StatusPreconditionFailed, map[string]any{
		"code": "failed_precondition", "message": "refused",
		"details": []map[string]any{{
			"type":  "controlplane.v1.DeployPromoteRefusal",
			"value": base64.RawStdEncoding.EncodeToString([]byte("proto-bytes")),
			"debug": json.RawMessage(debug),
		}},
	}, "promotion_conflict")

	var cerr *Error
	if !errors.As(callAgainst(t, srv), &cerr) {
		t.Fatal("want *cloud.Error")
	}
	if len(cerr.Details) != 1 {
		t.Fatalf("details = %+v", cerr.Details)
	}
	payload, ok := cerr.DetailJSON("controlplane.v1.DeployPromoteRefusal")
	if !ok {
		t.Fatal("the refusal detail must be readable by its type name")
	}
	var got struct {
		Reason        string `json:"reason"`
		ActualCurrent struct {
			ID             string `json:"id"`
			ReleaseVersion string `json:"releaseVersion"`
		} `json:"actualCurrent"`
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.Reason != "promotion_conflict" || got.ActualCurrent.ID != "pr_actual" ||
		got.ActualCurrent.ReleaseVersion != "v1.9.1" {
		t.Errorf("decoded refusal = %+v", got)
	}

	// A type URL and a bare full name name the same message.
	if _, ok := cerr.DetailJSON("type.googleapis.com/controlplane.v1.DeployPromoteRefusal"); !ok {
		t.Error("a type URL should resolve the same detail as a bare name")
	}
	if _, ok := cerr.DetailJSON("controlplane.v1.SomethingElse"); ok {
		t.Error("an absent detail must report not-found")
	}
}

// TestCall_DetailWithoutDebugIsNotReadable: base64 proto bytes alone are
// unreadable without the descriptor forge deliberately does not have, so
// handing them to a caller would hand over a payload it cannot use.
func TestCall_DetailWithoutDebugIsNotReadable(t *testing.T) {
	srv := connectErrorServer(t, http.StatusPreconditionFailed, map[string]any{
		"code": "failed_precondition",
		"details": []map[string]any{{
			"type":  "controlplane.v1.DeployPromoteRefusal",
			"value": base64.RawStdEncoding.EncodeToString([]byte("opaque")),
		}},
	}, "")
	var cerr *Error
	if !errors.As(callAgainst(t, srv), &cerr) {
		t.Fatal("want *cloud.Error")
	}
	if len(cerr.Details) != 1 {
		t.Fatalf("the detail itself should still be carried: %+v", cerr.Details)
	}
	if _, ok := cerr.DetailJSON("controlplane.v1.DeployPromoteRefusal"); ok {
		t.Error("a detail with no debug member must not report readable")
	}
}

// TestCall_NonConnectBodyStillClassifiesByStatus: a proxy's HTML error page
// is not a Connect envelope and has no code. The status is then the only
// classification available, and it must still be present rather than lost.
func TestCall_NonConnectBodyStillClassifiesByStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html><body>502 Bad Gateway</body></html>"))
	}))
	defer srv.Close()

	var cerr *Error
	if !errors.As(callAgainst(t, srv), &cerr) {
		t.Fatal("want *cloud.Error")
	}
	if cerr.HTTPStatus != http.StatusBadGateway {
		t.Errorf("HTTPStatus = %d", cerr.HTTPStatus)
	}
	if cerr.Code != "" {
		t.Errorf("a non-Connect body has no code; got %q", cerr.Code)
	}
	if !strings.Contains(cerr.Error(), "502") {
		t.Errorf("the body should reach the message for display; got:\n%s", cerr.Error())
	}
}

// TestCall_AuthFailureStaysActionableAndTyped: the auth message is the most
// load-bearing one forge prints, and making errors typed must not cost it.
// It is BOTH — a *cloud.Error carrying unauthenticated, and the same
// actionable text.
func TestCall_AuthFailureStaysActionableAndTyped(t *testing.T) {
	srv := connectErrorServer(t, http.StatusUnauthorized, map[string]any{
		"code": "unauthenticated", "message": "token expired",
	}, "")
	ep, err := ResolveEndpoint("prod", &Declaration{Endpoint: srv.URL, TokenEnv: "ACME_PROD_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(ep, Credential{Token: "stale", Source: SourceLogin, From: "/home/u/.forge/login.json"})
	callErr := client.Call(context.Background(), "controlplane.v1.DeployService/ListReleases", map[string]any{}, &struct{}{})

	var cerr *Error
	if !errors.As(callErr, &cerr) {
		t.Fatalf("want *cloud.Error; got %T", callErr)
	}
	if !cerr.HasCode(CodeUnauthenticated) {
		t.Errorf("Code = %q", cerr.Code)
	}
	msg := cerr.Error()
	for _, want := range []string{"forge login", "ACME_PROD_TOKEN", srv.URL, "/home/u/.forge/login.json", "token expired"} {
		if !strings.Contains(msg, want) {
			t.Errorf("auth error should contain %q; got:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "stale") {
		t.Errorf("the error must never echo the credential itself; got:\n%s", msg)
	}
}

// A 403 that NAMES a missing scope must not send the user round the circle
// "forge login" -> same token -> same 403. It says the token lacks the scope,
// that login picks it up only if the control plane grants it, and how an
// admin mints one.
func TestCall_MissingScopeHintIsNotCircular(t *testing.T) {
	srv := connectErrorServer(t, http.StatusForbidden, map[string]any{
		"code": "permission_denied", "message": "ListDomains rejected: token does not carry the domain:read scope",
	}, "")
	err := callAgainst(t, srv)
	if err == nil {
		t.Fatal("expected an error")
	}
	got := err.Error()
	for _, want := range []string{
		"lacks the domain:read scope",
		"re-run `forge login` to pick up the scope",
		"your role has no domain:read grant: ask an org admin",
		"forge cloud token create --env <env> --name <name> --scopes domain:read",
		"export ACME_TOKEN=<token>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("hint missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "(human — opens a browser)") {
		t.Errorf("a missing-scope 403 must not give the generic login advice:\n%s", got)
	}
}

func TestCall_PlainRejectionKeepsGenericLoginAdvice(t *testing.T) {
	srv := connectErrorServer(t, http.StatusUnauthorized, map[string]any{"code": "unauthenticated", "message": "expired"}, "")
	if got := callAgainst(t, srv).Error(); !strings.Contains(got, "forge login            (human — opens a browser)") {
		t.Errorf("generic advice lost:\n%s", got)
	}
}
