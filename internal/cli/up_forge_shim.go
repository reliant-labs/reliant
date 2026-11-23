package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/reliant-labs/forge/internal/toolcache"
)

// forgeShimKind is the tool-cache directory holding the `forge` shims host
// processes resolve through (forgeShimDir).
const forgeShimKind = "shims"

// forgeShimOnce memoises the shim directory for this process: every host
// workload and job of one `forge env up` gets the same one.
var forgeShimOnce = sync.OnceValues(ensureForgeShim)

// forgeShimWarnOnce says once per run that the shim could not be written.
var forgeShimWarnOnce sync.Once

// forgeShimDir returns a directory holding an executable named `forge` that
// runs the forge running this command, however it was invoked: a standalone
// forge, or `reliant forge` with no `forge` on PATH at all.
//
// WHY. A host process's own commands call forge by its bare name: the
// scaffolded .air.toml runs `forge storage check ./tmp` before every build. A
// developer who installed only `reliant` has no `forge` on PATH, so that
// command exited 127 and air never started the API (NS FG-12). One with a
// different forge on PATH ran a build that did not render this env. Putting
// this directory first on a host process's PATH makes the bare name mean the
// forge that is running the env, without the project's files naming a binary.
//
// A variable so the unit tests, whose executable is the test binary, write no
// shim into the developer's tool cache (internal/cli's TestMain).
var forgeShimDir = func() (string, error) { return forgeShimOnce() }

func ensureForgeShim() (string, error) {
	tokens, err := forgeExecCommand()
	if err != nil {
		return "", err
	}
	kind, err := toolcache.KindDir(forgeShimKind)
	if err != nil {
		return "", err
	}
	name, body := forgeShimScript(runtime.GOOS, tokens)
	sum := sha256.Sum256([]byte(body))
	dir := filepath.Join(kind, "forge-"+hex.EncodeToString(sum[:8]))
	path := filepath.Join(dir, name)
	if have, err := os.ReadFile(path); err == nil && string(have) == body {
		return dir, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".forge-shim-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.WriteString(body); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return dir, nil
}

// forgeShimScript is the shim's file name and contents for goos: a POSIX sh
// script, or a .cmd on Windows (air runs its commands through PowerShell
// there, which resolves forge.cmd through PATHEXT).
func forgeShimScript(goos string, tokens []string) (name, body string) {
	if goos == "windows" {
		quoted := make([]string, len(tokens))
		for i, t := range tokens {
			quoted[i] = `"` + t + `"`
		}
		return "forge.cmd", "@echo off\r\n" + strings.Join(quoted, " ") + " %*\r\n"
	}
	quoted := make([]string, len(tokens))
	for i, t := range tokens {
		quoted[i] = "'" + strings.ReplaceAll(t, "'", `'\''`) + "'"
	}
	return "forge", "#!/bin/sh\n# Written by forge env up: the bare `forge` a host process runs is the forge\n# running its env (see forgeShimDir).\nexec " + strings.Join(quoted, " ") + " \"$@\"\n"
}

// withForgeShimOnPath puts the forge shim first on env's PATH. When the shim
// cannot be written the env is returned unchanged, with one line saying why:
// a host process that never calls forge must still start.
func withForgeShimOnPath(env []string, who string) []string {
	dir, err := forgeShimDir()
	if err != nil {
		forgeShimWarnOnce.Do(func() {
			fmt.Printf("[up] host %s: could not put this forge on its PATH (%v): a bare `forge` it runs resolves through your PATH\n", who, err)
		})
		return env
	}
	return prependPathDir(env, dir)
}

// prependPathDir returns env with dir first on its PATH (Path on Windows,
// whose environment is case-insensitive).
func prependPathDir(env []string, dir string) []string {
	out := make([]string, 0, len(env)+1)
	found := false
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if ok && (k == "PATH" || (runtime.GOOS == "windows" && strings.EqualFold(k, "PATH"))) {
			if !found {
				kv = k + "=" + dir + string(os.PathListSeparator) + v
				found = true
			}
		}
		out = append(out, kv)
	}
	if !found {
		out = append(out, "PATH="+dir)
	}
	return out
}
