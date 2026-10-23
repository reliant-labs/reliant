package serverkit

import "syscall"

// peerClosedErrnos are the errnos a write or close returns on a connection
// the other end had already torn down. Winsock reports its own codes, which
// syscall.ECONNRESET does not match: a send on a reset connection is
// WSAECONNRESET, and one the stack aborted after the reset WSAECONNABORTED.
var peerClosedErrnos = []syscall.Errno{
	syscall.EPIPE, syscall.ECONNRESET,
	syscall.WSAECONNRESET, syscall.WSAECONNABORTED,
}
