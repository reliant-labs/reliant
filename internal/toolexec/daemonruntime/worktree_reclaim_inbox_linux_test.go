// Copyright (c) 2026 Reliant Labs
//go:build linux

package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/reliant-labs/forge/pkg/testkit"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/diskwatch"
	"github.com/reliant-labs/reliant/internal/handlers/inbox"
	"github.com/reliant-labs/reliant/internal/handlers/worktree_reclaim"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
	"github.com/reliant-labs/reliant/pkg/config"
)

// daemonOnTheWire is the Sender the server reaches this machine's daemon
// with: the command is answered by the daemon's own handler, as NATS would
// deliver it.
type daemonOnTheWire struct{ daemonID string }

func (d daemonOnTheWire) SendDaemonCommandToDaemon(ctx context.Context, _, daemonID, command string, payload []byte, _ int32) ([]byte, error) {
	if daemonID != d.daemonID || command != WorktreeReclaimDecideCommand {
		return nil, fmt.Errorf("unexpected command %q to %q", command, daemonID)
	}
	return handleWorktreeReclaimDecide(ctx, payload)
}

// The whole Inbox path, end to end: the machine sweep proposes an idle
// worktree with uncommitted work, the heartbeat carries the proposal and the
// gateway stores it, the Inbox lists it on the machine's item, the person
// approves it there, the decision reaches the daemon bound to the state they
// saw, and the daemon's next approval pass captures the worktree into a local
// ref and only then deletes it.
func TestInboxApprovalReachesTheDaemonWhichCapturesThenReclaims(t *testing.T) {
	testkit.Slow(t, "creates real git checkouts and a database, and reclaims one")
	f := newEnsureFixture(t, "")
	commitIn(t, f.root, "committed.txt")
	if err := os.WriteFile(filepath.Join(f.root, "a.txt"), []byte("unstaged edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The daemon's side: a machine sweep over this workspace, ten days on.
	state := t.TempDir()
	prevDir := reclaimStateDir
	reclaimStateDir = func() (string, error) { return state, nil }
	reclaimProposalsCache.invalidate()
	t.Cleanup(func() { reclaimStateDir = prevDir; reclaimProposalsCache.invalidate() })
	root, err := worktreereclaim.DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	r := worktreereclaim.NewReclaimer(root)
	r.Acquire = func(context.Context, []string) (worktreereclaim.RecoveryLease, error) { return sweepTestLease{}, nil }
	r.PRMerged, r.FetchBase = nil, nil
	r.Now = func() time.Time { return time.Now().Add(10 * 24 * time.Hour) }
	opts := worktreereclaim.SweepOptions{Mode: worktreereclaim.SweepOn, StateDir: state, Anchors: []string{f.project},
		Roots: []worktreereclaim.SweepRoot{{Path: filepath.Dir(f.root), Depth: 2}}}
	ctx := context.Background()
	for _, e := range r.SweepMachine(ctx, opts).Entries {
		if e.Path == f.root && e.Class != worktreereclaim.ClassNeedsApproval {
			t.Fatalf("workspace entry %+v, want it to need approval", e)
		}
	}

	// The heartbeat's disk report, stored as the gateway stores it.
	proposals := worktreeReclaimProposals(worktreereclaim.SweepOn)
	if proposals == nil {
		t.Fatal("the heartbeat carries no proposals")
	}
	repo := db.NewTestRepo(t)
	userID := uuid.NewString()
	daemonID := "d-" + uuid.NewString()
	userCtx := auth.WithUser(ctx, userID, "user", "u@example.com")
	now := time.Now().UTC()
	if err := repo.CreateProject(userCtx, &db.Project{ID: "p-" + userID, UserID: userID, Name: "proj", Path: f.project,
		CreatedAt: now, UpdatedAt: now, LastActive: now}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertDaemon(userCtx, &db.Daemon{ID: daemonID, UserID: userID, Hostname: ptr("laptop")}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertDaemonAttachment(userCtx, &db.DaemonAttachment{DaemonID: daemonID, UserID: userID, Source: db.DaemonAttachmentSourceInbound}); err != nil {
		t.Fatal(err)
	}
	stored, err := diskwatch.Encode(diskwatch.Stamp(&reliantv1.DiskReport{
		Volumes:         []*reliantv1.DiskVolume{{Label: "home", TotalBytes: 100 << 30, FreeBytes: 80 << 30}},
		WorktreeReclaim: proposals,
	}, nil, now))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetDaemonStorageState(userCtx, daemonID, stored); err != nil {
		t.Fatal(err)
	}

	// The Inbox lists it on the machine's item.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	inboxSvc, err := inbox.New(inbox.Deps{Logger: logger, Config: config.Defaults(), Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := inboxSvc.ListInbox(userCtx, connect.NewRequest(&reliantv1.ListInboxRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var shown *reliantv1.WorktreeReclaimProposal
	for _, item := range listed.Msg.Items {
		if item.Kind != reliantv1.InboxItemKind_INBOX_ITEM_KIND_WORKTREE_RECLAIM {
			continue
		}
		if got := item.GetWorktreeReclaim().GetDaemonId(); got != daemonID {
			t.Fatalf("item is for machine %q, want %q", got, daemonID)
		}
		for _, p := range item.GetWorktreeReclaim().GetProposals() {
			if p.Path == f.root {
				shown = p
			}
		}
	}
	if shown == nil {
		t.Fatalf("no worktree-reclaim item lists %s: %+v", f.root, listed.Msg.Items)
	}
	if shown.Key != f.worktreeID || shown.Fingerprint == "" || shown.Decision != "" {
		t.Fatalf("shown proposal %+v, want the workspace undecided", shown)
	}

	// The person approves it as shown.
	decider, err := worktree_reclaim.New(worktree_reclaim.Deps{Logger: logger, Config: config.Defaults(), Repo: repo, Sender: daemonOnTheWire{daemonID: daemonID}})
	if err != nil {
		t.Fatal(err)
	}
	decided, err := decider.DecideWorktreeReclaim(userCtx, connect.NewRequest(&reliantv1.DecideWorktreeReclaimRequest{
		DaemonId:  daemonID,
		Decisions: []*reliantv1.WorktreeReclaimDecision{{Key: shown.Key, Fingerprint: shown.Fingerprint, Approve: true}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res := decided.Msg.Results; len(res) != 1 || !res[0].Recorded {
		t.Fatalf("results %+v, want the approval recorded", res)
	}
	if !worktreereclaim.PendingApprovals(state, time.Time{}) {
		t.Fatal("no approval pending for the daemon to act on")
	}
	if got := worktreeReclaimProposals(worktreereclaim.SweepOn); got == nil || got.Proposals[0].Decision != "approve" {
		t.Fatalf("next heartbeat %+v, want the proposal approved", got)
	}

	// The daemon's approval pass captures it, then removes it.
	opts.ApprovedOnly = true
	pass := r.SweepMachine(ctx, opts)
	if len(pass.Entries) != 1 || pass.Entries[0].Outcome != worktreereclaim.OutcomeReclaimed {
		t.Fatalf("approved pass %+v, want the workspace reclaimed", pass.Entries)
	}
	if _, err := os.Stat(filepath.Join(f.root, ".git")); !os.IsNotExist(err) {
		t.Fatalf("the checkout is still there: %v", err)
	}
	capture, ok := worktreereclaim.LatestCapture(ctx, f.project, f.worktreeID, f.root)
	if !ok || capture.Ref == "" || capture.Clean {
		t.Fatalf("capture %+v (found %t), want a ref holding the uncommitted edit", capture, ok)
	}
	if got := rcGit(t, f.project, "rev-parse", "--verify", capture.Ref); got == "" {
		t.Fatalf("capture ref %s does not resolve", capture.Ref)
	}
	raw, _ := json.Marshal(capture.Meta)
	t.Logf("captured into %s (%s)", capture.Ref, raw)
}
