package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cluster"

	"github.com/reliant-labs/forge/pkg/release"
)

// `forge env status` tests.
//
// These assert on the PARSED report rather than on substrings of stdout, for
// the same reason env_verify_json_test.go does: the thing under test is a
// machine contract that a UI reads with jq, and a test matching the word
// "drift" somewhere in the output would pass for output no parser could read.
//
// Everything runs against the injected seams — a stub binding store, a stub
// target resolver, a stub cluster lister — so no cluster and no rendered KCL
// is needed. The one thing staged on disk is the release ledger directory,
// because reading it IS the behaviour under test.

// dg turns a short fixture label ("aaa3") into a canonical sha256 digest, so
// fixtures stay legible while every release and promotion still satisfies
// release.Validate — which now refuses a non-canonical digest.
func dg(label string) string {
	hex := ""
	for _, c := range label {
		hex += fmt.Sprintf("%02x", c)
	}
	return "sha256:" + (hex + strings.Repeat("0", 64))[:64]
}

// topologyBindings wires opts to STATE each env's ledger: bindings from the
// given store, releases from the project's files. An in-memory store makes
// the "ledger read failed" path reachable, which a file backend makes awkward.
func topologyBindings(t *testing.T, opts envTopologyOptions, store bindingStore) envTopologyOptions {
	t.Helper()
	releases := testReleases(t, opts.ProjectDir)
	opts.Ledgers = func(context.Context, string) (envLedger, error) {
		return envLedger{Bindings: store, Releases: releases}, nil
	}
	return opts
}

// stageReleaseLedger stages one release ledger on disk.
func stageReleaseLedger(t *testing.T, dir, version, createdAt string, dirty bool, images map[string]string) {
	t.Helper()
	artifacts := map[string]release.Artifact{}
	for name, digest := range images {
		artifacts[name] = release.Artifact{
			Kind:    release.KindOCI,
			Mode:    release.ModeShared,
			Digests: map[string]string{release.SharedVariant: digest},
		}
	}
	rel := release.Release{
		Version:   version,
		Git:       release.Git{Commit: "commit-" + version, Dirty: dirty},
		CreatedAt: mustTime(t, createdAt),
		Artifacts: artifacts,
	}
	if err := testCutRelease(t, dir, rel); err != nil {
		t.Fatalf("write release %s: %v", version, err)
	}
}

// declareEnvDir stages deploy/kcl/<env>/main.k, which is what makes an env
// "declared in this checkout".
func declareEnvDir(t *testing.T, projectDir, env string) {
	t.Helper()
	dir := filepath.Join(projectDir, "deploy", "kcl", env)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.k"), []byte("# test env\n"), 0o644); err != nil {
		t.Fatalf("write main.k: %v", err)
	}
}

// runTopologyJSON runs the command in --json mode and parses the result.
func runTopologyJSON(t *testing.T, envs []string, opts envTopologyOptions) (envTopologyReport, int, string) {
	t.Helper()
	opts.JSON = true
	if opts.Resolver == nil {
		opts.Resolver = stubResolver{target: envTarget{KubeContext: "test-context", Namespace: "test-ns"}}
	}

	var err error
	out := captureStdout(t, func() {
		err = runEnvTopology(context.Background(), envs, opts)
	})

	var report envTopologyReport
	if jsonErr := json.Unmarshal([]byte(out), &report); jsonErr != nil {
		t.Fatalf("--json output must be parseable JSON, got %v for output:\n%s", jsonErr, out)
	}
	return report, exitCodeOf(t, err), out
}

