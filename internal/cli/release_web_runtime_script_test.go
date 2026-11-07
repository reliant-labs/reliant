// File: internal/cli/release_web_runtime_script_test.go
//
// Exercises scripts/release-web-runtime.sh, which publishes
// @reliantlabs/forge-web-runtime for a forge commit with a COMPUTED version,
// and its use by scripts/release-forge.sh.
//
// What matters most:
//
//   - the version: a no-op version is never published (tests, and the
//     bookkeeping's own version sync, are not changes), and the bump keeps a
//     released forge's `^` floor resolving newer compatible runtimes;
//   - a failure is loud and final: a publish that did not reach npm exits
//     non-zero on every re-run until npm serves it, and a forge release whose
//     runtime cannot be published stops before forge's tag.
//
// The registry, gh and npm are stubs; origins are local bare repos, so nothing
// is published. Every test execs bash and git, so they skip in -short mode like
// the other release script tests.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func releaseWebRuntimeScriptPath(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	p := filepath.Join(cwd, "..", "..", "scripts", "release-web-runtime.sh")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("release-web-runtime.sh not found at %s: %v", p, err)
	}
	return p
}

// wrFixtureManifest is web-runtime/package.json at version v, laid out as
// prettier writes it (two-space indent: the top-level fields).
func wrFixtureManifest(v string) string {
	return "{\n  \"name\": \"@reliantlabs/forge-web-runtime\",\n  \"version\": \"" + v + "\",\n  \"type\": \"module\",\n  \"dependencies\": {\n    \"x\": \"^1.0.0\"\n  }\n}\n"
}

// wrFixtureLock is web-runtime/package-lock.json at version v: the lock's
// own version and its root package's.
func wrFixtureLock(v string) string {
	return "{\n  \"name\": \"@reliantlabs/forge-web-runtime\",\n  \"version\": \"" + v + "\",\n  \"lockfileVersion\": 3,\n  \"packages\": {\n    \"\": {\n      \"name\": \"@reliantlabs/forge-web-runtime\",\n      \"version\": \"" + v + "\"\n    },\n    \"node_modules/x\": {\n      \"version\": \"1.0.0\"\n    }\n  }\n}\n"
}

// wrFixtureFloor is internal/generator/frontend_webruntime.go declaring the
// scaffold floor ^v.
func wrFixtureFloor(v string) string {
	return "package generator\n\nconst webRuntimePublishedRange = \"^" + v + "\"\n"
}

// writeFiles writes rel → content under root.
func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// commitAndPush commits files on main and pushes main to origin.
func commitAndPush(t *testing.T, repo, msg string, files map[string]string) string {
	t.Helper()
	writeFiles(t, repo, files)
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "-c", "user.name=fixture", "-c", "user.email=fixture@example.com", "commit", "-q", "-m", msg)
	gitIn(t, repo, "push", "-q", "origin", "HEAD:refs/heads/main")
	return gitOut(t, repo, "rev-parse", "HEAD")
}

// tagWebRuntime tags HEAD web-runtime/v<v> and pushes the tag.
func tagWebRuntime(t *testing.T, repo, v string) {
	t.Helper()
	gitIn(t, repo, "-c", "user.name=fixture", "-c", "user.email=fixture@example.com", "tag", "-a", "web-runtime/v"+v, "-m", "web-runtime "+v)
	gitIn(t, repo, "push", "-q", "origin", "refs/tags/web-runtime/v"+v)
}

// newWebRuntimeReleaseRepo is a forge-shaped repo whose web runtime was last
// released as 0.13.1, tagged — as every release is — on a commit whose
// manifest still names the version before it (the bookkeeping that syncs it
// lands after the tag). Then the bookkeeping commit itself, which is NOT a
// change to what ships. Its origin is a local bare repo.
func newWebRuntimeReleaseRepo(t *testing.T) string {
	t.Helper()
	for _, bin := range []string{"bash", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"web-runtime/package.json":                  wrFixtureManifest("0.13.0"),
		"web-runtime/package-lock.json":             wrFixtureLock("0.13.0"),
		"web-runtime/src/index.ts":                  "export const one = 1;\n",
		"web-runtime/src/index.test.ts":             "// a test\n",
		"internal/generator/frontend_webruntime.go": wrFixtureFloor("0.13.0"),
		"README.md": "forge\n",
	})
	gitIn(t, root, "init", "-q", "-b", "main")
	gitIn(t, root, "add", ".")
	gitIn(t, root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.com", "commit", "-q", "-m", "fixture")
	origin := filepath.Join(t.TempDir(), "origin.git")
	gitIn(t, root, "init", "-q", "--bare", origin)
	gitIn(t, root, "remote", "add", "origin", origin)
	gitIn(t, root, "push", "-q", "origin", "HEAD:refs/heads/main")
	tagWebRuntime(t, root, "0.13.1")
	commitAndPush(t, root, "chore: release v1.0.15", map[string]string{
		"web-runtime/package.json":                  wrFixtureManifest("0.13.1"),
		"web-runtime/package-lock.json":             wrFixtureLock("0.13.1"),
		"internal/generator/frontend_webruntime.go": wrFixtureFloor("0.13.1"),
	})
	return root
}

