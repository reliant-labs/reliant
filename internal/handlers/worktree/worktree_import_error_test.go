// Copyright (c) 2025 Reliant Labs
package worktree

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/toolexec"
)

// ImportWorktree answered Internal for every failure of a daemon step: a user
// with no machine connected was told the server had failed. The code now says
// who has to act.
func TestImportWorktreeError_CodeSaysWhoMustAct(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		err      error
		code     connect.Code
		contains string
	}{
		"base branch missing": {
			err:  svcerr.WithMessage(svcerr.NotFound("base branch"), "base branch 'feat/x' doesn't exist in forge — check the name"),
			code: connect.CodeNotFound, contains: "base branch 'feat/x' doesn't exist in forge",
		},
		"branch checked out elsewhere": {
			err:  svcerr.FailedPrecondition("branch 'feat/y' is already checked out in another worktree of forge"),
			code: connect.CodeFailedPrecondition, contains: "already checked out in another worktree of forge",
		},
		"no machine": {
			err:  fmt.Errorf("daemon command worktree.discover_repos: %w", toolexec.ErrNoDaemon),
			code: connect.CodeFailedPrecondition, contains: "no daemon available",
		},
		"machine starting": {
			err:  fmt.Errorf("daemon command worktree.discover_repos: %w", toolexec.ErrDaemonPending),
			code: connect.CodeUnavailable, contains: "no daemon connected",
		},
		"machine not answering": {
			err:  fmt.Errorf("daemon command worktree.adopt_move: %w", toolexec.ErrDaemonTimeout),
			code: connect.CodeDeadlineExceeded,
		},
		"server fault": {
			err:  errors.New("failed to record worktree: pq: connection refused"),
			code: connect.CodeInternal, contains: "failed to import worktree",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := importWorktreeError("import worktree", tc.err)
			assert.Equal(t, tc.code, svcerr.Code(err))
			assert.Contains(t, err.Error(), tc.contains)
		})
	}
}

// Internal keeps its cause off the wire: the log has it, the client does not.
func TestImportWorktreeError_InternalDoesNotLeakTheCause(t *testing.T) {
	t.Parallel()

	err := importWorktreeError("import worktree", errors.New("pq: password authentication failed for user reliant"))
	assert.Equal(t, connect.CodeInternal, svcerr.Code(err))
	assert.NotContains(t, err.Error(), "pq:")
}
