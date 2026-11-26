package contract

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// The merge invariant for contract mocks: two branches that change
// DIFFERENT interfaces of one package merge with plain `git merge` — no
// merge driver, exactly as GitHub's server-side merge does — into the bytes
// forge generate writes for the merged contract.go.
//
// Before mocks were sectioned, every such pair conflicted on the mock's one
// forge:hash header line (reliant's internal/workflow/runtime/activities/
// handlers/mock_gen.go, 26 conflicting merges in the week to 2026-10-30).

// contractWith renders a contract.go whose interfaces carry the given
// extra method per interface name.
func contractWith(extra map[string]string) string {
	var b strings.Builder
	b.WriteString("package widgets\n\nimport \"context\"\n\n")
	for _, name := range []string{"Alpha", "Bravo", "Charlie"} {
		b.WriteString("// " + name + " is a boundary.\ntype " + name + " interface {\n\tGet(ctx context.Context, id string) (string, error)\n")
		if m := extra[name]; m != "" {
			b.WriteString("\t" + m + "\n")
		}
		b.WriteString("}\n\n")
	}
	return b.String()
}

func runGitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	// No user or system config: no merge driver, no attributes — plain git.
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// generateInto writes contract.go and runs the mock writer the way forge
// generate does (through the checksums chokepoint, so the file is stamped).
func generateInto(t *testing.T, root, contract string) {
	t.Helper()
	pkg := filepath.Join(root, "internal", "widgets")
	writeTree(t, root, map[string]string{"internal/widgets/contract.go": contract})
	cs, err := checksums.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := GenerateWithOptions(filepath.Join(pkg, "contract.go"), Options{ProjectRoot: root, Checksums: cs}); err != nil {
		t.Fatal(err)
	}
}

func TestMockMergeProperty_IndependentInterfacesMergeWithPlainGit(t *testing.T) {
	if testing.Short() {
		t.Skip("runs real git merges and the go/packages resolver (~5s); runs in task test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	writeTree(t, repo, map[string]string{"go.mod": "module example.com/proj\n\ngo 1.22\n"})
	generateInto(t, repo, contractWith(nil))
	runGitT(t, repo, "init", "-q", "-b", "main")
	runGitT(t, repo, "add", ".")
	runGitT(t, repo, "commit", "-qm", "base")

	// Alpha and Bravo are NEIGHBOURING sections: the shape that conflicted
	// while a section's hash rode on its header (Charlie sat between the
	// two changes in the original proof, so it never saw it).
	alpha := map[string]string{"Alpha": "Put(ctx context.Context, id, v string) error"}
	charlie := map[string]string{"Bravo": "Delete(ctx context.Context, id string) error"}

	runGitT(t, repo, "checkout", "-qb", "alpha")
	generateInto(t, repo, contractWith(alpha))
	runGitT(t, repo, "commit", "-qam", "alpha gains Put")

	runGitT(t, repo, "checkout", "-q", "main")
	generateInto(t, repo, contractWith(charlie))
	runGitT(t, repo, "commit", "-qam", "bravo gains Delete")

	// contract.go is hand-written: both branches change different
	// interfaces, so it merges too.
	runGitT(t, repo, "merge", "-q", "--no-edit", "alpha")

	merged, err := os.ReadFile(filepath.Join(repo, "internal", "widgets", "mock_gen.go"))
	if err != nil {
		t.Fatal(err)
	}

	// What forge generate writes for the merged inputs, in a fresh tree.
	fresh := t.TempDir()
	writeTree(t, fresh, map[string]string{"go.mod": "module example.com/proj\n\ngo 1.22\n"})
	both := map[string]string{"Alpha": alpha["Alpha"], "Bravo": charlie["Bravo"]}
	generateInto(t, fresh, contractWith(both))
	want, err := os.ReadFile(filepath.Join(fresh, "internal", "widgets", "mock_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(merged) != string(want) {
		t.Fatalf("plain git merge of two independent interface changes differs from forge generate on the merge:\n--- merged ---\n%s\n--- generate ---\n%s", merged, want)
	}
	if checksums.Verify(merged) != checksums.Pristine {
		t.Error("the merged mock must certify itself")
	}
}
