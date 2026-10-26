package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
	"github.com/stretchr/testify/require"
)

func recoveryFixture(t *testing.T) (*Repo, *WorktreeRecovery, worktreereclaim.RecoveryCapture) {
	t.Helper()
	repo := NewTestRepo(t)
	ctx := context.Background()
	p, err := repo.GetProject(ctx, "test-project")
	require.NoError(t, err)
	seedFilterWorktree(t, repo, p.ID, "recover-source", false, time.Now().UTC())
	require.NoError(t, repo.ArchiveWorktree(ctx, "recover-source"))
	w, err := repo.GetWorktree(ctx, "recover-source")
	require.NoError(t, err)
	d := worktreereclaim.RecoveryCapture{Version: 1, CaptureID: "capture", WorktreeID: w.ID, RepoID: "repo", RemoteURL: "https://github.com/test/test.git", RecoveryRef: "refs/heads/reliant-recovery/recover-source/capture/repo", CommitOID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ManifestDigest: "manifest", HeadOID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", SecretPolicy: "policy-v1", CapturedAt: time.Now().UTC()}
	_, err = repo.ORM().Bun().NewRaw(`INSERT INTO repos (id,project_id,name,relative_path,remote_url,created_at,updated_at) VALUES (?,?,?,'',?,now(),now())`, d.RepoID, p.ID, d.RepoID, d.RemoteURL).Exec(ctx)
	require.NoError(t, err)
	encoded, err := json.Marshal(d)
	require.NoError(t, err)
	r := &WorktreeRecovery{ID: "recovery", OwnerUserID: p.UserID, SourceWorktreeID: w.ID, ProjectID: p.ID, DaemonID: "daemon", CaptureID: d.CaptureID, Fence: strconv.FormatInt(w.DeletedAt.UnixNano(), 10), ActivityGeneration: w.ActivityGeneration, ObservationID: "epoch", ManifestDigest: worktreereclaim.RecoveryWorkspaceDigest([]worktreereclaim.RecoveryCapture{d}), IdempotencyKey: "request", Checkouts: []RecoveryCheckout{{RepoID: d.RepoID, RemoteURL: d.RemoteURL, RecoveryRef: d.RecoveryRef, CommitOID: d.CommitOID, ManifestDigest: d.ManifestDigest, DescriptorJSON: string(encoded)}}}
	require.NoError(t, repo.CreateWorktreeRecovery(ctx, r))
	return repo, r, d
}

func TestRecoveryCatalogRetentionAndOwnership(t *testing.T) {
	testkit.Slow(t, "private pgtest database migrations and catalog retention")
	t.Parallel()
	repo, r, _ := recoveryFixture(t)
	ctx := context.Background()
	require.NoError(t, repo.CreateWorktreeRecovery(ctx, r), "retry is idempotent")
	_, err := repo.GetWorktreeRecovery(ctx, "other-owner", r.ID)
	require.ErrorIs(t, err, core.ErrRecoveryNotFound)
	require.NoError(t, repo.DeleteWorktree(ctx, r.SourceWorktreeID))
	_, err = repo.ORM().Bun().NewRaw(`DELETE FROM projects WHERE id=?`, r.ProjectID).Exec(ctx)
	require.NoError(t, err)
	got, err := repo.GetWorktreeRecovery(ctx, r.OwnerUserID, r.ID)
	require.NoError(t, err)
	require.Len(t, got.Checkouts, 1)
	list, err := repo.ListWorktreeRecoveries(ctx, r.OwnerUserID, "")
	require.NoError(t, err)
	require.Len(t, list, 1)
	list, err = repo.ListWorktreeRecoveries(ctx, "other-owner", "")
	require.NoError(t, err)
	require.Empty(t, list)
}

func TestRecoveryClaimReceiptAndAdmission(t *testing.T) {
	testkit.Slow(t, "private pgtest database admission and receipt checks")
	t.Parallel()
	repo, r, d := recoveryFixture(t)
	ctx := context.Background()
	require.NoError(t, repo.ClaimWorktreeRecovery(ctx, r.OwnerUserID, r.ID, r.ActivityGeneration))
	require.ErrorIs(t, repo.UnarchiveWorktree(ctx, r.SourceWorktreeID), core.ErrWorktreeFinalizing)
	require.ErrorIs(t, repo.RecordWorktreeActivity(ctx, r.SourceWorktreeID), core.ErrWorktreeFinalizing)
	chat := &Chat{ID: "bound", ProjectID: r.ProjectID, UserID: r.OwnerUserID, WorktreeID: &r.SourceWorktreeID, State: ChatStateIdle, CreatedAt: time.Now(), UpdatedAt: time.Now(), LastActive: time.Now()}
	require.ErrorIs(t, repo.CreateChat(ctx, chat), core.ErrWorktreeFinalizing)
	require.ErrorIs(t, repo.AuthorizeWorktreeRecovery(ctx, r.OwnerUserID, r.ID, time.Now().Add(time.Minute)), core.ErrRecoveryConflict, "no receipt no delete")
	receipt := worktreereclaim.RecoveryReceipt{Capture: d, VerifiedAt: time.Now().UTC(), VerifierVersion: "independent-fetch-v1"}
	receipt.Capture.CommitOID = "different"
	b, _ := json.Marshal(receipt)
	require.ErrorIs(t, repo.RecordWorktreeRecoveryReceipt(ctx, r.OwnerUserID, r.ID, d.RepoID, string(b)), core.ErrRecoveryConflict)
	receipt.Capture = d
	b, _ = json.Marshal(receipt)
	require.NoError(t, repo.RecordWorktreeRecoveryReceipt(ctx, r.OwnerUserID, r.ID, d.RepoID, string(b)))
	require.NoError(t, repo.AuthorizeWorktreeRecovery(ctx, r.OwnerUserID, r.ID, time.Now().Add(time.Minute)))
	require.ErrorIs(t, repo.CancelWorktreeRecovery(ctx, r.OwnerUserID, r.ID, "new activity"), core.ErrRecoveryConflict, "in-flight authority cannot be revoked by a database boolean")
	_, err := repo.ORM().Bun().NewRaw(`UPDATE worktree_recoveries SET authorized_until=now()-interval '1 second' WHERE id=?`, r.ID).Exec(ctx)
	require.NoError(t, err)
	require.NoError(t, repo.CancelWorktreeRecovery(ctx, r.OwnerUserID, r.ID, "new activity"))
	require.NoError(t, repo.CreateChat(ctx, chat))
	require.ErrorIs(t, repo.ClaimWorktreeRecovery(ctx, r.OwnerUserID, r.ID, r.ActivityGeneration), core.ErrRecoveryConflict)
}

func TestRecoveryClaimChatRaceAndLockOrder(t *testing.T) {
	testkit.Slow(t, "private pgtest concurrent parent-lock admission proof")
	t.Parallel()
	repo, r, _ := recoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() { <-start; results <- repo.ClaimWorktreeRecovery(ctx, r.OwnerUserID, r.ID, r.ActivityGeneration) }()
	go func() {
		<-start
		results <- repo.CreateChat(ctx, &Chat{ID: "race-chat", ProjectID: r.ProjectID, UserID: r.OwnerUserID, WorktreeID: &r.SourceWorktreeID, State: ChatStateIdle, CreatedAt: time.Now(), UpdatedAt: time.Now(), LastActive: time.Now()})
	}()
	close(start)
	a, b := <-results, <-results
	require.True(t, (a == nil) != (b == nil), "exactly one claim or live binding wins: %v %v", a, b)
	require.NoError(t, ctx.Err())
	// Parent-before-journal ordering for concurrent claim/cancel cannot deadlock.
	require.NoError(t, repo.CancelWorktreeRecovery(ctx, r.OwnerUserID, r.ID, "reset"))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = repo.CancelWorktreeRecovery(ctx, r.OwnerUserID, r.ID, fmt.Sprint(i))
			_ = repo.ClaimWorktreeRecovery(ctx, r.OwnerUserID, r.ID, r.ActivityGeneration)
		}(i)
	}
	wg.Wait()
	require.NoError(t, ctx.Err())
}

