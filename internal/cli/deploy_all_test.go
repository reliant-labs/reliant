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

	"github.com/reliant-labs/forge/pkg/release"
)

// O-15: `forge env deploy <env>` with no version does ALL the deployment
// bits. These pin the ORDER and the write discipline, which is what the owner
// hit on hounders prod: the command that should ship the checkout instead
// printed a three-step runbook, one step of which cut a release with no
// images in it.

// deployAllProject is a self-managed (file-ledger) project with one env. The
// file ledger is deliberate: it makes "was a release cut" and "was a
// promotion written" two separate things a test can read off disk, with no
// server to interpret them.
func deployAllProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("forge.yaml", "name: deployall\nmodule_path: github.com/example/deployall\nversion: \"0.1.0\"\n")
	markServiceProject(t, dir)
	return dir
}

// deployTestTree is the tree hash these tests build "from". Stated, because
// a t.TempDir() is not a git repository and the real capture would report no
// tree at all — which is F-16's never-reuse path, the opposite of what the
// reuse tests exercise.
var deployTestTree = strings.Repeat("a", 40)

// deployTestForge is the forge these tests' checkout is built by.
const deployTestForge = "v0.1.44-0.20261009203931-bc24a9963fd6"

// deployTestImage is the one image env prod declares, keyed — as every image
// forge builds is — by its full repository.
const deployTestImage = "registry.test/acme/api"

// deployTestEnvRender is env prod's render: one cluster workload whose image
// forge builds.
const deployTestEnvRender = `{"output":{"workloads":[{"name":"api","kind":"service","image":"` + deployTestImage + `",` +
	`"build":{"type":"go","cmd":"./cmd/api"},"runtime":{"type":"cluster","cluster":"k3d-dev","namespace":"dev"},"spec":{"kind":"service"}}]}}`

// deployTestProvenance is the checkout these tests deploy: clean, hashed, and
// built by deployTestForge.
func deployTestProvenance() release.Provenance {
	return release.Provenance{Commit: strings.Repeat("c", 40), Tree: deployTestTree, ForgeVersion: deployTestForge}
}

// stubDeployCheckout states what a no-version deploy reads from the world
// before it builds: the checkout's provenance, the env's render, and a
// registry. The registry serves every image; a test about an expired one
// overrides releaseImageResolves after this.
func stubDeployCheckout(t *testing.T, prov release.Provenance, render string) {
	t.Helper()
	prevProv, prevResolves := captureReleaseProvenance, releaseImageResolves
	captureReleaseProvenance = func(context.Context, string) release.Provenance { return prov }
	releaseImageResolves = func(context.Context, string) (bool, error) { return true, nil }
	t.Cleanup(func() { captureReleaseProvenance, releaseImageResolves = prevProv, prevResolves })
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, render))
}

// stubDeployBuild replaces the build with a recorder that cuts the release
// the deploy asked for, recording the checkout's provenance through the same
// seam the real cut does (captureReleaseProvenance). It returns a pointer to
// the call log.
func stubDeployBuild(t *testing.T, dir string, artifacts map[string]string) *[]buildOptions {
	t.Helper()
	stubDeployCheckout(t, deployTestProvenance(), deployTestEnvRender)
	var calls []buildOptions
	prev := runDeployBuild
	runDeployBuild = func(ctx context.Context, opts buildOptions) error {
		calls = append(calls, opts)
		if opts.release == "" {
			t.Errorf("the deploy's build was asked to cut nothing; a no-version deploy builds only to cut")
			return nil
		}
		rel := ociRelease(opts.release, artifacts)
		rel.SetProvenance(captureReleaseProvenance(ctx, dir))
		if _, err := testStore(t, dir).CutRelease(rel); err != nil {
			t.Fatalf("stub build: cut %s: %v", opts.release, err)
		}
		return nil
	}
	t.Cleanup(func() { runDeployBuild = prev })
	return &calls
}

