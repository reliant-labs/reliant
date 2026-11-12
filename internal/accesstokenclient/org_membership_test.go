package accesstokenclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The wire contract of ResolveOrgMembership as control-plane serves it
// (control-plane#2425, internal/handlers/access_token_internal): the request
// names the user and, for a membership question, the org; the answer is the
// org it is about and whether the user is a live member of it.
func TestResolveOrgMembership_NamedAndDefault(t *testing.T) {
	srv := fakeCP(t, func(method string, body map[string]any) (int, string) {
		if method != "ResolveOrgMembership" || body["userId"] != "user-1" {
			t.Fatalf("got %s %v", method, body)
		}
		switch org, named := body["orgId"]; {
		case !named:
			return 200, `{"orgId":"org-home","member":true}`
		case org == "org-acme":
			return 200, `{"orgId":"org-acme","member":true}`
		default:
			t.Fatalf("unexpected orgId %v", org)
			return 0, ""
		}
	})
	defer srv.Close()
	c := New(Deps{BaseURL: srv.URL, Sign: sign})

	got, err := c.ResolveOrgMembership(context.Background(), "user-1", "org-acme")
	if err != nil || got != (OrgMembership{OrgID: "org-acme", Member: true}) {
		t.Fatalf("named org: %+v, %v", got, err)
	}
	got, err = c.ResolveOrgMembership(context.Background(), "user-1", "")
	if err != nil || got != (OrgMembership{OrgID: "org-home", Member: true}) {
		t.Fatalf("default org (no orgId sent): %+v, %v", got, err)
	}
}

// A negative answer is an answer, not an error: protojson omits a false
// member, and a user with no org omits the org too.
func TestResolveOrgMembership_NegativeAnswer(t *testing.T) {
	srv := fakeCP(t, func(_ string, body map[string]any) (int, string) {
		if body["orgId"] == "org-globex" {
			return 200, `{"orgId":"org-globex"}`
		}
		return 200, `{}`
	})
	defer srv.Close()
	c := New(Deps{BaseURL: srv.URL, Sign: sign})

	got, err := c.ResolveOrgMembership(context.Background(), "user-1", "org-globex")
	if err != nil || got != (OrgMembership{OrgID: "org-globex", Member: false}) {
		t.Fatalf("not a member: %+v, %v", got, err)
	}
	got, err = c.ResolveOrgMembership(context.Background(), "user-1", "")
	if err != nil || got != (OrgMembership{}) {
		t.Fatalf("no default org: %+v, %v", got, err)
	}
}

// An outage is an error and never ErrUnsupported (which a caller reads as
// "this deployment has no orgs") nor a negative answer.
func TestResolveOrgMembership_TransportErrorsAreErrors(t *testing.T) {
	srv := fakeCP(t, func(string, map[string]any) (int, string) {
		return 503, `{"code":"unavailable","message":"db down"}`
	})
	defer srv.Close()
	_, err := New(Deps{BaseURL: srv.URL, Sign: sign}).ResolveOrgMembership(context.Background(), "user-1", "org-acme")
	if err == nil || errors.Is(err, ErrUnsupported) {
		t.Fatalf("a control-plane outage: err=%v, want an error that is not ErrUnsupported", err)
	}

	down := httptest.NewServer(http.NotFoundHandler())
	url := down.URL
	down.Close() // nothing listens: a connection error
	_, err = New(Deps{BaseURL: url, Sign: sign}).ResolveOrgMembership(context.Background(), "user-1", "org-acme")
	if err == nil || errors.Is(err, ErrUnsupported) {
		t.Fatalf("an unreachable control plane: err=%v, want an error that is not ErrUnsupported", err)
	}
}

// A control plane that predates the rpc answers ErrUnsupported, both ways an
// older server says so: Connect's unimplemented, and the plain HTTP 404
// connect-go's mux answers for a procedure it does not know.
func TestResolveOrgMembership_OlderControlPlaneIsUnsupported(t *testing.T) {
	for name, answer := range map[string]struct {
		status int
		body   string
	}{
		"connect unimplemented": {501, `{"code":"unimplemented","message":"no such method"}`},
		"plain HTTP 404":        {404, `404 page not found`},
	} {
		t.Run(name, func(t *testing.T) {
			srv := fakeCP(t, func(string, map[string]any) (int, string) { return answer.status, answer.body })
			defer srv.Close()
			_, err := New(Deps{BaseURL: srv.URL, Sign: sign}).ResolveOrgMembership(context.Background(), "user-1", "org-acme")
			if !errors.Is(err, ErrUnsupported) {
				t.Fatalf("err=%v, want ErrUnsupported", err)
			}
		})
	}
}

// role (control-plane org-sharing S0) rides on a member's answer. A control
// plane that predates it omits it, which decodes as "" (unknown), and a role
// on a negative answer is never carried.
func TestResolveOrgMembership_Role(t *testing.T) {
	srv := fakeCP(t, func(_ string, body map[string]any) (int, string) {
		switch body["orgId"] {
		case "org-admin":
			return 200, `{"orgId":"org-admin","member":true,"role":"admin"}`
		case "org-old":
			return 200, `{"orgId":"org-old","member":true}`
		case "org-odd":
			return 200, `{"orgId":"org-odd","role":"owner"}`
		default:
			return 200, `{"orgId":"org-home","member":true,"role":"owner"}`
		}
	})
	defer srv.Close()
	c := New(Deps{BaseURL: srv.URL, Sign: sign})
	for org, want := range map[string]OrgMembership{
		"org-admin": {OrgID: "org-admin", Member: true, Role: "admin"},
		"org-old":   {OrgID: "org-old", Member: true},
		"org-odd":   {OrgID: "org-odd"},
		"":          {OrgID: "org-home", Member: true, Role: "owner"},
	} {
		got, err := c.ResolveOrgMembership(context.Background(), "user-1", org)
		if err != nil || got != want {
			t.Errorf("org %q: %+v, %v; want %+v", org, got, err, want)
		}
	}
}
