// Copyright (c) 2026 Reliant Labs

package diskwatch

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// goToolchainComms are the commands that read and write a Go build cache: the
// go command, the tools it runs, and golangci-lint, which builds through it.
var goToolchainComms = map[string]bool{
	"go": true, "compile": true, "link": true, "vet": true, "asm": true, "cgo": true,
	"golangci-lint": true,
}

// scanGoCacheRoots is scanGoCacheRootsIn over the real /proc.
func scanGoCacheRoots() []string { return scanGoCacheRootsIn(procRoot) }

// scanGoCacheRootsIn returns the Go build cache each running Go toolchain
// process under root builds into, once each. Only a process whose environment
// this user can read (its own, or every one as root) and that shares this
// process's mount namespace counts: a path read from a container's process
// names a directory in the container, which may be a different one here, or
// nothing. A process that exits mid-read is skipped.
func scanGoCacheRootsIn(root string) []string {
	self, err := os.Readlink(filepath.Join(root, "self", "ns", "mnt"))
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err != nil || pid <= 0 {
			continue
		}
		dir := filepath.Join(root, e.Name())
		comm, err := os.ReadFile(filepath.Join(dir, "comm"))
		if err != nil || !goToolchainComms[strings.TrimSpace(string(comm))] {
			continue
		}
		if ns, err := os.Readlink(filepath.Join(dir, "ns", "mnt")); err != nil || ns != self {
			continue
		}
		environ, err := os.ReadFile(filepath.Join(dir, "environ"))
		if err != nil {
			continue
		}
		cache := goCacheRootFromEnv(parseEnviron(environ))
		if cache == "" || !filepath.IsAbs(cache) || seen[cache] {
			continue
		}
		seen[cache] = true
		out = append(out, cache)
	}
	return out
}

// parseEnviron reads /proc/<pid>/environ: NUL-separated KEY=VALUE pairs.
func parseEnviron(b []byte) map[string]string {
	env := map[string]string{}
	for _, kv := range bytes.Split(b, []byte{0}) {
		if k, v, ok := strings.Cut(string(kv), "="); ok && k != "" {
			env[k] = v
		}
	}
	return env
}