// The control-plane shape, reduced: prod far ahead of two envs that share a
// release, and carrying an image the others do not have.
func realisticTopologyOpts(t *testing.T) envTopologyOptions {
	t.Helper()
	dir := t.TempDir()

	stageReleaseLedger(t, dir, "v1.3.0", "2026-07-01T00:25:07Z", true, map[string]string{
		"control-plane": dg("aaa3"), "reliant": dg("bbb3"),
	})
	// Intermediate cuts nobody is bound to. They are what makes
	// "releases behind" a count rather than a boolean.
	stageReleaseLedger(t, dir, "v1.4.0", "2026-07-20T00:00:00Z", false, map[string]string{"control-plane": dg("aaa4")})
	stageReleaseLedger(t, dir, "v1.5.0", "2026-08-01T00:00:00Z", false, map[string]string{"control-plane": dg("aaa5")})
	stageReleaseLedger(t, dir, "v1.5.15", "2026-09-10T13:53:22Z", false, map[string]string{
		"control-plane": dg("aaa15"), "reliant": dg("bbb15"), "internal-console": dg("ccc15"),
	})

	// Declare the three envs the way a real project does — one
	// deploy/kcl/<env>/main.k each. The file's CONTENT is irrelevant here
	// (the target resolver is stubbed); its EXISTENCE is what makes the
	// env declared, which is the same rule `forge env list` applies.
	for _, env := range []string{"prod", "staging", "preprod"} {
		declareEnvDir(t, dir, env)
	}

	return topologyBindings(t, envTopologyOptions{ProjectDir: dir}, newMemBindingStore(map[string]release.Promotion{
		"prod": {
			Release:    "v1.5.15",
			PromotedAt: mustTime(t, "2026-09-10T13:53:22Z"),
			Resolved: map[string]string{
				"control-plane": dg("aaa15"), "reliant": dg("bbb15"), "internal-console": dg("ccc15"),
			},
		},
		"staging": {
			Release:    "v1.3.0",
			PromotedAt: mustTime(t, "2026-07-01T00:25:15Z"),
			Resolved:   map[string]string{"control-plane": dg("aaa3"), "reliant": dg("bbb3")},
		},
		"preprod": {
			Release:    "v1.3.0",
			PromotedAt: mustTime(t, "2026-07-01T00:28:00Z"),
			Resolved:   map[string]string{"control-plane": dg("aaa3"), "reliant": dg("bbb3")},
		},
	}))
}

// TestTopology_MultiEnvDifferingReleases is the headline case: three envs, two
// distinct releases, and one env carrying an image the others lack.
//
// The image-union assertion is the important one. The matrix's column axis has
// to be the UNION across envs, because an image present in prod and absent
// from staging is only visible as a gap if the column exists — a consumer
// intersecting per-env lists would make exactly the image this screen most
// needs to surface disappear.
func TestTopology_MultiEnvDifferingReleases(t *testing.T) {
	opts := realisticTopologyOpts(t)
	report, code, out := runTopologyJSON(t, []string{"prod", "staging", "preprod"}, opts)

	if code != 0 {
		t.Fatalf("a ledger-only read must exit 0, got %d. Output:\n%s", code, out)
	}
	if !report.OK {
		t.Errorf("ledger-only mode cannot prove anything wrong, so ok must be true; detail = %q", report.Detail)
	}
	if report.LatestRelease != "v1.5.15" {
		t.Errorf("latest_release = %q, want v1.5.15 (the newest cut, by semver)", report.LatestRelease)
	}
	if len(report.Releases) != 4 || report.Releases[0] != "v1.5.15" || report.Releases[3] != "v1.3.0" {
		t.Errorf("releases must be newest-first, got %v", report.Releases)
	}

	wantImages := []string{"control-plane", "internal-console", "reliant"}
	if len(report.Images) != len(wantImages) {
		t.Fatalf("images must be the UNION across envs, got %v want %v", report.Images, wantImages)
	}
	for i, want := range wantImages {
		if report.Images[i] != want {
			t.Errorf("images[%d] = %q, want %q (sorted union)", i, report.Images[i], want)
		}
	}

	byEnv := map[string]topologyEnv{}
	for _, env := range report.Environments {
		byEnv[env.Env] = env
	}
	prod, staging := byEnv["prod"], byEnv["staging"]

	if prod.Release != "v1.5.15" || staging.Release != "v1.3.0" {
		t.Fatalf("prod/staging releases = %q/%q, want v1.5.15/v1.3.0", prod.Release, staging.Release)
	}
	// internal-console is prod-only. Staging must NOT carry a row for it —
	// that absence is the gap the matrix renders.
	if len(prod.Images) != 3 {
		t.Errorf("prod declares 3 images, got %d: %+v", len(prod.Images), prod.Images)
	}
	for _, img := range staging.Images {
		if img.Image == "internal-console" {
			t.Error("staging must not carry internal-console — it is not in v1.3.0, and inventing the row would hide the difference")
		}
	}
	// Provenance travels with the release, including the dirty bit: v1.3.0
	// was cut from a dirty tree and that is a finding about staging.
	if staging.Git == nil || !staging.Git.Dirty {
		t.Errorf("staging is bound to a release cut from a dirty tree; git = %+v", staging.Git)
	}
	if prod.Git == nil || prod.Git.Dirty {
		t.Errorf("prod's release was cut clean; git = %+v", prod.Git)
	}
	if prod.KubeContext != "test-context" || prod.Namespace != "test-ns" {
		t.Errorf("target = %q/%q, want test-context/test-ns", prod.KubeContext, prod.Namespace)
	}
}

