// Copyright (c) 2025 Reliant Labs
package workspacecreate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/errclass"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/worktreeerr"
)

// user_errors_test.go — a worktree the caller asked for wrongly is the
// caller's to fix, so it is a user error: typed, at a 4xx code, logged at INFO,
// never sent to Sentry, and explained in words the caller can act on.
//
// Prod, 2026-10-13 (Sentry GO-2A, GO-2B): an agent's worktree tool call named
// the base branch `feat/telemetry-delivery-c2`, which did not exist. Finish
// logged "Failed to create git worktree" with git's text as a string and
// "Worktree creation failed" with an untyped error; both were ERROR and both
// became Sentry issues.

func TestFinish_DiagnosedCheckoutFailuresAreUserErrors(t *testing.T) {
	cases := []struct {
		name     string
		resp     checkoutResponse
		adopt    bool
		wantCode connect.Code
		wantMsg  []string
	}{
		{
			name: "base branch does not exist",
			resp: checkoutResponse{
				Error:     "fatal: invalid reference: feat/telemetry-delivery-c2\n",
				ErrorCode: worktreeerr.BaseNotFound, ErrorDetail: "feat/telemetry-delivery-c2",
			},
			wantCode: connect.CodeNotFound,
			wantMsg:  []string{"base branch 'feat/telemetry-delivery-c2' doesn't exist in b", "check the name", "'origin/feat/telemetry-delivery-c2'"},
		},
		{
			name: "branch name git refuses",
			resp: checkoutResponse{
				Error:     "fatal: 'feat-x' is not a valid branch name\n",
				ErrorCode: worktreeerr.InvalidBranchName, ErrorDetail: "feat-x",
			},
			wantCode: connect.CodeInvalidArgument,
			wantMsg:  []string{"'feat-x' is not a valid git branch name"},
		},
		{
			name: "branch nests with an existing branch",
			resp: checkoutResponse{
				Error:     "fatal: cannot lock ref 'refs/heads/feat-x': 'refs/heads/feat-x/old' exists\n",
				ErrorCode: worktreeerr.BranchNameConflict, ErrorDetail: "feat-x/old",
			},
			wantCode: connect.CodeFailedPrecondition,
			wantMsg:  []string{"branch 'feat-x' can't be created in b: branch 'feat-x/old' already exists", "choose a different branch name"},
		},
		{
			name: "branch checked out elsewhere",
			resp: checkoutResponse{
				Error:     "fatal: 'feat-x' is already used by worktree at '/proj/b'\n",
				ErrorCode: worktreeerr.BranchCheckedOut, ErrorDetail: "/proj/b",
			},
			wantCode: connect.CodeFailedPrecondition,
			wantMsg:  []string{"branch 'feat-x' is already checked out at /proj/b", "choose a different branch"},
		},
		{
			name: "branch checked out elsewhere, adopting",
			resp: checkoutResponse{
				Error:     "fatal: 'feat-x' is already used by worktree at '/proj/b'\n",
				ErrorCode: worktreeerr.BranchCheckedOut, ErrorDetail: "/proj/b",
			},
			adopt:    true,
			wantCode: connect.CodeFailedPrecondition,
			wantMsg:  []string{"the adopt needs it free"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := tc.resp
			st, m := &fakeStore{}, &fakeMachine{root: "/w/demo/feat-1", failRepo: "b", failWith: &resp}
			req := twoRepoReq()
			if tc.adopt {
				req.Existing = map[string]ExistingCheckout{"r1": {Path: "/w/demo/feat-1/a", Base: "main"}}
			}
			wt, err := Insert(context.Background(), st, req)
			require.NoError(t, err)

			err = Finish(context.Background(), st, m, req, wt)
			require.Error(t, err)

			assert.Equal(t, svcerr.ClassUser, svcerr.Classify(err), "a caller's mistake is a user error: %v", err)
			assert.False(t, errclass.IsServerError(err), "must not be reported as a server fault")
			assert.Equal(t, tc.wantCode, svcerr.Code(err))
			for _, want := range tc.wantMsg {
				assert.Contains(t, err.Error(), want)
			}
			// What a client reads is that sentence, not a redacted
			// "internal server error".
			wire := svcerr.ToConnect(err)
			assert.Equal(t, tc.wantCode, wire.Code())
			assert.Equal(t, err.Error(), wire.Message())

			row := st.rows[wt.ID]
			assert.Equal(t, int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED), row.Status)
			// A UI create settles in the background: the row is how the
			// user learns what to change.
			assert.Equal(t, err.Error(), row.FailureReason)
		})
	}
}

// A failure the daemon did not diagnose — git crashed, the disk is full — is
// ours, and must keep reaching Sentry.
func TestFinish_UndiagnosedCheckoutFailureIsAServerFault(t *testing.T) {
	resp := checkoutResponse{Error: "fatal: write error: No space left on device\n"}
	st, m := &fakeStore{}, &fakeMachine{root: "/w/demo/feat-1", failRepo: "b", failWith: &resp}
	req := twoRepoReq()
	wt, err := Insert(context.Background(), st, req)
	require.NoError(t, err)

	err = Finish(context.Background(), st, m, req, wt)
	require.Error(t, err)
	assert.Equal(t, svcerr.ClassServer, svcerr.Classify(err))
	assert.True(t, errclass.IsServerError(err))
	assert.Contains(t, err.Error(), "No space left on device", "git's output stays in the server-side error")
	row := st.rows[wt.ID]
	assert.Equal(t, int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED), row.Status)
	assert.Empty(t, row.FailureReason, "our own fault is not shown to the user as theirs to fix")
}

