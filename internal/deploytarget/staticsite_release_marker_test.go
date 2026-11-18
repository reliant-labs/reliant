package deploytarget

import (
	"os"
	"path/filepath"
	"testing"
)

// forge#2231: the marker env status reads back from a live hosted site.
func TestStaticSiteReleaseMarkerRoundTrips(t *testing.T) {
	tree := t.TempDir()
	if err := os.WriteFile(filepath.Join(tree, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteStaticSiteReleaseMarker(tree, StaticSiteReleaseMarker{Project: "landing", Frontend: "web", Release: "20261117.101010-abc"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(tree, ".well-known", "forge-release.json"))
	if err != nil {
		t.Fatalf("marker not at %s: %v", StaticSiteReleaseMarkerPath, err)
	}
	m, err := ParseStaticSiteReleaseMarker(data)
	if err != nil {
		t.Fatal(err)
	}
	want := StaticSiteReleaseMarker{Schema: StaticSiteReleaseMarkerSchema, Project: "landing", Frontend: "web", Release: "20261117.101010-abc"}
	if m != want {
		t.Errorf("marker = %+v, want %+v", m, want)
	}
	// It is packed with the site: the release carries it.
	layer, err := PackStaticSiteTree(tree)
	if err != nil || len(layer) == 0 {
		t.Fatalf("pack: %v", err)
	}
}

func TestStaticSiteReleaseMarkerRefusesWhatIsNotOne(t *testing.T) {
	for name, doc := range map[string]string{
		"an HTML page in its place": "<!doctype html><html></html>",
		"another schema":            `{"schema":"other/v1","frontend":"web","release":"r1"}`,
		"no release":                `{"schema":"forge.static-site-release/v1","frontend":"web","release":""}`,
	} {
		if _, err := ParseStaticSiteReleaseMarker([]byte(doc)); err == nil {
			t.Errorf("%s: parsed, want an error", name)
		}
	}
	if err := WriteStaticSiteReleaseMarker(t.TempDir(), StaticSiteReleaseMarker{Frontend: "web"}); err == nil {
		t.Error("a marker without a release must not be written")
	}
}
