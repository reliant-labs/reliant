package serverkit

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// addrInUseErrnos are the errnos a bind returns for an address something
// else still holds. Winsock reports its own code, WSAEADDRINUSE ("Only one
// usage of each socket address"), which syscall.EADDRINUSE does not match:
// on Windows that constant is an errno Go invents, and no bind returns it.
var addrInUseErrnos = []syscall.Errno{syscall.EADDRINUSE, windows.WSAEADDRINUSE}
