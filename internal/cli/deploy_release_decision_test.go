package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// The release decision of a no-version deploy, against a REAL git checkout:
// the dirty verdict is the thing under test, so the provenance seam is left
// alone. These pin the 2026-10-12 prod incident — a bare `forge env deploy
// prod` cut and built a new release because an untracked nested worktree made
// the tree "dirty", and `--dry-run` said nothing about it.

func decisionGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// decisionCheckout is a committed git project with a release cut from its
// clean tree: the state a prod operator is in right after a deploy.
func decisionCheckout(t *testing.T) (dir string, ledger envLedger) {
	t.Helper()
	return decisionCheckoutWith(t, nil)
}

// decisionCheckoutWith is decisionCheckout, letting the cut release be
// adjusted (a frozen source pin) before it is recorded.
func decisionCheckoutWith(t *testing.T, adjust func(*release.Release)) (dir string, ledger envLedger) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a real git checkout; runs in task test")
	}
	dir = deployAllProject(t)
	ledger = testLedger(t, dir)
	t.Chdir(dir)
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, deployTestEnvRender))
	prevResolves := releaseImageResolves
	releaseImageResolves = func(context.Context, string) (bool, error) { return true, nil }
	t.Cleanup(func() { releaseImageResolves = prevResolves })

	decisionGit(t, dir, "init", "-q", "-b", "main")
	decisionGit(t, dir, "add", "-A")
	decisionGit(t, dir, "commit", "-q", "-m", "init")

	rel := ociRelease("20261012.030000-aaaaaaaaaaaa", map[string]string{deployTestImage: sha("1")})
	rel.SetProvenance(captureReleaseProvenance(context.Background(), dir))
	if adjust != nil {
		adjust(&rel)
	}
	if err := testCutRelease(t, dir, rel); err != nil {
		t.Fatalf("cut: %v", err)
	}
	return dir, ledger
}

func decide(t *testing.T, dir string, ledger envLedger) deployReleaseChoice {
	t.Helper()
	return decideDeployRelease(context.Background(), dir, "prod", ledger)
}

func TestDecideDeployRelease_CleanCheckoutReusesItsRelease(t *testing.T) {
	dir, ledger := decisionCheckout(t)
	got := decide(t, dir, ledger)
	if got.Reuse == nil || got.Reuse.Version != "20261012.030000-aaaaaaaaaaaa" {
		t.Fatalf("choice = %+v, want reuse of the cut release", got)
	}
	if got.Dirty || len(got.Nested) != 0 {
		t.Errorf("a clean checkout reported dirty=%v nested=%v", got.Dirty, got.Nested)
	}
}

// THE INCIDENT: an untracked nested worktree (and a nested repo) is not dirt.
func TestDecideDeployRelease_OnlyNestedCheckoutsStillReuses(t *testing.T) {
	dir, ledger := decisionCheckout(t)
	decisionGit(t, dir, "worktree", "add", "-q", "-b", "parked", filepath.Join(dir, "control-plane-hosted-identity"))
	if err := os.MkdirAll(filepath.Join(dir, "scratch-clone"), 0o755); err != nil {
		t.Fatal(err)
	}
	decisionGit(t, filepath.Join(dir, "scratch-clone"), "init", "-q")

	got := decide(t, dir, ledger)
	if got.Reuse == nil {
		t.Fatalf("a nested worktree made the tree dirty and forced a cut: %s", got.Reason)
	}
	if got.Dirty {
		t.Error("nested checkouts were reported as dirt")
	}
	if len(got.Nested) != 2 {
		t.Fatalf("nested = %v, want both checkouts named", got.Nested)
	}
	var line bytes.Buffer
	got.announce(&line, "prod", false)
	for _, want := range []string{"reusing release 20261012.030000-aaaaaaaaaaaa", "nested git checkouts", "control-plane-hosted-identity/", "scratch-clone/"} {
		if !strings.Contains(line.String(), want) {
			t.Errorf("decision line is missing %q:\n%s", want, line.String())
		}
	}
}

