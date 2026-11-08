package generator

// The bridge links a web-runtime CHECKOUT, and a checkout's dist/ is a
// gitignored build output: absent on a fresh clone or worktree, and stale
// after any pull or branch switch that moved src/. The frontends resolve the
// package through dist/ (package.json "exports"), so a missing or stale one
// is the bridge silently handing them some other version of the runtime —
// the owner's dogfood scaffold failed typecheck on createInfiniteQueryHook,
// createQueryOptions and ServiceKeyBuilders.infinite, exports its generated
// hooks call and its checkout's stale dist/ did not have yet; a fresh
// worktree fails on every import, because there is no dist/ at all.
//
// Nothing else reliably builds it. npm runs a linked package's `prepare`;
// pnpm, the scaffold default, does not run it for a workspace member. So the
// bridge builds dist/ itself whenever it is missing or older than what it is
// compiled from, before the install that resolves it.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// webRuntimeBuildScript is the checkout's dist/ build, the script its own
// `build` and `prepare` run. A web-runtime without it (a fixture, a
// published copy) has nothing forge can build, and is left as it is.
const webRuntimeBuildScript = "scripts/build.mjs"

// buildWebRuntimeDist runs the build in dir. A var so tests can observe the
// decision without a node toolchain.
var buildWebRuntimeDist = func(dir string) ([]byte, error) {
	cmd := exec.Command("node", filepath.FromSlash(webRuntimeBuildScript)) // #nosec G204 -- fixed script inside the bridged checkout
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// webRuntimeDistStale says why dir's dist/ must be rebuilt ("" when it is
// current): it is missing, or a file it is compiled from — src/ outside the
// tests tsconfig.build.json excludes, package.json, a tsconfig — is newer
// than everything in it. mtimes, not hashes: a build rewrites every output,
// and checkouts, pulls and edits all stamp what they touch.
func webRuntimeDistStale(dir string) string {
	newestOut, ok := newestModTime(filepath.Join(dir, "dist"), func(string) bool { return true })
	if !ok {
		return "dist/ is missing"
	}
	newestIn, _ := newestModTime(filepath.Join(dir, "src"), func(name string) bool {
		return !strings.HasSuffix(name, ".test.ts") && !strings.HasSuffix(name, ".test.tsx")
	})
	for _, f := range []string{"package.json", "tsconfig.json", "tsconfig.build.json"} {
		if st, err := os.Stat(filepath.Join(dir, f)); err == nil && st.ModTime().After(newestIn) {
			newestIn = st.ModTime()
		}
	}
	if newestIn.After(newestOut) {
		return "dist/ is older than src/"
	}
	return ""
}

// newestModTime is the newest mtime of the files under root that keep
// admits, and whether there was any.
func newestModTime(root string, keep func(name string) bool) (time.Time, bool) {
	var newest time.Time
	found := false
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !keep(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		found = true
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	return newest, found
}

// ensureWebRuntimeDist builds the bridged checkout's dist/ when it is
// missing or stale. Like the rest of the bridge, a failure is a warning that
// names the command to run, never a failed generate.
//
// Every project bridged to one checkout shares its dist/, and the build is
// not reentrant (its toolchain bootstrap is an `npm ci` in that directory),
// so concurrent generates serialize on a machine-wide lock per checkout and
// re-check under it: the second one finds the first one's fresh dist/.
func ensureWebRuntimeDist(dir string) {
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(webRuntimeBuildScript))); err != nil {
		return
	}
	if webRuntimeDistStale(dir) == "" {
		return
	}
	unlock, err := lockWebRuntimeBuild(dir)
	if err == nil {
		defer unlock()
	}
	why := webRuntimeDistStale(dir)
	if why == "" {
		return
	}
	fmt.Printf("🔨 building the linked %s (%s), so generated code resolves its current exports…\n", WebRuntimePackage, why)
	if out, err := buildWebRuntimeDist(dir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not build the linked %s in %s: %v\n%s\n"+
			"  the frontends will typecheck against a missing or stale dist/ until it is built: npm --prefix %s run build\n",
			WebRuntimePackage, dir, err, tailLines(string(out), 20), dir)
	}
}

// lockWebRuntimeBuild takes the machine-wide build lock for the checkout at
// dir. It lives in the user cache, not the checkout, so the forge working
// tree gains no untracked file.
func lockWebRuntimeBuild(dir string) (func(), error) {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	base = filepath.Join(base, "forge", "locks")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(resolvePath(dir)))
	path := filepath.Join(base, "web-runtime-build-"+hex.EncodeToString(sum[:8])+".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644) // #nosec G304 -- path derived from a hash under the user cache
	if err != nil {
		return nil, err
	}
	unlock, err := lockFileExclusive(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { unlock(); _ = f.Close() }, nil
}

// tailLines is the last n lines of s.
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
