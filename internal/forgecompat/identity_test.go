package forgecompat

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeModuleCache stands up forge's module-cache download directory with an
// .info file per tag, the way the go command records a fetched version.
func fakeModuleCache(t *testing.T, tags map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for tag, hash := range tags {
		body := `{"Version":"` + tag + `","Time":"2026-10-30T22:43:30Z","Origin":{"VCS":"git","Hash":"` + hash + `"}}`
		if err := os.WriteFile(filepath.Join(dir, tag+".info"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prev := forgeModuleCacheDir
	forgeModuleCacheDir = func() string { return dir }
	t.Cleanup(func() { forgeModuleCacheDir = prev })
}

func TestSameForge(t *testing.T) {
	fakeModuleCache(t, map[string]string{
		"v1.0.9":  "36d0220bfb607e391b6cb1925472609e244714e8",
		"v1.0.10": "aaaaaaaaaaaa7e391b6cb1925472609e244714e8",
	})
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical strings", "v1.0.9", "v1.0.9", true},
		// fca68889 / a68a8644: a clone that had fetched a newer tag derived
		// another base for the commit the go.mod pin names.
		{"two pseudo-versions of one commit", "v1.0.14-0.20261102231925-a88d3c30f302", "v1.0.15-0.20261102231925-a88d3c30f302", true},
		{"a clean local build of the pinned commit", "v1.0.14-0.20261102231925-a88d3c30f302", "v1.0.15-0.20261102231925-a88d3c30f302+dev", true},
		// 304e886b: go collapsed a commit pin to the tag on that commit.
		{"a tag and a pseudo-version of its commit", "v1.0.9", "v1.0.9-0.20261030224330-36d0220bfb60", true},
		{"a pseudo-version and the tag, either order", "v1.0.9-0.20261030224330-36d0220bfb60", "v1.0.9", true},
		{"different commits", "v1.0.14-0.20261102231925-a88d3c30f302", "v1.0.14-0.20261102231925-b88d3c30f302", false},
		{"a tag on another commit", "v1.0.10", "v1.0.9-0.20261030224330-36d0220bfb60", false},
		{"two tags", "v1.0.9", "v1.0.10", false},
		{"an unfetched tag proves nothing", "v1.0.8", "v1.0.8-0.20261030224330-36d0220bfb60", false},
		{"a dirty tree is not its commit", "v1.0.15-0.20261102231925-a88d3c30f302+dirty", "v1.0.14-0.20261102231925-a88d3c30f302", false},
		{"an identical dirty version proves no source", "v1.0.15-0.20261102231925-a88d3c30f302+dirty", "v1.0.15-0.20261102231925-a88d3c30f302+dirty", false},
		{"unknown", "", "v1.0.9", false},
		{"devel", "(devel)", "v1.0.9", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SameForge(tc.a, tc.b); got != tc.want {
				t.Fatalf("SameForge(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestVersionCommit(t *testing.T) {
	fakeModuleCache(t, map[string]string{"v1.0.9": "36D0220BFB607E391B6CB1925472609E244714E8"})
	cases := map[string]string{
		"v1.0.9":                                "36d0220bfb607e391b6cb1925472609e244714e8",
		"1.0.9":                                 "36d0220bfb607e391b6cb1925472609e244714e8",
		"v1.0.15-0.20261102231925-a88d3c30f302": "a88d3c30f302",
		"v0.0.0-20261102231925-a88d3c30f302":    "a88d3c30f302",
		"v1.0.15-0.20261102231925-a88d3c30f302+dev":   "a88d3c30f302",
		"v1.0.15-0.20261102231925-a88d3c30f302+dirty": "",
		"v1.0.9+dev":   "",
		"v1.0.8":       "",
		"(devel)":      "",
		"not-a-semver": "",
	}
	for v, want := range cases {
		if got := VersionCommit(v); got != want {
			t.Errorf("VersionCommit(%q) = %q, want %q", v, got, want)
		}
	}
}
