package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/gitsource"
	"github.com/reliant-labs/forge/internal/sbom"
	"github.com/reliant-labs/forge/internal/vulnscan"
	"github.com/reliant-labs/forge/pkg/release"
)

// Every artifact of a cut is accounted for: an SBOM, an unavailable ref with
// why, or a not-applicable ref with what it is. The summary counts the
// applicable ones and names the rest by class. An image carried unchanged
// from an earlier release is described like any other (from its registry),
// and a re-cut of it is a cache hit that still records the SBOM.
func TestGenerateReleaseSBOMs_EveryArtifactIsRecordedOrNotApplicable(t *testing.T) {
	dir := newLedgerTestProject(t, "sbomapplicable")
	fake := &cutSyft{fail: map[string]error{"docker": errors.New("No such image")}}
	useFakeSyft(t, fake, t.TempDir())
	carried := sbomTestImage(sha("o"))
	carried.CarriedFrom = "v0.9.0"
	cut := func(version string) (map[string]release.Artifact, string) {
		artifacts := map[string]release.Artifact{
			"ghcr.io/acme/api":  sbomTestImage(sha("a")),
			"ghcr.io/acme/otel": carried,
			"web.sourcemaps":    sbomTestImage(sha("s")),
			"docs.sourcemaps":   sbomTestImage(sha("d")),
			"web": {Kind: release.KindGit, Mode: release.ModeSource,
				Source: &release.Source{Repo: "github.com/acme/app", Ref: "main", Commit: strings.Repeat("c", 40)}},
		}
		var warn bytes.Buffer
		generateReleaseSBOMs(context.Background(), sbomCut{ProjectDir: dir, Env: "prod", Version: version,
			Built: map[string]bool{"ghcr.io/acme/api": true, "ghcr.io/acme/otel": true}}, artifacts, &warn)
		return artifacts, warn.String()
	}

	artifacts, out := cut("v1.0.0")
	for name, reason := range map[string]string{
		"web.sourcemaps":  release.SBOMNotApplicableSourcemaps,
		"docs.sourcemaps": release.SBOMNotApplicableSourcemaps,
		"web":             release.SBOMNotApplicableGitSource,
	} {
		ref := artifacts[name].SBOM
		if ref == nil || ref.Status != release.SBOMStatusNotApplicable || ref.Reason != reason || ref.HasSBOM() {
			t.Errorf("%s: SBOM %+v, want not applicable (%q)", name, ref, reason)
		}
	}
	for _, name := range []string{"ghcr.io/acme/api", "ghcr.io/acme/otel"} {
		if ref := artifacts[name].SBOM; !ref.HasSBOM() || ref.Source != release.SBOMSourceRegistry {
			t.Errorf("%s: SBOM %+v, want one read from the registry", name, ref)
		}
	}
	for _, src := range fake.calls() {
		if strings.HasPrefix(src, "docker:ghcr.io/acme/otel") {
			t.Errorf("a carried image was read from the docker daemon (%s); this pipeline did not build it", src)
		}
	}
	if err := (release.Release{Version: "v1.0.0", Artifacts: artifacts}).Validate(); err != nil {
		t.Errorf("the recorded artifacts no longer validate: %v", err)
	}
	want := "[build] SBOMs: 2 of 2 applicable artifacts recorded (0 from cache); 3 not applicable (2 source maps, 1 git source pin)\n"
	if !strings.Contains(out, want) {
		t.Errorf("summary:\n%s\nwant %q", out, want)
	}
	if strings.Contains(out, "warning") {
		t.Errorf("a not-applicable artifact was warned about:\n%s", out)
	}

	// The re-cut: both images from the cache, recorded all the same.
	again, out := cut("v1.0.1")
	for _, name := range []string{"ghcr.io/acme/api", "ghcr.io/acme/otel"} {
		if again[name].SBOM.Digest != artifacts[name].SBOM.Digest {
			t.Errorf("%s: re-cut SBOM %+v, want the cached %s", name, again[name].SBOM, artifacts[name].SBOM.Digest)
		}
	}
	if !strings.Contains(out, "SBOMs: 2 of 2 applicable artifacts recorded (2 from cache); 3 not applicable") {
		t.Errorf("re-cut summary: %s", out)
	}
}