// webRuntimeStubs is a PATH directory holding an npm that logs every call to
// $FAKE_NPM_LOG and fails any whose arguments contain $FAKE_NPM_FAIL, and a gh
// whose `run list` reports one finished publish run ending $FAKE_GH_CONCLUSION
// (none at all when it is empty). It also writes the NPM_VIEW_CMD probe:
// the registry serves a version only when $FAKE_NPM_SERVES is 1.
func webRuntimeStubs(t *testing.T) (pathDir, viewCmd string) {
	t.Helper()
	dir := t.TempDir()
	stubs := map[string]string{
		"npm": `#!/usr/bin/env bash
[ -z "${FAKE_NPM_LOG:-}" ] || echo "$*" >>"$FAKE_NPM_LOG"
if [ -n "${FAKE_NPM_FAIL:-}" ] && [[ "$*" == *"$FAKE_NPM_FAIL"* ]]; then echo "npm stub: $* failed" >&2; exit 1; fi
case "$1" in
  pack) echo '[{"files":[{"path":"dist/index.js"},{"path":"dist/index.d.ts"},{"path":"LICENSE"},{"path":"NOTICE"}]}]' ;;
esac
exit 0
`,
		"gh": `#!/usr/bin/env bash
if [ "$1 $2" = "run list" ] && [ -n "${FAKE_GH_CONCLUSION:-}" ]; then
  echo "4242 completed $FAKE_GH_CONCLUSION https://github.com/reliant-labs/forge/actions/runs/4242"
fi
exit 0
`,
		"view.sh": `#!/usr/bin/env bash
[ "${FAKE_NPM_SERVES:-0}" = 1 ] && { echo stub-version; exit 0; }
echo "npm error code E404" >&2
exit 1
`,
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir, "bash " + filepath.Join(dir, "view.sh")
}

// webRuntimeEnv is a hermetic environment for the script: the stubs first on
// PATH, every wait shrunk to seconds, plus extra.
func webRuntimeEnv(t *testing.T, extra ...string) []string {
	t.Helper()
	stubDir, viewCmd := webRuntimeStubs(t)
	env := append(os.Environ(),
		"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FORGE_RELEASE_GH="+filepath.Join(stubDir, "gh"),
		"NPM_VIEW_CMD="+viewCmd,
		"NPM_VERIFY_MAX_SECONDS=2", "NPM_VERIFY_FIRST_DELAY=1", "NPM_VERIFY_MAX_DELAY=1",
		"FORGE_WEB_RUNTIME_POLL=1", "FORGE_WEB_RUNTIME_RUN_TIMEOUT=2",
	)
	return append(env, extra...)
}

type webRuntimeResult struct {
	Schema      string `json:"schema"`
	Package     string `json:"package"`
	Commit      string `json:"commit"`
	Changed     bool   `json:"changed"`
	Previous    string `json:"previous"`
	PreviousTag string `json:"previous_tag"`
	Version     string `json:"version"`
	Tag         string `json:"tag"`
	Bump        string `json:"bump"`
	State       string `json:"state"`
	Detail      string `json:"detail"`
}

// runWebRuntimeScript runs the script against repo; it returns the parsed
// last stdout line (which must be the only stdout line), stderr, and the
// exit error.
func runWebRuntimeScript(t *testing.T, repo string, env []string, args ...string) (webRuntimeResult, string, error) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "bash", append([]string{releaseWebRuntimeScriptPath(t), "--repo", repo}, args...)...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("stdout must be exactly the JSON result line, got %d lines:\n%s\nstderr:\n%s", len(lines), stdout.String(), stderr.String())
	}
	var res webRuntimeResult
	if jerr := json.Unmarshal([]byte(lines[0]), &res); jerr != nil {
		t.Fatalf("the result line is not JSON: %v\n%s\nstderr:\n%s", jerr, lines[0], stderr.String())
	}
	if res.Schema != "web-runtime-release/v1" {
		t.Fatalf("schema = %q, want web-runtime-release/v1", res.Schema)
	}
	return res, stderr.String(), err
}