// TestTopology_PromotionLag pins both lag units. They answer different
// questions and can disagree, so both must be right: prod is current, staging
// is three releases and ~71 days behind.
func TestTopology_PromotionLag(t *testing.T) {
	opts := realisticTopologyOpts(t)
	report, _, _ := runTopologyJSON(t, []string{"prod", "staging"}, opts)

	byEnv := map[string]topologyEnv{}
	for _, env := range report.Environments {
		byEnv[env.Env] = env
	}

	prod := byEnv["prod"]
	if prod.Lag == nil {
		t.Fatal("prod must carry a lag block")
	}
	if !prod.Lag.Current || prod.Lag.ReleasesBehind != 0 {
		t.Errorf("prod is bound to the newest release, so it must be current with 0 behind; got %+v", prod.Lag)
	}

	staging := byEnv["staging"]
	if staging.Lag == nil {
		t.Fatal("staging must carry a lag block")
	}
	if staging.Lag.Current {
		t.Error("staging is not on the newest release and must not report current")
	}
	// v1.3.0 sits at index 3 of [v1.5.15, v1.5.0, v1.4.0, v1.3.0].
	if staging.Lag.ReleasesBehind != 3 {
		t.Errorf("releases_behind = %d, want 3 (three releases were cut after v1.3.0)", staging.Lag.ReleasesBehind)
	}
	if staging.Lag.LatestRelease != "v1.5.15" {
		t.Errorf("lag must name the release it is measured against; got %q", staging.Lag.LatestRelease)
	}
	// 2026-07-01 → 2026-09-10 is 71 days and change.
	const wantSeconds = 6182895
	if staging.Lag.BehindSeconds != wantSeconds {
		t.Errorf("behind_seconds = %d, want %d", staging.Lag.BehindSeconds, wantSeconds)
	}
	if staging.Lag.Behind != "71d13h" {
		t.Errorf("behind = %q, want 71d13h", staging.Lag.Behind)
	}
}

