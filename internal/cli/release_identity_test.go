// File: internal/cli/release_identity_test.go
//
// Pins WHO a release is made by. scripts/release-forge.sh (the bookkeeping
// commit and the vX.Y.Z tag) and scripts/release-web-runtime.sh (the
// web-runtime/vX.Y.Z tag) make every release as reliant-labs[bot], whichever
// machine cuts it, by setting the identity on those commands alone. Two
// failures this guards against:
//
//   - a machine with no git identity at all could not tag ("Committer identity
//     unknown"), so the release stopped after every validation had passed;
//   - a machine whose git config, or whose inherited GIT_AUTHOR_*/GIT_COMMITTER_*
//     environment, names a person released as that person.
//
// Both scripts run for real against isolated fixture repos (a local bare
// origin for forge, stubbed node/npm for web-runtime): global and system git
// config are replaced by a file the test owns, and the test asserts that file
// and the repo's own config are byte-identical afterwards, so the identity
// never leaks into git config either. They exec bash and git, so they are
// skipped in -short mode like the other release script tests.
package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const releaseBotIdentity = "reliant-labs[bot] <186876276+reliant-labs[bot]@users.noreply.github.com>"

// releaseIdentityCase is one git identity a release machine might present.
type releaseIdentityCase struct {
	name string
	// globalConfig is the whole "global" git config the script sees.
	globalConfig string
	// repoIdentity is set as the release repo's local user.name/user.email.
	repoIdentity bool
	// inherited is identity environment the script inherits from its caller.
	inherited []string
}

var releaseIdentityCases = []releaseIdentityCase{
	{
		// No identity anywhere, and git forbidden from guessing one from the
		// hostname: a bare release machine.
		name:         "no identity",
		globalConfig: "[user]\n\tuseConfigOnly = true\n",
	},
	{
		// A person everywhere: global config, repo config and environment.
		name:         "a person's identity",
		globalConfig: "[user]\n\tname = Global Person\n\temail = global@example.com\n",
		repoIdentity: true,
		inherited: []string{
			"GIT_AUTHOR_NAME=Inherited Author", "GIT_AUTHOR_EMAIL=author@example.com",
			"GIT_COMMITTER_NAME=Inherited Committer", "GIT_COMMITTER_EMAIL=committer@example.com",
		},
	},
}

// releaseIdentityEnv is the caller's environment with every git identity and
// config-location variable removed, then git pointed at globalConfigFile alone
// (no system config), plus inherited.
func releaseIdentityEnv(globalConfigFile string, inherited []string) []string {
	var env []string
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "GIT_") || key == "EMAIL" {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "GIT_CONFIG_GLOBAL="+globalConfigFile, "GIT_CONFIG_NOSYSTEM=1")
	return append(env, inherited...)
}

// setReleaseRepoIdentity makes repo's local git config hold a person's
// identity (repoIdentity) or none at all.
func setReleaseRepoIdentity(t *testing.T, repo string, repoIdentity bool) {
	t.Helper()
	for _, key := range []string{"user.name", "user.email"} {
		cmd := exec.CommandContext(context.Background(), "git", "config", "--local", "--unset-all", key)
		cmd.Dir = repo
		_ = cmd.Run() // exit 5: the key was not set
	}
	if repoIdentity {
		gitIn(t, repo, "config", "--local", "user.name", "Repo Person")
		gitIn(t, repo, "config", "--local", "user.email", "repo@example.com")
	}
}

