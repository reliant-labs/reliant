package daemonruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/reliant-labs/reliant/internal/worktreereclaim"
	"github.com/stretchr/testify/require"
)

// Build independent source repositories, then restore to distinct installed
// clones using copied closure. No source disk survives the simulated restart.
func recoveryRestartFixture(t *testing.T, prepared bool) (RecoveryRestoreCommand, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("multi-repository restore proof uses Git subprocesses")
	}
	if runtime.GOOS != "linux" {
		t.Skip("Linux safe capture/publication")
	}
	ctx := context.Background()
	root := t.TempDir()
	req := RecoveryRestoreCommand{RecoveryRestoreRequest: worktreereclaim.RecoveryRestoreRequest{RecoveryID: "catalog", IdempotencyKey: "retry-key", DestinationPath: filepath.Join(root, "published"), Branch: "new-recovery"}, Repositories: map[string]string{}, Token: "ephemeral"}
	stage, err := os.MkdirTemp(root, ".reliant-recovery-")
	require.NoError(t, err)
	workspace := filepath.Join(stage, "workspace")
	require.NoError(t, os.Mkdir(workspace, 0700))
	for _, id := range []string{"alpha", "beta"} {
		f := newPublishFixture(t)
		require.NoError(t, os.WriteFile(filepath.Join(f.clone, "raw"), []byte(id+" disk\r\n"), 0644))
		c := worktreereclaim.RecoveryCapture{WorktreeID: "wt", CaptureID: "capture", RepoID: id, Rel: id, RecoveryRef: "refs/heads/reliant-recovery/wt/capture/" + id}
		c, err = worktreereclaim.CaptureRecovery(ctx, f.clone, c)
		require.NoError(t, err)
		parent := filepath.Join(root, id+"-parent")
		pubGit(t, root, "clone", "-q", f.remote, parent)
		bare := filepath.Join(root, id+".git")
		pubGit(t, root, "init", "--bare", bare)
		pubGit(t, bare, "fetch", f.clone, c.CommitOID)
		require.NoError(t, worktreereclaim.RestoreRecoveryLinkedCheckout(ctx, bare, parent, filepath.Join(workspace, id), req.Branch, c))
		req.Repositories[id] = parent
		req.Captures = append(req.Captures, c)
		require.NoError(t, os.RemoveAll(f.clone))
		require.NoError(t, os.RemoveAll(bare))
	}
	identity, err := json.Marshal(struct {
		Request      worktreereclaim.RecoveryRestoreRequest
		Repositories map[string]string
	}{req.RecoveryRestoreRequest, req.Repositories})
	require.NoError(t, err)
	sum := sha256.Sum256(identity)
	marker := recoveryRestoreMarker{Digest: hex.EncodeToString(sum[:]), RepoIDs: []string{"alpha", "beta"}, StagePath: workspace, Prepared: prepared}
	data, err := json.Marshal(marker)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(req.DestinationPath+".reliant-recovery.json", data, 0600))
	return req, workspace
}

func TestRecoveryStageCleanupNeverDeletesLateFile(t *testing.T) {
	stage := t.TempDir()
	workspace := filepath.Join(stage, "workspace")
	require.NoError(t, os.Mkdir(workspace, 0700))
	late := filepath.Join(workspace, "late-user-write")
	require.NoError(t, os.WriteFile(late, []byte("only copy"), 0600))
	require.Error(t, removeRecoveryStageDirectories(stage))
	data, err := os.ReadFile(late)
	require.NoError(t, err)
	require.Equal(t, "only copy", string(data))
}

func TestRecoveryRestoreMultiRepoPreparedRestartAndResponseLoss(t *testing.T) {
	req, workspace := recoveryRestartFixture(t, true)
	got := restoreRecoveryWorkspace(context.Background(), req)
	require.Equal(t, worktreereclaim.OutcomeRestored, got.Outcome, got.Detail)
	require.Equal(t, []string{"alpha", "beta"}, got.RestoredRepoIDs)
	require.NoDirExists(t, workspace)
	for _, c := range req.Captures {
		require.NoError(t, worktreereclaim.ValidateRestoredRecovery(context.Background(), filepath.Join(got.Path, c.Rel), req.Branch, c))
	}
	// Discard the first response: exact persisted state is the only authority.
	retry := restoreRecoveryWorkspace(context.Background(), req)
	require.Equal(t, got, retry)
	require.NoError(t, os.WriteFile(filepath.Join(got.Path, "beta", "raw"), []byte("user after response loss"), 0644))
	changed := restoreRecoveryWorkspace(context.Background(), req)
	require.Equal(t, worktreereclaim.OutcomeHeld, changed.Outcome)
	data, err := os.ReadFile(filepath.Join(got.Path, "beta", "raw"))
	require.NoError(t, err)
	require.Equal(t, "user after response loss", string(data))
}

func TestRecoveryRestoreIncompleteRestartPreservesAllStaging(t *testing.T) {
	req, workspace := recoveryRestartFixture(t, false)
	// Ordinary own partial materialization plus user data in a later repo or
	// container. Preflight must hold before deleting even the unchanged alpha.
	require.NoError(t, os.Remove(filepath.Join(workspace, "alpha", "raw")))
	for _, where := range []string{"beta/untracked", "beta/raw", "outside-checkouts", "../outside-workspace"} {
		t.Run(where, func(t *testing.T) {
			user := filepath.Join(workspace, where)
			previous, previousErr := os.ReadFile(user)
			t.Cleanup(func() {
				if os.IsNotExist(previousErr) {
					require.NoError(t, os.Remove(user))
				} else {
					require.NoError(t, os.WriteFile(user, previous, 0644))
				}
			})
			require.NoError(t, os.WriteFile(user, []byte("only user copy"), 0644))
			got := restoreRecoveryWorkspace(context.Background(), req)
			require.Equal(t, worktreereclaim.OutcomeHeld, got.Outcome)
			data, err := os.ReadFile(user)
			require.NoError(t, err)
			require.Equal(t, "only user copy", string(data))
			require.FileExists(t, filepath.Join(workspace, "alpha", ".git"))
			require.FileExists(t, req.DestinationPath+".reliant-recovery.json")
		})
	}
}