// A real untracked file still counts, and the reason names it — even with a
// nested checkout beside it.
func TestDecideDeployRelease_RealUntrackedFileCutsAndNamesIt(t *testing.T) {
	dir, ledger := decisionCheckout(t)
	decisionGit(t, dir, "worktree", "add", "-q", "-b", "parked", filepath.Join(dir, "parked-worktree"))
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := decide(t, dir, ledger)
	if got.Reuse != nil {
		t.Fatalf("reused %s from a tree with an untracked file", got.Reuse.Version)
	}
	if !got.Dirty || len(got.DirtyPaths) != 1 || got.DirtyPaths[0] != "notes.txt" {
		t.Errorf("dirty=%v paths=%v, want notes.txt", got.Dirty, got.DirtyPaths)
	}
	for _, want := range []string{"uncommitted changes (notes.txt)"} {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("reason is missing %q:\n%s", want, got.Reason)
		}
	}
	if len(got.Nested) != 1 || got.Nested[0] != "parked-worktree/" {
		t.Errorf("nested = %v, want [parked-worktree/]", got.Nested)
	}
}

func TestDecideDeployRelease_SourcePinChangeCuts(t *testing.T) {
	// The release froze a pinned frontend at one commit; its ref resolves to another today.
	frozen := release.Artifact{Kind: release.KindGit, Mode: release.ModeSource,
		Source: &release.Source{Repo: "github.com/acme/web", Ref: "main", Commit: strings.Repeat("1", 40)}}
	dir, ledger := decisionCheckoutWith(t, func(r *release.Release) { r.Artifacts["web"] = frozen })

	resolved := strings.Repeat("1", 40)
	prev := resolveFrontendPinArtifacts
	resolveFrontendPinArtifacts = func(_ context.Context, _ string, _ *KCLEntities, out map[string]release.Artifact) error {
		moved := frozen
		moved.Source = &release.Source{Repo: "github.com/acme/web", Ref: "main", Commit: resolved}
		out["web"] = moved
		return nil
	}
	t.Cleanup(func() { resolveFrontendPinArtifacts = prev })

	if got := decide(t, dir, ledger); got.Reuse == nil {
		t.Fatalf("an unmoved pin must still reuse: %s", got.Reason)
	}
	resolved = strings.Repeat("2", 40)
	got := decide(t, dir, ledger)
	if got.Reuse != nil {
		t.Fatalf("reused %s although a source pin moved", got.Reuse.Version)
	}
	for _, want := range []string{"web", "resolves to 222222222222", "froze 111111111111"} {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("reason is missing %q:\n%s", want, got.Reason)
		}
	}
}

func TestDecideDeployRelease_NoReleaseCuts(t *testing.T) {
	dir, ledger := decisionCheckout(t)
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}
	decisionGit(t, dir, "add", "-A")
	decisionGit(t, dir, "commit", "-q", "-m", "next")
	got := decide(t, dir, ledger)
	if got.Reuse != nil || !strings.Contains(got.Reason, "no release in") {
		t.Fatalf("choice = %+v, want a cut because no release records this tree", got)
	}
}