// TestReleaseWebRuntime_ComputesTheVersion: the decision, in --plan mode (no
// worktree, no build, no tag).
func TestReleaseWebRuntime_ComputesTheVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the web-runtime release script over a fixture git repo")
	}
	cases := []struct {
		name        string
		setup       func(t *testing.T, repo string)
		wantChanged bool
		wantVersion string
		wantBump    string
	}{
		{
			name:        "the bookkeeping's own version sync is not a change",
			setup:       func(*testing.T, string) {},
			wantVersion: "0.13.1", wantBump: "none",
		},
		{
			name: "a test-only change ships nothing",
			setup: func(t *testing.T, repo string) {
				commitAndPush(t, repo, "test(web-runtime): cover more", map[string]string{"web-runtime/src/index.test.ts": "// more tests\n"})
			},
			wantVersion: "0.13.1", wantBump: "none",
		},
		{
			name: "a change outside web-runtime ships nothing",
			setup: func(t *testing.T, repo string) {
				commitAndPush(t, repo, "feat(cli)!: something else", map[string]string{"README.md": "changed\n"})
			},
			wantVersion: "0.13.1", wantBump: "none",
		},
		{
			name: "a fix is a patch",
			setup: func(t *testing.T, repo string) {
				commitAndPush(t, repo, "fix(web-runtime): name spans", map[string]string{"web-runtime/src/index.ts": "export const one = 2;\n"})
			},
			wantChanged: true, wantVersion: "0.13.2", wantBump: "patch",
		},
		{
			// Before 1.0 a caret range admits patches only, so a feature that
			// bumped the minor would leave every released forge's ^0.13.x floor
			// on the runtime before it.
			name: "before 1.0 a feature is a patch",
			setup: func(t *testing.T, repo string) {
				commitAndPush(t, repo, "feat(web-runtime): report client hints", map[string]string{"web-runtime/src/index.ts": "export const two = 2;\n"})
			},
			wantChanged: true, wantVersion: "0.13.2", wantBump: "patch",
		},
		{
			// A breaking change raises the scaffold floor in the same pull
			// request (TestReleaseWebRuntime_RefusesAFloorThatCannotResolveTheRelease).
			name: "before 1.0 a breaking title bumps the minor",
			setup: func(t *testing.T, repo string) {
				commitAndPush(t, repo, "feat(web-runtime)!: drop the old session API", map[string]string{
					"web-runtime/src/index.ts": "export {};\n", "internal/generator/frontend_webruntime.go": wrFixtureFloor("0.14.0"),
				})
			},
			wantChanged: true, wantVersion: "0.14.0", wantBump: "minor",
		},
		{
			name: "a BREAKING CHANGE footer bumps the minor",
			setup: func(t *testing.T, repo string) {
				commitAndPush(t, repo, "refactor(web-runtime): one session store\n\nBREAKING CHANGE: Session.legacy is gone.", map[string]string{
					"web-runtime/src/index.ts": "export {};\n", "internal/generator/frontend_webruntime.go": wrFixtureFloor("0.14.0"),
				})
			},
			wantChanged: true, wantVersion: "0.14.0", wantBump: "minor",
		},
		{
			name: "a dependency change in the manifest is a change",
			setup: func(t *testing.T, repo string) {
				commitAndPush(t, repo, "chore(deps): bump x", map[string]string{
					"web-runtime/package.json": strings.Replace(wrFixtureManifest("0.13.1"), "^1.0.0", "^1.1.0", 1),
				})
			},
			wantChanged: true, wantVersion: "0.13.2", wantBump: "patch",
		},
		{
			name: "a declared version is a floor",
			setup: func(t *testing.T, repo string) {
				commitAndPush(t, repo, "fix(web-runtime): x", map[string]string{
					"web-runtime/package.json":                  wrFixtureManifest("0.15.0"),
					"web-runtime/src/index.ts":                  "export const three = 3;\n",
					"internal/generator/frontend_webruntime.go": wrFixtureFloor("0.15.0"),
				})
			},
			wantChanged: true, wantVersion: "0.15.0", wantBump: "declared",
		},
		{
			// Only the manifest and lock versions changed, which shipped_differs
			// ignores: the declared version is still a floor, not lowered to
			// the previous release.
			name: "a declared version alone is published",
			setup: func(t *testing.T, repo string) {
				commitAndPush(t, repo, "chore(web-runtime): declare 0.13.5", map[string]string{
					"web-runtime/package.json":      wrFixtureManifest("0.13.5"),
					"web-runtime/package-lock.json": wrFixtureLock("0.13.5"),
				})
			},
			wantChanged: true, wantVersion: "0.13.5", wantBump: "declared",
		},
		{
			name: "a declared version alone raises the floor with it",
			setup: func(t *testing.T, repo string) {
				commitAndPush(t, repo, "chore(web-runtime)!: declare 0.14.0", map[string]string{
					"web-runtime/package.json":                  wrFixtureManifest("0.14.0"),
					"web-runtime/package-lock.json":             wrFixtureLock("0.14.0"),
					"internal/generator/frontend_webruntime.go": wrFixtureFloor("0.14.0"),
				})
			},
			wantChanged: true, wantVersion: "0.14.0", wantBump: "declared",
		},
		{
			name: "from 1.0 a feature is a minor",
			setup: func(t *testing.T, repo string) {
				commitAndPush(t, repo, "chore: one", map[string]string{"web-runtime/package.json": wrFixtureManifest("1.2.3"), "web-runtime/src/index.ts": "export const v1 = 1;\n", "internal/generator/frontend_webruntime.go": wrFixtureFloor("1.2.3")})
				tagWebRuntime(t, repo, "1.2.3")
				commitAndPush(t, repo, "feat(web-runtime): more", map[string]string{"web-runtime/src/index.ts": "export const v1 = 2;\n"})
			},
			wantChanged: true, wantVersion: "1.3.0", wantBump: "minor",
		},
		{
			name: "from 1.0 a breaking change is a major",
			setup: func(t *testing.T, repo string) {
				commitAndPush(t, repo, "chore: one", map[string]string{"web-runtime/package.json": wrFixtureManifest("1.2.3"), "web-runtime/src/index.ts": "export const v1 = 1;\n", "internal/generator/frontend_webruntime.go": wrFixtureFloor("1.2.3")})
				tagWebRuntime(t, repo, "1.2.3")
				commitAndPush(t, repo, "fix(web-runtime)!: less", map[string]string{"web-runtime/src/index.ts": "export {};\n", "internal/generator/frontend_webruntime.go": wrFixtureFloor("2.0.0")})
			},
			wantChanged: true, wantVersion: "2.0.0", wantBump: "major",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newWebRuntimeReleaseRepo(t)
			tc.setup(t, repo)
			res, stderr, err := runWebRuntimeScript(t, repo, webRuntimeEnv(t), "--plan")
			if err != nil {
				t.Fatalf("plan failed: %v\n%s", err, stderr)
			}
			if res.State != "planned" || res.Changed != tc.wantChanged || res.Version != tc.wantVersion || res.Bump != tc.wantBump {
				t.Errorf("got state=%s changed=%v version=%s bump=%s, want planned changed=%v version=%s bump=%s\n%s",
					res.State, res.Changed, res.Version, res.Bump, tc.wantChanged, tc.wantVersion, tc.wantBump, stderr)
			}
			if res.Tag != "web-runtime/v"+res.Version || res.Package != "@reliantlabs/forge-web-runtime" {
				t.Errorf("tag=%s package=%s for version %s", res.Tag, res.Package, res.Version)
			}
			if res.Commit != gitOut(t, repo, "rev-parse", "origin/main") && res.Commit != gitOut(t, repo, "rev-parse", "HEAD") {
				t.Errorf("planned for %s, want origin/main", res.Commit)
			}
			// The plan reads git only: no worktree, no tag, and the shared
			// checkout's refs are as it left them.
			assertNoReleaseWorktree(t, repo)
			if refs := gitOut(t, repo, "for-each-ref", "refs/web-runtime-release/"); refs != "" {
				t.Errorf("the script left its private refs behind:\n%s", refs)
			}
		})
	}
}

