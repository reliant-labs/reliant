// Copyright (c) 2026 Reliant Labs
//go:build linux

package daemonruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

type sweepTestLease struct{}

func (sweepTestLease) Check(context.Context) error { return nil }
func (sweepTestLease) Release()                    {}

// A Reliant workspace whose chat is still open is reclaimed by the machine
// sweep (here with its uncommitted work approved by a person in the Inbox), and the
// chat's next tool call gets it back: worktree.ensure recreates the checkout
// from its branch and puts the captured work back, staged and unstaged
// apart, and the notice says so.
func TestSweepReclaimedWorkspaceIsRestoredWithItsCaptureByEnsure(t *testing.T) {
	testkit.Slow(t, "creates real git checkouts, reclaims one and recreates it")
	f := newEnsureFixture(t, "")
	commitIn(t, f.root, "committed.txt")
	write := func(rel, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(f.root, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.root, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("staged.txt", "staged\n")
	rcGit(t, f.root, "add", "staged.txt")
	write("a.txt", "unstaged edit\n")
	write("notes/untracked.md", "untracked\n")

	root, err := worktreereclaim.DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	r := worktreereclaim.NewReclaimer(root)
	r.Acquire = func(context.Context, []string) (worktreereclaim.RecoveryLease, error) { return sweepTestLease{}, nil }
	r.PRMerged, r.FetchBase = nil, nil
	// Process inspection fails closed on any process of this user it cannot
	// read, which on a shared host is another agent's; worktreereclaim tests
	// it. Stubbed here so the restore is always asserted.
	r.ProcessUse = func(context.Context, []string) error { return nil }
	r.Cwds = func(context.Context) ([]string, error) { return nil, nil }
	r.Now = func() time.Time { return time.Now().Add(10 * 24 * time.Hour) }
	state := t.TempDir()
	opts := worktreereclaim.SweepOptions{Mode: worktreereclaim.SweepOn, StateDir: state, Anchors: []string{f.project},
		Roots: []worktreereclaim.SweepRoot{{Path: filepath.Dir(f.root), Depth: 2}}}
	ctx := context.Background()

	var entry worktreereclaim.SweepEntry
	for _, e := range r.SweepMachine(ctx, opts).Entries {
		if e.Path == f.root {
			entry = e
		}
	}
	if entry.Owner != "reliant" || entry.Key != f.worktreeID || entry.Class != worktreereclaim.ClassNeedsApproval {
		t.Fatalf("workspace entry %+v, want a Reliant workspace needing approval", entry)
	}
	if _, err := worktreereclaim.RecordDecisions(ctx, state, worktreereclaim.DecisionApprove, []string{entry.Key}, "test"); err != nil {
		t.Fatal(err)
	}
	// The approval as the server delivers it (worktree.reclaim_approve).
	opts.Authorized = []worktreereclaim.AuthorizedApproval{{Key: entry.Key, Fingerprint: entry.Fingerprint, Path: entry.Path, ApprovedBy: "inbox:u1"}}
	pass := r.SweepMachine(ctx, opts)
	if len(pass.Entries) != 1 || pass.Entries[0].Outcome != worktreereclaim.OutcomeReclaimed {
		t.Fatalf("approved pass %+v, want the workspace reclaimed", pass.Entries)
	}
	if _, err := os.Stat(filepath.Join(f.root, ".git")); !os.IsNotExist(err) {
		t.Fatalf("the checkout is still there: %v", err)
	}

	resp := ensureVia(t, newWorkspaceEnsurer(), f.request("thread-a"))
	if resp.Status != toolexec.WorkspaceRepaired || resp.Notice == nil || !strings.Contains(resp.Notice.RestoredWork, "put back from its capture") {
		t.Fatalf("ensure = %+v (notice %+v), want repaired with its captured work put back", resp, resp.Notice)
	}
	for rel, want := range map[string]string{"committed.txt": "work\n", "staged.txt": "staged\n", "a.txt": "unstaged edit\n", "notes/untracked.md": "untracked\n"} {
		b, err := os.ReadFile(filepath.Join(f.root, rel))
		if err != nil || string(b) != want {
			t.Fatalf("%s = %q, %v; want %q", rel, b, err, want)
		}
	}
	status := rcGit(t, f.root, "status", "--porcelain", "--untracked-files=all")
	for _, want := range []string{"A  staged.txt", " M a.txt", "?? notes/untracked.md"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status lacks %q:\n%s", want, status)
		}
	}
	if _, ok := worktreereclaim.LatestCapture(ctx, f.project, f.worktreeID, f.root); ok {
		t.Fatal("the applied capture would be applied again")
	}
}