// TestDeployNoVersion_BuildsCutsThenPromotes is the O-15 happy path: build,
// cut the auto version, and promote — IN THAT ORDER.
//
// The order is the assertion, not an implementation detail. A promotion
// written before the images are pushed names digests that do not exist yet,
// and with the converger on, the promotion IS the deploy — so it would roll
// out a release whose bytes had not landed.
func TestDeployNoVersion_BuildsCutsThenPromotes(t *testing.T) {
	if testing.Short() {
		t.Skip("drives the deploy path end to end; skipped in -short")
	}
	dir := deployAllProject(t)
	t.Chdir(dir)
	calls := stubDeployBuild(t, dir, map[string]string{"api": sha("1")})

	ledger := testLedger(t, dir)
	prov := release.Provenance{Commit: strings.Repeat("c", 40), Tree: deployTestTree}
	cut, err := buildAndCutForDeploy(context.Background(), dir, "prod", deployCmdFlags{}, ledger)
	if err != nil {
		t.Fatalf("buildAndCutForDeploy: %v", err)
	}

	// 1. The build ran, and it ran with --push and --release <auto version>.
	if len(*calls) != 1 {
		t.Fatalf("build ran %d times, want once", len(*calls))
	}
	got := (*calls)[0]
	// pushIfDeclared, not push: both publish every declared reference, and
	// they differ only on an env that declares NONE — where --push is a
	// usage error and this verb must still proceed to the cut (a hosted env
	// whose images CI pushes is perfectly deployable).
	if !got.pushIfDeclared {
		t.Error("the build did not push: a release pins digests, which require a registry")
	}
	if got.push {
		t.Error("the build used --push, which refuses an env with nothing of its own to push")
	}
	if got.release != cut.Version {
		t.Errorf("build cut %q but the deploy returned %q", got.release, cut.Version)
	}
	if got.env != "prod" {
		t.Errorf("build env = %q, want prod", got.env)
	}

	// 2. The version is the auto-version shape, naming this tree.
	if want := autoVersionFor(prov, time.Now()); cut.Version[:8] != want[:8] {
		t.Errorf("version %q does not carry today's UTC date like %q", cut.Version, want)
	}
	if !strings.HasSuffix(cut.Version, "-"+strings.Repeat("a", 12)) {
		t.Errorf("version %q does not end in the tree's first 12 hex", cut.Version)
	}
	if cut.Reused {
		t.Error("the first deploy of a tree must CUT, not reuse")
	}

	// 3. The release is in the ledger and nothing is promoted yet — the
	// promotion is runPromote's job, behind the confirmation gate.
	rel, err := ledger.Releases.Get(context.Background(), cut.Version)
	if err != nil || rel == nil {
		t.Fatalf("release %s was not recorded: %v", cut.Version, err)
	}
	if _, bound, _ := ledger.Bindings.Current(context.Background(), "prod"); bound {
		t.Error("a promotion was written by the build-and-cut half, before the plan was even computed")
	}
}

// TestDeployNoVersion_RetryOfTheSameTreeReusesTheRelease: a second deploy of
// an unchanged checkout cuts NOTHING and builds NOTHING.
//
// Without the reuse, every retry — a declined confirmation, a failed rollout,
// a CI re-run — names a new version for bytes that already have one. Without
// skipping the build, every retry pushes NEW bytes: a build is not
// reproducible byte for byte, and on 2026-10-09 a rebuild of an unchanged
// prod checkout produced a reliant image with a different digest.
func TestDeployNoVersion_RetryOfTheSameTreeReusesTheRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("drives the deploy path end to end; skipped in -short")
	}
	dir := deployAllProject(t)
	t.Chdir(dir)
	calls := stubDeployBuild(t, dir, map[string]string{deployTestImage: sha("1")})
	ledger := testLedger(t, dir)

	first, err := buildAndCutForDeploy(context.Background(), dir, "prod", deployCmdFlags{}, ledger)
	if err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	var second deployCutResult
	out := captureStdout(t, func() {
		second, err = buildAndCutForDeploy(context.Background(), dir, "prod", deployCmdFlags{}, ledger)
	})
	if err != nil {
		t.Fatalf("second deploy: %v", err)
	}

	if second.Version != first.Version {
		t.Errorf("the retry cut %q, want the existing %q — one tree is cut once", second.Version, first.Version)
	}
	if !second.Reused {
		t.Error("the retry did not report the release as reused")
	}
	if len(*calls) != 1 {
		t.Fatalf("build ran %d times, want once — a reused release is not rebuilt", len(*calls))
	}
	if want := "[deploy] reusing release " + first.Version + " (cut at "; !strings.Contains(out, want) {
		t.Errorf("the retry did not say up front which release it reuses (want %q):\n%s", want, out)
	}
	// Exactly one release exists.
	all, err := ledger.Releases.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Errorf("%d releases in the ledger, want 1", len(all))
	}
}

