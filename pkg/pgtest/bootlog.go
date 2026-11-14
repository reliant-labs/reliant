package pgtest

import (
	"os"
	"path/filepath"
)

// bootLog is where an embedded server's initdb and startup transcript goes:
// a file in its runtime directory, never the caller's stdout.
//
// embedded-postgres copies that transcript to Config.Logger at the end of
// Start (and again in Stop), and its default Logger is os.Stdout. So every
// command that booted a server — `forge generate` projecting the schema,
// `forge lint`, a `go test -v` — printed some thirty lines of initdb and
// postmaster chatter, among them `LOG: could not bind IPv6 address "::1":
// Cannot assign requested address`, which reads as an error and is not one
// (a host with no IPv6 loopback). A boot that FAILS loses nothing by this:
// the library puts initdb's and pg_ctl's output in the error it returns.
//
// The file lives and goes with the server's runtime directory, so a running
// server's transcript is there to read (bootLogPath) and needs no cleanup of
// its own. Writes never fail: the library fails Start or Stop on a Logger
// error, and a transcript nobody can store is no reason to fail a database.
type bootLog struct{ path string }

// bootLogFile is the transcript's name inside a runtime directory.
const bootLogFile = "boot.log"

// bootLogPath is the transcript of the server whose runtime directory is dir.
func bootLogPath(dir string) string { return filepath.Join(dir, bootLogFile) }

func (l bootLog) Write(p []byte) (int, error) {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return len(p), nil
	}
	_, _ = f.Write(p)
	_ = f.Close()
	return len(p), nil
}
