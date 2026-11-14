package kclprojects

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/forgecompat"
	"github.com/reliant-labs/forge/internal/gitsource"
	"github.com/reliant-labs/forge/pkg/testkit"
)

const (
	infraRepo   = "github.com/acme/infra"
	infraCommit = "0123456789abcdef0123456789abcdef01234567"
)

// infraFetcher stands in for git: it materializes a forge project (forge.yaml
// at the repository root, KCL at deploy/kcl) and reports commit.
type infraFetcher struct {
	files  map[string]string
	commit string
	err    error
	calls  int
}

func (f *infraFetcher) Fetch(_ context.Context, _ gitsource.Source, dst string) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	for rel, body := range f.files {
		p := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return "", err
		}
	}
	return f.commit, nil
}

// gitImporter lays out a consumer with NO go.mod — a non-Go project — whose
// kcl.mod imports `infra` by the given git-pin table body.
func gitImporter(t *testing.T, pin, entry string) (projectDir, kclRoot string) {
	t.Helper()
	projectDir = filepath.Join(t.TempDir(), "deploy-repo")
	write(t, projectDir, map[string]string{
		"forge.yaml":         "name: deploy-repo\nforge_version: " + pin + "\n",
		"deploy/kcl/kcl.mod": kclModHead + "\n[forge_projects]\ninfra = { " + entry + " }\n",
	})
	return projectDir, filepath.Join(projectDir, "deploy", "kcl")
}

