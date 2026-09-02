// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// newAnnounceTestClient builds a client whose send() lands in a buffered
// channel, so a test can read what the daemon would have written to the
// gateway without standing up a stream.
func newAnnounceTestClient() *daemonClient {
	return &daemonClient{
		sendCh:      make(chan *reliantv1.DaemonMessage, 8),
		sessionDone: make(chan struct{}),
		// handleDaemonCommand registers a cancel func for every request; a nil
		// map panics on assignment.
		cancelByReq: make(map[string]context.CancelFunc),
	}
}

// drainFileSystemChanged returns the first FileSystemChanged message queued,
// or nil when none was sent.
func drainFileSystemChanged(d *daemonClient) *reliantv1.FileSystemChanged {
	for {
		select {
		case msg := <-d.sendCh:
			if fsc := msg.GetFileSystemChanged(); fsc != nil {
				return fsc
			}
		default:
			return nil
		}
	}
}

// drainCommandFailed returns the first DaemonCommandFailed message queued,
// or nil when none was sent.
func drainCommandFailed(d *daemonClient) *reliantv1.DaemonCommandFailed {
	for {
		select {
		case msg := <-d.sendCh:
			if f := msg.GetDaemonCommandFailed(); f != nil {
				return f
			}
		default:
			return nil
		}
	}
}

func clonePayload(t *testing.T, path string) []byte {
	t.Helper()
	payload, err := json.Marshal(gitCloneResponse{Success: true, Path: path})
	require.NoError(t, err)
	return payload
}

// TestAnnounceFilesystemMutationOnClone is the behaviour the control-plane's
// CloneRepo depends on. That call is a 10-60s NATS request/reply issued on
// reliant's daemon subject, and a client that backgrounds mid-flight (a phone)
// never sees the reply — the repo lands on disk with nobody informed. The
// announcement turns the outcome into a user_updates row, which replays on
// reconnect.
func TestAnnounceFilesystemMutationOnClone(t *testing.T) {
	d := newAnnounceTestClient()

	d.announceFilesystemMutation("req-1", "git.clone", clonePayload(t, "/home/workspace/projects/acme"), nil)

	fsc := drainFileSystemChanged(d)
	require.NotNil(t, fsc, "a successful clone must announce the new directory")
	assert.Equal(t, "/home/workspace/projects/acme", fsc.ProjectPath)
	assert.NotZero(t, fsc.TimestampUnixMs)
}

// TestAnnounceFilesystemMutationAnnouncesFailures is the failure counterpart
// of the success path above, and the fix for the "clone fails silently"
// gap: a git.clone dispatched from DAEMON_PENDING_COMMANDS (no live RPC
// waiter — control-plane's CloneRepo enqueues and returns immediately) has
// nowhere else to report a failure. Without this, the repo would just never
// appear and nothing would tell the user why.
func TestAnnounceFilesystemMutationAnnouncesFailures(t *testing.T) {
	d := newAnnounceTestClient()

	d.announceFilesystemMutation("req-1", "git.clone", clonePayload(t, "/home/workspace/projects/acme"),
		errors.New("git clone failed: authentication required"))

	// Check DaemonCommandFailed FIRST: both drain helpers discard any
	// message that isn't the type they're looking for, so draining
	// FileSystemChanged first would consume (and hide) the one message
	// actually sent.
	failed := drainCommandFailed(d)
	require.NotNil(t, failed, "a failed clone must announce the failure")
	assert.Nil(t, drainFileSystemChanged(d), "a failed clone must not announce a filesystem change")
	assert.Equal(t, "req-1", failed.RequestId)
	assert.Equal(t, "git.clone", failed.CommandType)
	assert.Contains(t, failed.ErrorMessage, "authentication required")
	assert.NotZero(t, failed.TimestampUnixMs)
}

// TestAnnounceFilesystemMutationIgnoresUnrelatedCommands keeps this narrow.
// Most daemon commands mutate files inside a directory the file-tree watcher
// already polls; announcing for those would duplicate its signal on every
// read, write, and shell invocation. Applies to both the success and
// failure announcement paths.
func TestAnnounceFilesystemMutationIgnoresUnrelatedCommands(t *testing.T) {
	for _, commandType := range []string{"fs.read_file", "fs.write_file", "exec.run", "worktree.create"} {
		d := newAnnounceTestClient()
		d.announceFilesystemMutation("req-1", commandType, clonePayload(t, "/some/path"), nil)
		assert.Nil(t, drainFileSystemChanged(d), "%s must not announce", commandType)

		d2 := newAnnounceTestClient()
		d2.announceFilesystemMutation("req-1", commandType, nil, errors.New("boom"))
		assert.Nil(t, drainCommandFailed(d2), "%s failure must not announce", commandType)
	}
}