// chooseAt is the instant the choice tests deploy at.
var chooseAt = time.Date(2026, 10, 9, 21, 34, 18, 0, time.UTC)

// cutTestRelease records version over images, with prov, in dir's machine
// ledger — what `forge env build prod --release <version>` leaves behind.
func cutTestRelease(t *testing.T, dir, version string, prov release.Provenance, images map[string]string) {
	t.Helper()
	rel := ociRelease(version, images)
	rel.SetProvenance(prov)
	if err := testCutRelease(t, dir, rel); err != nil {
		t.Fatalf("cut %s: %v", version, err)
	}
}

// chooseFor is the choice a no-version deploy of env prod makes for prov.
func chooseFor(t *testing.T, dir string, prov release.Provenance) deployReleaseChoice {
	t.Helper()
	return chooseDeployRelease(context.Background(), deployReuseQuery{
		ProjectDir: dir, Env: "prod", Provenance: prov,
		Releases: testLedger(t, dir).Releases, Now: chooseAt,
	})
}

// The match is on what a release RECORDS, never on its name: a release a
// script cut as `<date>-<commit>` is found by its tree, its forge and its
// coverage of the env — the 2026-10-09 prod shape.
func TestChooseDeployRelease_ReusesAReleaseOfThisCheckoutWhateverItIsNamed(t *testing.T) {
	dir := deployAllProject(t)
	stubDeployCheckout(t, deployTestProvenance(), deployTestEnvRender)
	cutTestRelease(t, dir, "20261009.205925-d29da50b", deployTestProvenance(), map[string]string{deployTestImage: sha("1")})

	var asked []string
	releaseImageResolves = func(_ context.Context, ref string) (bool, error) {
		asked = append(asked, ref)
		return true, nil
	}
	got := chooseFor(t, dir, deployTestProvenance())
	if got.Reuse == nil || got.Reuse.Version != "20261009.205925-d29da50b" {
		t.Fatalf("choice = %+v, want reuse of 20261009.205925-d29da50b", got)
	}
	// Its image was asked of the registry BY DIGEST, at the repository it
	// was pushed to.
	if want := deployTestImage + "@" + sha("1"); len(asked) != 1 || asked[0] != want {
		t.Errorf("registry was asked %v, want exactly [%s]", asked, want)
	}
}

