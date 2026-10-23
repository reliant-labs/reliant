// Package frontenddeps ensures a frontend's node_modules is installed and
// current against its lockfile. It is the single install step behind
// `forge env up`, `forge build` and `forge lint`, so a fresh checkout (every
// release worktree) never reaches a toolchain with missing dependencies.
package frontenddeps

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/envutil"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// Ensure is the install step shared by `forge env up`, `forge build` and
// `forge lint`: install dir's dependencies when node_modules is missing or
// older than the lockfile, no-op otherwise.
//
// frozen selects reproducible installs (`npm ci`, `pnpm/yarn --frozen-lockfile`)
// for builds, where a drifted lockfile must fail loudly rather than ship a
// different tree; the dev loop passes false and tolerates drift.
//
// The package manager is taken from the lockfile on disk, falling back to
// runner: a frontend whose dev_runner says npm but which commits
// pnpm-lock.yaml is a pnpm project. FORGE_SKIP_NPM_INSTALL short-circuits it.
func Ensure(ctx context.Context, logPrefix, name, dir, runner string, frozen bool) error {
	if dir == "" || os.Getenv("FORGE_SKIP_NPM_INSTALL") != "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
		return nil // not a node project (or no manifest) — nothing to install
	}
	if !Stale(dir) {
		return nil
	}
	runner = lockfilePackageManager(dir, runner)
	args := installArgs(runner, dir, frozen)
	fmt.Printf("%s %s: node_modules missing/stale — running `%s %s`\n", logPrefix, name, runner, strings.Join(args, " "))
	err := runInstall(ctx, runner, dir, args)
	if err != nil && transientFailure(err.Error()) {
		// The package manager reported ITS OWN internal failure, not a problem
		// with the tree — retrying converges where failing the whole run does
		// not. Without this a known npm bug ends a run that had already spent
		// minutes building images, and re-running hits the same coin flip.
		fmt.Printf("%s %s: `%s install` hit a package-manager internal error — retrying once\n", logPrefix, name, runner)
		err = runInstall(ctx, runner, dir, args)
	}
	if err != nil {
		return fmt.Errorf("install deps in %s: %w", dir, err)
	}
	markInstallOK(dir)
	return nil
}

// lockfilePackageManager names the package manager that wrote dir's lockfile,
// or fallback (default npm) when there is none.
func lockfilePackageManager(dir, fallback string) string {
	for _, lock := range []struct{ file, runner string }{
		{"pnpm-lock.yaml", "pnpm"},
		{"yarn.lock", "yarn"},
		{"package-lock.json", "npm"},
	} {
		if _, err := os.Stat(filepath.Join(dir, lock.file)); err == nil {
			return lock.runner
		}
	}
	if fallback == "" {
		return "npm"
	}
	return fallback
}

// installArgs returns the install verb and flags. Frozen mode needs a
// lockfile to honour; npm without one — at the root it actually installs
// at, see NpmInstallVerb — falls back to `install`.
func installArgs(runner, dir string, frozen bool) []string {
	if !frozen {
		return []string{"install"}
	}
	switch runner {
	case "npm":
		return []string{NpmInstallVerb(dir)}
	case "pnpm":
		return []string{"install", "--frozen-lockfile"}
	case "yarn":
		// Yarn berry (.yarnrc.yml) spells it --immutable; classic --frozen-lockfile.
		if _, err := os.Stat(filepath.Join(dir, ".yarnrc.yml")); err == nil {
			return []string{"install", "--immutable"}
		}
		return []string{"install", "--frozen-lockfile"}
	}
	return []string{"install"}
}

// proxiedInstallSockets caps a package manager's parallel connections when the
// install runs through an HTTP proxy.
//
// Package managers open many sockets at once — npm's default is 15 — which a
// local debugging/inspection proxy (Proxyman, Charles, mitmproxy) does not
// survive: measured on a 579-package tree, 15 sockets through such a proxy did
// not finish in FOUR MINUTES, while 8 finished in 7.6s against 6.6s direct.
// The failure is also silent, because npm's fetch-timeout is 300s with 2
// retries: the install simply sits there for up to fifteen minutes looking
// hung, and the eventual error names nothing useful.
//
// 8 is chosen with margin: 10 also measured clean, 15 did not, so the cliff
// sits between them. The cost when proxied is ~1s on that tree; the cost when
// NOT proxied is zero, because the cap is only applied when a proxy is set.
const proxiedInstallSockets = 8

