package cli

// THE DRIVER RUNS THE FORGE THE PROJECT PINS.
//
// The driver command in .git/config names one forge binary by absolute path,
// and a clone outlives many pins. reliant's shared clone kept a driver naming
// `~/.cache/fm-forge-bin/v0.1.45-…6f9811cb/forge`, several pins old: a
// cherry-pick warned that this was not the pinned forge and merged with it
// anyway. A forge from another pin merges by rules that may no longer be the
// project's — which fingerprint lines exist, how a state file merges — and its
// verdict is written into the tree.
//
// So, at merge time, the driver compares itself with the pin of the project
// the merged file belongs to:
//
//   - the pinned forge: it merges;
//   - another forge: it hands the merge to the pinned one, if that has run in
//     this clone (every pinned forge records its own driver command, below),
//     else to the pinned one it installs (installedForge: `go install` of the
//     pin into the durable-copy directory, once per pin per machine);
//   - and when the pinned forge can be neither found nor installed, it merges
//     as git would without a driver — a plain three-way text merge, a normal
//     conflict where the sides overlap — and says why, rather than apply a
//     mismatched forge's rules.
//
// And every writing forge command run by the pinned forge (re)points the
// driver at the pin, so the first `forge generate` after a pin bump repairs a
// stale driver. A forge that is NOT the project's pin never rewrites the
// driver, and a copy of the pin at another path keeps the copy already
// registered rather than taking the driver over.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/forgecompat"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// pinnedDriverSection holds, per forge identity (a pseudo-version's commit,
// or a tag), the driver command of a forge binary of that identity that has
// run in this clone: `forge-merge-driver.<identity>.command`.
const pinnedDriverSection = "forge-merge-driver"

// delegatedDriverEnv marks a merge-generated run started by another driver,
// which must not delegate again whatever it finds.
const delegatedDriverEnv = "FORGE_MERGE_DRIVER_DELEGATED"

// mergeDriverPinCheck reports, for the project at root, the pin and this
// binary's version, and whether they are different forges. A seam for tests.
var mergeDriverPinCheck = func(root string) (pin, binary string, skewed bool) {
	pin, binary, skewed, _ = pinSkewFor(root)
	if forgecompat.SkewOverridden() {
		skewed = false
	}
	return pin, binary, skewed
}

// mergeDriverIdentity is the identity this binary registers under.
var mergeDriverIdentity = func() string { return forgeIdentity(mergeDriverVersion()) }

// mergeDriverVersion is this binary's forge module version: what a driver
// command reinstalls when the binary it names is gone (driverCommandLine).
// A seam for tests.
var mergeDriverVersion = buildinfo.Version

// registerPinnedDriver records command as the driver of this binary's
// identity in the clone at root, and reports whether that changed anything.
// The key is per identity, so worktrees on different pins never contend for
// it: it lives in the shared config, where every worktree's driver finds it.
func registerPinnedDriver(root, command string) bool {
	id := mergeDriverIdentity()
	if id == "" {
		return false
	}
	key := pinnedDriverSection + "." + id + ".command"
	cur, _ := exec.CommandContext(context.Background(), "git", "-C", root, "config", "--get", key).Output()
	if strings.TrimSpace(string(cur)) == command {
		return false
	}
	return exec.CommandContext(context.Background(), "git", "-C", root, "config", "--local", key, command).Run() == nil
}

// pinnedDriverCommand returns the driver command a forge of pin's identity
// registered in the clone at dir, when its binary still exists.
func pinnedDriverCommand(dir, pin string) (string, bool) {
	id := forgeIdentity(pin)
	if id == "" {
		return "", false
	}
	out, err := exec.CommandContext(context.Background(), "git", "-C", dir, "config", "--get",
		pinnedDriverSection+"."+id+".command").Output()
	if err != nil {
		return "", false
	}
	command := strings.TrimSpace(string(out))
	if command == "" || !driverCommandUsable(command) {
		return "", false
	}
	return command, true
}

// projectRootForMergedPath is the directory of the nearest forge.yaml above
// relPath (repository-relative; the driver runs at the top of the work tree),
// or the project root found from the working directory when relPath is empty.
func projectRootForMergedPath(relPath string) string {
	if relPath != "" {
		rel := filepath.ToSlash(relPath)
		for dir := path.Dir(rel); ; dir = path.Dir(dir) {
			if _, err := os.Stat(filepath.Join(filepath.FromSlash(dir), "forge.yaml")); err == nil {
				return filepath.FromSlash(dir)
			}
			if dir == "." || dir == "/" {
				break
			}
		}
	}
	root, err := cmdutil.FindProjectRoot()
	if err != nil {
		return ""
	}
	return root
}