// Each of these must CUT a new release, and say why. The checkout always
// holds a release cut by `forge env build --release` for deployTestTree; each
// case changes one thing about the checkout, the env or the world.
func TestChooseDeployRelease_CutsANewReleaseAndSaysWhy(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates a KCL project natively; runs in task test")
	}
	cleanDifferentTree := deployTestProvenance()
	cleanDifferentTree.Tree = strings.Repeat("d", 40)
	dirtyEdit := cleanDifferentTree
	dirtyEdit.Dirty = true
	dirtySameTree := deployTestProvenance()
	dirtySameTree.Dirty = true
	otherForge := deployTestProvenance()
	otherForge.ForgeVersion = "v0.1.45"
	unhashed := deployTestProvenance()
	unhashed.Tree = ""
	cutFromDirty := deployTestProvenance()
	cutFromDirty.Dirty = true

	twoImages := `{"output":{"workloads":[` +
		`{"name":"api","kind":"service","image":"` + deployTestImage + `","build":{"type":"go","cmd":"./cmd/api"},"runtime":{"type":"cluster","cluster":"k3d-dev","namespace":"dev"},"spec":{"kind":"service"}},` +
		`{"name":"worker","kind":"service","image":"registry.test/acme/worker","build":{"type":"go","cmd":"./cmd/worker"},"runtime":{"type":"cluster","cluster":"k3d-dev","namespace":"dev"},"spec":{"kind":"service"}}]}}`
	bareImage := strings.ReplaceAll(deployTestEnvRender, deployTestImage, "api")

	cases := []struct {
		name     string
		checkout release.Provenance
		recorded release.Provenance // the existing release's provenance
		images   map[string]string  // the existing release's images
		render   string
		resolves func(string) (bool, error)
		want     []string // in the reason
	}{
		{
			name: "the tree was edited after the cut (dirty)", checkout: dirtyEdit,
			want: []string{"uncommitted changes"},
		},
		{
			name: "the same tree, but the checkout reports itself dirty", checkout: dirtySameTree,
			want: []string{"uncommitted changes"},
		},
		{
			name: "a commit after the cut (clean, another tree)", checkout: cleanDifferentTree,
			want: []string{"no release in", "records this checkout's tree (dddddddddddd)"},
		},
		{
			name: "a different forge version", checkout: otherForge,
			want: []string{"built by forge " + deployTestForge, "this is forge v0.1.45"},
		},
		{
			name: "the env now declares an artifact the release lacks", render: twoImages,
			want: []string{"does not cover everything env prod declares", "registry.test/acme/worker (image, used by worker)"},
		},
		{
			name: "an image the release pins has expired from its registry",
			resolves: func(string) (bool, error) {
				return false, errors.New("registry.test answered HTTP 404 for the manifest")
			},
			want: []string{"pins " + deployTestImage + "@" + sha("1"), "could not be confirmed in its registry", "HTTP 404"},
		},
		{
			name: "a release that records no registry for its image", render: bareImage, images: map[string]string{"api": sha("1")},
			want: []string{"pins api", "records no registry"},
		},
		{
			name: "a release cut from a dirty checkout", recorded: cutFromDirty,
			want: []string{"cut from a checkout with uncommitted changes"},
		},
		{
			name: "the tree could not be hashed", checkout: unhashed,
			want: []string{"could not be hashed"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := deployAllProject(t)
			if tc.checkout == (release.Provenance{}) {
				tc.checkout = deployTestProvenance()
			}
			if tc.recorded == (release.Provenance{}) {
				tc.recorded = deployTestProvenance()
			}
			if tc.images == nil {
				tc.images = map[string]string{deployTestImage: sha("1")}
			}
			if tc.render == "" {
				tc.render = deployTestEnvRender
			}
			stubDeployCheckout(t, tc.checkout, tc.render)
			if tc.resolves != nil {
				releaseImageResolves = func(_ context.Context, ref string) (bool, error) { return tc.resolves(ref) }
			}
			cutTestRelease(t, dir, "20261009.205925-d29da50b", tc.recorded, tc.images)

			got := chooseFor(t, dir, tc.checkout)
			if got.Reuse != nil {
				t.Fatalf("reused %s; want a new cut", got.Reuse.Version)
			}
			if want := autoVersionFor(tc.checkout, chooseAt); got.Version != want {
				t.Errorf("version = %q, want the auto version %q", got.Version, want)
			}
			for _, w := range tc.want {
				if !strings.Contains(got.Reason, w) {
					t.Errorf("reason is missing %q:\n%s", w, got.Reason)
				}
			}
			var announced bytes.Buffer
			got.announce(&announced, "prod", false)
			if want := "[deploy] cutting new release " + got.Version + " because "; !strings.HasPrefix(announced.String(), want) {
				t.Errorf("announcement = %q, want it to start %q", announced.String(), want)
			}
		})
	}
}

// TestAutoVersion covers the naming rule, including F-16's unhashed tree.
func TestAutoVersion(t *testing.T) {
	at := time.Date(2026, 10, 2, 17, 4, 5, 0, time.UTC)
	tree := strings.Repeat("a", 40)
	commit := strings.Repeat("c", 40)

	if got, want := autoVersionFor(release.Provenance{Tree: tree, Commit: commit}, at),
		"20261002.170405-"+strings.Repeat("a", 12); got != want {
		t.Errorf("tree version = %q, want %q", got, want)
	}
	// F-16: an unhashed tree falls back to the commit. The name still means
	// something, and chooseDeployRelease refuses to reuse it.
	if got, want := autoVersionFor(release.Provenance{Commit: commit}, at),
		"20261002.170405-"+strings.Repeat("c", 12); got != want {
		t.Errorf("commit fallback = %q, want %q", got, want)
	}
	// Not a git tree at all: the timestamp is still unique per project and
	// still sorts, which is all the ledger needs.
	if got, want := autoVersionFor(release.Provenance{}, at), "20261002.170405"; got != want {
		t.Errorf("bare version = %q, want %q", got, want)
	}
	// It is UTC, not local: two machines in two zones must not name the
	// same tree differently.
	zone := time.FixedZone("UTC+9", 9*3600)
	if got := autoVersionFor(release.Provenance{Tree: tree}, at.In(zone)); !strings.HasPrefix(got, "20261002.170405") {
		t.Errorf("version %q is not UTC-normalized", got)
	}
}

