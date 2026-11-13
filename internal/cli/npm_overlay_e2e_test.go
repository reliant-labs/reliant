//go:build e2e

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/internal/npmoverlay"
)

// npmOverlayE2EEnv turns the overlay off when set to "0": every npm then
// resolves @reliantlabs/forge-web-runtime from the public registry, which is
// how a user's install resolves it after a release. For a maintainer checking
// that the published runtime still works with this checkout's scaffold.
const npmOverlayE2EEnv = "FORGE_E2E_NPM_OVERLAY"

// startNpmOverlayE2E routes every npm the corpus runs — the tests' own and the
// ones the forge subprocesses run — through an overlay registry that serves
// THIS checkout's web-runtime as @reliantlabs/forge-web-runtime, beside every
// published version (package npmoverlay).
//
// Without it a pull request that raises the scaffold's runtime range to the
// version it ships failed every frontend e2e test with ETARGET until the
// runtime was published after the merge, and every other pull request tested
// the PREVIOUS release's runtime against its own scaffold. The scaffold keeps
// its published range; only where npm fetches that scope from changes.
//
// Process-global, like linkForgeEnv and the shared postgres URL: the variable
// must reach the forge subprocesses, and t.Setenv is unavailable to parallel
// tests. The pack is lazy — on the first install that asks for the runtime —
// so a run with no frontend test never builds it.
func startNpmOverlayE2E(t *testing.T) {
	t.Helper()
	npmOverlayOnce.Do(func() {
		if os.Getenv(npmOverlayE2EEnv) == "0" {
			return
		}
		root := findRepoRoot(t)
		webRuntime := filepath.Join(root, "web-runtime")
		if _, err := os.Stat(filepath.Join(webRuntime, "package.json")); err != nil {
			return // no web-runtime in this checkout: nothing to overlay
		}
		o, err := npmoverlay.Start(npmoverlay.Options{
			Packages: []npmoverlay.Package{{
				Dir:     webRuntime,
				Prepare: func() error { return prebuildWebRuntime(root) },
			}},
		})
		if err != nil {
			npmOverlayErr = err
			return
		}
		// Never closed: the server lives as long as the test process, and
		// the process exiting closes its listener. Its directory — the one
		// holding the npm config file Env names — goes with the suite's
		// other shared temp dirs.
		for _, kv := range o.Env() {
			k, v, _ := strings.Cut(kv, "=")
			if err := os.Setenv(k, v); err != nil {
				npmOverlayErr = err
				return
			}
			if k == "npm_config_globalconfig" {
				registerSharedTempDir(filepath.Dir(v))
			}
		}
	})
	if npmOverlayErr != nil {
		t.Fatalf("start the npm overlay serving this checkout's web-runtime: %v", npmOverlayErr)
	}
}

var (
	npmOverlayOnce sync.Once
	npmOverlayErr  error
)
