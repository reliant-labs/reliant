package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/testkit"
)

func gitAdoptIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(c.Environ(), "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@b.c", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@b.c")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// The documented flow: `project new --in-place`, commit, deploy. The lock kpm
// writes on the first render used to appear AFTER that commit and make the
// next deploy see an uncommitted change. Adoption now writes it, so it is in
// the commit and a render leaves the tree clean.
func TestAdoptionWritesTheKCLLockSoARenderLeavesTheTreeClean(t *testing.T) {
	testkit.Slow(t, "scaffolds a site project and renders KCL")
	for _, kind := range []string{"site", "app"} {
		t.Run(kind, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "my-landing")
			files := map[string]string{"package.json": `{"devDependencies":{"vite":"^5.0.0"}}`, "index.html": "<h1>hi</h1>\n"}
			if kind == "app" {
				files = map[string]string{"Dockerfile": "FROM scratch\nEXPOSE 8080\n"}
			}
			writeAdoptFixture(t, dir, files)
			t.Chdir(dir)
			t.Setenv("FORGE_SKIP_NPM_INSTALL", "1")
			gitAdoptIn(t, dir, "init", "-q")
			args := []string{"project", "new", "--in-place", "--kind", kind, "--name", "my-landing"}
			if out, err := runForge(t, args...); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			lock := filepath.Join(dir, "deploy", "kcl", "kcl.mod.lock")
			if info, err := os.Stat(lock); err != nil || info.Size() != 0 {
				t.Fatalf("adoption must write an empty deploy/kcl/kcl.mod.lock: %v", err)
			}
			if ignored := gitAdoptIn(t, dir, "status", "--porcelain", "--untracked-files=all", "--ignored", "deploy/kcl/kcl.mod.lock"); strings.HasPrefix(ignored, "!!") {
				t.Fatalf("the lock is gitignored, so it cannot be committed: %s", ignored)
			}
			gitAdoptIn(t, dir, "add", "-A")
			gitAdoptIn(t, dir, "commit", "-qm", "adopt")
			if out, err := runForge(t, "env", "render", "prod"); err != nil {
				t.Fatalf("render: %v\n%s", err, out)
			}
			if st := gitAdoptIn(t, dir, "status", "--porcelain", "--untracked-files=all"); st != "" {
				t.Errorf("a render after the adoption commit left the tree dirty:\n%s", st)
			}
		})
	}
}

// When the only uncommitted files are the ones this run's adoption wrote, the
// release decision says so instead of warning about the user's changes; any
// other change keeps the ordinary warning.
func TestReleaseDecisionNamesAdoptionRatherThanUserChanges(t *testing.T) {
	adopted := []string{"forge.yaml", "deploy/kcl/prod/main.k"}
	q := deployReuseQuery{
		Env:        "prod",
		Provenance: release.Provenance{Tree: "abc123", Dirty: true},
		Changes:    release.Changes{Paths: []string{"forge.yaml", "deploy/kcl/prod/main.k"}},
		Adopted:    adopted,
		Now:        time.Now(),
	}
	got := chooseDeployReleaseFor(context.Background(), q).Reason
	if !strings.Contains(got, "forge added") || strings.Contains(got, "the checkout has uncommitted changes") {
		t.Errorf("adoption-only dirt reads as the user's changes: %q", got)
	}
	q.Changes.Paths = append(q.Changes.Paths, "src/App.tsx")
	if got := chooseDeployReleaseFor(context.Background(), q).Reason; !strings.Contains(got, "the checkout has uncommitted changes") || !strings.Contains(got, "src/App.tsx") {
		t.Errorf("a user's own change is hidden behind adoption's: %q", got)
	}
	q.Changes.Paths, q.Adopted = []string{"forge.yaml"}, nil
	if got := chooseDeployReleaseFor(context.Background(), q).Reason; !strings.Contains(got, "the checkout has uncommitted changes") {
		t.Errorf("without an adoption in this run, dirt is the user's: %q", got)
	}
	if !onlyAdoptedPaths([]string{"web/forge.yaml"}, []string{"forge.yaml"}) {
		t.Error("a project below the git top must match its adopted paths by suffix")
	}
}