// An unknown version's fix is `forge env deploy <env>`, NEVER --no-build.
// This is the exact text the owner was given on hounders prod.
func TestDeployUnknownVersion_FixIsTheDeployVerbNotNoBuild(t *testing.T) {
	dir := deployAllProject(t)
	t.Chdir(dir)
	err := runPromote(context.Background(), "v9.9.9", "prod", promoteOptions{
		Ledger: testLedger(t, dir), ProjectDir: dir,
	})
	if err == nil {
		t.Fatal("deploying a version nobody cut must fail")
	}
	got := err.Error()
	if strings.Contains(got, "--no-build") {
		t.Errorf("the fix still recommends --no-build, which cuts a release with no images:\n%s", got)
	}
	for _, want := range []string{"was never cut", "forge env deploy prod", "builds, pushes, cuts"} {
		if !strings.Contains(got, want) {
			t.Errorf("error is missing %q:\n%s", want, got)
		}
	}
}

// ─── The confirmation gate (O-13, §13 F-18) ─────────────────────────────────

// No TTY and no --yes: exit 5, plan_unconfirmed, with the release CUT and NO
// promotion written.
//
// The two halves both matter. Exit 5 is what a pipeline branches on. "Release
// cut, promotion absent" is what makes the refusal cheap to recover from: the
// images are pushed and the version exists, so approving it needs no rebuild
// — which is why the message names the exact command.
func TestDeployConfirm_NoTTYNoYesExits5AndWritesNoPromotion(t *testing.T) {
	if testing.Short() {
		t.Skip("drives the deploy path end to end; skipped in -short")
	}
	dir := deployAllProject(t)
	t.Chdir(dir)
	stubDeployBuild(t, dir, map[string]string{"api": sha("1")})
	ledger := testLedger(t, dir)

	cut, err := buildAndCutForDeploy(context.Background(), dir, "prod", deployCmdFlags{}, ledger)
	if err != nil {
		t.Fatalf("build and cut: %v", err)
	}
	err = runPromote(context.Background(), cut.Version, "prod", promoteOptions{
		Ledger: ledger, ProjectDir: dir,
		Confirm: &deployConfirm{AutoVersion: cut.Version, Interactive: false},
	})
	if err == nil {
		t.Fatal("a non-interactive deploy with no --yes must refuse")
	}
	var coded exitCodeError
	if !errors.As(err, &coded) {
		t.Fatalf("err = %v (%T), want an exitCodeError carrying 5", err, err)
	}
	// Both: the named constant, and the literal a pipeline branches on.
	if coded.ExitCode() != exitPlanUnconfirmed || coded.ExitCode() != 5 {
		t.Errorf("exit code = %d, want 5 (plan_unconfirmed)", coded.ExitCode())
	}
	for _, want := range []string{"plan_unconfirmed", "NO promotion was written", "--yes", cut.Version} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal is missing %q:\n%v", want, err)
		}
	}
	// THE RELEASE IS CUT.
	if rel, gerr := ledger.Releases.Get(context.Background(), cut.Version); gerr != nil || rel == nil {
		t.Errorf("release %s is absent; the refusal must keep what the build produced", cut.Version)
	}
	// AND NO PROMOTION WAS WRITTEN. Asserted against the ledger, not
	// against the message: a promotion IS the deploy, so this is the
	// property the whole gate exists for.
	if _, bound, berr := ledger.Bindings.Current(context.Background(), "prod"); berr != nil || bound {
		t.Error("a promotion was written despite the plan never being confirmed")
	}
}