// TestTopology_UnverifiedIsNotGreen is the test this command most needs.
//
// Without --verify nothing reads any cluster, so every cell must be
// not_verified — which is UNKNOWN, not OK. A state model where "nobody looked"
// shares a value with "checked and matching" paints a green screen over
// environments nobody looked at.
func TestTopology_UnverifiedIsNotGreen(t *testing.T) {
	opts := realisticTopologyOpts(t)
	// A lister that would report a clean MATCH if anything called it. It
	// must NOT be called: the point is that the default mode reads no
	// cluster at all.
	called := false
	opts.Lister = &recordingLister{onCall: func() { called = true }}

	report, code, out := runTopologyJSON(t, []string{"prod", "staging"}, opts)

	if called {
		t.Error("the default mode must not read any cluster — that is what makes it offline and instant")
	}
	if report.Verified {
		t.Error("verified must be false when --verify was not passed")
	}
	if code != 0 || !report.OK {
		t.Errorf("ledger-only mode always exits 0 and is ok; got code %d ok %v. Output:\n%s", code, report.OK, out)
	}

	total := 0
	for _, env := range report.Environments {
		for _, img := range env.Images {
			total++
			if img.State != topologyNotVerified {
				t.Errorf("%s/%s state = %v, want not_verified — nothing was checked",
					env.Env, img.Image, img.State)
			}
			if img.Running != "" {
				t.Errorf("%s/%s reports a running digest %q without having read a cluster",
					env.Env, img.Image, img.Running)
			}
		}
	}
	if report.Tally.NotVerified != total || report.Tally.Match != 0 {
		t.Errorf("tally = %+v, want all %d cells in not_verified and 0 in match", report.Tally, total)
	}

	// The wire form must be distinguishable too — a consumer keying off
	// the string is the whole audience for this field.
	if !json.Valid([]byte(out)) {
		t.Fatal("output must be valid JSON")
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	envs := raw["environments"].([]any)
	firstImg := envs[0].(map[string]any)["images"].([]any)[0].(map[string]any)
	if firstImg["state"] != "not_verified" {
		t.Errorf("wire state = %v, want the literal string \"not_verified\"", firstImg["state"])
	}
}

// recordingLister notes whether the cluster was read at all.
type recordingLister struct {
	onCall func()
	images []cluster.WorkloadImage
	err    error
}

func (l *recordingLister) ListWorkloadImages(context.Context, string, string) ([]cluster.WorkloadImage, error) {
	if l.onCall != nil {
		l.onCall()
	}
	return l.images, l.err
}

// TestTopology_VerifyReconciles covers --verify: the states become real
// verdicts, and drift flips the exit code to 1 exactly as `forge env status`
// would for the same environment.
func TestTopology_VerifyReconciles(t *testing.T) {
	opts := realisticTopologyOpts(t)
	opts.Verify = true
	opts.Lister = &recordingLister{images: []cluster.WorkloadImage{
		// control-plane matches; reliant runs something else entirely;
		// internal-console is deployed nowhere.
		deployImage("control-plane", "ghcr.io/acme/control-plane@"+dg("aaa15")),
		deployImage("reliant", "ghcr.io/acme/reliant@"+dg("wrong")),
	}}

	report, code, out := runTopologyJSON(t, []string{"prod"}, opts)

	if !report.Verified {
		t.Error("verified must be true under --verify")
	}
	if code != 1 {
		t.Fatalf("drift must exit 1, got %d. Output:\n%s", code, out)
	}
	if report.OK {
		t.Error("ok must be false when an environment does not run what it is bound to")
	}

	states := map[string]topologyImageState{}
	for _, img := range report.Environments[0].Images {
		states[img.Image] = img.State
	}
	if states["control-plane"] != topologyMatch {
		t.Errorf("control-plane = %v, want match", states["control-plane"])
	}
	if states["reliant"] != topologyDrift {
		t.Errorf("reliant = %v, want drift", states["reliant"])
	}
	if states["internal-console"] != topologyMissing {
		t.Errorf("internal-console = %v, want missing", states["internal-console"])
	}
	if report.Tally.Match != 1 || report.Tally.Drift != 1 || report.Tally.Missing != 1 {
		t.Errorf("tally = %+v, want 1 match / 1 drift / 1 missing", report.Tally)
	}
}

// TestTopology_VerifyUnreachableExits2 pins the split between "proven wrong"
// and "could not check". A VPN drop is not evidence that a release is wrong,
// and a gate reporting both with the same code gets switched off.
func TestTopology_VerifyUnreachableExits2(t *testing.T) {
	opts := realisticTopologyOpts(t)
	opts.Verify = true
	opts.Lister = &recordingLister{err: context.DeadlineExceeded}

	report, code, out := runTopologyJSON(t, []string{"prod"}, opts)

	if code != 2 {
		t.Fatalf("an unreadable cluster must exit 2, not 1, got %d. Output:\n%s", code, out)
	}
	if report.Tally.Unreachable != 3 || report.Tally.Drift != 0 {
		t.Errorf("tally = %+v, want 3 unreachable and 0 drift", report.Tally)
	}
}

// TestTopology_UnboundEnv: an env that was never promoted is a normal state,
// not a failure and not missing data.
func TestTopology_UnboundEnv(t *testing.T) {
	opts := realisticTopologyOpts(t)
	report, code, _ := runTopologyJSON(t, []string{"prod", "sandbox"}, opts)

	if code != 0 {
		t.Errorf("an unbound env is healthy, so the command must exit 0; got %d", code)
	}
	var sandbox topologyEnv
	for _, env := range report.Environments {
		if env.Env == "sandbox" {
			sandbox = env
		}
	}
	if sandbox.Env != "sandbox" {
		t.Fatalf("sandbox must appear in the report even though it is unbound; got %+v", report.Environments)
	}
	if sandbox.Bound {
		t.Error("sandbox has no binding and must report bound: false")
	}
	if sandbox.Release != "" {
		t.Errorf("an unbound env must carry no release, got %q", sandbox.Release)
	}
	if sandbox.Lag != nil {
		t.Errorf("an unbound env has nothing to be behind; lag = %+v", sandbox.Lag)
	}
	if sandbox.Images == nil {
		t.Error("images must be [] rather than null so a consumer can range over it")
	}
	if sandbox.Note == "" {
		t.Error("an unbound env must explain itself, or it reads as missing data")
	}
}

// TestTopology_BindingWithMissingReleaseLedger: a binding can name a release
// cut on another branch. The BINDING is still real and still says what the env
// runs — only the provenance and the lag are unavailable, and those must be
// omitted rather than guessed.
func TestTopology_BindingWithMissingReleaseLedger(t *testing.T) {
	opts := realisticTopologyOpts(t)
	opts = topologyBindings(t, opts, newMemBindingStore(map[string]release.Promotion{
		"prod": {
			Release:    "v9.9.9-branch",
			PromotedAt: mustTime(t, "2026-10-01T00:00:00Z"),
			Resolved:   map[string]string{"control-plane": dg("branchy")},
		},
	}))

	report, code, out := runTopologyJSON(t, []string{"prod"}, opts)

	if code != 0 {
		t.Fatalf("a binding whose ledger is absent is a state, not an error; exit %d. Output:\n%s", code, out)
	}
	prod := report.Environments[0]
	if !prod.Bound || prod.Release != "v9.9.9-branch" {
		t.Fatalf("the binding is real and must be reported in full; got bound=%v release=%q", prod.Bound, prod.Release)
	}
	if prod.ReleaseKnown {
		t.Error("release_known must be false — this checkout has no ledger for that version")
	}
	if prod.Git != nil {
		t.Errorf("provenance is unknown and must be omitted, not guessed; got %+v", prod.Git)
	}
	if prod.Lag != nil {
		t.Errorf("lag against a release set that does not contain this release is meaningless and must be omitted; got %+v", prod.Lag)
	}
	// The digests the binding froze are still the env's declaration and
	// must survive — they are what a deploy would pin.
	if len(prod.Images) != 1 || prod.Images[0].Digest != dg("branchy") {
		t.Errorf("the binding's resolved digests must still be reported; got %+v", prod.Images)
	}
	if prod.Note == "" {
		t.Error("the missing ledger must be explained on the row")
	}
}

// TestTopology_UndeclaredEnvIsReported: an env BOUND in the ledger but absent
// from this checkout's deploy/kcl/ — a release promoted on a branch that has
// the env, inspected from one that does not. It is reported as
// declared:false with no cluster resolved, rather than dropped or errored.
func TestTopology_UndeclaredEnvIsReported(t *testing.T) {
	opts := realisticTopologyOpts(t)
	opts = topologyBindings(t, opts, newMemBindingStore(map[string]release.Promotion{
		"hotfix": {
			Release:    "v1.3.0",
			PromotedAt: mustTime(t, "2026-07-02T00:00:00Z"),
			Resolved:   map[string]string{"control-plane": dg("aaa3")},
		},
	}))

	report, code, _ := runTopologyJSON(t, []string{"hotfix"}, opts)

	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	hotfix := report.Environments[0]
	if hotfix.Declared {
		t.Error("the test project declares no deploy/kcl/hotfix, so declared must be false")
	}
	if hotfix.KubeContext != "" || hotfix.Namespace != "" {
		t.Errorf("an env with no KCL in this checkout has no resolvable cluster; got %q/%q",
			hotfix.KubeContext, hotfix.Namespace)
	}
	if !hotfix.Bound || hotfix.Release != "v1.3.0" {
		t.Error("being undeclared here does not make the binding less real")
	}
	if hotfix.Lag == nil || hotfix.Lag.ReleasesBehind != 3 {
		t.Errorf("an undeclared env still has a computable lag; got %+v", hotfix.Lag)
	}
}

// TestTopology_DefaultsToDeclaredEnvs: with no arguments, the env set comes
// from deploy/kcl/, by the same rule `forge env list` uses.
func TestTopology_DefaultsToDeclaredEnvs(t *testing.T) {
	opts := realisticTopologyOpts(t)
	report, code, _ := runTopologyJSON(t, nil, opts)

	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	want := []string{"preprod", "prod", "staging"}
	if len(report.Environments) != len(want) {
		t.Fatalf("expected the three declared envs, got %d: %+v", len(report.Environments), report.Environments)
	}
	for i, name := range want {
		if report.Environments[i].Env != name {
			t.Errorf("environments[%d] = %q, want %q (sorted)", i, report.Environments[i].Env, name)
		}
		if !report.Environments[i].Declared {
			t.Errorf("%s came from deploy/kcl/ and must report declared: true", name)
		}
	}
}

// TestSortReleasesNewestFirst pins CUT TIME as the order, across version
// schemes. Semver cannot parse a date-named auto cut, so a semver-first sort
// ranked every v1.x.y above every date-named release and called a forward move
// from v1.7.40 to 20261014.042859-cd678a0d6531 "67 releases OLDER". A lexical
// sort additionally puts v1.5.9 after v1.5.15.
func TestSortReleasesNewestFirst(t *testing.T) {
	t.Run("date-named cuts after semver cuts are newer", func(t *testing.T) {
		releases := []release.Release{
			{Version: "v1.7.38", CreatedAt: mustTime(t, "2026-10-09T00:00:00Z")},
			{Version: "20261014.042859-cd678a0d6531", CreatedAt: mustTime(t, "2026-10-14T04:28:59Z")},
			{Version: "v1.7.40", CreatedAt: mustTime(t, "2026-10-12T00:00:00Z")},
			{Version: "20261013.002646-aaaaaaaaaaaa", CreatedAt: mustTime(t, "2026-10-13T00:26:46Z")},
		}
		sortReleasesNewestFirst(releases)
		want := []string{"20261014.042859-cd678a0d6531", "20261013.002646-aaaaaaaaaaaa", "v1.7.40", "v1.7.38"}
		for i, w := range want {
			if releases[i].Version != w {
				t.Fatalf("order[%d] = %q, want %q (full: %v)", i, releases[i].Version, w, versionsOf(releases))
			}
		}
	})

	t.Run("semver labels cut at the same instant fall back to semver, not lexical", func(t *testing.T) {
		at := mustTime(t, "2026-08-01T00:00:00Z")
		releases := []release.Release{
			{Version: "v1.5.9", CreatedAt: at}, {Version: "v1.5.15", CreatedAt: at}, {Version: "v1.3.0", CreatedAt: at},
		}
		sortReleasesNewestFirst(releases)
		if got := versionsOf(releases); got[0] != "v1.5.15" || got[1] != "v1.5.9" || got[2] != "v1.3.0" {
			t.Fatalf("order = %v, want [v1.5.15 v1.5.9 v1.3.0]", got)
		}
	})

	t.Run("an unknown cut time sorts oldest and the order is a stable total order", func(t *testing.T) {
		releases := []release.Release{
			{Version: "v1.0.0"},
			{Version: "v1.1.0", CreatedAt: mustTime(t, "2026-01-01T00:00:00Z")},
			{Version: "20260301.000000-abc", CreatedAt: mustTime(t, "2026-03-01T00:00:00Z")},
		}
		sortReleasesNewestFirst(releases)
		if got := versionsOf(releases); got[0] != "20260301.000000-abc" || got[1] != "v1.1.0" || got[2] != "v1.0.0" {
			t.Fatalf("order = %v", got)
		}
	})
}

func versionsOf(rs []release.Release) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Version
	}
	return out
}