// pinnedMerge decides who merges one file: this binary (handled=false), the
// pinned forge it delegates to, or git's plain merge. When handled, the
// result is already in args[1] (%A) and clean reports whether it merged
// cleanly.
func pinnedMerge(ctx context.Context, stderr io.Writer, args []string, relPath string, base, ours, theirs []byte, marker int) (handled, clean bool, err error) {
	root := projectRootForMergedPath(relPath)
	if root == "" {
		return false, false, nil
	}
	pin, binary, skewed := mergeDriverPinCheck(root)
	if !skewed {
		return false, false, nil
	}
	if os.Getenv(delegatedDriverEnv) == "" {
		if command, ok := pinnedDriverCommand(root, pin); ok {
			fmt.Fprintf(stderr, "forge merge-generated %s: this driver is forge %s; merging with the pinned forge %s\n", relPath, binary, pin)
			return true, runDelegatedDriver(ctx, command, args) == nil, nil
		}
		// No pinned forge has run in this clone (a fresh worktree, or a
		// checkout whose scripts opt out of the driver wiring): install the
		// pin and merge with it, rather than merge without its rules.
		bin, ierr := installedForge(ctx, stderr, pin)
		if ierr == nil {
			fmt.Fprintf(stderr, "forge merge-generated %s: this driver is forge %s; merging with the pinned forge %s (%s)\n", relPath, binary, pin, bin)
			return true, runDelegatedDriver(ctx, shellQuote(bin)+" merge-generated %O %A %B %L %P", args) == nil, nil
		}
		fmt.Fprintf(stderr, "forge merge-generated %s: could not install the pinned forge %s: %v\n", relPath, pin, ierr)
	}
	merged, _, err := plainMerge(ctx, base, ours, theirs, marker)
	if err != nil {
		return true, false, err
	}
	if err := atomicfile.WriteFile(args[1], merged, 0o644); err != nil {
		return true, false, err
	}
	fmt.Fprintf(stderr, "forge merge-generated %s: this driver is forge %s, but forge.yaml pins %s, and that forge has not run in "+
		"this clone; preserved git's text merge but left it unresolved because the pinned forge did not run. Run any command with the pinned forge (`%s generate`) "+
		"to point the driver at it.\n", relPath, binary, pin, Name())
	return true, false, nil
}

// runDelegatedDriver runs another driver command the way git runs one —
// through the shell, with %O %A %B %L %P substituted — and reports its
// non-zero exit as an error.
func runDelegatedDriver(ctx context.Context, command string, args []string) error {
	subst := map[string]string{}
	for i, p := range []string{"%O", "%A", "%B", "%L", "%P"} {
		if i < len(args) {
			subst[p] = shellQuote(args[i])
		} else {
			subst[p] = ""
		}
	}
	for p, v := range subst {
		command = strings.ReplaceAll(command, p, v)
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Env = append(os.Environ(), delegatedDriverEnv+"=1")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// plainMerge is git's own text merge, with no fingerprint handling: what the
// file would get with no driver configured.
func plainMerge(ctx context.Context, base, ours, theirs []byte, marker int) ([]byte, bool, error) {
	dir, err := os.MkdirTemp("", "forge-merge-plain-")
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	for name, body := range map[string][]byte{"ours": ours, "base": base, "theirs": theirs} {
		if err := atomicfile.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			return nil, false, err
		}
	}
	if marker <= 0 {
		marker = 7
	}
	cmd := exec.CommandContext(ctx, "git", "merge-file", "-p", "--marker-size="+strconv.Itoa(marker),
		"-L", "ours", "-L", "base", "-L", "theirs",
		filepath.Join(dir, "ours"), filepath.Join(dir, "base"), filepath.Join(dir, "theirs"))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, runErr := cmd.Output()
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		return out, true, nil
	case errors.As(runErr, &exitErr) && exitErr.ExitCode() > 0 && exitErr.ExitCode() < 128:
		return out, false, nil
	}
	return nil, false, fmt.Errorf("git merge-file: %v: %s", runErr, strings.TrimSpace(stderr.String()))
}
