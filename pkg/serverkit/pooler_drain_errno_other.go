//go:build !windows

package serverkit

import "syscall"

// peerClosedErrnos are the errnos a write or close returns on a connection
// the other end had already torn down.
var peerClosedErrnos = []syscall.Errno{syscall.EPIPE, syscall.ECONNRESET}
