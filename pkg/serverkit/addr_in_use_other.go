//go:build !windows

package serverkit

import "syscall"

// addrInUseErrnos are the errnos a bind returns for an address something
// else still holds.
var addrInUseErrnos = []syscall.Errno{syscall.EADDRINUSE}