// concurrencyFlags caps network concurrency for this runner, but only
// when the environment actually routes through a proxy. Returning nil in the
// common case keeps a normal install at full speed.
func concurrencyFlags(runner string, env []string) []string {
	proxied := false
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if envutil.Lookup(env, key) != "" {
			proxied = true
			break
		}
	}
	if !proxied {
		return nil
	}
	switch runner {
	case "npm":
		return []string{fmt.Sprintf("--maxsockets=%d", proxiedInstallSockets)}
	case "pnpm", "yarn":
		return []string{fmt.Sprintf("--network-concurrency=%d", proxiedInstallSockets)}
	default:
		return nil
	}
}

// runInstall runs one install attempt, teeing output to the terminal
// while retaining it so the caller can classify the failure.
func runInstall(ctx context.Context, runner, dir string, args []string) error {
	var buf bytes.Buffer
	if flags := concurrencyFlags(runner, os.Environ()); len(flags) > 0 {
		args = append(args, flags...)
		fmt.Printf("[up] proxy detected — capping %s network concurrency at %d (an inspection proxy stalls at the default)\n",
			runner, proxiedInstallSockets)
	}
	cmd := exec.CommandContext(ctx, runner, args...)
	cmd.Dir = dir
	cmd.Stdout = io.MultiWriter(os.Stdout, &buf)
	cmd.Stderr = io.MultiWriter(os.Stderr, &buf)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(buf.String()))
	}
	return nil
}

// transientInstallFailureSignatures are messages by which a package manager
// reports a fault in ITSELF rather than in the project. They are worth exactly
// one retry: the tree is fine, the tool tripped.
var transientInstallFailureSignatures = []string{
	// npm's own internal-error marker; it prints "This is an error with npm
	// itself" and asks the user to file a bug. Frequently succeeds on retry.
	"Exit handler never called!",
	// Registry/network flakes that are equally not the project's fault.
	"ECONNRESET",
	"ETIMEDOUT",
	"ERR_SOCKET_TIMEOUT",
	"registry error",
}

func transientFailure(output string) bool {
	for _, sig := range transientInstallFailureSignatures {
		if strings.Contains(output, sig) {
			return true
		}
	}
	return false
}

// Stale reports whether a frontend's node_modules is missing
// or older than its lockfile/manifest — the cheap staleness gate that
// keeps ensureFrontendDeps a no-op in the steady state. node_modules'
// directory mtime is bumped by every install, so a lockfile/manifest
// edit (or a never-installed tree) is what trips this.
func Stale(dir string) bool {
	nm, err := os.Stat(filepath.Join(dir, "node_modules"))
	if err != nil {
		return true // missing → must install
	}
	// A SUCCESSFUL install is the reference point, not node_modules' mtime.
	// An install that fails part-way still writes packages, which bumps that
	// mtime — so the directory looks current, the next run skips the install,
	// and a half-populated tree is treated as done. That is the whole gate
	// inverting itself precisely when it matters. The stamp is written only
	// after the package manager exits 0.
	var ref time.Time
	if stamp, err := os.Stat(installStamp(dir)); err == nil {
		ref = stamp.ModTime()
	} else {
		// No stamp: either a tree installed before forge wrote stamps, or one
		// left behind by a failed install. Fall back to the mtime heuristic so
		// an existing healthy checkout is not force-reinstalled.
		ref = nm.ModTime()
	}
	for _, manifest := range []string{"package-lock.json", "pnpm-lock.yaml", "yarn.lock", "package.json"} {
		if info, err := os.Stat(filepath.Join(dir, manifest)); err == nil {
			if info.ModTime().After(ref) {
				return true
			}
		}
	}
	return false
}

// installStamp is the marker written after a package manager exits
// successfully, so "are these deps current?" asks about the last install that
// COMPLETED rather than the last one that merely ran.
func installStamp(dir string) string {
	return filepath.Join(dir, "node_modules", ".forge-install-ok")
}

// markInstallOK records a completed install. Best-effort: a tree that
// cannot be stamped just falls back to the mtime heuristic next time.
func markInstallOK(dir string) {
	_ = atomicfile.WriteFile(installStamp(dir), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
}
