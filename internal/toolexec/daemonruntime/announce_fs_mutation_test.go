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

	d.announceFilesystemMutation("git.clone", clonePayload(t, "/home/workspace/projects/acme"), nil)

	fsc := drainFileSystemChanged(d)
	require.NotNil(t, fsc, "a successful clone must announce the new directory")
	assert.Equal(t, "/home/workspace/projects/acme", fsc.ProjectPath)
	assert.NotZero(t, fsc.TimestampUnixMs)
}

// TestAnnounceFilesystemMutationSkipsFailures guards against telling clients to
// refetch a directory that was never created.
func TestAnnounceFilesystemMutationSkipsFailures(t *testing.T) {
	d := newAnnounceTestClient()

	d.announceFilesystemMutation("git.clone", clonePayload(t, "/home/workspace/projects/acme"),
		errors.New("git clone failed: authentication required"))

	assert.Nil(t, drainFileSystemChanged(d), "a failed clone must not announce")
}

// TestAnnounceFilesystemMutationIgnoresUnrelatedCommands keeps this narrow.
// Most daemon commands mutate files inside a directory the file-tree watcher
// already polls; announcing for those would duplicate its signal on every
// read, write, and shell invocation.
func TestAnnounceFilesystemMutationIgnoresUnrelatedCommands(t *testing.T) {
	for _, commandType := range []string{"fs.read_file", "fs.write_file", "exec.run", "worktree.create"} {
		d := newAnnounceTestClient()
		d.announceFilesystemMutation(commandType, clonePayload(t, "/some/path"), nil)
		assert.Nil(t, drainFileSystemChanged(d), "%s must not announce", commandType)
	}
}

// TestAnnounceFilesystemMutationCoversRepoRemoval — a removed repo changes the
// tree just as much as a new one, and the watcher stops polling a path once it
// is gone.
func TestAnnounceFilesystemMutationCoversRepoRemoval(t *testing.T) {
	for _, commandType := range []string{"git.reclone", "git.remove"} {
		d := newAnnounceTestClient()
		d.announceFilesystemMutation(commandType, clonePayload(t, "/home/workspace/projects/acme"), nil)
		assert.NotNil(t, drainFileSystemChanged(d), "%s must announce", commandType)
	}
}

// TestAnnounceFilesystemMutationRequiresPath — the server resolves a project
// from the path, so an announcement without one has nothing to scope a refetch
// to and would be dropped there anyway.
func TestAnnounceFilesystemMutationRequiresPath(t *testing.T) {
	d := newAnnounceTestClient()

	d.announceFilesystemMutation("git.clone", clonePayload(t, ""), nil)
	assert.Nil(t, drainFileSystemChanged(d), "no path means nothing to announce")

	d2 := newAnnounceTestClient()
	d2.announceFilesystemMutation("git.clone", nil, nil)
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
