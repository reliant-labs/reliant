//go:build linux

package osutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestSameUIDPayloadEndpointCannotForgeSupervisorCredentials(t *testing.T) {
	wrapper := exec.Command("true")
	require.NoError(t, wrapper.Run())
	sockets, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	require.NoError(t, err)
	reader := os.NewFile(uintptr(sockets[0]), "reader")
	defer reader.Close()
	writer := os.NewFile(uintptr(sockets[1]), "writer")
	defer writer.Close()
	require.NoError(t, unix.SetsockoptInt(sockets[0], unix.SOL_SOCKET, unix.SO_PASSCRED, 1))
	// Deliberately give the attacker the endpoint: even that cannot make
	// kernel-supplied SCM_CREDENTIALS name the supervisor PID.
	attacker := exec.Command(os.Args[0], "-test.run=^TestSupervisorForgedPacketHelper$")
	attacker.Env = append(os.Environ(), "RELIANT_TEST_FORGE_RESULT=1")
	attacker.ExtraFiles = []*os.File{writer}
	require.NoError(t, attacker.Run())
	_, err = receiveSupervisorResult(reader, wrapper)
	require.Error(t, err, "different sender PID must not authenticate, even with same UID and endpoint")
}
func TestSupervisorForgedPacketHelper(t *testing.T) {
	if os.Getenv("RELIANT_TEST_FORGE_RESULT") != "1" {
		return
	}
	require.NoError(t, unix.Sendmsg(3, []byte(`{"complete":true}`), nil, nil, 0))
}
func TestPayloadCannotWriteRegularCompletionThroughProc(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// Attempt the original same-UID writable-FD attack. Sockets cannot be
	// opened as regular files, and nondumpable wrapper denies proc access.
	cmd := exec.CommandContext(ctx, "bash", "-c", `for f in /proc/$PPID/fd/*; do printf '{"complete":true}' > "$f" 2>/dev/null || :; done; exit 124`)
	supervision, err := PrepareCommandSupervision(cmd, 20*time.Millisecond)
	require.NoError(t, err)
	defer supervision.Close()
	err = cmd.Run()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	require.Equal(t, 124, exit.ExitCode(), fmt.Sprint(err))
	incomplete, err := supervision.Finish()
	require.NoError(t, err)
	require.False(t, incomplete)
}
