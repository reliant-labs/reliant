//go:build windows

package cli

import (
	"errors"
	"syscall"
)

// wsaeConnRefused is WSAECONNREFUSED, the Winsock error a refused dial
// carries. syscall does not name it.
const wsaeConnRefused syscall.Errno = 10061

// isConnRefused reports whether a dial failed because nothing is listening —
// the peer answered with a reset, as opposed to a timeout or a DNS failure.
//
// Not errors.Is(err, syscall.ECONNREFUSED): on Windows that constant is an
// invented value Winsock never returns, and Errno.Is does not map one onto
// the other, so the check was always false there.
func isConnRefused(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && errno == wsaeConnRefused
}
