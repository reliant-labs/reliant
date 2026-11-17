package serverkit

import (
	"fmt"
	"net"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// On Windows a bind to an address still held fails with Winsock's own code
// ("bind: Only one usage of each socket address ... is normally permitted"),
// not syscall.EADDRINUSE, so the boot-probe takeover retry never fired there.
func TestAddrInUse_WinsockCodeIsInUse(t *testing.T) {
	t.Parallel()
	bindErr := &net.OpError{Op: "listen", Net: "tcp", Err: os.NewSyscallError("bind", windows.WSAEADDRINUSE)}
	if err := fmt.Errorf("listen 127.0.0.1:1: %w", bindErr); !addrInUse(err) {
		t.Errorf("%v must count as address-in-use", err)
	}
}
