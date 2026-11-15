//go:build e2e

package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// prebuildWebRuntimeE2E builds the repo's web-runtime/dist ONCE per test
// binary, before any parallel test can trigger it as a side effect.
//
// # WHY THIS EXISTS
//
// Every frontend e2e test installs THIS checkout's web-runtime: under CI
// through the npm overlay (npm_overlay_e2e_test.go), which packs it, and on a
// maintainer's machine through the --link-forge dev bridge, whose `npm
// install` runs the package's `prepare` script IN THE SHARED web-runtime/
// directory. Either way something builds web-runtime/dist, and the build is
// not reentrant.
//
// build.mjs bootstraps a missing toolchain by running `npm install` in that
// directory when node_modules/typescript is absent. The e2e tests are
// t.Parallel(), and web-runtime/dist is gitignored, so on a fresh CI checkout
// several tests reach that bootstrap at once and npm's two concurrent
// installs collide while staging the same package:
//
//	npm error code ENOTEMPTY
//	npm error syscall rename
//	npm error path      .../web-runtime/node_modules/typescript
//	npm error dest      .../web-runtime/node_modules/.typescript-nBeJZ4MU
//
// The loser is left with a half-populated typescript/ (its bin/tsc present,
// its lib/ not), so the NEXT build.mjs sees node_modules/typescript existing,
// skips the bootstrap, and dies on `Cannot find module '../lib/tsc.js'` —
// which is how this surfaced: four unrelated frontend tests failing on a
// missing tsc none of them installs.
//
// Serializing the tests would cost minutes of wall-clock on the critical
// path. Instead, do the shared, non-reentrant work exactly once, up front:
// after this returns, node_modules/typescript and dist/ both exist, every
// per-test `npm install` finds the prepare script's fast path, and the
// concurrent installs never overlap. The overlay's pack runs this same once
// (its Prepare hook) and then packs with --ignore-scripts, so the two never
// build at the same time either.
//
// It is safe (and cheap) for a test to call this more than once, and safe on
// a contributor's machine where the build has already been done — build.mjs
// is idempotent and the sync.Once makes repeat calls free.
func prebuildWebRuntimeE2E(t *testing.T) {
	t.Helper()
	if err := prebuildWebRuntime(findRepoRoot(t)); err != nil {
		t.Fatalf("web-runtime prebuild failed — every frontend e2e test installs "+
			"this checkout's web-runtime and would race building it:\n%v", err)
	}
}

// prebuildWebRuntime is prebuildWebRuntimeE2E's once, without a *testing.T,
// for the overlay's Prepare hook. repoRoot is the forge checkout.
func prebuildWebRuntime(repoRoot string) error {
	webRuntimePrebuildOnce.Do(func() {
		dir := filepath.Join(repoRoot, "web-runtime")
		if _, err := os.Stat(dir); err != nil {
			// No web-runtime in this checkout: nothing to prebuild, and the
			// frontend tests that need it will fail with their own message.
			return
		}

		// A HALF-INSTALLED toolchain left behind by an earlier run defeats
		// the once-ness above: build.mjs skips its bootstrap whenever
		// node_modules/typescript merely EXISTS, so a directory containing
		// bin/tsc but no lib/ makes every build die on `Cannot find module
		// '../lib/tsc.js'` — and no amount of serializing fixes it, because
		// the damage predates this process. Observed in CI on a checkout
		// whose web-runtime/node_modules survived from a previous job.
		//
		// Probing for the file build.mjs actually needs (rather than the
		// directory it checks) tells the two states apart, and removing the
		// broken tree is what lets the bootstrap re-run.
		tsLib := filepath.Join(dir, "node_modules", "typescript", "lib", "tsc.js")
		tsDir := filepath.Join(dir, "node_modules", "typescript")
		if _, statErr := os.Stat(tsDir); statErr == nil {
			if _, libErr := os.Stat(tsLib); libErr != nil {
				_ = os.RemoveAll(tsDir)
			}
		}

		// `npm run build` is build.mjs directly — the same script npm would
		// run as `prepare`, minus the install-time indirection. It installs
		// this package's devDependencies itself if the toolchain is missing.
		cmd := exec.Command("npm", "run", "build")
		cmd.Dir = dir
		done := make(chan struct{})
		var out []byte
		var err error
		go func() {
			out, err = cmd.CombinedOutput()
			close(done)
		}()

		select {
		case <-done:
			if err != nil {
				webRuntimePrebuildErr = &cmdError{
					name: "npm", args: []string{"run", "build"}, dir: dir,
					err: err, output: string(out),
				}
			}
		case <-time.After(10 * time.Minute):
			_ = cmd.Process.Kill()
			webRuntimePrebuildErr = &cmdError{
				name: "npm", args: []string{"run", "build"}, dir: dir,
				err: errors.New("timed out after 10m"), output: string(out),
			}
		}
	})
	return webRuntimePrebuildErr
}

var (
	webRuntimePrebuildOnce sync.Once
	webRuntimePrebuildErr  error
)
