package accesstokenclient

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
)

// webScopeCatalog is the web token UI's mirror of forge's scope catalog
// (forge pkg/accesstoken/catalog.go). The API-tokens page previews what a
// bundle resolves to BEFORE the token exists, so it cannot ask the server; it
// expands the bundle against this copy instead.
var webScopeCatalog = filepath.Join("..", "..", "frontends", "web", "src", "services", "controlPlane", "scopeCatalog.ts")

var (
	webCatalogRow     = regexp.MustCompile(`^\{ scope: "([a-z:]+)", class: "([a-z]+)", bundles: \[([^\]]*)\] \},$`)
	webBundlesLine    = regexp.MustCompile(`^export const FORGE_BUNDLES = \[([^\]]*)\] as const;$`)
	webQuotedListItem = regexp.MustCompile(`"([a-z]+)"`)
)

// The copy must be forge's catalog exactly, at the forge this module pins:
// every scope in AllScopes order, its class, and the bundles that grant it
// (BundlesOf, which never names a bundle for a sensitive scope). A forge bump
// that adds a scope, reclassifies one or moves one between bundles fails here
// until the web copy is updated, so the preview never shows a person a set the
// server would not mint.
func TestWebScopeCatalogMirrorsForge(t *testing.T) {
	raw, err := os.ReadFile(webScopeCatalog)
	if err != nil {
		t.Fatalf("reading the web scope catalog: %v", err)
	}
	src := string(raw)

	var want strings.Builder
	for _, s := range fat.AllScopes {
		quoted := make([]string, 0, 3)
		for _, b := range fat.BundlesOf(s) {
			quoted = append(quoted, fmt.Sprintf("%q", b))
		}
		fmt.Fprintf(&want, "  { scope: %q, class: %q, bundles: [%s] },\n", s, fat.ClassOf(s), strings.Join(quoted, ", "))
	}

	begin := strings.Index(src, "// forge-scope-catalog:begin")
	end := strings.Index(src, "// forge-scope-catalog:end")
	if begin < 0 || end < begin {
		t.Fatalf("%s has no forge-scope-catalog:begin / :end markers", webScopeCatalog)
	}
	var got strings.Builder
	for _, line := range strings.Split(src[begin:end], "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") || strings.HasPrefix(line, "export const FORGE_SCOPE_CATALOG") || line == "];" {
			continue
		}
		m := webCatalogRow.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("%s: a catalog line this test cannot read (keep one row per line):\n  %s", webScopeCatalog, line)
		}
		fmt.Fprintf(&got, "  { scope: %q, class: %q, bundles: [%s] },\n", m[1], m[2], m[3])
	}
	if got.String() != want.String() {
		t.Errorf("%s is not forge's catalog. Replace the rows between the markers with:\n%s\ngot:\n%s", webScopeCatalog, want.String(), got.String())
	}

	var bundles []string
	for _, line := range strings.Split(src, "\n") {
		if m := webBundlesLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			for _, q := range webQuotedListItem.FindAllStringSubmatch(m[1], -1) {
				bundles = append(bundles, q[1])
			}
		}
	}
	wantBundles := make([]string, 0, len(fat.AllBundles))
	for _, b := range fat.AllBundles {
		wantBundles = append(wantBundles, string(b))
	}
	if strings.Join(bundles, ",") != strings.Join(wantBundles, ",") {
		t.Errorf("%s FORGE_BUNDLES = %v, want forge's AllBundles %v", webScopeCatalog, bundles, wantBundles)
	}
}
