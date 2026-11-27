//go:build windows

package shadowdb

import (
	"fmt"
	"net"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// A refused dial on Windows is connectex's WSAECONNREFUSED inside a
// net.OpError, never syscall.ECONNREFUSED.
func TestServerGoneClassifiesWinsockErrors(t *testing.T) {
	for _, errno := range []error{windows.WSAECONNREFUSED, windows.WSAECONNRESET, windows.WSAECONNABORTED} {
		err := fmt.Errorf("pgtest: connect: %w", &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connectex", errno)})
		if !serverGone(err) {
			t.Errorf("serverGone(%v) = false, want true", err)
		}
	}
}
