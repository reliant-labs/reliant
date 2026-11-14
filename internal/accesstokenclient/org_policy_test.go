package accesstokenclient

import (
	"context"
	"errors"
	"testing"
)

// The wire contract of ResolveOrgPolicy as control-plane serves it
// (internal/handlers/access_token_internal, wire_json_test.go there): the
// request names the org; the answer is the version (an int64, so a protojson
// string) and the OrgPolicy document, decoded with the vendored type.
func TestResolveOrgPolicy_DecodesTheDocument(t *testing.T) {
	t.Parallel()
	srv := fakeCP(t, func(method string, body map[string]any) (int, string) {
		if method != "ResolveOrgPolicy" || body["orgId"] != "org-a" {
			t.Fatalf("got %s %v", method, body)
		}
		return 200, `{"version":"12","policy":{
			"providers":{"mode":"ORG_POLICY_MODE_ENFORCE","allowedSources":["PROVIDER_SOURCE_ORG_ACCOUNTS","PROVIDER_SOURCE_RELIANT"]},
			"automations":{"allowedSources":["PROVIDER_SOURCE_RELIANT"],"mode":"ORG_POLICY_MODE_AUDIT"},
			"futureSection":{"x":1}
		}}`
	})
	defer srv.Close()
	got, err := New(Deps{BaseURL: srv.URL, Sign: sign}).ResolveOrgPolicy(context.Background(), "org-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 12 || len(got.Policy.GetProviders().GetAllowedSources()) != 2 ||
		got.Policy.GetAutomations().GetMode().String() != "ORG_POLICY_MODE_AUDIT" {
		t.Fatalf("got v%d %v", got.Version, got.Policy)
	}

	// No policy: protojson omits the zero version and the empty document.
	none := fakeCP(t, func(string, map[string]any) (int, string) { return 200, `{"policy":{}}` })
	defer none.Close()
	got, err = New(Deps{BaseURL: none.URL, Sign: sign}).ResolveOrgPolicy(context.Background(), "org-b")
	if err != nil || got.Version != 0 || got.Policy == nil {
		t.Fatalf("no policy: %+v, %v", got, err)
	}
	empty := fakeCP(t, func(string, map[string]any) (int, string) { return 200, `{}` })
	defer empty.Close()
	got, err = New(Deps{BaseURL: empty.URL, Sign: sign}).ResolveOrgPolicy(context.Background(), "org-b")
	if err != nil || got.Version != 0 || got.Policy == nil {
		t.Fatalf("empty answer: %+v, %v", got, err)
	}
}

// An older control plane is ErrUnsupported (no policy); an outage, and an
// answer that is not one, are errors that are not.
func TestResolveOrgPolicy_UnsupportedOutageAndGarbage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		status      int
		body        string
		unsupported bool
	}{
		{"old control plane", 404, `404 page not found`, true},
		{"unimplemented", 501, `{"code":"unimplemented","message":"no"}`, true},
		{"outage", 503, `{"code":"unavailable","message":"db down"}`, false},
		{"bad version", 200, `{"version":"x"}`, false},
		{"bad document", 200, `{"version":"1","policy":{"models":{"mode":7.5}}}`, false},
	} {
		srv := fakeCP(t, func(string, map[string]any) (int, string) { return tc.status, tc.body })
		_, err := New(Deps{BaseURL: srv.URL, Sign: sign}).ResolveOrgPolicy(context.Background(), "o")
		srv.Close()
		if err == nil || errors.Is(err, ErrUnsupported) != tc.unsupported {
			t.Errorf("%s: err = %v, want unsupported=%v", tc.name, err, tc.unsupported)
		}
	}
}
