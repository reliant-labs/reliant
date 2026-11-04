package worktreereclaim

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Real Git capture -> push -> independent exact-ref fetch -> closure proof ->
// quarantine -> removal -> separate restore. No transport success or movement
// is mocked; only daemon admission/process observations are deterministic.
func TestFinalizeRecoveryRemovesOnlyAfterIndependentRemoteProof(t *testing.T) {
	heavyUnderShort(t, "real Git remote closure, quarantine removal and fresh restore")
	ctx := context.Background()
	parent := recoveryTestRepo(t)
	root := t.TempDir()
	id := uuid.NewString()
	source := filepath.Join(root, id)
	recoveryTestGit(t, parent, "worktree", "add", "-q", "-b", "recover", source)
	require.NoError(t, LockCheckout(ctx, source, id))
	recoveryTestWrite(t, source, "file.txt", []byte("staged bytes\n"), 0644)
	recoveryTestGit(t, source, "add", "file.txt")
	recoveryTestWrite(t, source, "file.txt", []byte("raw disk bytes\n"), 0644)
	descriptor := recoveryTestDescriptor()
	descriptor.WorktreeID = id
	descriptor.Rel = ""
	descriptor.RecoveryRef = "refs/heads/reliant-recovery/" + id + "/" + descriptor.CaptureID + "/" + descriptor.RepoID
	capture, err := CaptureRecovery(ctx, source, descriptor)
	require.NoError(t, err)
	remote := t.TempDir()
	recoveryTestGit(t, remote, "init", "--bare", "-q", ".")
	recoveryTestGit(t, source, "push", "--", remote, capture.CommitOID+":"+capture.RecoveryRef)
	verify := func(_ context.Context, c RecoveryCapture) (RecoveryReceipt, error) {
		proof := t.TempDir()
		recoveryTestGit(t, proof, "init", "--bare", "-q", ".")
		recoveryTestGit(t, proof, "fetch", "--no-tags", "--", remote, c.RecoveryRef)
		require.Equal(t, c.CommitOID, strings.TrimSpace(string(recoveryTestGit(t, proof, "rev-parse", "FETCH_HEAD"))))
		recoveryTestGit(t, proof, "fsck", "--strict", "--full", "--no-reflogs", c.CommitOID)
		err := ValidateRecoveryCapture(ctx, proof, c)
		return RecoveryReceipt{Capture: c, VerifiedAt: time.Now().UTC(), VerifierVersion: "independent-bare-fetch-test"}, err
	}
	receipt, err := verify(ctx, capture)
	require.NoError(t, err)
	now := time.Now().UTC().Add(10 * time.Minute)
	activity := RecoveryActivity{Generation: 1, ObservationID: "epoch", ObservedSince: now.Add(-8 * 24 * time.Hour), LastActivity: now.Add(-8 * 24 * time.Hour), ObservedThrough: now, Complete: true}
	req := RecoveryRequest{RecoveryID: "journal", Worktree: Worktree{ID: id, Path: source, State: StateArchived, Fence: "123", Checkouts: []Checkout{{RepoPath: parent, Rel: ""}}}, Captures: []RecoveryCapture{capture}, Receipts: []RecoveryReceipt{receipt}, Activity: activity}
	req.Authorization = &RecoveryAuthorization{RecoveryID: req.RecoveryID, CaptureID: capture.CaptureID, WorktreeID: id, Fence: "123", ObservationID: activity.ObservationID, ManifestDigest: RecoveryWorkspaceDigest(req.Captures), ExpiresAt: now.Add(25 * time.Second)}
	r := NewReclaimer(root)
	r.Now = func() time.Time { return now }
	out := r.FinalizeRecovery(ctx, req, RecoveryFinalizeDeps{Verify: verify, Observe: func(context.Context, string) (RecoveryActivity, error) { return activity, nil }, Acquire: func(context.Context, []string) (RecoveryLease, error) { return finalizeTestLease{}, nil }, CheckProcessUse: func(context.Context, []string) error { return nil }})
	require.Equal(t, OutcomeRemoved, out.Outcome, "%+v", out)
	require.Equal(t, []string{capture.RepoID}, out.RemovedRepoIDs)
	_, err = os.Lstat(source)
	require.True(t, os.IsNotExist(err), "verified original removed")
	require.Empty(t, r.quarantined(id))
	// Source and its entire administrative/object store are gone. Restore may
	// depend only on the independently fetched remote object closure.
	require.NoError(t, os.RemoveAll(parent))
	proof := t.TempDir()
	recoveryTestGit(t, proof, "init", "--bare", "-q", ".")
	recoveryTestGit(t, proof, "fetch", "--no-tags", "--", remote, capture.RecoveryRef)
	destination := filepath.Join(t.TempDir(), "restored")
	require.NoError(t, RestoreRecoveryCheckout(ctx, proof, destination, "recovered-result", capture))
	require.NoError(t, ValidateRestoredRecovery(ctx, destination, "recovered-result", capture))
	data, err := os.ReadFile(filepath.Join(destination, "file.txt"))
	require.NoError(t, err)
	require.Equal(t, "raw disk bytes\n", string(data))
	require.Equal(t, capture.CommitOID, strings.TrimSpace(string(recoveryTestGit(t, remote, "rev-parse", capture.RecoveryRef))), "restore retains exact remote ref")
}
