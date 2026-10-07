package terminal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A terminal asked to start in a directory that is not there used to start
// the shell in $HOME and say nothing. In prod the owner opened forge ~500ms
// after CreateProjectFromRepo queued its clone: the daemon was asked for
// /home/workspace/projects/forge, found nothing there yet, and handed back a
// healthy-looking prompt in /home/workspace. A project with no checkout on
// the active machine did the same. Nothing recorded either directory, so the
// wrong cwd could not be diagnosed from logs.
//
// These tests pin the contract: a requested directory the shell cannot start
// in is an error the caller can act on, never a silent substitute.

func TestCreateSession_MissingWorkingDirFailsInsteadOfStartingInHome(t *testing.T) {
	m := NewManager()
	t.Cleanup(m.Cleanup)
	missing := filepath.Join(t.TempDir(), "projects", "forge")

	session, err := m.CreateSession(missing, "")

	require.Error(t, err, "a missing working directory must fail, not start the shell in $HOME")
	require.ErrorIs(t, err, ErrWorkingDirUnavailable)
	require.Contains(t, err.Error(), missing, "the error must name the directory it could not use")
	require.Nil(t, session)
	require.Empty(t, m.ListSessions(), "a refused request must not leave a shell running")
}

func TestCreateSession_RefusedWorkingDirDoesNotEvictASession(t *testing.T) {
	m := NewManager()
	m.maxSessions = 1
	live := addIdleSession(m, "live", time.Now())

	_, err := m.CreateSession(filepath.Join(t.TempDir(), "missing"), "")

	require.ErrorIs(t, err, ErrWorkingDirUnavailable)
	require.False(t, isClosed(live), "a request that is refused must not close a live session to make room")
	require.Len(t, m.sessions, 1)
}

func TestIsWorkingDirUnavailable_SurvivesTheDaemonTransport(t *testing.T) {
	_, refusal := resolveWorkingDir(filepath.Join(t.TempDir(), "missing"))
	require.ErrorIs(t, refusal, ErrWorkingDirUnavailable)

	// The NATS router delivers a daemon command's error as its message only.
	overTheWire := errors.New(`daemon command "terminal.create" failed: create session: ` + refusal.Error())

	require.True(t, IsWorkingDirUnavailable(refusal))
	require.True(t, IsWorkingDirUnavailable(overTheWire))
	require.False(t, IsWorkingDirUnavailable(errors.New(`daemon command "terminal.create" failed: no daemon connected`)))
	require.False(t, IsWorkingDirUnavailable(nil))
}

func TestResolveWorkingDir_UsesTheRequestedDirectoryAsGiven(t *testing.T) {
	dir := t.TempDir()

	got, err := resolveWorkingDir(dir)

	require.NoError(t, err)
	require.Equal(t, dir, got)
}

func TestCreateSession_RelativeWorkingDirFails(t *testing.T) {
	m := NewManager()
	t.Cleanup(m.Cleanup)

	// Relative to what? The daemon's own cwd is not a location the caller
	// can know, so honouring it would start the shell somewhere arbitrary.
	session, err := m.CreateSession(filepath.Join("projects", "forge"), "")

	require.ErrorIs(t, err, ErrWorkingDirUnavailable)
	require.Nil(t, session)
	require.Empty(t, m.ListSessions())
}

func TestCreateSession_FileAsWorkingDirFails(t *testing.T) {
	m := NewManager()
	t.Cleanup(m.Cleanup)
	file := filepath.Join(t.TempDir(), "forge")
	require.NoError(t, os.WriteFile(file, nil, 0o600))

	session, err := m.CreateSession(file, "")

	require.ErrorIs(t, err, ErrWorkingDirUnavailable)
	require.Nil(t, session)
}
