//go:build !windows

package shadowdb

import "syscall"

// platformGoneErrnos adds nothing outside Windows: the POSIX errnos in
// goneErrnos are what the dialer returns.
var platformGoneErrnos []syscall.Errno
