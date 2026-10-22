// Copyright (c) 2025 Reliant Labs

// Package memtemp gives a test a temporary directory in memory (tmpfs) when the
// machine has one, so the fsyncs the code under test makes cost nothing.
//
// WHY. Credential stores and the prove_test journal fsync every write, as they
// must: a credential or a journal entry that is not durable is lost on a crash.
// In a test that durability buys nothing, and on a shared disk it costs
// seconds. The test lanes run ~100 packages at once on the one disk every agent
// on the machine writes to, and a single fsync there was measured at 0.4-0.8s,
// with a test's few fsyncs summing to 8-70s under load (cmd/reliant/cmd's auth
// tests, internal/llm/tools' prove_test tests, 2026-10-22). That pushed whole
// packages past their 60s `-short` budget. On tmpfs an fsync returns at once.
//
// Where there is no tmpfs (macOS), or it is nearly full, Dir is t.TempDir().
//
// LEAKS. t.Cleanup removes the directory, but it does not run when a test
// binary panics on its -timeout or is killed, and a directory left in tmpfs
// holds memory. Each directory therefore carries its process's pid, and the
// first Dir of a process removes the directories of processes that are gone.
// It never touches a directory of a live process, or anything not made here.
//
// Test-only by intent: nothing outside _test.go files imports it.
//
//forge:exclude-contract: test-only: hands tests a tmpfs temp directory and reaps the ones dead test processes left; imported only by _test.go files
package memtemp

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// root is where the tmpfs directories go; a var so tests can point it at a
// directory of their own.
var root = "/dev/shm"

const (
	prefix = "reliant-memtemp-"
	// minFree is the tmpfs space Dir wants before using it: tests write
	// kilobytes, and a nearly full /dev/shm belongs to someone else's need.
	minFree = 64 << 20
)

var reapOnce sync.Once

// Dir returns a new empty directory for t, in tmpfs when this machine has
// room in one, removed when t finishes.
func Dir(t testing.TB) string {
	t.Helper()
	if !usable(root) {
		return t.TempDir()
	}
	reapOnce.Do(func() { Reap(root) })
	dir, err := os.MkdirTemp(root, prefix+strconv.Itoa(os.Getpid())+"-")
	if err != nil {
		return t.TempDir()
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// Reap removes the directories Dir made under dir for processes that are no
// longer running, and returns what it removed. A directory whose name does not
// carry Dir's prefix and a pid is never touched.
func Reap(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var removed []string
	for _, e := range entries {
		rest, ok := strings.CutPrefix(e.Name(), prefix)
		if !ok || !e.IsDir() {
			continue
		}
		pidText, _, ok := strings.Cut(rest, "-")
		pid, err := strconv.Atoi(pidText)
		if !ok || err != nil || pid <= 0 || alive(pid) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if os.RemoveAll(path) == nil {
			removed = append(removed, path)
		}
	}
	return removed
}
