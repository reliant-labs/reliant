package accesstokenclient

import (
	"context"
	"testing"
)

// org_scopes on the wire (control-plane#2759): a member's answer carries the
// org-wide grants; a negative carries none, whatever the server sent; an
// older server sends none, which reads as "holds none" (fail closed).
func TestResolveOrgMembership_OrgScopesDecode(t *testing.T) {
	srv := fakeCP(t, func(_ string, body map[string]any) (int, string) {
		switch body["orgId"] {
		case "org-admin":
			return 200, `{"orgId":"org-admin","member":true,"role":"owner","orgScopes":["billing:read","deploy:read","billing:read",""]}`
		case "org-old":
			return 200, `{"orgId":"org-old","member":true,"role":"member"}`
		default:
			return 200, `{"orgId":"org-left","orgScopes":["billing:read"]}`
		}
	})
	defer srv.Close()
	c := New(Deps{BaseURL: srv.URL, Sign: sign})
	ctx := context.Background()

	got, err := c.ResolveOrgMembership(ctx, "u", "org-admin")
	if err != nil || got.OrgScopes != NewOrgScopeSet("billing:read", "deploy:read") || !got.OrgScopes.Has("billing:read") {
		t.Fatalf("member: %+v, %v; want billing:read and deploy:read, once each", got, err)
	}
	if l := got.OrgScopes.List(); len(l) != 2 || l[0] != "billing:read" || l[1] != "deploy:read" {
		t.Fatalf("List() = %v; want control-plane's order", l)
	}
	got, err = c.ResolveOrgMembership(ctx, "u", "org-old")
	if err != nil || got.OrgScopes != "" || got.OrgScopes.Has("billing:read") {
		t.Fatalf("an older server: %+v, %v; want no grant", got, err)
	}
	got, err = c.ResolveOrgMembership(ctx, "u", "org-left")
	if err != nil || got != (OrgMembership{OrgID: "org-left"}) {
		t.Fatalf("a negative: %+v, %v; want nothing carried", got, err)
	}
}
