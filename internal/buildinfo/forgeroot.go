package buildinfo

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
)

// ForgeRootEnv names the forge checkout for --link-forge, like --forge-root.
const ForgeRootEnv = "FORGE_ROOT"

// startupDir is the working directory this process started in, captured
// before anything can chdir: a forge run from inside its own checkout (a
// `go test`, `go run ./cmd/forge`) is found from here.
var startupDir, _ = os.Getwd()

// ForgeRoot is a local forge checkout and how it was found.
type ForgeRoot struct {
	Dir    string
	Source string
}

// ResolveForgeRoot finds the local forge checkout `forge project new
// --link-forge` bridges a project to.
//
// Discovery used to rest on one thing: a path the compiler baked into the
// binary (an ldflag, or runtime.Caller). Any -trimpath build erases both,
// and the Reliant workspace image makes -trimpath the toolchain default, so
// --link-forge failed for every binary built there, and for `go test`. Now,
// first match wins:
//
//  1. explicit, the --forge-root flag, else $FORGE_ROOT: a stated path is
//     never second-guessed, and one that is not a forge checkout is an error;
//  2. the DevForgeRoot ldflag a contributor build stamps;
//  3. the source path compiled into this binary (runtime.Caller);
//  4. the `replace github.com/reliant-labs/forge => <path>` in the build info
//     of a host binary that embeds forge (reliant), resolved against the
//     directories the binary and this process live under, since build info
//     records the path as written in go.mod;
//  5. the checkout the executable sits inside (bin/forge, go-build's output
//     under the checkout);
//  6. the checkout the process started in.
//
// A module-cache copy is never a checkout (nothing to edit, read-only).
// ok is false when none is found; err is set only for a bad explicit path.
func ResolveForgeRoot(explicit string) (ForgeRoot, bool, error) {
	if explicit == "" {
		explicit = strings.TrimSpace(os.Getenv(ForgeRootEnv))
		if explicit != "" {
			return resolveExplicit(explicit, ForgeRootEnv+"="+explicit)
		}
	} else {
		return resolveExplicit(explicit, "--forge-root")
	}
	if DevForgeRoot != "" {
		return ForgeRoot{DevForgeRoot, "stamped into this binary at build time"}, true, nil
	}
	if d := DiscoverDevForgeRootFromSource(); d != "" {
		return ForgeRoot{d, "the source path compiled into this binary"}, true, nil
	}
	// A pinned discovery result (the test seam) stands for every implicit
	// source below: under `go test` the process starts inside the checkout,
	// so they would otherwise always find it.
	mu.RLock()
	ov, ovSet := discoverOverride, discoverOverrideSet
	mu.RUnlock()
	if ovSet {
		return ForgeRoot{ov, "pinned"}, ov != "", nil
	}
	exe, _ := os.Executable()
	if r, ok := forgeRootFromBuildInfoReplace(rawBuildInfo(), searchBases(exe, startupDir)); ok {
		return ForgeRoot{r, "the go.mod replace this binary was built with"}, true, nil
	}
	if exe != "" {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		if r := forgeRootFromFile(exe); r != "" {
			return ForgeRoot{r, "the checkout this executable is inside"}, true, nil
		}
	}
	if startupDir != "" {
		if r := forgeRootFromFile(filepath.Join(startupDir, "x")); r != "" {
			return ForgeRoot{r, "the checkout forge was started in"}, true, nil
		}
	}
	return ForgeRoot{}, false, nil
}

func resolveExplicit(path, source string) (ForgeRoot, bool, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return ForgeRoot{}, false, err
	}
	if r := forgeRootFromFile(filepath.Join(abs, "x")); r != abs {
		return ForgeRoot{}, false, fmt.Errorf("%s is not a forge checkout: want a directory whose go.mod declares %s and that has pkg/ (and is not a module-cache copy)", abs, forgeModulePath)
	}
	return ForgeRoot{abs, source}, true, nil
}

// searchBases are the directories a relative replace path may be relative to:
// every ancestor of the executable and of the starting directory. build info
// keeps the path as go.mod wrote it ("../forge"), relative to the host
// module, whose location a -trimpath binary no longer knows.
func searchBases(paths ...string) []string {
	var bases []string
	seen := map[string]bool{}
	for _, p := range paths {
		for dir := filepath.Dir(p); p != "" && !seen[dir]; dir = filepath.Dir(dir) {
			seen[dir] = true
			bases = append(bases, dir)
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	return bases
}

// forgeRootFromBuildInfoReplace is the pure part of source 4.
func forgeRootFromBuildInfoReplace(info *debug.BuildInfo, bases []string) (string, bool) {
	if info == nil {
		return "", false
	}
	dep, embedded := forgeModuleDep(info)
	if !embedded || dep.Replace == nil || dep.Replace.Version != "" || dep.Replace.Path == "" {
		return "", false
	}
	p := dep.Replace.Path
	if filepath.IsAbs(p) {
		if r := forgeRootFromFile(filepath.Join(p, "x")); r != "" {
			return r, true
		}
		return "", false
	}
	for _, base := range bases {
		if r := forgeRootFromFile(filepath.Join(filepath.Join(base, p), "x")); r != "" {
			return r, true
		}
	}
	return "", false
}
