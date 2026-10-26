// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/worktreereclaim"
	"github.com/stretchr/testify/require"
)

// TestGitRecoveryDisposableGitHubCapability is deliberately never automatic.
// Prepare a B-validated fixture capture whose RemoteURL names a DISPOSABLE
// repository named reliant-recovery-capability-<opaque>, then provide:
//
//	RELIANT_GIT_RECOVERY_DISPOSABLE=YES_DELETE_TEST_REF
//	RELIANT_GIT_RECOVERY_CAPTURE_FILE=<descriptor JSON, never a credential>
//	RELIANT_GIT_RECOVERY_SOURCE=<fixture source repository>
//	RELIANT_GIT_RECOVERY_TOKEN_FD=<inherited read-only pipe descriptor>
//
// The caller supplies the credential over that pipe, NOT argv/env/config/file.
// The token must be the supported owner-scoped App/user credential class. Run
// this separately with a read-only credential/ruleset/workflow-history denial
// and RELIANT_GIT_RECOVERY_EXPECT_DENIED=1 to assert that no receipt is issued.
// Fixture branch is exact-CAS created and exact-lease deleted only; never point
// this at a production repository. No live proof is claimed by skipped runs.
func TestGitRecoveryDisposableGitHubCapability(t *testing.T) {
	if testing.Short() {
		t.Skip("opt-in disposable actual-host write; full explicit harness only")
	}
	if os.Getenv("RELIANT_GIT_RECOVERY_DISPOSABLE") != "YES_DELETE_TEST_REF" {
		t.Skip("requires explicit disposable GitHub fixture opt-in; no live host proof performed")
	}
	raw, err := os.ReadFile(os.Getenv("RELIANT_GIT_RECOVERY_CAPTURE_FILE"))
	require.NoError(t, err)
	var capture worktreereclaim.RecoveryCapture
	require.NoError(t, json.Unmarshal(raw, &capture))
	u, err := recoveryGitHubURL(capture.RemoteURL)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(strings.Split(strings.TrimPrefix(u.Path, "/"), "/")[1], "reliant-recovery-capability-"), "only explicitly named disposable repositories permitted")
	fd, err := strconv.Atoi(os.Getenv("RELIANT_GIT_RECOVERY_TOKEN_FD"))
	require.NoError(t, err)
	require.GreaterOrEqual(t, fd, 3)
	pipe := os.NewFile(uintptr(fd), "disposable-credential-pipe")
	require.NotNil(t, pipe)
	defer pipe.Close()
	stat, err := pipe.Stat()
	require.NoError(t, err)
	require.NotZero(t, stat.Mode()&os.ModeNamedPipe, "credential must arrive through a pipe, not a file")
	secret, err := io.ReadAll(io.LimitReader(pipe, 4097))
	require.NoError(t, err)
	require.LessOrEqual(t, len(secret), 4096)
	token := string(secret)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	receipt, err := PublishGitRecovery(ctx, os.Getenv("RELIANT_GIT_RECOVERY_SOURCE"), capture, token)
	if os.Getenv("RELIANT_GIT_RECOVERY_EXPECT_DENIED") == "1" {
		require.Error(t, err)
		require.True(t, receipt.VerifiedAt.IsZero())
		return
	}
	require.NoError(t, err)
	// If later assertions fail, attempt ONLY the exact-old fixture ref deletion.
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		require.NoError(t, DeleteGitRecovery(cleanup, capture, token))
	})
	require.Equal(t, capture, receipt.Capture)
	destination := t.TempDir() + "/restored"
	_, err = WithVerifiedGitRecovery(ctx, capture, token, func(bare string) error {
		return worktreereclaim.RestoreRecoveryCheckout(ctx, bare, destination, "reliant-capability-restore", capture)
	})
	require.NoError(t, err)
	require.NoError(t, DeleteGitRecovery(ctx, capture, token))
}
