package cloud

import (
	"os"
	"testing"

	"github.com/reliant-labs/forge/pkg/cloudcred"
)

// TestMain keeps the developer's real session out of this package's tests. A
// shell a host application spawned (a Reliant agent's) exports a credential
// helper, and every test that expects "no credential" would otherwise mint a
// real token through it. Tests that want a helper set one with t.Setenv.
func TestMain(m *testing.M) {
	_ = os.Unsetenv(cloudcred.HelperEnv)
	os.Exit(m.Run())
}
