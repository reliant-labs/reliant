package cluster

import (
	"runtime"
	"testing"
)

// requirePOSIXFake skips a test whose stand-in for an external tool is a
// POSIX shell script, and is called by every helper that writes one, before
// it touches PATH.
//
// Windows cannot execute a script by name: CreateProcess ignores a shebang
// and exec.LookPath only resolves names carrying a PATHEXT extension. So the
// fake is never found there — the runner's REAL kubectl or helm is, which
// then fails against a cluster context that does not exist ("context
// \"k3d-alpha\" does not exist"). The fakes also lean on awk, mktemp and cat.
// What these tests pin — apply ordering, namespace threading, rollout and
// pre-rollout gates — is forge's own argv construction, which is
// platform-independent and covered on every Linux job.
func requirePOSIXFake(t *testing.T, tool string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skipf("the fake %s is a POSIX sh script, which Windows cannot exec by name "+
			"(the runner's real %s would run instead); covered on Linux", tool, tool)
	}
}
