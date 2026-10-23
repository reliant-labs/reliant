package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

// The 2026-10-09 prod incident, end to end through the command tree, against
// a real git checkout:
//
//  1. `forge env build prod --release 20261009.205925-d29da50b` cut a release
//     from a clean checkout and recorded it;
//  2. `forge env deploy prod`, with NO version, in the same unchanged
//     checkout, must reuse that release — whatever it is named — and build
//     nothing.
//
// What actually happened: the deploy rebuilt every image and named a new
// release, because the hosted ledger's read path dropped the provenance the
// cut had sent, so no release ever matched the checkout. The control plane
// held the tree all along (deploy_releases.git_tree = 46b1851c4168…, the
// checkout's HEAD^{tree}); forge never read it back.
//
// Run against BOTH ledgers: the control plane's (prod's) and the machine's.
// The provenance is CAPTURED, not stubbed — the checkout is a real git
// repository — so this also pins that the cut records the tree the deploy
// computes. --plan-only, so nothing is promoted: the subject is what the
// deploy decides BEFORE it would build.
func TestDeployNoVersion_ReusesTheReleaseEnvBuildCut(t *testing.T) {
	if testing.Short() {
		t.Skip("drives `env build` and `env deploy` through the command tree against a git checkout; skipped in -short")
	}
	const cutVersion = "20261009.205925-d29da50b"
	fake := newFakeDeployService(map[string]string{"prod": "env-prod-uuid"})
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	for _, tc := range []struct {
		name   string
		render string
	}{
		{
			name: "control-plane ledger",
			render: fmt.Sprintf(`{"output":{"control_plane":{"type":"control_plane","endpoint":%q,"token_env":"FORGE_E2E_CP_TOKEN"},`+
				`"workloads":[{"name":"api","kind":"service","image":%q,"runtime":{"type":"hosted"},"spec":{"kind":"service"}}]}}`,
				srv.URL, deployTestImage),
		},
		{name: "machine ledger", render: deployTestEnvRender},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A clean checkout. .forge/ is ignored, as it is in every forge
			// project, so the build state and bundles the commands write do
			// not dirty it. ensureLedgerReady names the project in forge.yaml
			// BEFORE the commit, so that write does not dirty it either.
			dir := t.TempDir()
			ensureLedgerReady(t, dir)
			initGitRepo(t, dir, map[string]string{
				".gitignore":             ".forge/\nbin/\n",
				"deploy/kcl/prod/main.k": "# fixture; the render comes from FORGE_KCL_RENDER_FIXTURE\n",
			})
			headTree, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD^{tree}").Output()
			if err != nil {
				t.Fatal(err)
			}
			tree := strings.TrimSpace(string(headTree))
			t.Chdir(dir)
			t.Setenv("FORGE_E2E_CP_TOKEN", "rlat_e2e")
			t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, tc.render))
			// The build state the cut's build left behind.
			if err := WriteBuildState(dir, "prod", BuildState{
				Image: deployTestImage, Tag: cutVersion, Pushed: true, PushedAt: nowRFC3339(), Digest: sha("1"),
			}); err != nil {
				t.Fatal(err)
			}
			// No registry or cluster stands behind this fixture. The registry
			// serves the cut's image; the platform guard and the apply have
			// their own tests.
			prevResolves, prevPlatforms, prevApply := releaseImageResolves, hostedPlatformResolver, runPromoteClientDeploy
			releaseImageResolves = func(_ context.Context, ref string) (bool, error) {
				return ref == deployTestImage+"@"+sha("1"), nil
			}
			hostedPlatformResolver = func(context.Context, string) ([]string, error) { return []string{"linux/amd64"}, nil }
			runPromoteClientDeploy = func(_ context.Context, _ string, opts deployOptions) error {
				// The deployability preflight runs as a dry run before any
				// record (preflightBeforeRecord); anything else is an apply.
				if !opts.dryRun {
					t.Error("--plan-only applied something")
				}
				return nil
			}
			t.Cleanup(func() {
				releaseImageResolves, hostedPlatformResolver, runPromoteClientDeploy = prevResolves, prevPlatforms, prevApply
			})

			run := func(args ...string) (string, error) {
				root := newTestRootCmd(t)
				var buf bytes.Buffer
				root.SetOut(&buf)
				root.SetErr(&buf)
				root.SetArgs(args)
				var err error
				out := captureStdout(t, func() { err = root.Execute() })
				return out + buf.String(), err
			}
			releases := func() []string {
				t.Helper()
				ledger, err := ledgerFor(context.Background(), dir, "prod")
				if err != nil {
					t.Fatalf("ledger: %v", err)
				}
				list, err := ledger.Releases.List(context.Background())
				if err != nil {
					t.Fatalf("list releases: %v", err)
				}
				var versions []string
				for _, rel := range list {
					versions = append(versions, rel.Version)
				}
				return versions
			}

			// 1. The cut, by `forge env build --release` (--no-build: the
			// images are the build state above).
			if out, err := run("env", "build", "prod", "--release", cutVersion, "--no-build"); err != nil {
				t.Fatalf("forge env build --release: %v\n%s", err, out)
			}
			ledger, err := ledgerFor(context.Background(), dir, "prod")
			if err != nil {
				t.Fatal(err)
			}
			cut, err := ledger.Releases.Get(context.Background(), cutVersion)
			if err != nil || cut == nil {
				t.Fatalf("release %s was not recorded: %v", cutVersion, err)
			}
			// The ledger hands back the checkout's tree, exactly as prod's
			// control plane held it.
			if p := cut.Provenance; p == nil || p.Tree != tree || p.Dirty {
				t.Fatalf("the ledger returned provenance %+v for the cut, want the clean tree %s", p, tree)
			}

			// 2. The no-version deploy. Its build must never run.
			var builds []buildOptions
			prevBuild := runDeployBuild
			runDeployBuild = func(_ context.Context, opts buildOptions) error {
				builds = append(builds, opts)
				return nil
			}
			t.Cleanup(func() { runDeployBuild = prevBuild })

			out, err := run("env", "deploy", "prod", "--plan-only")
			if len(builds) != 0 {
				t.Fatalf("the deploy built %d time(s), cutting %q; a checkout that already has a release must build nothing:\n%s",
					len(builds), builds[0].release, out)
			}
			if err != nil {
				t.Fatalf("forge env deploy prod --plan-only: %v\n%s", err, out)
			}
			if want := "[deploy] reusing release " + cutVersion + " (cut at "; !strings.Contains(out, want) {
				t.Errorf("the deploy did not say up front that it reuses %s (want %q):\n%s", cutVersion, want, out)
			}
			if strings.Contains(out, "cutting new release") {
				t.Errorf("the deploy announced a new cut:\n%s", out)
			}
			if got := releases(); len(got) != 1 || got[0] != cutVersion {
				t.Errorf("releases after the deploy = %v, want only %s — the deploy cut a second release of the same checkout", got, cutVersion)
			}
			if want := "--plan-only: release " + cutVersion; !strings.Contains(out, want) {
				t.Errorf("the plan does not name the reused release (want %q):\n%s", want, out)
			}
			if _, bound, err := ledger.Bindings.Current(context.Background(), "prod"); err != nil || bound {
				t.Errorf("--plan-only bound prod (bound=%v, err=%v)", bound, err)
			}
		})
	}
}