func TestFinish_UserErrorNeverReachesSentry(t *testing.T) {
	reporter, lines := installProcessLogging(t)

	st, m := &fakeStore{}, &fakeMachine{root: "/w/demo/feat-1", failRepo: "b"}
	req := twoRepoReq()
	req.BaseBranch = "feat/telemetry-delivery-c2"
	wt, err := Insert(context.Background(), st, req)
	require.NoError(t, err)
	require.Error(t, Finish(context.Background(), st, m, req, wt))

	time.Sleep(50 * time.Millisecond) // the bridge reports from a goroutine
	assert.Empty(t, reporter.captured(), "a user's bad base branch must never reach Sentry")
	for _, msg := range []string{"Failed to create git worktree", "Worktree creation failed"} {
		line := logLine(t, lines, msg)
		assert.Equal(t, "INFO", line["level"], msg)
		assert.Equal(t, "user", line["error_class"], msg)
		assert.Contains(t, line["error"], "feat/telemetry-delivery-c2", "the line still says what went wrong: %s", msg)
	}

	// The same path with a real fault still reports, so the assertion above
	// cannot pass by the reporter being disconnected.
	disk := checkoutResponse{Error: "fatal: write error: No space left on device\n"}
	st, m = &fakeStore{}, &fakeMachine{root: "/w/demo/feat-2", failRepo: "b", failWith: &disk}
	req = twoRepoReq()
	wt, err = Insert(context.Background(), st, req)
	require.NoError(t, err)
	require.Error(t, Finish(context.Background(), st, m, req, wt))
	require.Eventually(t, func() bool { return len(reporter.captured()) > 0 }, 2*time.Second, 5*time.Millisecond,
		"a server fault on the same path must still reach Sentry")
}

func TestInsert_RefusalsAreUserErrors(t *testing.T) {
	t.Run("invalid branch name", func(t *testing.T) {
		st := &fakeStore{}
		req := twoRepoReq()
		req.Branch = "bad..name"
		_, err := Insert(context.Background(), st, req)
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
		assert.Equal(t, svcerr.ClassUser, svcerr.Classify(err))
		assert.Contains(t, err.Error(), "bad..name")
		assert.Empty(t, st.rows, "no row for a create that cannot work")
	})

	t.Run("name taken", func(t *testing.T) {
		st := &fakeStore{}
		req := twoRepoReq()
		_, err := Insert(context.Background(), st, req)
		require.NoError(t, err)
		_, err = Insert(context.Background(), st, req)
		var taken *NameTakenError
		require.ErrorAs(t, err, &taken)
		assert.Equal(t, svcerr.ClassUser, svcerr.Classify(err))
	})

	t.Run("name taken by a concurrent create", func(t *testing.T) {
		st := &racingStore{fakeStore: &fakeStore{}}
		_, err := Insert(context.Background(), st, twoRepoReq())
		var taken *NameTakenError
		require.ErrorAs(t, err, &taken)
		assert.ErrorIs(t, err, core.ErrWorktreeNameTaken)
		assert.Equal(t, svcerr.ClassUser, svcerr.Classify(err))
	})

	t.Run("branch held by another worktree", func(t *testing.T) {
		st := &fakeStore{}
		req := twoRepoReq()
		_, err := Insert(context.Background(), st, req)
		require.NoError(t, err)
		req.Name = "other"
		_, err = Insert(context.Background(), st, req)
		var held *BranchHeldError
		require.ErrorAs(t, err, &held)
		assert.Equal(t, svcerr.ClassUser, svcerr.Classify(err))
	})
}

// racingStore loses the name to a concurrent create after claimName passed,
// the way the unique index reports it.
type racingStore struct{ *fakeStore }

func (s *racingStore) CreateWorktree(context.Context, *db.Worktree) error {
	return core.ErrWorktreeNameTaken
}

// recordingReporter records what the Sentry bridge sends.
type recordingReporter struct {
	telemetry.NoopReporter
	mu   sync.Mutex
	errs []error
}

func (r *recordingReporter) CaptureExceptionWithContext(err error, _ map[string]string, _ map[string]interface{}) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, err)
	return "event"
}

func (r *recordingReporter) CaptureLogEvent(event telemetry.LogEvent) string {
	err := event.Err
	if err == nil {
		err = errors.New(event.Message)
	}
	return r.CaptureExceptionWithContext(err, event.Tags, event.Extra)
}

func (r *recordingReporter) captured() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.errs...)
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// installProcessLogging installs the process logger the servers run — the
// error-class policy and the Sentry bridge included — writing JSON lines to
// the returned buffer, with a recording reporter behind the bridge.
func installProcessLogging(t *testing.T) (*recordingReporter, *syncBuffer) {
	t.Helper()
	t.Setenv("LOG_FORMAT", "json")
	previousLogger, previousOutput := slog.Default(), logging.DefaultOutput
	previousReporter := telemetry.GetReporter()
	t.Cleanup(func() {
		telemetry.SetReporter(previousReporter)
		logging.DefaultOutput = previousOutput
		slog.SetDefault(previousLogger)
	})
	lines := &syncBuffer{}
	logging.DefaultOutput = lines
	logging.Setup(logging.Settings{Format: "json"}, slog.LevelInfo)
	reporter := &recordingReporter{}
	telemetry.SetReporter(reporter)
	return reporter, lines
}

func logLine(t *testing.T, lines *syncBuffer, msg string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(lines.String()), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil && rec["msg"] == msg {
			return rec
		}
	}
	t.Fatalf("no %q line in:\n%s", msg, lines.String())
	return nil
}