// TestAnnounceFilesystemMutationCoversRepoRemoval — a removed repo changes the
// tree just as much as a new one, and the watcher stops polling a path once it
// is gone.
func TestAnnounceFilesystemMutationCoversRepoRemoval(t *testing.T) {
	for _, commandType := range []string{"git.reclone", "git.remove"} {
		d := newAnnounceTestClient()
		d.announceFilesystemMutation("req-1", commandType, clonePayload(t, "/home/workspace/projects/acme"), nil)
		assert.NotNil(t, drainFileSystemChanged(d), "%s must announce", commandType)
	}
}

// TestAnnounceFilesystemMutationRequiresPath — the server resolves a project
// from the path, so an announcement without one has nothing to scope a refetch
// to and would be dropped there anyway. Only applies to the success path —
// a failure has no path to require.
func TestAnnounceFilesystemMutationRequiresPath(t *testing.T) {
	d := newAnnounceTestClient()

	d.announceFilesystemMutation("req-1", "git.clone", clonePayload(t, ""), nil)
	assert.Nil(t, drainFileSystemChanged(d), "no path means nothing to announce")

	d2 := newAnnounceTestClient()
	d2.announceFilesystemMutation("req-1", "git.clone", nil, nil)
	assert.Nil(t, drainFileSystemChanged(d2), "an empty payload must not panic or announce")
}

// TestHandleDaemonCommandAnnouncesClone pins the WIRING, not just the helper.
// The unit tests above call announceFilesystemMutation directly, so they keep
// passing if the call is dropped from the dispatch path — which is the whole
// feature. This drives a real command through handleDaemonCommand instead.
func TestHandleDaemonCommandAnnouncesClone(t *testing.T) {
	clonedTo := t.TempDir()
	RegisterCommand("test.fake_clone", func(_ context.Context, _ []byte) ([]byte, error) {
		return json.Marshal(gitCloneResponse{Success: true, Path: clonedTo})
	})
	// Registered under a test-only name, then treated as filesystem-mutating
	// for the duration — the real git.clone shells out to git, which this test
	// has no business doing.
	filesystemMutatingCommands["test.fake_clone"] = true
	t.Cleanup(func() { delete(filesystemMutatingCommands, "test.fake_clone") })

	d := newAnnounceTestClient()
	d.handleDaemonCommand(&reliantv1.DaemonCommandRequest{
		RequestId:   "req-1",
		CommandType: "test.fake_clone",
	})

	var sawResponse bool
	var announced *reliantv1.FileSystemChanged
	for {
		select {
		case msg := <-d.sendCh:
			if msg.GetDaemonCommandResponse() != nil {
				sawResponse = true
			}
			if fsc := msg.GetFileSystemChanged(); fsc != nil {
				announced = fsc
			}
			continue
		default:
		}
		break
	}

	assert.True(t, sawResponse, "the command response must still be sent")
	require.NotNil(t, announced, "dispatch must announce a filesystem-mutating command")
	assert.Equal(t, clonedTo, announced.ProjectPath)
}

// TestHandleDaemonCommandAnnouncesCloneFailure is the failure-path twin of
// TestHandleDaemonCommandAnnouncesClone, pinning the WIRING for a command
// that fails — including a command dispatched with no live RPC waiter,
// exactly like the drain path in nats_bridge.go. Verifies both the normal
// (still-sent) DaemonCommandResponse AND the DaemonCommandFailed
// announcement fire off a single real handleDaemonCommand call.
func TestHandleDaemonCommandAnnouncesCloneFailure(t *testing.T) {
	RegisterCommand("test.fake_clone_fail", func(_ context.Context, _ []byte) ([]byte, error) {
		return nil, errors.New("git clone failed: repository not found")
	})
	filesystemMutatingCommands["test.fake_clone_fail"] = true
	t.Cleanup(func() { delete(filesystemMutatingCommands, "test.fake_clone_fail") })

	d := newAnnounceTestClient()
	d.handleDaemonCommand(&reliantv1.DaemonCommandRequest{
		RequestId:   "req-2",
		CommandType: "test.fake_clone_fail",
	})

	var sawResponse bool
	var responseSuccess bool
	var announced *reliantv1.DaemonCommandFailed
	for {
		select {
		case msg := <-d.sendCh:
			if resp := msg.GetDaemonCommandResponse(); resp != nil {
				sawResponse = true
				responseSuccess = resp.Success
			}
			if f := msg.GetDaemonCommandFailed(); f != nil {
				announced = f
			}
			continue
		default:
		}
		break
	}

	assert.True(t, sawResponse, "the command response must still be sent")
	assert.False(t, responseSuccess, "the command response must report failure")
	require.NotNil(t, announced, "dispatch must announce the failure of a filesystem-mutating command")
	assert.Equal(t, "req-2", announced.RequestId)
	assert.Equal(t, "test.fake_clone_fail", announced.CommandType)
	assert.Contains(t, announced.ErrorMessage, "repository not found")
}