// TestReleaseWebRuntime_RefusesAFloorThatCannotResolveTheRelease: forge's
// release ships the generator of the commit it tags, so a breaking runtime
// whose commit still declares the old caret window (^0.13.1 for 0.14.0, ^1.2.3
// for 2.0.0) would release a forge scaffolding a range that cannot install
// the runtime its code needs. The plan refuses it, before any publish.
func TestReleaseWebRuntime_RefusesAFloorThatCannotResolveTheRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the web-runtime release script over a fixture git repo")
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, repo string)
		floor string
	}{
		{
			name: "before 1.0 a breaking change with the old minor's floor",
			setup: func(t *testing.T, repo string) {
				commitAndPush(t, repo, "feat(web-runtime)!: drop the old session API", map[string]string{"web-runtime/src/index.ts": "export {};\n"})
			},
			floor: "^0.13.1",
		},
		{
			name: "from 1.0 a breaking change with the old major's floor",
			setup: func(t *testing.T, repo string) {
				commitAndPush(t, repo, "chore: one", map[string]string{"web-runtime/package.json": wrFixtureManifest("1.2.3"), "web-runtime/src/index.ts": "export const v1 = 1;\n", "internal/generator/frontend_webruntime.go": wrFixtureFloor("1.2.3")})
				tagWebRuntime(t, repo, "1.2.3")
				commitAndPush(t, repo, "fix(web-runtime)!: less", map[string]string{"web-runtime/src/index.ts": "export {};\n"})
			},
			floor: "^1.2.3",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newWebRuntimeReleaseRepo(t)
			tc.setup(t, repo)
			res, stderr, err := runWebRuntimeScript(t, repo, webRuntimeEnv(t), "--plan")
			if err == nil || res.State != "failed" || !strings.Contains(res.Detail, "declares "+tc.floor+", which does not admit") {
				t.Errorf("want the %s floor refused, got err=%v %+v\n%s", tc.floor, err, res, stderr)
			}
		})
	}
}

