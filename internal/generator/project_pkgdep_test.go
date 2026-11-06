package generator

import (
	"testing"

	"github.com/reliant-labs/forge/internal/buildinfo"
)

// restoreBuildinfo puts the process-global version stamp back to its zero
// state. buildinfo has no Clear for Set, and these tests are not parallel
// because of it.
func restoreBuildinfo(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { buildinfo.Set("dev", "", "unknown") })
}

// TestResolveForgeVersion_ReleasePin: a released binary pins its own version,
// which is the whole point of one module — the version that generated the
// code and the version the code compiles against are the same number.
func TestResolveForgeVersion_ReleasePin(t *testing.T) {
	restoreBuildinfo(t)
	buildinfo.Set("v0.3.0", "", "")

	if got := resolveForgeVersion(); got != "v0.3.0" {
		t.Errorf("resolveForgeVersion = %q, want v0.3.0", got)
	}
}

// TestResolveForgeVersion_UntaggedCommitPinsPseudoVersion: `go install
// .../cmd/forge@main` records a pseudo-version, which IS proxy-resolvable and
// is what control-plane's commit-pinning mode depends on. It must be pinned
// verbatim, not rounded to a tag.
func TestResolveForgeVersion_UntaggedCommitPinsPseudoVersion(t *testing.T) {
	restoreBuildinfo(t)
	const pseudo = "v0.1.16-0.20260916085636-c01e07ec6ef2"
	buildinfo.Set(pseudo, "", "")

	if got := resolveForgeVersion(); got != pseudo {
		t.Errorf("resolveForgeVersion = %q, want the pseudo-version %q", got, pseudo)
	}
}

// TestResolveForgeVersion_UnreleasableBuildPinsPublishedFloor: a dirty or
// unstamped local build is on no proxy, so it cannot pin itself — but it must
// still pin SOMETHING. An empty require let `go mod tidy` (which ignores the
// dev go.work bridge) resolve forge/pkg/* imports to the retired
// github.com/reliant-labs/forge/pkg module, which generate then refused and
// env up's `tidy -diff` preflight reported as a stale graph forever. The
// newest published release this source descends from is a version the proxy
// serves and that provides every forge/pkg package; the go.work bridge still
// decides what compiles.
func TestResolveForgeVersion_UnreleasableBuildPinsPublishedFloor(t *testing.T) {
	restoreBuildinfo(t)

	floor := buildinfo.PublishedFloor()
	if floor == "" {
		t.Fatal("PublishedFloor is empty — the embedded VERSION file is missing or malformed")
	}
	for _, v := range []string{
		"v0.1.16-0.20260916085636-c01e07ec6ef2+dirty", // dirty tree
		"dev",         // no stamp at all
		"(devel)",     // plain `go build`
		"v0.1.15+dev", // an old VERSION-file floor spelling
	} {
		t.Run(v, func(t *testing.T) {
			buildinfo.Set(v, "", "")
			if got := resolveForgeVersion(); got != floor {
				t.Errorf("resolveForgeVersion = %q for build %q, want the published floor %q — "+
					"an empty require is what let `go mod tidy` pick the retired forge/pkg module", got, v, floor)
			}
		})
	}
}
