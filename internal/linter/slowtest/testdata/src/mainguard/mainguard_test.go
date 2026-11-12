package mainguard

import (
	"os"
	"os/exec"
	"testing"
)

// A testing.Short check in TestMain covers the package: it decides there what
// a -short run of this package does.
func TestMain(m *testing.M) {
	if testing.Short() {
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestExecsGo(t *testing.T) {
	_ = exec.Command("go", "version").Run()
}