// An unavailable SBOM is counted against the applicable artifacts and warned
// with its reason, beside the not-applicable ones.
func TestPrintSBOMCoverage_Unavailable(t *testing.T) {
	t.Parallel()
	gitPin := release.Artifact{Kind: release.KindGit, Mode: release.ModeSource, Source: &release.Source{Repo: "github.com/acme/app", Ref: "main"}}
	na, _ := release.NotApplicableSBOMRef("site", gitPin)
	gitPin.SBOM = &na
	broken := sbomTestImage(sha("b"))
	broken.SBOM = &release.SBOMRef{Status: release.SBOMStatusUnavailable, Reason: "syft is unavailable"}
	ok := sbomTestImage(sha("a"))
	ok.SBOM = &release.SBOMRef{Status: release.SBOMStatusOK, Digest: sha("sbom")}
	var out bytes.Buffer
	printSBOMCoverage(&out, "v2.0.0", map[string]release.Artifact{"api": ok, "broken": broken, "site": gitPin}, nil)
	for _, want := range []string{
		"[build] warning: release v2.0.0 recorded without an SBOM for broken: syft is unavailable\n",
		"[build] SBOMs: 1 of 2 applicable artifacts recorded (0 from cache), 1 unavailable; 1 not applicable (1 git source pin)\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output:\n%s\nwant %q", out.String(), want)
		}
	}
}

func TestNotApplicableSummary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		reasons []string
		want    string
	}{
		{nil, ""},
		{[]string{release.SBOMNotApplicableGitSource}, "1 not applicable (1 git source pin)"},
		{[]string{release.SBOMNotApplicableGitSource, release.SBOMNotApplicableSourcemaps, "a future kind", release.SBOMNotApplicableSourcemaps, release.SBOMNotApplicableGitSource},
			"5 not applicable (2 source maps, 2 git source pins, 1 other)"},
	} {
		if got := notApplicableSummary(tc.reasons); got != tc.want {
			t.Errorf("notApplicableSummary(%q) = %q, want %q", tc.reasons, got, tc.want)
		}
	}
}

// stubPins answers a git-sourced frontend's pin for the SBOM step.
type stubPins struct {
	res   gitsource.Resolution
	err   error
	calls int
}

func (s *stubPins) Resolve(context.Context, gitsource.Source) (gitsource.Resolution, error) {
	s.calls++
	return s.res, s.err
}

