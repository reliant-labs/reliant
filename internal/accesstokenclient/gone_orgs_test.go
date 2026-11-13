package accesstokenclient

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The wire contract of ResolveGoneOrgs as control-plane serves it
// (internal/handlers/access_token_internal): the request names the orgs, and
// the answer lists the gone ones with when each went. Only an answer about an
// org asked for, with a time, counts: the caller destroys what it names.
func TestResolveGoneOrgs_ReportsTheGoneOrgsAskedAbout(t *testing.T) {
	srv := fakeCP(t, func(method string, body map[string]any) (int, string) {
		ids, _ := body["orgIds"].([]any)
		if method != "ResolveGoneOrgs" || len(ids) != 3 {
			t.Fatalf("got %s %v", method, body)
		}
		return 200, `{"gone":[
			{"orgId":"org-gone","goneAt":"2026-11-10T12:00:00Z"},
			{"orgId":"org-not-asked","goneAt":"2026-11-10T12:00:00Z"},
			{"orgId":"org-no-time"}
		]}`
	})
	defer srv.Close()
	c := New(Deps{BaseURL: srv.URL, Sign: sign})

	got, err := c.ResolveGoneOrgs(context.Background(), []string{"org-gone", "org-live", "org-no-time"})
	if err != nil {
		t.Fatal(err)
	}
	want := []GoneOrg{{OrgID: "org-gone", At: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	// Nothing gone: protojson omits the empty list.
	none := fakeCP(t, func(string, map[string]any) (int, string) { return 200, `{}` })
	defer none.Close()
	got, err = New(Deps{BaseURL: none.URL, Sign: sign}).ResolveGoneOrgs(context.Background(), []string{"org-live"})
	if err != nil || len(got) != 0 {
		t.Fatalf("none gone: %+v, %v", got, err)
	}
}

// An older control plane is ErrUnsupported (purge nothing), an outage an error
// that is not; an over-long list is refused before it is sent.
func TestResolveGoneOrgs_UnsupportedOutageAndLimit(t *testing.T) {
	old := fakeCP(t, func(string, map[string]any) (int, string) {
		return 404, `404 page not found`
	})
	defer old.Close()
	if _, err := New(Deps{BaseURL: old.URL, Sign: sign}).ResolveGoneOrgs(context.Background(), []string{"o"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("an older control plane: err=%v, want ErrUnsupported", err)
	}
	down := fakeCP(t, func(string, map[string]any) (int, string) {
		return 503, `{"code":"unavailable","message":"db down"}`
	})
	defer down.Close()
	if _, err := New(Deps{BaseURL: down.URL, Sign: sign}).ResolveGoneOrgs(context.Background(), []string{"o"}); err == nil || errors.Is(err, ErrUnsupported) {
		t.Fatalf("an outage: err=%v, want an error that is not ErrUnsupported", err)
	}
	ids := make([]string, MaxResolveGoneOrgs+1)
	if _, err := New(Deps{BaseURL: down.URL, Sign: sign}).ResolveGoneOrgs(context.Background(), ids); err == nil {
		t.Fatal("an over-long list must be refused")
	}
}
