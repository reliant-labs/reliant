// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// checkoutsJSON is a canned `forge project checkouts --json` document.
func checkoutsJSON(paths ...string) string {
	type checkout struct {
		Label string `json:"label"`
		Kind  string `json:"kind"`
		Path  string `json:"path,omitempty"`
		Head  string `json:"head"`
		Dirty bool   `json:"dirty"`
	}
	doc := struct {
		Project   string     `json:"project"`
		MainRef   string     `json:"main_ref"`
		Checkouts []checkout `json:"checkouts"`
	}{
		Project: "reliant",
		MainRef: "origin/main",
		// The remote entry carries NO path — there is nothing on disk to
		// render until someone checks it out. Included because it is in
		// forge's real document, and an allowlist that tripped over it
		// would be wrong in production rather than in a fixture.
		Checkouts: []checkout{{Label: "origin/main", Kind: "remote", Head: "abc123"}},
	}
	for _, path := range paths {
		doc.Checkouts = append(doc.Checkouts, checkout{
			Label: filepath.Base(path), Kind: "worktree", Path: path, Head: "def456",
		})
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// =============================================================================
// THE ALLOWLIST. A project_path is honoured only if forge listed it.
// =============================================================================

// An EMPTY requested checkout means the project's main checkout, and resolves
// without consulting git — it is the root the caller already named.
func TestResolveCheckoutPath_EmptyMeansTheProjectItself(t *testing.T) {
	call := stubForge(t, forgeCommandResult{Stdout: []byte(checkoutsJSON())}, nil)

	got, err := resolveCheckoutPath(context.Background(), "/p", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "/p" {
		t.Errorf("resolved %q, want the project path", got)
	}
	if call.Count != 0 {
		t.Errorf("resolving the default ran %d forge processes; it needs none", call.Count)
	}
}

// A listed checkout resolves, and resolves to FORGE's spelling of the path
// rather than the caller's.
func TestResolveCheckoutPath_AcceptsAListedCheckout(t *testing.T) {
	project := forgeProject(t)
	worktree := t.TempDir()
	stubForge(t, forgeCommandResult{Stdout: []byte(checkoutsJSON(worktree))}, nil)

	got, err := resolveCheckoutPath(context.Background(), project, worktree)
	if err != nil {
		t.Fatalf("a listed checkout must be accepted: %v", err)
	}
	if got != worktree {
		t.Errorf("resolved %q, want %q", got, worktree)
	}
}

// THE SECURITY CASE. An unlisted path is refused, so a request cannot point
// forge — which builds images, pushes them and reads secrets — at an arbitrary
// directory on the daemon's filesystem.
//
// The traversal spellings matter: a check that compared raw strings would let
// `<listed>/../../etc` through a naive prefix test, and one that compared only
// exact strings would refuse a legitimate path that differed by a trailing
// slash. Both are covered.
func TestResolveCheckoutPath_RefusesAnUnlistedCheckout(t *testing.T) {
	project := forgeProject(t)
	listed := t.TempDir()

	for name, requested := range map[string]string{
		"an unrelated directory":               t.TempDir(),
		"the filesystem root":                  "/",
		"a traversal out of a listed checkout": filepath.Join(listed, "..", "..", "etc"),
		"a sibling by traversal":               filepath.Join(listed, "..", "elsewhere"),
	} {
		t.Run(name, func(t *testing.T) {
			stubForge(t, forgeCommandResult{Stdout: []byte(checkoutsJSON(listed))}, nil)

			got, err := resolveCheckoutPath(context.Background(), project, requested)
			if err == nil {
				t.Fatalf("resolved unlisted path %q to %q; a preview may only render a "+
					"checkout this project actually has", requested, got)
			}
			// The refusal must not echo the daemon's filesystem layout
			// back to the caller.
			if strings.Contains(err.Error(), listed) {
				t.Errorf("the refusal names a path that was not asked about: %v", err)
			}
		})
	}
}

// A path that differs only cosmetically — trailing slash, a redundant `.`, a
// `..` that cancels out — is the SAME directory and must be accepted. Refusing
// it would be a false negative that makes the picker randomly unusable.
func TestResolveCheckoutPath_AcceptsCosmeticallyDifferentSpellings(t *testing.T) {
	project := forgeProject(t)
	listed := t.TempDir()

	for name, requested := range map[string]string{
		"trailing slash":    listed + "/",
		"redundant dot":     filepath.Join(listed, "."),
		"cancelling parent": filepath.Join(listed, "sub", ".."),
	} {
		t.Run(name, func(t *testing.T) {
			stubForge(t, forgeCommandResult{Stdout: []byte(checkoutsJSON(listed))}, nil)

			if _, err := resolveCheckoutPath(context.Background(), project, requested); err != nil {
				t.Errorf("%q is the same directory as the listed checkout and must be "+
					"accepted: %v", requested, err)
			}
		})
	}
}

// A symlink to a listed checkout is the same directory, so it resolves.
func TestResolveCheckoutPath_ResolvesSymlinks(t *testing.T) {
	project := forgeProject(t)
	listed := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(listed, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	stubForge(t, forgeCommandResult{Stdout: []byte(checkoutsJSON(listed))}, nil)

	if _, err := resolveCheckoutPath(context.Background(), project, link); err != nil {
		t.Errorf("a symlink to a listed checkout is that checkout: %v", err)
	}
}

// A forge that cannot list checkouts must not fail OPEN. With no allowlist
// there is nothing to authorise against, so a specific checkout is refused.
func TestResolveCheckoutPath_RefusesWhenTheListIsUnavailable(t *testing.T) {
	project := forgeProject(t)

	// An old forge: cobra's "unknown command", exit 1, nothing on stdout.
	stubForge(t, forgeCommandResult{
		ExitCode: 1,
		Stderr:   []byte(`Error: unknown command "checkouts" for "forge project"`),
	}, nil)

	if got, err := resolveCheckoutPath(context.Background(), project, t.TempDir()); err == nil {
		t.Errorf("resolved %q with no allowlist available; a checkout must not be authorised "+
			"by a list that could not be read", got)
	}
}
