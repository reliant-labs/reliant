package prod

import (
	"os/exec"
	"testing"
)

// TrimCache is production code that imports "testing" only to ask
// testing.Testing(), the shape of forge's gocache trim: under a test binary it
// returns before it execs anything.
func TrimCache() {
	if testing.Testing() {
		return
	}
	_ = exec.Command("go", "env", "GOFLAGS").Run()
}
