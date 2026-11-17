package pgtest_test

import (
	"os"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// TestMain releases this test binary's share of the cross-process embedded
// pool after the suite finishes. Without it a raw `go test ./pkg/pgtest`
// would leave the shared server (and its data dir + SysV IPC) running until
// reapStaleInstances swept it — harmless but untidy. The forge CLI wires the
// same Shutdown into every generate/test invocation.
func TestMain(m *testing.M) {
	code := m.Run()
	pgtest.Shutdown()
	os.Exit(code)
}