func testSources(t *testing.T, projectDir string, f gitsource.Fetcher, overrides map[string]string) gitsource.SourceResolver {
	t.Helper()
	opts := []gitsource.Option{gitsource.WithFetcher(f), gitsource.WithCacheRoot(t.TempDir())}
	if overrides != nil {
		opts = append(opts, gitsource.WithOverrides(overrides))
	}
	r, err := gitsource.NewResolver(projectDir, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func resolveGit(t *testing.T, kclRoot string, sources gitsource.SourceResolver) ([]Resolved, []string, error) {
	t.Helper()
	var logs []string
	rs, err := ResolveWith(context.Background(), kclRoot, Options{Sources: sources, Log: func(l string) { logs = append(logs, l) }})
	return rs, logs, err
}

// A git-pinned import is the polyrepo answer for a project with no go.mod:
// it resolves through the shared source cache, fetches once, and a second
// resolution of the same pin is a cache hit with no network work.
func TestResolveGitPinNeedsNoGoMod(t *testing.T) {
	projectDir, kclRoot := gitImporter(t, pinA, `repo = "`+infraRepo+`", ref = "v1.4.0"`)
	f := &infraFetcher{files: producerFiles("unused", pinA), commit: infraCommit}
	sources := testSources(t, projectDir, f, nil)

	rs, logs, err := resolveGit(t, kclRoot, sources)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(rs) != 1 {
		t.Fatalf("want 1 import, got %+v", rs)
	}
	r := rs[0]
	if !r.IsGit() || r.Repo != infraRepo || r.Ref != "v1.4.0" || r.Version != "v1.4.0" || r.ResolvedCommit != infraCommit || r.Overridden {
		t.Fatalf("got %+v", r)
	}
	if r.Dir != filepath.Join(r.ModuleDir, "deploy", "kcl") || r.ForgePin != pinA {
		t.Fatalf("Dir/ForgePin: got %+v", r)
	}
	if _, err := os.Stat(filepath.Join(r.Dir, "workloads.k")); err != nil {
		t.Fatalf("the import's KCL is not at Dir: %v", err)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "fetched "+infraRepo+"@v1.4.0") {
		t.Fatalf("a fetch is reported once: %q", logs)
	}

	rs2, logs, err := resolveGit(t, kclRoot, sources)
	if err != nil || len(rs2) != 1 || rs2[0].Dir != r.Dir || rs2[0].ResolvedCommit != infraCommit {
		t.Fatalf("second resolve: %+v, %v", rs2, err)
	}
	if f.calls != 1 || len(logs) != 0 {
		t.Fatalf("a cached pin must not fetch or log again: calls=%d logs=%q", f.calls, logs)
	}
}

// commit is the lock: the ref must resolve to it, so a moved tag renders
// nothing rather than different KCL. A matching (abbreviated) lock passes.
func TestResolveGitPinCommitLock(t *testing.T) {
	t.Run("mismatch", func(t *testing.T) {
		projectDir, kclRoot := gitImporter(t, pinA, `repo = "`+infraRepo+`", ref = "v1.4.0", commit = "fedcba9876543210fedcba9876543210fedcba98"`)
		sources := testSources(t, projectDir, &infraFetcher{files: producerFiles("unused", pinA), commit: infraCommit}, nil)
		_, _, err := resolveGit(t, kclRoot, sources)
		wantErr(t, err, "pin mismatch", infraRepo+"@v1.4.0", "resolved to commit "+infraCommit,
			"declares commit fedcba98", "forge kcl imports --refresh", `commit = "`+infraCommit+`"`)
	})
	t.Run("a full-sha ref is its own lock", func(t *testing.T) {
		other := "fedcba9876543210fedcba9876543210fedcba98"
		projectDir, kclRoot := gitImporter(t, pinA, `repo = "`+infraRepo+`", ref = "`+other+`"`)
		sources := testSources(t, projectDir, &infraFetcher{files: producerFiles("unused", pinA), commit: infraCommit}, nil)
		_, _, err := resolveGit(t, kclRoot, sources)
		wantErr(t, err, "pin mismatch", "declares ref "+other)
	})
	t.Run("match", func(t *testing.T) {
		projectDir, kclRoot := gitImporter(t, pinA, `repo = "`+infraRepo+`", ref = "v1.4.0", commit = "`+infraCommit[:12]+`"`)
		sources := testSources(t, projectDir, &infraFetcher{files: producerFiles("unused", pinA), commit: infraCommit}, nil)
		if _, _, err := resolveGit(t, kclRoot, sources); err != nil {
			t.Fatalf("an abbreviated lock that matches must resolve: %v", err)
		}
	})
}

// .forge/source-overrides.yaml points a git pin at a working copy: read in
// place, never fetched, the lock not judged — and reported, because it is not
// the pin. A stale override is an error naming the file.
func TestResolveGitPinHonoursTheOverrideFile(t *testing.T) {
	projectDir, kclRoot := gitImporter(t, pinA, `repo = "`+infraRepo+`", ref = "v1.4.0", commit = "`+infraCommit+`"`)
	work := filepath.Join(filepath.Dir(projectDir), "infra")
	write(t, work, producerFiles("unused", pinA))
	if err := gitsource.WriteOverrides(projectDir, map[string]string{"https://" + infraRepo + ".git": "../infra"}); err != nil {
		t.Fatal(err)
	}
	f := &infraFetcher{err: errors.New("must not fetch an overridden repo")}
	// Built the production way, so the override FILE is what is read.
	sources, err := gitsource.NewResolver(projectDir, gitsource.WithFetcher(f), gitsource.WithCacheRoot(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	rs, logs, err := resolveGit(t, kclRoot, sources)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	r := rs[0]
	if !r.Overridden || r.ModuleDir != work || r.Dir != filepath.Join(work, "deploy", "kcl") || r.ResolvedCommit != "" || r.Version != "" {
		t.Fatalf("got %+v", r)
	}
	if !strings.Contains(r.Via, "NOT the pinned ref") || len(logs) != 1 || !strings.Contains(logs[0], "NOT the pinned ref") {
		t.Fatalf("an override must say it is not the pin: via=%q logs=%q", r.Via, logs)
	}

	if err := os.RemoveAll(work); err != nil {
		t.Fatal(err)
	}
	_, _, err = resolveGit(t, kclRoot, sources)
	wantErr(t, err, "overridden to", "source-overrides.yaml")
}

// A pin that cannot be fetched (offline, a typo'd ref) fails naming the pin
// and both ways forward.
func TestResolveGitPinFetchFailureNamesThePin(t *testing.T) {
	projectDir, kclRoot := gitImporter(t, pinA, `repo = "`+infraRepo+`", ref = "v9.9.9"`)
	sources := testSources(t, projectDir, &infraFetcher{err: errors.New("could not resolve host")}, nil)
	_, _, err := resolveGit(t, kclRoot, sources)
	wantErr(t, err, `forge project "infra" pinned to `+infraRepo+"@v9.9.9", "could not resolve host",
		"forge kcl imports", "source-overrides.yaml", infraRepo+": ../<checkout>")
}

// The forge-version skew refusal applies to a git pin too, with a remedy that
// moves the ref rather than a go.mod require.
func TestResolveGitPinRefusesForgeSkew(t *testing.T) {
	t.Setenv(forgecompat.SkewOverrideEnv, "")
	projectDir, kclRoot := gitImporter(t, pinA, `repo = "`+infraRepo+`", ref = "v1.4.0"`)
	sources := testSources(t, projectDir, &infraFetcher{files: producerFiles("unused", pinB), commit: infraCommit}, nil)
	_, _, err := resolveGit(t, kclRoot, sources)
	wantErr(t, err, "version skew", pinA, pinB, "move `ref` (and `commit`) of [forge_projects] infra")
}

// The imported repository must be a forge project — its forge.yaml may sit
// above the KCL package, but never above the checkout.
func TestResolveGitPinFindsForgeYAMLWithinTheCheckout(t *testing.T) {
	files := map[string]string{
		"platform/forge.yaml":         "name: platform\nforge_version: " + pinA + "\n",
		"platform/deploy/kcl/kcl.mod": kclModHead,
		"platform/deploy/kcl/envs.k":  "region = \"us\"\n",
	}
	projectDir, kclRoot := gitImporter(t, pinA, `repo = "`+infraRepo+`", ref = "v1.4.0", subdir = "platform/deploy/kcl"`)
	rs, _, err := resolveGit(t, kclRoot, testSources(t, projectDir, &infraFetcher{files: files, commit: infraCommit}, nil))
	if err != nil || rs[0].ForgePin != pinA {
		t.Fatalf("forge.yaml above the KCL dir, inside the checkout: %+v, %v", rs, err)
	}

	delete(files, "platform/forge.yaml")
	projectDir, kclRoot = gitImporter(t, pinA, `repo = "`+infraRepo+`", ref = "v1.4.0", subdir = "platform/deploy/kcl"`)
	_, _, err = resolveGit(t, kclRoot, testSources(t, projectDir, &infraFetcher{files: files, commit: infraCommit}, nil))
	wantErr(t, err, "not a forge project", "Add a forge.yaml declaring forge_version")
}

// A module import still needs go.mod; without one the error offers the git pin.
func TestResolveModuleImportWithoutGoModSuggestsAGitPin(t *testing.T) {
	_, kclRoot := gitImporter(t, pinA, `module = "`+producerMod+`"`)
	_, err := Resolve(kclRoot)
	wantErr(t, err, "there is no go.mod", `infra = { repo = "<host/owner/name>", ref = "<tag, branch or sha>" }`)
}

func TestParseGitImportRefusals(t *testing.T) {
	for _, tc := range []struct{ entry, want string }{
		{`module = "` + producerMod + `", repo = "` + infraRepo + `", ref = "v1"`, "mutually exclusive"},
		{`repo = "` + infraRepo + `"`, "source.ref is required"},
		{`ref = "v1"`, "repo + ref (a git pin)"},
		{`repo = "` + infraRepo + `", tag = "v1"`, "spell the tag, branch or sha as `ref`"},
		{`repo = "` + infraRepo + `", ref = "v1", commit = "HEAD"`, "commit must be a commit sha"},
		{`module = "` + producerMod + `", commit = "abcdef0"`, `"commit" is not allowed with module`},
		{`repo = "not a repo", ref = "v1"`, "is not a recognized repository"},
	} {
		dir := t.TempDir()
		write(t, dir, map[string]string{"kcl.mod": kclModHead + "\n[forge_projects]\ninfra = { " + tc.entry + " }\n"})
		_, err := readKCLMod(filepath.Join(dir, "kcl.mod"))
		wantErr(t, err, tc.want)
	}
}

// TestResolveGitPinFromABareRepo is the real thing: a local bare repository
// fetched by git, a tag that moves, and the lock refusing the moved tag.
func TestResolveGitPinFromABareRepo(t *testing.T) {
	testkit.Slow(t, "execs git to build and fetch a bare repository")
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	root := t.TempDir()
	work, bare := filepath.Join(root, "infra-work"), filepath.Join(root, "infra.git")
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write(t, work, producerFiles("unused", pinA))
	git(work, "init", "--quiet", "-b", "main")
	git(work, "add", ".")
	git(work, "commit", "--quiet", "-m", "v1")
	git(work, "tag", "v1.0.0")
	first := git(work, "rev-parse", "HEAD")
	git(root, "clone", "--quiet", "--bare", work, bare)

	repoURL := "file://" + bare
	projectDir, kclRoot := gitImporter(t, pinA, `repo = "`+repoURL+`", ref = "v1.0.0", commit = "`+first+`"`)
	sources, err := gitsource.NewResolver(projectDir, gitsource.WithCacheRoot(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	rs, err := ResolveWith(context.Background(), kclRoot, Options{Sources: sources, Log: func(string) {}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if rs[0].ResolvedCommit != first {
		t.Fatalf("resolved %q, want %q", rs[0].ResolvedCommit, first)
	}
	if _, err := os.Stat(filepath.Join(rs[0].Dir, "workloads.k")); err != nil {
		t.Fatalf("fetched checkout has no KCL: %v", err)
	}

	// Re-point the tag at a new commit, as a careless release would.
	write(t, work, map[string]string{"deploy/kcl/config_gen.k": "name = \"moved\"\n"})
	git(work, "commit", "--quiet", "-am", "v1 again")
	git(work, "tag", "-f", "v1.0.0")
	git(work, "push", "--quiet", "--force", bare, "main", "refs/tags/v1.0.0")

	// The cache still holds the locked commit: no network, same bytes.
	if _, err := ResolveWith(context.Background(), kclRoot, Options{Sources: sources, Log: func(string) {}}); err != nil {
		t.Fatalf("a cached pin must keep resolving offline from the moved tag: %v", err)
	}
	// A refresh fetches the moved tag, and the lock refuses it.
	_, err = ResolveWith(context.Background(), kclRoot, Options{Sources: sources, Refresh: true, Log: func(string) {}})
	wantErr(t, err, "pin mismatch", "declares commit "+first)
}
