//go:build windows

package shadowdb

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// platformGoneErrnos are Winsock's spellings of a refused or dropped
// connection: a dial to a closed port fails with WSAECONNREFUSED (connectex),
// which syscall.ECONNREFUSED — an invented errno on Windows — never matches.
var platformGoneErrnos = []syscall.Errno{windows.WSAECONNREFUSED, windows.WSAECONNRESET, windows.WSAECONNABORTED}
