package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// A hosted frontend publishes NO domains, whatever else it declares.
//
// On hosted a custom hostname is an org-scoped control-plane resource bound
// to one environment (`forge domain add` + `forge domain bind`), so the
// published StaticSite document must not carry one: a domain in spec is a
// field the control plane does not read as a binding, which deploys green
// and never resolves.
//
// MUTATION VERIFIED RED: re-adding a `spec.Domains = ...` assignment to
// hostedStaticSpec fails this.
func TestHostedStaticSpec_CarriesNoDomains(t *testing.T) {
	spec := hostedStaticSpec(FrontendEntity{Name: "web", BasePath: "/admin"})
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "domains") {
		t.Errorf("a hosted frontend published domains in its spec: %s", raw)
	}
	const want = `{"basePath":"/admin","keepReleases":10}`
	if string(raw) != want {
		t.Errorf("published StaticSite spec:\n got %s\nwant %s", raw, want)
	}
}

// That `Frontend.domains` is not a field at all — a compile-time refusal
// naming the member, not a value forge drops — is pinned by
// kcl/tests/closedschema_frontend_domains.k, which runs the closed schema
// through the same render this package uses.

// A hosted frontend publishes its content-hashed asset directory, so the
// platform can cache it forever and keep previous releases' copies serveable
// for tabs opened before a deploy (OnFirebase's keep_asset_releases, on the
// hosted runtime).
//
// MUTATION VERIFIED RED: dropping `AssetDir: f.AssetDir` from
// hostedStaticSpec fails this.
func TestHostedStaticSpec_CarriesAssetDir(t *testing.T) {
	spec := hostedStaticSpec(FrontendEntity{Name: "web", AssetDir: "assets"})
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"assetDir":"assets","keepReleases":10}`
	if string(raw) != want {
		t.Errorf("published StaticSite spec:\n got %s\nwant %s", raw, want)
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("the published spec does not validate: %v", err)
	}
}
