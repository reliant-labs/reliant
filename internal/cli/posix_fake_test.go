package cli

import (
	"runtime"
	"testing"
)

// requirePOSIXFake skips a test whose stand-in for an external tool is a
// POSIX shell script, and is called by every helper (or test) that writes
// one, before it touches PATH.
//
// Windows cannot execute a script by name: CreateProcess ignores a shebang
// and exec.LookPath only resolves names carrying a PATHEXT extension. So the
// fake is never found there. Either nothing is ("exec: \"buf\": executable
// file not found in %PATH%"), or — worse — the runner's REAL docker, kubectl
// or helm is, and fails against a daemon or cluster context that does not
// exist. What these tests pin is forge's own argv and sequencing, which is
// platform-independent and covered on every Linux job.
func requirePOSIXFake(t *testing.T, tool string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skipf("the fake %s is a POSIX sh script, which Windows cannot exec by name "+
			"(the runner's real %s, if any, would run instead); covered on Linux", tool, tool)
	}
}