func TestRecoveryArchivedChatWorkflowAdmission(t *testing.T) {
	testkit.Slow(t, "private pgtest archived-chat run admission")
	t.Parallel()
	repo, r, _ := recoveryFixture(t)
	ctx := context.Background()
	_, err := repo.ORM().Bun().NewRaw(`INSERT INTO chats (id,project_id,user_id,state,worktree_id,title,created_at,updated_at,last_active) VALUES ('archived-run',?,?,3,?,'',now(),now(),now())`, r.ProjectID, r.OwnerUserID, r.SourceWorktreeID).Exec(ctx)
	require.NoError(t, err)
	require.NoError(t, repo.ClaimWorktreeRecovery(ctx, r.OwnerUserID, r.ID, r.ActivityGeneration))
	_, err = repo.ORM().Bun().NewRaw(`INSERT INTO workflows (id,chat_id,workflow_name,thread,state,created_at) VALUES ('blocked','archived-run','test','main',2,now())`).Exec(ctx)
	require.Error(t, err)
	require.NoError(t, repo.CancelWorktreeRecovery(ctx, r.OwnerUserID, r.ID, "retry"))
	_, err = repo.ORM().Bun().NewRaw(`INSERT INTO workflows (id,chat_id,workflow_name,thread,state,created_at) VALUES ('allowed','archived-run','test','main',2,now())`).Exec(ctx)
	require.NoError(t, err)
	w, err := repo.GetWorktree(ctx, r.SourceWorktreeID)
	require.NoError(t, err)
	require.Greater(t, w.ActivityGeneration, r.ActivityGeneration)
	require.ErrorIs(t, repo.ClaimWorktreeRecovery(ctx, r.OwnerUserID, r.ID, w.ActivityGeneration), core.ErrRecoveryConflict)
}

