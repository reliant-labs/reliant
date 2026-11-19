// Package toolcache is the one machine-wide directory every tool forge
// installs for itself lives in: the gate's pinned linters and scanners
// (internal/cigate), forge lint's golangci-lint build (internal/cli/lint), and
// the codegen plugins and air that `forge generate` and `forge env up` pin
// (internal/cli). Every worktree of every project on the machine shares it.
//
// # Layout
//
//	$FORGE_TOOLS_CACHE (default <UserCacheDir>/forge/tools)/
//	  <kind>/                      gate, golangci-lint, pinned
//	    <key>/                     one ENTRY: a tool at one version (+toolchain)
//	      <binary>
//	    .build-<key>-*/            an install in progress
//
// An entry's key names everything that decides its bytes (tool, version and,
// where it matters, the Go toolchain), so an entry is never rewritten for a
// different version: a bump is a new key beside the old one, and a checkout
// still on the old pin keeps working.
//
// # Concurrency
//
// An install builds into a private staging directory and is published by one
// rename, so no reader ever execs a half-written binary (a `go version -m` of
// one failed the pin with "installed binary reports unknown":
// TestE2EFixtureCorpusKalshiShaped). There is no lock: two processes that miss
// the same key at once both build it — the second build is mostly Go build
// cache hits — and the one that loses the rename uses the winner's entry.
//
// Nothing here evicts. An entry is a few binaries per pinned version; the
// machine's disk steward, not forge, decides what a full disk gives up.
//
//forge:exclude-contract: a filesystem cache with package-level operations, not an injected application component
package toolcache

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EnvRoot relocates the whole tool cache: a read-only home directory, a CI
// runner that caches one directory, or a test that must not touch the
// developer's real cache.
const EnvRoot = "FORGE_TOOLS_CACHE"

// The kinds of entry forge installs. Each is a directory under the root.
const (
	// KindGate holds `forge ci gate`'s pinned tools (internal/cigate).
	KindGate = "gate"
	// KindGolangci holds forge lint's own golangci-lint builds.
	KindGolangci = "golangci-lint"
	// KindPinned holds go.mod-pinned codegen plugins and the dev loop's air.
	KindPinned = "pinned"
)

const (
	// stagingPrefix names an install in progress.
	stagingPrefix = ".build-"
	// replacedPrefix names an unusable entry moved aside for its replacement.
	replacedPrefix = ".replaced-"
)

// Root is the tool cache root: $FORGE_TOOLS_CACHE, else
// <UserCacheDir>/forge/tools.
func Root() (string, error) {
	if dir := os.Getenv(EnvRoot); dir != "" {
		return dir, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate the user cache directory for forge's tool cache: %w (set %s)", err, EnvRoot)
	}
	return filepath.Join(base, "forge", "tools"), nil
}

// KindDir is the directory holding one kind of entry.
func KindDir(kind string) (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, kind), nil
}

// Ensure resolves the entry <kindDir>/<key>, installing it when ready reports
// it unusable, and returns its directory.
//
// ready(dir) answers whether the entry at dir is usable as is (the binary
// exists and reports the pinned version). build(ctx, stage) installs the
// entry's files into stage, an empty directory that becomes the entry when
// build succeeds. Ensure does not re-check ready after its own build: a caller
// that wants its own message for a build that produced the wrong thing
// verifies the returned directory itself.
func Ensure(ctx context.Context, kindDir, key string, ready func(dir string) bool, build func(ctx context.Context, stage string) error) (string, error) {
	if err := validKey(key); err != nil {
		return "", err
	}
	dir := filepath.Join(kindDir, key)
	if ready(dir) {
		return dir, nil
	}
	if err := os.MkdirAll(kindDir, 0o755); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(kindDir, stagingPrefix+key+"-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	if err := build(ctx, stage); err != nil {
		return "", err
	}
	// MkdirTemp makes 0700; the cache may be shared by several users.
	if err := os.Chmod(stage, 0o755); err != nil {
		return "", err
	}
	return dir, publish(stage, dir, ready)
}

// publish renames the finished stage into place as dir. A concurrent install
// of the same key may have published first: its entry is as good as this one,
// so it is kept. An existing entry ready rejects (a corrupt or wrong build, or
// one an older forge wrote into in place) is moved aside first, so the rename
// never lands INSIDE it.
func publish(stage, dir string, ready func(string) bool) error {
	if _, err := os.Lstat(dir); err == nil {
		if ready(dir) {
			return nil
		}
		trash := filepath.Join(filepath.Dir(dir), fmt.Sprintf("%s%s-%d", replacedPrefix, filepath.Base(dir), time.Now().UnixNano()))
		if err := os.Rename(dir, trash); err != nil {
			return fmt.Errorf("replace unusable tool cache entry %s: %w", dir, err)
		}
		defer func() { _ = os.RemoveAll(trash) }()
	}
	if err := os.Rename(stage, dir); err != nil {
		if ready(dir) {
			return nil // lost the race to a concurrent install of the same key
		}
		return err
	}
	return nil
}

// validKey rejects a key that is not one path element of an entry name.
func validKey(key string) error {
	if key == "" || strings.HasPrefix(key, ".") || strings.ContainsAny(key, `/\`) || key != filepath.Base(key) {
		return fmt.Errorf("toolcache: invalid entry key %q", key)
	}
	return nil
}
