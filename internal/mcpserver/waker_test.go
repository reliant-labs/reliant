// Copyright (c) 2025 Reliant Labs

package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// scriptedReadiness returns readiness answers from a script, so a wake
// sequence can be exercised without waiting in real time.
type scriptedReadiness struct {
	mu sync.Mutex

	// readyAfter is how many IsReady calls occur before it reports ready.
	readyAfter int
	calls      int

	resumeCalls int
	resumeErr   error
	readyErr    error
	neverReady  bool
}

func (s *scriptedReadiness) IsReady(context.Context, string, string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.readyErr != nil {
		return false, s.readyErr
	}
	if s.neverReady {
		return false, nil
	}
	return s.calls > s.readyAfter, nil
}

func (s *scriptedReadiness) Resume(context.Context, string, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resumeCalls++
	return s.resumeErr
}

func newTestWaker(readiness DaemonReadiness) *PollingWaker {
	return NewPollingWaker(readiness, nil)
}

// TestAlreadyRunningWorkspaceIsFastPath: the common case in a live session
// must cost one check and no resume.
func TestAlreadyRunningWorkspaceIsFastPath(t *testing.T) {
	readiness := &scriptedReadiness{readyAfter: 0}
	w := newTestWaker(readiness)

	require.NoError(t, w.EnsureAwake(context.Background(), "user-1", "daemon-1"))
	require.Equal(t, 1, readiness.calls)
	require.Zero(t, readiness.resumeCalls, "a running workspace must not be resumed")
}

// TestSuspendedWorkspaceStartsWithoutWaiting pins the central decision: the
// wake is triggered and the call returns immediately.
//
// Blocking through a cold start held an MCP tool call open for the better part
// of a minute behind a bare spinner. Returning at once, with a distinguishable
// "starting" answer, lets the client say something useful and come back.
func TestSuspendedWorkspaceStartsWithoutWaiting(t *testing.T) {
	readiness := &scriptedReadiness{neverReady: true}
	w := newTestWaker(readiness)

	err := w.EnsureAwake(context.Background(), "user-1", "daemon-1")
	require.ErrorIs(t, err, ErrWorkspaceStarting)
	require.Equal(t, 1, readiness.resumeCalls, "the wake must actually be triggered")
	require.Equal(t, 1, readiness.calls,
		"readiness must be checked once, not polled — this call does not wait")
}

// A resume that fails is a different answer from one that is under way: it
// will not resolve on its own, so the caller must not be told to retry.
func TestUnresumableWorkspaceIsDistinctFromStarting(t *testing.T) {
	readiness := &scriptedReadiness{
		neverReady: true,
		resumeErr:  errors.New("no orchestrator"),
	}
	w := newTestWaker(readiness)

	err := w.EnsureAwake(context.Background(), "user-1", "daemon-1")
	require.ErrorIs(t, err, ErrWorkspaceUnavailable)
	require.NotErrorIs(t, err, ErrWorkspaceStarting)
	require.Contains(t, err.Error(), "no orchestrator",
		"the specific reason must survive for the user")
}

// TestReadinessLookupFailureDoesNotBlockTheCall: a failed readiness lookup is
// not itself evidence the workspace is down, and the command may well succeed.
func TestReadinessLookupFailureDoesNotBlockTheCall(t *testing.T) {
	readiness := &scriptedReadiness{readyErr: errors.New("db unavailable")}
	w := newTestWaker(readiness)

	require.NoError(t, w.EnsureAwake(context.Background(), "user-1", "daemon-1"),
		"a readiness lookup failure should let the command proceed and report the real error")
	require.Zero(t, readiness.resumeCalls)
}

func TestMissingDaemonIDIsUnavailableNotStarting(t *testing.T) {
	w := newTestWaker(&scriptedReadiness{})
	err := w.EnsureAwake(context.Background(), "user-1", "")
	require.ErrorIs(t, err, ErrWorkspaceUnavailable,
		"a connector with no workspace bound cannot be fixed by retrying")
}

// The guidance a model receives has to separate the two outcomes, since one is
// worth coming back for and the other is not.
func TestExplainUnavailableSeparatesRetryableFromTerminal(t *testing.T) {
	starting := explainUnavailable(ErrWorkspaceStarting)
	require.Contains(t, starting, "starting")
	require.Contains(t, starting, "minute", "a retry needs a time to come back at")

	terminal := explainUnavailable(
		fmt.Errorf("%w: no orchestrator", ErrWorkspaceUnavailable))
	require.Contains(t, terminal, "no orchestrator")
	require.Contains(t, terminal, "not fix itself",
		"a terminal state must tell the model not to retry")
	require.NotContains(t, terminal, "wait about a minute")
}

func TestNilWakerIsPermissive(t *testing.T) {
	var w *PollingWaker
	require.NoError(t, w.EnsureAwake(context.Background(), "user-1", "daemon-1"))
}

// fakeAttachments implements AttachmentReader.
type fakeAttachments struct {
	rows []*db.DaemonAttachment
	err  error
}

func (f *fakeAttachments) ListFreshDaemonAttachmentsForUser(context.Context, string, time.Duration) ([]*db.DaemonAttachment, error) {
	return f.rows, f.err
}

type fakeResumer struct {
	called bool
	err    error
}

func (f *fakeResumer) ResumeDaemon(context.Context, string, string) error {
	f.called = true
	return f.err
}

func TestAttachmentReadiness(t *testing.T) {
	ctx := context.Background()

	t.Run("fresh attachment means ready", func(t *testing.T) {
		r := NewAttachmentReadiness(&fakeAttachments{
			rows: []*db.DaemonAttachment{{DaemonID: "daemon-1"}},
		}, nil)

		ready, err := r.IsReady(ctx, "user-1", "daemon-1")
		require.NoError(t, err)
		require.True(t, ready)
	})

	// The grant's daemon is the only one that counts: another daemon being up
	// says nothing about this one.
	t.Run("a different daemon does not count", func(t *testing.T) {
		r := NewAttachmentReadiness(&fakeAttachments{
			rows: []*db.DaemonAttachment{{DaemonID: "daemon-other"}},
		}, nil)

		ready, err := r.IsReady(ctx, "user-1", "daemon-1")
		require.NoError(t, err)
		require.False(t, ready)
	})

	t.Run("no attachments means not ready", func(t *testing.T) {
		r := NewAttachmentReadiness(&fakeAttachments{}, nil)
		ready, err := r.IsReady(ctx, "user-1", "daemon-1")
		require.NoError(t, err)
		require.False(t, ready)
	})

	t.Run("resume without an orchestrator is reported honestly", func(t *testing.T) {
		r := NewAttachmentReadiness(&fakeAttachments{}, nil)
		err := r.Resume(ctx, "user-1", "daemon-1")
		require.Error(t, err)
		require.Contains(t, err.Error(), "cannot start it automatically")
	})

	t.Run("resume delegates when an orchestrator exists", func(t *testing.T) {
		resumer := &fakeResumer{}
		r := NewAttachmentReadiness(&fakeAttachments{}, resumer)

		require.NoError(t, r.Resume(ctx, "user-1", "daemon-1"))
		require.True(t, resumer.called)
	})
}