func TestRecoveryClaimAfterReceiptAndCrashCancellation(t *testing.T) {
	testkit.Slow(t, "private pgtest receipt-first authorization and crashed claim recovery")
	t.Parallel()
	repo, r, d := recoveryFixture(t)
	ctx := context.Background()
	receipt, _ := json.Marshal(worktreereclaim.RecoveryReceipt{Capture: d, VerifiedAt: time.Now().UTC(), VerifierVersion: "fetch-v1"})
	require.NoError(t, repo.RecordWorktreeRecoveryReceipt(ctx, r.OwnerUserID, r.ID, d.RepoID, string(receipt)))
	require.NoError(t, repo.ClaimWorktreeRecovery(ctx, r.OwnerUserID, r.ID, r.ActivityGeneration))
	claimed, err := repo.GetWorktreeRecovery(ctx, r.OwnerUserID, r.ID)
	require.NoError(t, err)
	require.Equal(t, "verified", claimed.Phase, "receipt-first claim must remain authorizable")
	require.NoError(t, repo.AuthorizeWorktreeRecovery(ctx, r.OwnerUserID, r.ID, time.Now().Add(time.Minute)))
	_, err = repo.ORM().Bun().NewRaw(`UPDATE worktree_recoveries SET authorized_until=now()-interval '1 second' WHERE id=?`, r.ID).Exec(ctx)
	require.NoError(t, err)
	require.NoError(t, repo.CancelWorktreeRecovery(ctx, r.OwnerUserID, r.ID, "expired"))
	require.NoError(t, repo.ClaimWorktreeRecovery(ctx, r.OwnerUserID, r.ID, r.ActivityGeneration))
	// Simulate a worker dying after the claim commit, before authorization.
	require.ErrorIs(t, repo.RecordWorktreeActivity(ctx, r.SourceWorktreeID), core.ErrWorktreeFinalizing)
	require.NoError(t, repo.CancelWorktreeRecovery(ctx, r.OwnerUserID, r.ID, "preauthorization claim expired"))
	require.ErrorIs(t, repo.AuthorizeWorktreeRecovery(ctx, r.OwnerUserID, r.ID, time.Now().Add(time.Minute)), core.ErrRecoveryConflict, "delayed worker cannot resurrect canceled claim")
	require.NoError(t, repo.RecordWorktreeActivity(ctx, r.SourceWorktreeID))
	require.NoError(t, repo.UnarchiveWorktree(ctx, r.SourceWorktreeID), "conversation can continue after crashed claim")
}

func TestRecoveryOperationRetryBindsTargetAndSerializesDelete(t *testing.T) {
	testkit.Slow(t, "private pgtest operation retry fencing")
	t.Parallel()
	repo, r, d := recoveryFixture(t)
	ctx := context.Background()
	receipt, _ := json.Marshal(worktreereclaim.RecoveryReceipt{Capture: d, VerifiedAt: time.Now().UTC(), VerifierVersion: "fetch-v1"})
	require.NoError(t, repo.RecordWorktreeRecoveryReceipt(ctx, r.OwnerUserID, r.ID, d.RepoID, string(receipt)))
	require.NoError(t, repo.BeginWorktreeRecoveryOperation(ctx, r.OwnerUserID, r.ID, "restoring", "op-key", "target-a"))
	require.ErrorIs(t, repo.BeginWorktreeRecoveryOperation(ctx, r.OwnerUserID, r.ID, "restoring", "op-key", "target-b"), core.ErrRecoveryConflict)
	require.ErrorIs(t, repo.BeginWorktreeRecoveryOperation(ctx, r.OwnerUserID, r.ID, "deletion_requested", "delete-key", ""), core.ErrRecoveryConflict)
	require.NoError(t, repo.CompleteWorktreeRecoveryCheckout(ctx, r.OwnerUserID, r.ID, d.RepoID, "held", "retry"))
	require.ErrorIs(t, repo.BeginWorktreeRecoveryOperation(ctx, r.OwnerUserID, r.ID, "deletion_requested", "delete-key", ""), core.ErrRecoveryConflict)
	require.NoError(t, repo.BeginWorktreeRecoveryOperation(ctx, r.OwnerUserID, r.ID, "restoring", "op-key", "target-a"))
	require.NoError(t, repo.CompleteWorktreeRecoveryCheckout(ctx, r.OwnerUserID, r.ID, d.RepoID, "restored", ""))
	require.NoError(t, repo.BeginWorktreeRecoveryOperation(ctx, r.OwnerUserID, r.ID, "restoring", "op-key", "target-a"))
}