// TestReleaseWebRuntime_RefusesAVersionArgument: the version is computed. A
// hand-typed one is refused, and the refusal is still a JSON result.
func TestReleaseWebRuntime_RefusesAVersionArgument(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the web-runtime release script")
	}
	repo := newWebRuntimeReleaseRepo(t)
	res, stderr, err := runWebRuntimeScript(t, repo, webRuntimeEnv(t), "v0.13.2")
	if err == nil || res.State != "failed" || !strings.Contains(stderr, "computed from the commit") {
		t.Errorf("want a refusal naming the computed version, got err=%v state=%s\n%s", err, res.State, stderr)
	}
}

// TestReleaseWebRuntime_DryRunValidatesAndTagsNothing: a dry run builds,
// typechecks, tests and packs the exact version it would publish, and leaves
// no tag anywhere.
func TestReleaseWebRuntime_DryRunValidatesAndTagsNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the web-runtime release script over a fixture git repo")
	}
	repo := newWebRuntimeReleaseRepo(t)
	commitAndPush(t, repo, "fix(web-runtime): x", map[string]string{"web-runtime/src/index.ts": "export const one = 2;\n"})
	log := filepath.Join(t.TempDir(), "npm.log")
	res, stderr, err := runWebRuntimeScript(t, repo, webRuntimeEnv(t, "FAKE_NPM_LOG="+log), "--dry-run")
	if err != nil || res.State != "validated" || res.Version != "0.13.2" {
		t.Fatalf("dry run: err=%v state=%s version=%s\n%s", err, res.State, res.Version, stderr)
	}
	calls, _ := os.ReadFile(log)
	for _, want := range []string{"ci ", "pkg set version=0.13.2", "run typecheck", "test", "run build", "pack --dry-run --json"} {
		if !strings.Contains(string(calls), want) {
			t.Errorf("the dry run did not run npm %q; calls:\n%s", want, calls)
		}
	}
	if tags := gitOut(t, fixtureOrigin(t, repo), "tag", "-l", "web-runtime/v0.13.2"); tags != "" {
		t.Errorf("a dry run pushed %s", tags)
	}
	if tags := gitOut(t, repo, "tag", "-l", "web-runtime/v0.13.2"); tags != "" {
		t.Errorf("a dry run made the local tag %s", tags)
	}
	assertNoReleaseWorktree(t, repo)
}

// TestReleaseWebRuntime_AnyFailingCheckStopsBeforeTheTag: each npm step of the
// validation is fatal on its own, and none of them leaves a tag. (Each must be
// checked by hand: `set -e` does not reach inside a block whose status is
// tested, which once let a red typecheck through to the tag.)
func TestReleaseWebRuntime_AnyFailingCheckStopsBeforeTheTag(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the web-runtime release script over a fixture git repo")
	}
	for _, step := range []string{"ci ", "run typecheck", "test", "run build", "pack"} {
		t.Run(step, func(t *testing.T) {
			repo := newWebRuntimeReleaseRepo(t)
			commitAndPush(t, repo, "fix(web-runtime): x", map[string]string{"web-runtime/src/index.ts": "export const one = 2;\n"})
			res, stderr, err := runWebRuntimeScript(t, repo, webRuntimeEnv(t, "FAKE_NPM_FAIL="+step))
			if err == nil || res.State != "failed" || !strings.Contains(res.Detail, "does not validate") {
				t.Errorf("npm %q failing: want a validation failure, got err=%v %+v\n%s", step, err, res, stderr)
			}
			if tags := gitOut(t, fixtureOrigin(t, repo), "tag", "-l", "web-runtime/v0.13.2"); tags != "" {
				t.Errorf("npm %q failing still pushed %s", step, tags)
			}
		})
	}
}

