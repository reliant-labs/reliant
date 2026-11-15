package serverkit

import (
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"
)

// On Windows a close_notify written to a connection the pooler already reset
// fails with Winsock's own code ("wsasend: An existing connection was forcibly
// closed by the remote host"), not syscall.ECONNRESET.
func TestPeerClosedErrors_WinsockResetIsNotACloseFailure(t *testing.T) {
	t.Parallel()
	for _, errno := range []syscall.Errno{syscall.WSAECONNRESET, syscall.WSAECONNABORTED} {
		writeErr := &net.OpError{Op: "write", Net: "tcp", Err: os.NewSyscallError("wsasend", errno)}
		err := fmt.Errorf("tls: failed to send closeNotify alert (but connection was closed anyway): %w", writeErr)
		if got := withoutPeerClosedErrors(err); got != nil {
			t.Errorf("%v must not be reported as a close failure, got %v", err, got)
		}
	}
}
