// Copyright (c) 2026 Reliant Labs
//go:build linux

package daemonruntime

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
	if daemonID != d.daemonID {
		return nil, fmt.Errorf("unexpected machine %q", daemonID)
	}
	switch command {
	case WorktreeReclaimDecideCommand:
		return handleWorktreeReclaimDecide(ctx, payload)
	case WorktreeReclaimApproveCommand:
		return handleWorktreeReclaimApprove(ctx, payload)
	}
	return nil, fmt.Errorf("unexpected command %q", command)
}

// The whole Inbox path, end to end: the machine sweep proposes an idle
// worktree with uncommitted work, the heartbeat carries the proposal and the
// gateway stores it, and the Inbox lists it on the machine's item. An
// approval written straight into approvals.json (anything running as the
// user can) deletes nothing. The person approves it in the Inbox; the server
// records the decision and sends worktree.reclaim_approve, and the daemon
// removes exactly that worktree, its work captured into a local ref first,
// recording who approved it. Sending the approval again does nothing again.
func TestInboxApprovalIsTheOnlyApprovalTheDaemonActsOn(t *testing.T) {
	testkit.Slow(t, "creates real git checkouts and a database, and sweeps them")
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
	// Process inspection reads every process of this user on the machine,
	// and fails closed on one it cannot read (another test binary, another
	// agent): that is worktreereclaim's to test, with its own stubs. Here it
	// is stubbed, so this test's assertions run on any host.
	r.ProcessUse = func(context.Context, []string) error { return nil }
	r.Cwds = func(context.Context) ([]string, error) { return nil, nil }
	// One fixed clock for the whole test. The checkout's activity (mtimes,
	// reflog) is what git wrote while building the fixture, so the clock is
	// pinned once, after that: every sweep runs at the same instant, ten
	// days past the last write, and the server's rows are stamped at built.
	built := time.Now().UTC()
	sweptAt := built.Add(10 * 24 * time.Hour)
	r.Now = func() time.Time { return sweptAt }
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
	now := built
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

	// An approval written into approvals.json, as any process running as
	// the user could, is not authority: a pass deletes nothing for it.
	if _, err := worktreereclaim.RecordDecisions(ctx, state, worktreereclaim.DecisionApprove, []string{shown.Key}, "agent"); err != nil {
		t.Fatal(err)
	}
	for _, approvedOnly := range []bool{false, true} {
		opts.ApprovedOnly = approvedOnly
		for _, e := range r.SweepMachine(ctx, opts).Entries {
			if e.Path == f.root && (e.Outcome != worktreereclaim.OutcomeAwaiting || !strings.Contains(e.Detail, "did not come from the server")) {
				t.Fatalf("pass on a forged approval: %+v, want awaiting, not acted on", e)
			}
		}
	}
	opts.ApprovedOnly = false
	if _, err := os.Stat(filepath.Join(f.root, ".git")); err != nil {
		t.Fatalf("a forged approval removed the checkout: %v", err)
	}
	if _, ok := worktreereclaim.LatestCapture(ctx, f.project, f.worktreeID, f.root); ok {
		t.Fatal("a forged approval captured the checkout")
	}

	// The daemon's side of the approve command: its sweep configuration, this
	// reclaimer, and the removal run inline so its outcome can be read.
	setReclaimSweepConfig(worktreereclaim.SweepConfig{Mode: worktreereclaim.SweepOn, Roots: opts.Roots})
	prevReclaimer, prevStart := approvalReclaimer, startApprovalRun
	runs := 0
	approvalReclaimer = func() (*worktreereclaim.Reclaimer, error) { return r, nil }
	startApprovalRun = func(run func()) { runs++; run() }
	t.Cleanup(func() {
		approvalReclaimer, startApprovalRun = prevReclaimer, prevStart
		setReclaimSweepConfig(worktreereclaim.SweepConfig{Mode: worktreereclaim.SweepOff})
	})

	// The person approves it in the Inbox.
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
	if res := decided.Msg.Results; len(res) != 1 || !res[0].Recorded || res[0].Removal != "removing" || runs != 1 {
		t.Fatalf("results %+v after %d runs, want the approval recorded and its removal started once", res, runs)
	}
	results, err := worktreereclaim.ApprovalCommandResults(state)
	if err != nil || len(results) != 1 {
		t.Fatalf("command results %+v, %v", results, err)
	}
	done := results[0]
	if done.Status != worktreereclaim.ApprovalReclaimed || done.ApprovedBy != "inbox:"+userID || done.CaptureRef == "" ||
		done.CommandID == "" || done.IssuedAt.IsZero() {
		t.Fatalf("command result %+v, want reclaimed, approved by the person, with its capture", done)
	}
	if _, err := os.Stat(filepath.Join(f.root, ".git")); !os.IsNotExist(err) {
		t.Fatalf("the checkout is still there: %v", err)
	}
	capture, ok := worktreereclaim.LatestCapture(ctx, f.project, f.worktreeID, f.root)
	if !ok || capture.Ref != done.CaptureRef || capture.Clean {
		t.Fatalf("capture %+v (found %t), want %s holding the uncommitted edit", capture, ok, done.CaptureRef)
	}
	if got := rcGit(t, f.project, "rev-parse", "--verify", capture.Ref); got == "" {
		t.Fatalf("capture ref %s does not resolve", capture.Ref)
	}

	// The same approve command again (a retried delivery: the id and issue
	// time the server sent) is answered from the record and removes nothing
	// again.
	again, err := handleWorktreeReclaimApprove(ctx, []byte(fmt.Sprintf(`{"approvals":[{"key":%q,"fingerprint":%q,"path":%q}],"approved_by":"inbox:%s","command_id":%q,"issued_at_ms":%d}`,
		shown.Key, shown.Fingerprint, shown.Path, userID, done.CommandID, done.IssuedAt.UnixMilli())))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(again), `"status":"reclaimed"`) || !strings.Contains(string(again), `"replayed":true`) || runs != 1 {
		t.Fatalf("replayed command answered %s after %d runs, want the reclaimed answer and no new run", again, runs)
	}
}