// TestReleaseWebRuntime_PublishesThenWaitsForNPM: the release pushes one tag
// on the commit, as the bot, and reports `published` only once the publish
// run passed and the registry serves the version.
func TestReleaseWebRuntime_PublishesThenWaitsForNPM(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the web-runtime release script over a fixture git repo")
	}
	repo := newWebRuntimeReleaseRepo(t)
	commit := commitAndPush(t, repo, "fix(web-runtime): x", map[string]string{"web-runtime/src/index.ts": "export const one = 2;\n"})
	env := webRuntimeEnv(t, "FORGE_RELEASE_GITHUB_REPO=reliant-labs/forge", "FAKE_GH_CONCLUSION=success", "FAKE_NPM_SERVES=1")
	res, stderr, err := runWebRuntimeScript(t, repo, env)
	if err != nil || res.State != "published" || res.Version != "0.13.2" || !res.Changed {
		t.Fatalf("release: err=%v %+v\n%s", err, res, stderr)
	}
	origin := fixtureOrigin(t, repo)
	if got := gitOut(t, origin, "rev-parse", "web-runtime/v0.13.2^{commit}"); got != commit {
		t.Errorf("origin's web-runtime/v0.13.2 is on %s, want %s", got, commit)
	}
	if tagger := gitOut(t, origin, "for-each-ref", "--format=%(taggername) %(taggeremail)", "refs/tags/web-runtime/v0.13.2"); tagger != releaseBotIdentity {
		t.Errorf("tagged by %q, want %q", tagger, releaseBotIdentity)
	}
	// A re-run is idempotent: the commit is released, npm serves it.
	res, stderr, err = runWebRuntimeScript(t, repo, env)
	if err != nil || res.State != "present" || res.Version != "0.13.2" || res.Changed {
		t.Errorf("re-run: err=%v %+v\n%s", err, res, stderr)
	}
}

// TestReleaseWebRuntime_AFailedPublishFailsLoudlyEveryTime: a publish run that
// failed is a failure now and on every re-run, until npm serves the version.
// The tag is never moved to "fix" it.
func TestReleaseWebRuntime_AFailedPublishFailsLoudlyEveryTime(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the web-runtime release script over a fixture git repo")
	}
	repo := newWebRuntimeReleaseRepo(t)
	commit := commitAndPush(t, repo, "fix(web-runtime): x", map[string]string{"web-runtime/src/index.ts": "export const one = 2;\n"})
	env := webRuntimeEnv(t, "FORGE_RELEASE_GITHUB_REPO=reliant-labs/forge", "FAKE_GH_CONCLUSION=failure")
	res, stderr, err := runWebRuntimeScript(t, repo, env)
	if err == nil || res.State != "failed" || !strings.Contains(res.Detail, "ended failure") || !strings.Contains(res.Detail, "never move the tag") {
		t.Fatalf("a failed publish run: err=%v %+v\n%s", err, res, stderr)
	}
	res, stderr, err = runWebRuntimeScript(t, repo, webRuntimeEnv(t, "FORGE_RELEASE_GITHUB_REPO=reliant-labs/forge"))
	if err == nil || res.State != "failed" || !strings.Contains(res.Detail, "does not serve") {
		t.Errorf("a re-run while npm lacks the version must fail: err=%v %+v\n%s", err, res, stderr)
	}
	if got := gitOut(t, fixtureOrigin(t, repo), "rev-parse", "web-runtime/v0.13.2^{commit}"); got != commit {
		t.Errorf("the tag moved to %s", got)
	}
}

// TestReleaseWebRuntime_UnchangedProvesNPMServesIt: nothing changed, so
// nothing is published, but the version consumers will be pinned to must be
// on npm (a tag whose publish failed is the state 0.3.0 shipped in).
func TestReleaseWebRuntime_UnchangedProvesNPMServesIt(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the web-runtime release script over a fixture git repo")
	}
	repo := newWebRuntimeReleaseRepo(t)
	res, stderr, err := runWebRuntimeScript(t, repo, webRuntimeEnv(t, "FORGE_RELEASE_GITHUB_REPO=reliant-labs/forge", "FAKE_NPM_SERVES=1"))
	if err != nil || res.State != "present" || res.Version != "0.13.1" {
		t.Errorf("served: err=%v %+v\n%s", err, res, stderr)
	}
	res, stderr, err = runWebRuntimeScript(t, repo, webRuntimeEnv(t, "FORGE_RELEASE_GITHUB_REPO=reliant-labs/forge"))
	if err == nil || res.State != "failed" {
		t.Errorf("not served: want failed, got err=%v %+v\n%s", err, res, stderr)
	}
	if tags := gitOut(t, fixtureOrigin(t, repo), "tag", "-l", "web-runtime/*"); tags != "web-runtime/v0.13.1" {
		t.Errorf("an unchanged runtime got a new tag: %q", tags)
	}
}

