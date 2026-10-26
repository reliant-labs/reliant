package worktreesweep

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
	"github.com/stretchr/testify/require"
)

type pipelineCredentials struct{}

func (pipelineCredentials) UserAccessToken(context.Context, string, string) (gitcredentialclient.Token, error) {
	return gitcredentialclient.NewToken("ephemeral-test-credential", nil), nil
}

type pipelineSender struct {
	t         *testing.T
	now       time.Time
	finalized bool
}

func (s *pipelineSender) SendDaemonCommandToDaemon(_ context.Context, _, _, command string, payload []byte, _ int32) ([]byte, error) {
	switch command {
	case CommandRecoveryObserve:
		return json.Marshal(worktreereclaim.RecoveryResponse{Activity: worktreereclaim.RecoveryActivity{ObservationID: "epoch", Complete: true, ObservedSince: s.now.Add(-8 * 24 * time.Hour), LastActivity: s.now.Add(-8 * 24 * time.Hour), ObservedThrough: s.now}})
	case CommandRecoveryCapture:
		var req worktreereclaim.RecoveryRequest
		require.NoError(s.t, json.Unmarshal(payload, &req))
		for i := range req.Captures {
			c := &req.Captures[i]
			c.CommitOID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			c.ManifestDigest = "manifest"
			c.InventoryDigest = "inventory"
			c.SecretPolicy = "test-policy"
		}
		return json.Marshal(worktreereclaim.RecoveryResponse{Outcome: worktreereclaim.OutcomeCaptured, Captures: req.Captures})
	case "git.recovery_publish":
		var req struct {
			Capture worktreereclaim.RecoveryCapture `json:"capture"`
		}
		require.NoError(s.t, json.Unmarshal(payload, &req))
		return json.Marshal(struct {
			Receipt worktreereclaim.RecoveryReceipt `json:"receipt"`
		}{worktreereclaim.RecoveryReceipt{Capture: req.Capture, VerifiedAt: s.now, VerifierVersion: "test-transport"}})
	case CommandRecoveryFinalize:
		var req worktreereclaim.RecoveryRequest
		require.NoError(s.t, json.Unmarshal(payload, &req))
		require.NotNil(s.t, req.Authorization)
		require.Len(s.t, req.Receipts, len(req.Captures))
		require.Equal(s.t, worktreereclaim.RecoveryWorkspaceDigest(req.Captures), req.Authorization.ManifestDigest)
		s.finalized = true
		var ids []string
		for _, c := range req.Captures {
			ids = append(ids, c.RepoID)
		}
		return json.Marshal(worktreereclaim.RecoveryResponse{Outcome: worktreereclaim.OutcomeRemoved, RemovedRepoIDs: ids})
	}
	return nil, fmt.Errorf("unexpected pipeline command %s", command)
}

// Real database orchestration proof; transport/finalizer safety is exercised by
// their real-Git suites. In particular receipts must survive claim unchanged.
func TestRecoveryPipelineReceiptClaimAuthorization(t *testing.T) {
	testkit.Slow(t, "private pgtest journal and real Git source fixture")
	t.Parallel()
	e := newEnv(t)
	wt, p := e.worktree(t, "pipeline", true)
	now := time.Now().UTC()
	_, err := e.repo.ORM().Bun().NewRaw(`INSERT INTO repos (id,project_id,name,relative_path,remote_url,created_at,updated_at) VALUES ('pipeline-repo',?,'repo','','https://github.com/test/test.git',now(),now())`, wt.ProjectID).Exec(e.ctx)
	require.NoError(t, err)
	sender := &pipelineSender{t: t, now: now}
	e.sweeper.sender = sender
	e.sweeper.WithGitRecovery(e.repo, pipelineCredentials{}, true)
	e.sweeper.now = func() time.Time { return now }
	current := e.get(t, wt.ID)
	w := worktreereclaim.Worktree{ID: wt.ID, Path: p, State: worktreereclaim.StateArchived, Fence: Fence(current), Checkouts: []worktreereclaim.Checkout{{RepoPath: filepath.Clean(e.project.Path), Rel: ""}}}
	require.True(t, e.sweeper.recoverHeld(e.ctx, target{e.userID, e.daemon}, &core.ReclaimCandidate{Worktree: current, OwnerUserID: e.userID, ProjectPath: e.project.Path}, w))
	require.True(t, sender.finalized, "verified receipt followed by claim must reach finalizer")
	rows, err := e.repo.ListWorktreeRecoveries(e.ctx, e.userID, e.project.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "removed", rows[0].Phase)
	require.NotEmpty(t, rows[0].Checkouts[0].ReceiptJSON)
	require.True(t, e.get(t, wt.ID).CleanupMetadata.DirectoryDeleted)
}
