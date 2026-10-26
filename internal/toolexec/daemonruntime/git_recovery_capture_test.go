// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/reliant/internal/worktreereclaim"
	"github.com/stretchr/testify/require"
)

// Real format + policy + transport + restore proof. The bare remote is injected
// only in this test; it is not evidence of actual GitHub policy compatibility.
func TestRecoveryRealCaptureSurvivesSourceLoss(t *testing.T) {
	t.Parallel()
	f, c, tr := recoveryFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.clone, "README.md"), []byte("staged version\n"), 0600))
	pubGit(t, f.clone, "add", "README.md")
	require.NoError(t, os.WriteFile(filepath.Join(f.clone, "README.md"), []byte("disk version\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(f.clone, "untracked.txt"), []byte("ordinary notes\n"), 0600))
	// Capture returns the frozen descriptor and does not stage or commit real HEAD.
	capture, err := worktreereclaim.CaptureRecovery(context.Background(), f.clone, c)
	require.NoError(t, err)
	tr.validate = worktreereclaim.ValidateRecoveryCapture
	receipt, err := tr.publish(context.Background(), f.clone, capture, publishToken)
	require.NoError(t, err)
	require.Equal(t, capture, receipt.Capture)
	require.NoError(t, os.RemoveAll(f.clone))
	destination := filepath.Join(t.TempDir(), "restored")
	s, err := tr.session(capture, publishToken)
	require.NoError(t, err)
	defer s.close()
	_, err = s.verify(context.Background(), capture, func(bare string) error {
		return worktreereclaim.RestoreRecoveryCheckout(context.Background(), bare, destination, "recovered-fixture", capture)
	})
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(destination, "README.md"))
	require.NoError(t, err)
	require.Equal(t, "disk version\n", string(data))
	require.Equal(t, "staged version", pubGit(t, destination, "show", ":README.md"))
	require.Equal(t, capture.HeadOID, pubGit(t, destination, "rev-parse", "HEAD"))
	require.Equal(t, "MM README.md\n?? untracked.txt", pubGit(t, destination, "status", "--porcelain=v1"))
	require.Equal(t, capture.CommitOID, pubGit(t, f.remote, "rev-parse", capture.RecoveryRef), "restore never retires remote recovery ref")
}