// TestReleaseWebRuntime_RefusesAVersionTakenElsewhere: versions are immutable;
// a computed version some other commit already carries is refused before any
// build, not overwritten.
func TestReleaseWebRuntime_RefusesAVersionTakenElsewhere(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the web-runtime release script over a fixture git repo")
	}
	repo := newWebRuntimeReleaseRepo(t)
	base := gitOut(t, repo, "rev-parse", "HEAD")
	gitIn(t, repo, "checkout", "-q", "-b", "side")
	commitAndPushTo(t, repo, "side", "fix(web-runtime): elsewhere", map[string]string{"web-runtime/src/index.ts": "export const side = 1;\n"})
	tagWebRuntime(t, repo, "0.13.2")
	gitIn(t, repo, "checkout", "-q", "main")
	gitIn(t, repo, "reset", "-q", "--hard", base)
	commitAndPush(t, repo, "fix(web-runtime): here", map[string]string{"web-runtime/src/index.ts": "export const here = 1;\n"})
	log := filepath.Join(t.TempDir(), "npm.log")
	res, stderr, err := runWebRuntimeScript(t, repo, webRuntimeEnv(t, "FAKE_NPM_LOG="+log), "--dry-run")
	if err == nil || res.State != "failed" || !strings.Contains(res.Detail, "already exists") {
		t.Errorf("want a refusal of the taken 0.13.2, got err=%v %+v\n%s", err, res, stderr)
	}
	if calls, _ := os.ReadFile(log); len(calls) != 0 {
		t.Errorf("the refusal came after npm ran:\n%s", calls)
	}
}

// commitAndPushTo is commitAndPush onto another branch.
func commitAndPushTo(t *testing.T, repo, branch, msg string, files map[string]string) {
	t.Helper()
	writeFiles(t, repo, files)
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "-c", "user.name=fixture", "-c", "user.email=fixture@example.com", "commit", "-q", "-m", msg)
	gitIn(t, repo, "push", "-q", "origin", "HEAD:refs/heads/"+branch)
}

// addWebRuntimeToForgeFixture gives newForgeFixtureRepo's repo a web runtime
// released as 0.13.0 plus an unreleased fix, so the forge release must publish
// 0.13.1 first.
func addWebRuntimeToForgeFixture(t *testing.T, repo string) {
	t.Helper()
	commitAndPush(t, repo, "feat: web runtime", map[string]string{
		"web-runtime/package.json":                  wrFixtureManifest("0.13.0"),
		"web-runtime/package-lock.json":             wrFixtureLock("0.13.0"),
		"web-runtime/src/index.ts":                  "export const one = 1;\n",
		"internal/generator/frontend_webruntime.go": wrFixtureFloor("0.13.0"),
	})
	tagWebRuntime(t, repo, "0.13.0")
	commitAndPush(t, repo, "fix(web-runtime): a fix", map[string]string{"web-runtime/src/index.ts": "export const one = 2;\n"})
}

// TestReleaseForgeScript_PublishesTheWebRuntimeFirst: a forge release whose
// commit changed the web runtime publishes it (web-runtime/v0.13.1) before its
// own tag, and its bookkeeping writes 0.13.1 into the runtime's manifests and
// the scaffold floor.
func TestReleaseForgeScript_PublishesTheWebRuntimeFirst(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a git push")
	}
	repo := newForgeFixtureRepo(t)
	addWebRuntimeToForgeFixture(t, repo)
	stubDir, _ := webRuntimeStubs(t)
	out, err := runForgeScriptEnv(t, repo, []string{"PATH=" + stubDir + string(os.PathListSeparator) + os.Getenv("PATH")}, "v0.2.0")
	if err != nil {
		t.Fatalf("release failed: %v\n%s", err, out)
	}
	origin := fixtureOrigin(t, repo)
	tagged := gitOut(t, origin, "rev-parse", "v0.2.0^{commit}")
	if got := gitOut(t, origin, "rev-parse", "web-runtime/v0.13.1^{commit}"); got != tagged {
		t.Errorf("web-runtime/v0.13.1 is on %s, want forge's released commit %s", got, tagged)
	}
	if v := bookkeepingFile(t, origin, "v0.2.0", "web-runtime/package.json"); !strings.Contains(v, `"version": "0.13.1"`) {
		t.Errorf("the bookkeeping did not sync web-runtime/package.json to 0.13.1:\n%s", v)
	}
	if v := bookkeepingFile(t, origin, "v0.2.0", "web-runtime/package-lock.json"); strings.Count(v, `"version": "0.13.1"`) != 2 || !strings.Contains(v, `"version": "1.0.0"`) {
		t.Errorf("the bookkeeping did not sync exactly the lock's two own versions:\n%s", v)
	}
	if v := bookkeepingFile(t, origin, "v0.2.0", "internal/generator/frontend_webruntime.go"); !strings.Contains(v, `webRuntimePublishedRange = "^0.13.1"`) {
		t.Errorf("the bookkeeping did not raise the scaffold floor to ^0.13.1:\n%s", v)
	}
	if !strings.Contains(out, "web runtime: @reliantlabs/forge-web-runtime 0.13.1") {
		t.Errorf("the release did not report the runtime it shipped:\n%s", out)
	}
}