// THE DRY RUN PREDICTS THE REAL RUN: for each state of the checkout, the
// preview prints the real run's decision, and the real run (build stubbed)
// then does exactly that.
func TestDryRunPredictsRealRun(t *testing.T) {
	cases := []struct {
		name       string
		setup      func(t *testing.T, dir string)
		wantReuse  bool
		wantInLine []string
	}{
		{"clean", func(*testing.T, string) {}, true,
			[]string{"would reuse release 20261012.030000-aaaaaaaaaaaa", "nothing is built"}},
		{"nested worktree only", func(t *testing.T, dir string) {
			decisionGit(t, dir, "worktree", "add", "-q", "-b", "parked", filepath.Join(dir, "parked"))
		}, true, []string{"would reuse release", "nested git checkouts", "parked/"}},
		{"untracked file", func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("wip"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, false, []string{"would cut new release", "uncommitted changes (notes.txt)", "builds and pushes every artifact env prod declares"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, ledger := decisionCheckout(t)
			tc.setup(t, dir)

			var preview string
			report := newDeployReport("prod", true)
			preview = captureStdout(t, func() {
				previewDeployDecision(context.Background(), "prod", deployCmdFlags{dryRun: true}, report)
			})
			_ = preview
			// The text preview (not --json) is what an operator reads.
			text := captureStdout(t, func() {
				previewDeployDecision(context.Background(), "prod", deployCmdFlags{dryRun: true}, nil)
			})
			for _, want := range tc.wantInLine {
				if !strings.Contains(text, want) {
					t.Errorf("dry-run output is missing %q:\n%s", want, text)
				}
			}
			if strings.Count(text, "[deploy] would ") != 1 {
				t.Errorf("want exactly one decision line:\n%s", text)
			}

			dec := report.document().ReleaseDecision
			if dec == nil {
				t.Fatal("--json dry-run carries no release_decision")
			}
			if (dec.Action == "reuse") != tc.wantReuse {
				t.Errorf("json action = %q, want reuse=%v", dec.Action, tc.wantReuse)
			}

			// The real run does what the preview said.
			var builds []buildOptions
			prev := runDeployBuild
			runDeployBuild = func(_ context.Context, o buildOptions) error {
				builds = append(builds, o)
				rel := ociRelease(o.release, map[string]string{deployTestImage: sha("2")})
				rel.SetProvenance(captureReleaseProvenance(context.Background(), dir))
				_, err := testStore(t, dir).CutRelease(rel)
				return err
			}
			t.Cleanup(func() { runDeployBuild = prev })
			var real deployCutResult
			var err error
			out := captureStdout(t, func() {
				real, err = buildAndCutForDeploy(context.Background(), dir, "prod", deployCmdFlags{promote: promoteCmdFlags{yes: true}}, ledger)
			})
			if err != nil {
				t.Fatal(err)
			}
			if real.Reused != tc.wantReuse || (len(builds) == 0) != tc.wantReuse {
				t.Errorf("real run reused=%v builds=%d, preview said reuse=%v", real.Reused, len(builds), tc.wantReuse)
			}
			if dec.Version != real.Version && !strings.HasPrefix(dec.Version, real.Version[:8]) {
				t.Errorf("preview version %q vs real %q", dec.Version, real.Version)
			}
			// Same sentence, different tense.
			realLine := strings.TrimSpace(out)
			if tc.wantReuse && !strings.HasPrefix(realLine, "[deploy] reusing release "+real.Version) {
				t.Errorf("real run line = %q", realLine)
			}
			if !tc.wantReuse && !strings.Contains(realLine, "because "+dec.Reason) {
				t.Errorf("real run's reason differs from the preview's:\n real: %s\n preview reason: %s", realLine, dec.Reason)
			}
		})
	}
}

func TestEnvIsShared(t *testing.T) {
	cluster := func(ctx string) *KCLEntities {
		return &KCLEntities{Workloads: []WorkloadEntity{{Name: "api", Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{Cluster: ctx}}}}}
	}
	for _, tc := range []struct {
		name string
		e    *KCLEntities
		want bool
	}{
		{"nil", nil, false},
		{"k3d dev cluster", cluster("k3d-dev"), false},
		{"gke prod cluster", cluster("gke_acme_us-central1_prod"), true},
		{"cluster workload naming no cluster", cluster(""), true},
		{"host only", &KCLEntities{Workloads: []WorkloadEntity{{Name: "api", Runtime: RuntimeEntity{Type: RuntimeHost}}}}, false},
		{"hosted", &KCLEntities{Workloads: []WorkloadEntity{{Name: "api", Runtime: RuntimeEntity{Type: RuntimeHosted}}}}, true},
	} {
		if got := envIsShared(tc.e); got != tc.want {
			t.Errorf("%s: envIsShared = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestGateDirtyCut(t *testing.T) {
	choice := deployReleaseChoice{Version: "20261012.030000-cccccccccccc", Dirty: true}
	var out bytes.Buffer
	exitCode := func(err error) int {
		var ec exitCodeError
		if errors.As(err, &ec) {
			return ec.code
		}
		return -1
	}

	// No terminal, no flag: refused BEFORE a build, with exit 5 and the way out.
	err := gateDirtyCut("prod", choice, deployConfirm{}, &out)
	if exitCode(err) != exitPlanUnconfirmed {
		t.Fatalf("no TTY, no --yes: err = %v, want exit %d", err, exitPlanUnconfirmed)
	}
	for _, want := range []string{"dirty_cut_unconfirmed", "Nothing was built", "--yes", "--dry-run"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal is missing %q:\n%v", want, err)
		}
	}
	// Consent forms proceed.
	for name, c := range map[string]deployConfirm{
		"--yes": {Yes: true}, "--approve": {Approve: "abc"}, "--plan-only": {PlanOnly: true},
	} {
		if err := gateDirtyCut("prod", choice, c, &out); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A terminal is asked, defaulting to no.
	var asked string
	c := deployConfirm{Interactive: true, prompt: func(q string) (bool, error) { asked = q; return false, nil }}
	if err := gateDirtyCut("prod", choice, c, &out); exitCode(err) != exitPlanUnconfirmed {
		t.Errorf("declined prompt: err = %v", err)
	}
	if !strings.Contains(asked, choice.Version) || !strings.Contains(asked, "uncommitted") {
		t.Errorf("prompt = %q", asked)
	}
	c.prompt = func(string) (bool, error) { return true, nil }
	if err := gateDirtyCut("prod", choice, c, &out); err != nil {
		t.Errorf("accepted prompt: %v", err)
	}
}

// Only a dirty cut on a shared env asks; a local env and a reuse never do.
func TestDirtyCutNeedsConfirmation(t *testing.T) {
	dir := deployAllProject(t)
	dirty := deployReleaseChoice{Version: "v", Dirty: true}
	render := func(ctx string) {
		body := strings.ReplaceAll(deployTestEnvRender, "k3d-dev", ctx)
		t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, body))
	}
	render("gke_acme_us-central1_prod")
	if !dirtyCutNeedsConfirmation(context.Background(), dir, "prod", dirty) {
		t.Error("a dirty cut on a gke cluster must ask")
	}
	if dirtyCutNeedsConfirmation(context.Background(), dir, "prod", deployReleaseChoice{Version: "v"}) {
		t.Error("a clean cut must not ask")
	}
	if dirtyCutNeedsConfirmation(context.Background(), dir, "prod", deployReleaseChoice{Reuse: &release.Release{}, Dirty: true}) {
		t.Error("a reuse builds nothing and must not ask")
	}
	render("k3d-dev")
	if dirtyCutNeedsConfirmation(context.Background(), dir, "prod", dirty) {
		t.Error("a dirty cut on a local k3d cluster must not ask")
	}
}

// The gate is wired into the real run: on a shared env a dirty cut is refused
// BEFORE the build starts, and --yes lets it through.
func TestBuildAndCutForDeploy_DirtyCutOnSharedEnvIsGatedBeforeTheBuild(t *testing.T) {
	dir, ledger := decisionCheckout(t)
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, strings.ReplaceAll(deployTestEnvRender, "k3d-dev", "gke_acme_us-central1_prod")))
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}
	var builds int
	prev := runDeployBuild
	runDeployBuild = func(_ context.Context, o buildOptions) error {
		builds++
		rel := ociRelease(o.release, map[string]string{deployTestImage: sha("2")})
		rel.SetProvenance(captureReleaseProvenance(context.Background(), dir))
		_, err := testStore(t, dir).CutRelease(rel)
		return err
	}
	t.Cleanup(func() { runDeployBuild = prev })

	var err error
	out := captureStdout(t, func() {
		_, err = buildAndCutForDeploy(context.Background(), dir, "prod", deployCmdFlags{}, ledger)
	})
	var ec exitCodeError
	if !errors.As(err, &ec) || ec.code != exitPlanUnconfirmed {
		t.Fatalf("err = %v, want exit %d", err, exitPlanUnconfirmed)
	}
	if builds != 0 {
		t.Fatalf("the build ran %d times before the dirty cut was confirmed", builds)
	}
	if !strings.Contains(out, "[deploy] cutting new release") || !strings.Contains(out, "notes.txt") {
		t.Errorf("the decision line must precede the refusal and name the file:\n%s", out)
	}

	_ = captureStdout(t, func() {
		_, err = buildAndCutForDeploy(context.Background(), dir, "prod", deployCmdFlags{promote: promoteCmdFlags{yes: true}}, ledger)
	})
	if err != nil || builds != 1 {
		t.Fatalf("with --yes: err=%v builds=%d, want one build", err, builds)
	}
}