// gitConfigSnapshot is everything git config a release could have written:
// the global file byte for byte, and the repo's local config.
func gitConfigSnapshot(t *testing.T, repo, globalConfigFile string) string {
	t.Helper()
	global, err := os.ReadFile(globalConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	local, err := os.ReadFile(filepath.Join(gitOut(t, repo, "rev-parse", "--absolute-git-dir"), "config"))
	if err != nil {
		t.Fatal(err)
	}
	return "global:\n" + string(global) + "\nlocal:\n" + string(local)
}

func writeGlobalGitConfig(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestReleaseForgeScript_ReleasesAsTheBot: the bookkeeping commit's author and
// committer and the release tag's tagger are reliant-labs[bot], with no
// identity on the machine and with a person's identity in config and in the
// environment, and the release leaves git config as it found it.
func TestReleaseForgeScript_ReleasesAsTheBot(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a git push")
	}
	for _, tc := range releaseIdentityCases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newForgeFixtureRepo(t)
			origin := fixtureOrigin(t, repo)
			setReleaseRepoIdentity(t, repo, tc.repoIdentity)
			globalConfig := writeGlobalGitConfig(t, tc.globalConfig)
			before := gitConfigSnapshot(t, repo, globalConfig)

			out, err := runForgeScriptBaseEnv(t, repo, releaseIdentityEnv(globalConfig, tc.inherited), nil, "v0.2.0")
			if err != nil {
				t.Fatalf("release failed: %v\n%s", err, out)
			}

			bookkeeping := gitOut(t, origin, "log", "-1", "--format=%an <%ae>%n%cn <%ce>", "release/v0.2.0")
			if want := releaseBotIdentity + "\n" + releaseBotIdentity; bookkeeping != want {
				t.Errorf("bookkeeping commit author/committer:\n%s\nwant both %s", bookkeeping, releaseBotIdentity)
			}
			for _, dir := range []string{origin, repo} {
				if tagger := gitOut(t, dir, "for-each-ref", "--format=%(taggername) %(taggeremail)", "refs/tags/v0.2.0"); tagger != releaseBotIdentity {
					t.Errorf("tag v0.2.0 in %s is tagged by %q, want %q", dir, tagger, releaseBotIdentity)
				}
			}
			if after := gitConfigSnapshot(t, repo, globalConfig); after != before {
				t.Errorf("the release changed git config:\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

// TestReleaseWebRuntimeScript_TagsAsTheBot: the web-runtime/vX.Y.Z tag's
// tagger is reliant-labs[bot] in the same two situations. node and npm are
// stubs, so nothing is built, packed for real or published.
func TestReleaseWebRuntimeScript_TagsAsTheBot(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the web-runtime release script over a fixture git repo")
	}
	for _, bin := range []string{"bash", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(cwd, "..", "..", "scripts", "release-web-runtime.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("release-web-runtime.sh not found at %s: %v", script, err)
	}

	for _, tc := range releaseIdentityCases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newWebRuntimeFixtureRepo(t)
			setReleaseRepoIdentity(t, repo, tc.repoIdentity)
			globalConfig := writeGlobalGitConfig(t, tc.globalConfig)
			before := gitConfigSnapshot(t, repo, globalConfig)

			env := releaseIdentityEnv(globalConfig, tc.inherited)
			env = append(env, "PATH="+webRuntimeToolStubs(t)+string(os.PathListSeparator)+os.Getenv("PATH"))
			cmd := exec.CommandContext(context.Background(), "bash", script, "v0.12.0")
			cmd.Dir = repo
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("release failed: %v\n%s", err, out)
			}

			if tagger := gitOut(t, repo, "for-each-ref", "--format=%(taggername) %(taggeremail)", "refs/tags/web-runtime/v0.12.0"); tagger != releaseBotIdentity {
				t.Errorf("tag web-runtime/v0.12.0 is tagged by %q, want %q", tagger, releaseBotIdentity)
			}
			if after := gitConfigSnapshot(t, repo, globalConfig); after != before {
				t.Errorf("the release changed git config:\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

// newWebRuntimeFixtureRepo is a repo shaped like forge from
// release-web-runtime.sh's point of view: the package manifest and the
// scaffold range it checks, both at 0.12.0.
func newWebRuntimeFixtureRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range map[string]string{
		"web-runtime/package.json":                  "{\n  \"name\": \"@reliantlabs/forge-web-runtime\",\n  \"version\": \"0.12.0\"\n}\n",
		"internal/generator/frontend_webruntime.go": "package generator\n\nconst webRuntimePublishedRange = \"^0.12.0\"\n",
	} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, root, "init", "-q", "-b", "main")
	gitIn(t, root, "add", ".")
	gitIn(t, root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.com", "commit", "-q", "-m", "fixture")
	return root
}

// webRuntimeToolStubs is a directory holding a node that answers the
// script's two `node -p require(...)` reads, and an npm whose build,
// typecheck and test succeed and whose pack lists the entry points.
func webRuntimeToolStubs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"node": `#!/usr/bin/env bash
case "$*" in
  *.version*) echo 0.12.0 ;;
  *.name*) echo @reliantlabs/forge-web-runtime ;;
  *) echo "unexpected node $*" >&2; exit 1 ;;
esac
`,
		"npm": `#!/usr/bin/env bash
case "$*" in
  pack*) echo '[{"files":[{"path":"dist/index.js"},{"path":"dist/index.d.ts"}]}]' ;;
  "run build"|"run typecheck"|test) ;;
  *) echo "unexpected npm $*" >&2; exit 1 ;;
esac
`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