// TestTopologyImageState_RejectsUnknown: an unrecognized state must NOT decode
// to the zero value. Here the zero value is not_verified rather than match, so
// even a silent default would be safe — but a newer forge with a seventh state
// must be visible as version skew rather than silently flattened.
func TestTopologyImageState_RejectsUnknown(t *testing.T) {
	var s topologyImageState
	if err := json.Unmarshal([]byte(`"probably_fine"`), &s); err == nil {
		t.Fatal("an unknown state must be an error, not a default")
	}
	for _, name := range []string{"not_verified", "match", "drift", "missing", "untagged", "unreachable"} {
		var got topologyImageState
		if err := json.Unmarshal([]byte(`"`+name+`"`), &got); err != nil {
			t.Fatalf("%q must decode: %v", name, err)
		}
		encoded, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshal %q: %v", name, err)
		}
		if string(encoded) != `"`+name+`"` {
			t.Errorf("round trip of %q produced %s", name, encoded)
		}
	}
	// The zero value is the unknown state, so a never-populated struct
	// reads as "not checked" rather than as a clean bill of health.
	var zero topologyImageState
	if zero != topologyNotVerified {
		t.Error("the zero value must be not_verified — a zero-value struct must never read as healthy")
	}
}

// TestTopology_LedgerReadFailureIsPerRow: one env's failed ledger read must not
// blank the whole screen.
func TestTopology_LedgerReadFailureIsPerRow(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates a KCL project natively; runs in task test")
	}
	opts := realisticTopologyOpts(t)
	opts = topologyBindings(t, opts, &memBindingStore{err: context.DeadlineExceeded})

	report, code, out := runTopologyJSON(t, []string{"prod"}, opts)
	if code != 0 {
		t.Fatalf("exit %d, want 0. Output:\n%s", code, out)
	}
	if report.Environments[0].Note == "" {
		t.Error("a failed ledger read must be explained on the row rather than silently producing an empty env")
	}
}

// TestTopology_TextModeRunsClean exercises the human renderer end to end. It
// asserts little about the wording — that is presentation — but a renderer
// that panics on a nil lag or an unbound env would take down the default
// invocation, which is the one most people run.
func TestTopology_TextModeRunsClean(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates a KCL project natively; runs in task test")
	}
	opts := realisticTopologyOpts(t)
	var err error
	out := captureStdout(t, func() {
		err = runEnvTopology(context.Background(), []string{"prod", "staging", "sandbox"}, opts)
	})
	if err != nil {
		t.Fatalf("text mode must exit 0 for a ledger-only read: %v", err)
	}
	for _, want := range []string{"NOT READ", "v1.5.15", "v1.3.0", "NOT-VERIFIED", "Images by environment"} {
		if !contains(out, want) {
			t.Errorf("text output should mention %q; got:\n%s", want, out)
		}
	}
}