// --plan-only writes no promotion and exits 0: it is the first stage of a
// two-stage pipeline, and a stage that failed would make the pipeline red for
// doing exactly what it was asked.
func TestDeployConfirm_PlanOnlyWritesNoPromotionAndSucceeds(t *testing.T) {
	if testing.Short() {
		t.Skip("drives the deploy path end to end; skipped in -short")
	}
	dir := deployAllProject(t)
	t.Chdir(dir)
	stubDeployBuild(t, dir, map[string]string{"api": sha("1")})
	ledger := testLedger(t, dir)

	cut, err := buildAndCutForDeploy(context.Background(), dir, "prod", deployCmdFlags{}, ledger)
	if err != nil {
		t.Fatalf("build and cut: %v", err)
	}
	if err := runPromote(context.Background(), cut.Version, "prod", promoteOptions{
		Ledger: ledger, ProjectDir: dir,
		Confirm: &deployConfirm{PlanOnly: true, AutoVersion: cut.Version, Interactive: false},
	}); err != nil {
		t.Fatalf("--plan-only must exit 0, got: %v", err)
	}
	if _, bound, _ := ledger.Bindings.Current(context.Background(), "prod"); bound {
		t.Error("--plan-only wrote a promotion")
	}
	// --plan-only beats --yes: a caller that passes both asked for the
	// preview, and a preview that deployed would be the one command nobody
	// could run safely.
	if err := runPromote(context.Background(), cut.Version, "prod", promoteOptions{
		Ledger: ledger, ProjectDir: dir,
		Confirm: &deployConfirm{PlanOnly: true, Yes: true, AutoVersion: cut.Version, Interactive: false},
	}); err != nil {
		t.Fatalf("--plan-only --yes must exit 0, got: %v", err)
	}
	if _, bound, _ := ledger.Bindings.Current(context.Background(), "prod"); bound {
		t.Error("--plan-only --yes wrote a promotion; --plan-only must win")
	}
}

// --yes proceeds, and an interactive NO does not. Both end states are read
// off the ledger.
func TestDeployConfirm_YesWritesAndDeclineDoesNot(t *testing.T) {
	if testing.Short() {
		t.Skip("drives the deploy path end to end; skipped in -short")
	}
	dir := deployAllProject(t)
	t.Chdir(dir)
	stubDeployBuild(t, dir, map[string]string{"api": sha("1")})
	ledger := testLedger(t, dir)
	cut, err := buildAndCutForDeploy(context.Background(), dir, "prod", deployCmdFlags{}, ledger)
	if err != nil {
		t.Fatalf("build and cut: %v", err)
	}

	// An interactive "no" is a SUCCESS that writes nothing: the caller
	// asked what would happen and found out.
	if err := runPromote(context.Background(), cut.Version, "prod", promoteOptions{
		Ledger: ledger, ProjectDir: dir,
		Confirm: &deployConfirm{
			AutoVersion: cut.Version,
			Interactive: true,
			prompt:      func(string) (bool, error) { return false, nil },
		},
	}); err != nil {
		t.Fatalf("a declined confirmation must exit 0, got: %v", err)
	}
	if _, bound, _ := ledger.Bindings.Current(context.Background(), "prod"); bound {
		t.Fatal("answering no wrote a promotion — this is the v1.7.13 regression")
	}

	// --yes writes it. Follow is nil, so this is the ledger write alone.
	if err := runPromote(context.Background(), cut.Version, "prod", promoteOptions{
		Ledger: ledger, ProjectDir: dir,
		Confirm: &deployConfirm{Yes: true, AutoVersion: cut.Version, Interactive: false},
	}); err != nil {
		t.Fatalf("--yes must deploy, got: %v", err)
	}
	bound, ok, err := ledger.Bindings.Current(context.Background(), "prod")
	if err != nil || !ok {
		t.Fatalf("--yes wrote no promotion: ok=%v err=%v", ok, err)
	}
	if bound.Release != cut.Version {
		t.Errorf("promoted %q, want %q", bound.Release, cut.Version)
	}
}

// An interactive prompt defaults to NO: a bare Enter must not ship a release.
func TestDeployConfirm_PromptDefaultsToNo(t *testing.T) {
	plan := promotePlan{Env: "prod", Target: promotePlanTarget{Release: "v1"}}
	var asked string
	out := confirmDeployPlan("prod", plan, deployConfirm{
		Interactive: true,
		prompt: func(q string) (bool, error) {
			asked = q
			// What promptYesNo returns for a bare Enter.
			return false, nil
		},
		out: os.Stderr,
	}, false)
	if out.Confirmed {
		t.Error("a bare Enter confirmed the deploy")
	}
	if out.Err != nil {
		t.Errorf("declining is not an error: %v", out.Err)
	}
	if !strings.Contains(asked, "prod") || !strings.Contains(asked, "v1") {
		t.Errorf("the prompt must name the env and the release, got %q", asked)
	}
}