// A hosted site built from a forge.GitSource has no directory in this
// checkout. Its SBOM is of the lockfile in the SOURCE checkout at the pinned
// commit: the directory the build resolved, or the pin resolved at the cut
// (a --no-build cut, or one whose build --target skipped the sites), never a
// lockfile of this repository. The lockfile walk climbs to the source
// checkout's root, where a workspace keeps it.
func TestPlanReleaseSBOMTargets_GitSourcedSiteReadsTheSourceLockfile(t *testing.T) {
	t.Parallel()
	write := func(p, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	project := t.TempDir()
	write(filepath.Join(project, "package-lock.json"), `{"lockfileVersion":3}`) // this repository's: never the site's
	checkout := filepath.Join(t.TempDir(), "github.com-acme-app@main")
	siteDir := filepath.Join(checkout, "frontends", "web")
	write(filepath.Join(siteDir, "package.json"), `{"name":"web","private":true}`)
	write(filepath.Join(checkout, "pnpm-lock.yaml"), "lockfileVersion: '9.0'\n")
	commit := strings.Repeat("c", 40)

	fe := FrontendEntity{Name: "web", Image: "registry.example.com/acme/web", Runtime: FrontendRuntime{Type: FrontendRuntimeHosted},
		Source: &config.GitSource{Repo: "github.com/acme/app", Ref: "main", Subdir: "frontends/web"}}
	plan := func(fe FrontendEntity, pins *stubPins) ([]plannedSBOM, map[string]release.SBOMRef, string) {
		e := &KCLEntities{Frontends: []FrontendEntity{fe}}
		key := hostedStaticDestinationForDecl(e, fe.Image)
		artifacts := map[string]release.Artifact{
			key:   sbomTestImage(sha("w")),
			"web": {Kind: release.KindGit, Mode: release.ModeSource, Source: &release.Source{Repo: "github.com/acme/app", Ref: "main", Subdir: "frontends/web", Commit: commit}},
		}
		targets, refused := planReleaseSBOMTargets(context.Background(), sbomCut{ProjectDir: project, Entities: e, Sources: pins}, artifacts)
		return targets, refused, key
	}
	wantLock := filepath.Join(checkout, "pnpm-lock.yaml")

	t.Run("resolved at the cut", func(t *testing.T) {
		pins := &stubPins{res: gitsource.Resolution{Dir: siteDir, Commit: commit}}
		targets, refused, key := plan(fe, pins)
		if len(targets) != 1 || targets[0].name != key || targets[0].target.Kind != sbom.KindLockfile || targets[0].target.Ref != wantLock {
			t.Fatalf("targets %+v (refused %+v), want the source checkout's %s", targets, refused, wantLock)
		}
		if pins.calls != 1 {
			t.Errorf("resolved the pin %d time(s), want 1", pins.calls)
		}
	})
	t.Run("resolved by the build", func(t *testing.T) {
		built := fe
		built.Path, built.SourceCommit = siteDir, commit
		pins := &stubPins{err: errors.New("the build already resolved it")}
		targets, refused, _ := plan(built, pins)
		if len(targets) != 1 || targets[0].target.Ref != wantLock || pins.calls != 0 {
			t.Fatalf("targets %+v (refused %+v, %d resolve(s)), want %s from the build's directory", targets, refused, pins.calls, wantLock)
		}
	})
	t.Run("another commit than the release pins", func(t *testing.T) {
		_, refused, key := plan(fe, &stubPins{res: gitsource.Resolution{Dir: siteDir, Commit: strings.Repeat("d", 40)}})
		if r := refused[key]; r.Status != release.SBOMStatusUnavailable || !strings.Contains(r.Reason, "not the commit") {
			t.Errorf("refused %+v, want unavailable naming the commit mismatch", r)
		}
	})
	t.Run("pin does not resolve", func(t *testing.T) {
		_, refused, key := plan(fe, &stubPins{err: errors.New("fetch github.com/acme/app@main: no route to host")})
		if r := refused[key]; r.Status != release.SBOMStatusUnavailable || !strings.Contains(r.Reason, "could not be resolved") || !strings.Contains(r.Reason, "no route to host") {
			t.Errorf("refused %+v, want unavailable with the resolver's error", r)
		}
	})
	t.Run("no lockfile in the source", func(t *testing.T) {
		bare := filepath.Join(t.TempDir(), "github.com-acme-app@main", "frontends", "web")
		write(filepath.Join(bare, "package.json"), `{"name":"web"}`)
		targets, refused, key := plan(fe, &stubPins{res: gitsource.Resolution{Dir: bare, Commit: commit}})
		if len(targets) != 0 {
			t.Fatalf("targets %+v: read a lockfile outside the source checkout", targets)
		}
		if r := refused[key]; r.Status != release.SBOMStatusUnavailable || !strings.Contains(r.Reason, "its source github.com/acme/app") {
			t.Errorf("refused %+v, want unavailable naming the source", r)
		}
	})
}

func TestSourceCheckoutRoot(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator)+"cache", "repo@main")
	for _, tc := range []struct{ dir, subdir, want string }{
		{filepath.Join(root, "frontends", "web"), "frontends/web", root},
		{filepath.Join(root, "frontends", "web"), "/frontends/web/", root},
		{root, "", root},
		{filepath.Join(root, "other"), "frontends/web", filepath.Join(root, "other")}, // an override elsewhere: its own dir
	} {
		if got := sourceCheckoutRoot(tc.dir, tc.subdir); got != tc.want {
			t.Errorf("sourceCheckoutRoot(%q, %q) = %q, want %q", tc.dir, tc.subdir, got, tc.want)
		}
	}
}

// `forge vuln list` says the same coverage as the cut: N of N applicable,
// and what was not applicable.
func TestWriteVulnDocument_NotApplicable(t *testing.T) {
	t.Parallel()
	rel := &release.Release{Version: "v1.0.0", Artifacts: map[string]release.Artifact{
		"api":            sbomTestImage(sha("a")),
		"web.sourcemaps": sbomTestImage(sha("s")),
		"web":            {Kind: release.KindGit, Mode: release.ModeSource, Source: &release.Source{Repo: "github.com/acme/app", Ref: "main"}},
	}}
	doc := vulnscan.BuildDocument(vulnscan.DocumentInput{Env: "prod", Release: "v1.0.0",
		Artifacts: []vulnscan.ArtifactScan{{Artifact: "api", Reason: "no SBOM"}}, NotApplicable: vulnscan.NotApplicableOf(rel)})
	var out bytes.Buffer
	writeVulnDocument(&out, doc, time.Now(), "")
	if want := "0 of 1 applicable artifacts scanned; 2 not applicable (1 source maps, 1 git source pin)"; !strings.Contains(out.String(), want) {
		t.Errorf("output:\n%s\nwant %q", out.String(), want)
	}
	if strings.Contains(out.String(), "not scanned: web") {
		t.Errorf("a not-applicable artifact reads as not scanned:\n%s", out.String())
	}
}