// TestReleaseForgeScript_ABreakingRuntimeReleasesAGeneratorThatResolvesIt: a
// breaking runtime change raises the floor in its own commit, so the commit
// forge tags (whose generator the archives are built from) already declares
// ^0.14.0; and the same change with the old floor is not released at all.
func TestReleaseForgeScript_ABreakingRuntimeReleasesAGeneratorThatResolvesIt(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the release script, which builds the fixture module")
	}
	t.Run("floor raised", func(t *testing.T) {
		repo := newForgeFixtureRepo(t)
		addWebRuntimeToForgeFixture(t, repo)
		commitAndPush(t, repo, "feat(web-runtime)!: a new session API", map[string]string{
			"web-runtime/src/index.ts":                  "export {};\n",
			"web-runtime/package.json":                  wrFixtureManifest("0.14.0"),
			"internal/generator/frontend_webruntime.go": wrFixtureFloor("0.14.0"),
		})
		stubDir, _ := webRuntimeStubs(t)
		out, err := runForgeScriptEnv(t, repo, []string{"PATH=" + stubDir + string(os.PathListSeparator) + os.Getenv("PATH")}, "v0.2.0")
		if err != nil {
			t.Fatalf("release failed: %v\n%s", err, out)
		}
		origin := fixtureOrigin(t, repo)
		if got, tagged := gitOut(t, origin, "rev-parse", "web-runtime/v0.14.0^{commit}"), gitOut(t, origin, "rev-parse", "v0.2.0^{commit}"); got != tagged {
			t.Errorf("web-runtime/v0.14.0 is on %s, want forge's released commit %s", got, tagged)
		}
		if v := gitOut(t, origin, "show", "v0.2.0:internal/generator/frontend_webruntime.go"); !strings.Contains(v, `webRuntimePublishedRange = "^0.14.0"`) {
			t.Errorf("the released generator does not declare ^0.14.0:\n%s", v)
		}
	})
	t.Run("floor not raised", func(t *testing.T) {
		repo := newForgeFixtureRepo(t)
		addWebRuntimeToForgeFixture(t, repo)
		commitAndPush(t, repo, "feat(web-runtime)!: a new session API", map[string]string{"web-runtime/src/index.ts": "export {};\n"})
		stubDir, _ := webRuntimeStubs(t)
		out, err := runForgeScriptEnv(t, repo, []string{"PATH=" + stubDir + string(os.PathListSeparator) + os.Getenv("PATH")}, "v0.2.0")
		if err == nil || !strings.Contains(out, "forge v0.2.0 is NOT released") || !strings.Contains(out, "does not admit the 0.14.0") {
			t.Fatalf("want the release refused for its ^0.13.0 floor, got err=%v:\n%s", err, out)
		}
		if tags := gitOut(t, fixtureOrigin(t, repo), "tag", "-l", "v0.2.0", "web-runtime/v0.14.0"); tags != "" {
			t.Errorf("a refused release pushed tags: %q", tags)
		}
	})
}

// TestReleaseForgeScript_AWebRuntimeThatCannotPublishStopsTheRelease: the
// runtime fails to validate, so forge is NOT released: no forge tag, no
// bookkeeping branch, no runtime tag, and a non-zero exit saying so.
func TestReleaseForgeScript_AWebRuntimeThatCannotPublishStopsTheRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the release script, which builds the fixture module")
	}
	repo := newForgeFixtureRepo(t)
	addWebRuntimeToForgeFixture(t, repo)
	stubDir, _ := webRuntimeStubs(t)
	out, err := runForgeScriptEnv(t, repo, []string{
		"PATH=" + stubDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FAKE_NPM_FAIL=run typecheck",
	}, "v0.2.0")
	if err == nil || !strings.Contains(out, "forge v0.2.0 is NOT released") {
		t.Fatalf("want the release refused, got err=%v:\n%s", err, out)
	}
	origin := fixtureOrigin(t, repo)
	if tags := gitOut(t, origin, "tag", "-l", "v0.2.0", "web-runtime/v0.13.1"); tags != "" {
		t.Errorf("a refused release pushed tags: %q", tags)
	}
	if branches := gitOut(t, origin, "branch", "--list", "release/*"); branches != "" {
		t.Errorf("a refused release pushed a bookkeeping branch: %q", branches)
	}
	if tags := gitOut(t, repo, "tag", "-l", "v0.2.0"); tags != "" {
		t.Errorf("a refused release left the local tag %s", tags)
	}
	assertNoReleaseWorktree(t, repo)
}
