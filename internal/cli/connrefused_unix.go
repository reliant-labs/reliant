//go:build !windows

package cli

import (
	"errors"
	"syscall"
)

// isConnRefused reports whether a dial failed because nothing is listening —
// the peer answered with a reset, as opposed to a timeout or a DNS failure.
func isConnRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}
