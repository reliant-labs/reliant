package forge_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestWebRuntimeShipsTheRepoLicense keeps the npm package's LICENSE and NOTICE
// byte-identical to the repository's.
//
// @reliantlabs/forge-web-runtime is published from web-runtime/, and npm packs
// only what sits in that directory — it never reaches up to the repo root. So
// the package carries its own copies, and Apache-2.0 §4 makes them load-bearing:
// every app that bundles the runtime redistributes it, and must be able to pass
// on the license and the NOTICE it was given. A copy that drifted from the root
// would publish terms the project no longer grants.
//
// Cache-sound under -short: the test process opens every file it compares.
func TestWebRuntimeShipsTheRepoLicense(t *testing.T) {
	for _, name := range []string{"LICENSE", "NOTICE"} {
		root, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		pkg, err := os.ReadFile(filepath.Join("web-runtime", name))
		if err != nil {
			t.Fatalf("read web-runtime/%s: %v — the published package would ship without it", name, err)
		}
		if !bytes.Equal(root, pkg) {
			t.Errorf("web-runtime/%s differs from the repo-root %s; copy it: cp %s web-runtime/%s", name, name, name, name)
		}
	}
}
