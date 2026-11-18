package ledgerfile

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestTwoWorktreesShareOneLedger is the central property of the move out of
// the checkout: the id is derived from the project's NAME and REMOTE, never
// from a path, so every worktree of a project resolves the same ledger.
//
// The retired backend keyed on the directory, which meant two worktrees saw
// two histories of the same environment — and a feature branch answered
// "what does prod run" with whatever its own checkout happened to hold.
func TestTwoWorktreesShareOneLedger(t *testing.T) {
	t.Parallel()
	// The same project reached from two different worktrees. Nothing about
	// the path enters the derivation, which is why the inputs are equal.
	primary, err := ProjectID("control-plane", "git@github.com:reliant-labs/control-plane.git")
	if err != nil {
		t.Fatalf("ProjectID: %v", err)
	}
	worktree, err := ProjectID("control-plane", "git@github.com:reliant-labs/control-plane.git")
	if err != nil {
		t.Fatalf("ProjectID: %v", err)
	}
	if primary != worktree {
		t.Fatalf("two worktrees of one project must share one ledger id, got %q and %q", primary, worktree)
	}
}

// TestSameRepoReachedTwoWaysIsOneProject pins that the remote is
// CANONICALIZED: ssh and https URLs for one repo are one project, so cloning
// over https does not strand the ledger written from an ssh clone.
func TestSameRepoReachedTwoWaysIsOneProject(t *testing.T) {
	t.Parallel()
	ssh, err := ProjectID("control-plane", "git@github.com:reliant-labs/control-plane.git")
	if err != nil {
		t.Fatalf("ProjectID: %v", err)
	}
	https, err := ProjectID("control-plane", "https://github.com/reliant-labs/control-plane")
	if err != nil {
		t.Fatalf("ProjectID: %v", err)
	}
	if ssh != https {
		t.Fatalf("one repo reached two ways must be one project, got %q and %q", ssh, https)
	}
}

// TestSameNameDifferentRepoAreDifferentProjects is the other direction: two
// unrelated projects that happen to share a name must NOT merge ledgers, or
// one project's promotions would answer the other's "what does prod run".
func TestSameNameDifferentRepoAreDifferentProjects(t *testing.T) {
	t.Parallel()
	mine, err := ProjectID("api", "git@github.com:acme/api.git")
	if err != nil {
		t.Fatalf("ProjectID: %v", err)
	}
	theirs, err := ProjectID("api", "git@github.com:other/api.git")
	if err != nil {
		t.Fatalf("ProjectID: %v", err)
	}
	if mine == theirs {
		t.Fatalf("two different repos named %q must not share a ledger (both resolved to %q)", "api", mine)
	}
}

func TestProjectIDShape(t *testing.T) {
	t.Parallel()
	t.Run("with a remote, name plus 12 hex", func(t *testing.T) {
		t.Parallel()
		got, err := ProjectID("control-plane", "git@github.com:reliant-labs/control-plane.git")
		if err != nil {
			t.Fatalf("ProjectID: %v", err)
		}
		if !strings.HasPrefix(got, "control-plane-") {
			t.Fatalf("the id must stay readable and lead with the name, got %q", got)
		}
		hash := strings.TrimPrefix(got, "control-plane-")
		if len(hash) != repoHashLen {
			t.Fatalf("the hash must be %d hex chars, got %q", repoHashLen, hash)
		}
	})

	t.Run("no remote, the bare name", func(t *testing.T) {
		t.Parallel()
		got, err := ProjectID("scratch", "")
		if err != nil {
			t.Fatalf("ProjectID: %v", err)
		}
		if got != "scratch" {
			t.Fatalf("a project with no remote is keyed by name alone, got %q", got)
		}
	})

	t.Run("no name is refused", func(t *testing.T) {
		t.Parallel()
		if _, err := ProjectID("  ", "git@github.com:acme/api.git"); err == nil {
			t.Fatal("a project with no forge.yaml name cannot be keyed and must be refused")
		}
	})

	// A project id becomes a directory name under the ledger home, so the
	// property that matters is CONTAINMENT: joining it to the home must
	// stay under the home. A literal ".." left in the name would break
	// that; a flattened ".._.._etc" is a harmless single segment, so the
	// assertion is about where the path lands, not which bytes survived.
	t.Run("a traversing name cannot escape the home", func(t *testing.T) {
		t.Parallel()
		for _, name := range []string{"../../etc", "..", ".", "/absolute", `..\..\windows`} {
			got, err := ProjectID(name, "")
			if err != nil {
				t.Fatalf("ProjectID(%q): %v", name, err)
			}
			if strings.ContainsAny(got, `/\`) {
				t.Fatalf("ProjectID(%q) = %q, which spans directories", name, got)
			}
			// Cleaned like the joined path, so the prefix comparison uses the
			// host separator on both sides (\tmp\ledger on Windows).
			home := filepath.Clean("/tmp/ledger")
			resolved := filepath.Clean(filepath.Join(home, got))
			if !strings.HasPrefix(resolved, home+string(filepath.Separator)) {
				t.Fatalf("ProjectID(%q) = %q resolves to %q, outside the ledger home", name, got, resolved)
			}
		}
	})
}
