package cli

// Installing the generated-file merge driver on purpose, and checking it.
//
// forge wires the driver into a checkout as a side effect of every writing
// command (ensureGeneratedMergeDriver). A checkout no writing forge has run in
// — a fresh worktree, or a merge queue's pool checkout whose scripts set
// FORGE_NO_MERGE_DRIVER so a gate never rewrites git config — merges with
// whatever driver the shared config happens to name, which may be several
// pins old. `forge merge-driver install` is the explicit step for those:
// it wires THIS forge into the checkout (ignoring FORGE_NO_MERGE_DRIVER,
// because it is asked for), then checks the result and fails if the checkout
// still cannot merge forge's files. `forge merge-driver check` is the check
// alone, for a script to run before a merge it cannot afford to get wrong.
//
// A forge binary a driver needs and cannot find is installed rather than
// worked around: the driver command reinstalls its own version
// (driverCommandLine), and a driver from another pin installs the pin
// (installedForge, below). Only when neither is possible does a merge fail,
// and it fails loudly.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
)

// installForge installs forge at version into gobin. A seam for tests: the
// real one needs the Go toolchain and the module proxy.
var installForge = func(ctx context.Context, w io.Writer, version, gobin string) error {
	cmd := exec.CommandContext(ctx, "go", "install", forgeModulePath+"/cmd/forge@"+version)
	// Outside any module and workspace, with no inherited -mod or -tags:
	// `go install <module>@<version>` builds exactly that version.
	cmd.Dir = os.TempDir()
	cmd.Env = append(os.Environ(), "GOBIN="+gobin, "GOWORK=off", "GOFLAGS=")
	cmd.Stdout, cmd.Stderr = w, w
	return cmd.Run()
}

// installedForge returns the durable copy of forge at version, installing it
// into the durable-copy directory (<cache>/<identity>/forge) when it is not
// there. The copy is marked used, so pruning keeps it.
func installedForge(ctx context.Context, w io.Writer, version string) (string, error) {
	id := forgeIdentity(version)
	if id == "" || !installableForgeVersion(version) {
		return "", fmt.Errorf("%q names no forge version to install", version)
	}
	cache, err := mergeDriverCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, id)
	// go install names the binary for the host: forge.exe on Windows.
	bin := filepath.Join(dir, "forge"+exeSuffix())
	if !isExecutableFile(bin) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		fmt.Fprintf(w, "forge merge-generated: installing forge %s into %s\n", version, dir)
		if err := installForge(ctx, w, version, dir); err != nil {
			return "", fmt.Errorf("go install forge@%s: %w", version, err)
		}
		if !isExecutableFile(bin) {
			return "", fmt.Errorf("go install forge@%s wrote no %s", version, bin)
		}
		pruneDriverCopies(cache, id)
	}
	now := time.Now()
	_ = os.Chtimes(dir, now, now)
	return bin, nil
}

// isExecutableFile reports whether p is a file a driver command can run. On
// Windows that is any regular file: NTFS keeps no execute bit (os.Stat never
// reports one), and the sh git runs a driver through executes a script or an
// .exe by its contents.
func isExecutableFile(p string) bool {
	info, err := os.Stat(p)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return runtime.GOOS == "windows" || info.Mode()&0o111 != 0
}

// touchDriverCopy marks the durable copy this process runs from as used, so
// pruning (keptDriverCopies) evicts copies nothing merges with before one a
// checkout's driver is still running.
func touchDriverCopy() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cache, err := mergeDriverCacheDir()
	if err != nil {
		return
	}
	dir := filepath.Dir(exe)
	if rel, err := filepath.Rel(cache, dir); err != nil || rel == "." || strings.HasPrefix(rel, "..") || strings.Contains(rel, string(filepath.Separator)) {
		return
	}
	now := time.Now()
	_ = os.Chtimes(dir, now, now)
}

// mergeDriverReport is what `forge merge-driver check` found.
type mergeDriverReport struct {
	command  string   // the driver command this checkout runs
	problems []string // each one means a merge here will not run forge
	notes    []string // worth saying, not a failure
}

// checkMergeDriver inspects the checkout at root: are forge's files routed to
// the driver, and does the driver command name a forge that exists?
func checkMergeDriver(root string) mergeDriverReport {
	var r mergeDriverReport
	probe := "forge_merge_driver_probe_gen.go"
	attr := gitTrimmed(root, "check-attr", "merge", "--", probe)
	if !strings.HasSuffix(attr, ": merge: "+generatedMergeDriver) {
		r.problems = append(r.problems, fmt.Sprintf("generated files are not routed to the driver (git check-attr merge %s: %q): neither .gitattributes nor info/attributes says merge=%s", probe, attr, generatedMergeDriver))
	}
	r.command = gitTrimmed(root, "config", "--get", "merge."+generatedMergeDriver+".driver")
	switch {
	case r.command == "":
		r.problems = append(r.problems, "no merge."+generatedMergeDriver+".driver is configured: git would merge forge's files as plain text")
	case !driverCommandUsable(r.command):
		r.problems = append(r.problems, fmt.Sprintf("the driver command names a forge that is gone, or one too old to merge state files: %s", r.command))
	}
	if pin, binary, skewed := mergeDriverPinCheck(root); skewed {
		if _, ok := pinnedDriverCommand(root, pin); !ok {
			r.notes = append(r.notes, fmt.Sprintf("this checkout's forge is %s and forge.yaml pins %s; a merge installs the pinned forge on first use", binary, pin))
		}
	}
	return r
}

func (r mergeDriverReport) write(w io.Writer, root string) error {
	for _, n := range r.notes {
		fmt.Fprintf(w, "note: %s\n", n)
	}
	if len(r.problems) == 0 {
		fmt.Fprintf(w, "merge driver OK in %s: %s\n", root, r.command)
		return nil
	}
	for _, p := range r.problems {
		fmt.Fprintf(w, "merge driver: %s\n", p)
	}
	return exitCodeError{code: 1, msg: fmt.Sprintf("forge's merge driver is not usable in %s; run `%s merge-driver install` with the pinned forge", root, cmdutil.Name())}
}

func newMergeDriverCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "merge-driver",
		Short: "Install or check the git merge driver for forge-generated files in this checkout",
		Long: `forge wires its merge driver for generated files into a checkout whenever a
writing forge command runs there. A checkout no such command has run in (a
fresh worktree, a merge queue's pool checkout) can name a forge that is gone
or several pins old. install wires this forge into the checkout explicitly
(even under FORGE_NO_MERGE_DRIVER=1) and fails if the checkout still cannot
merge forge's files; check reports the same without changing anything.`,
		Hidden:      true,
		Annotations: map[string]string{skipHookActivationAnnotation: ""},
	}
	cmd.AddCommand(&cobra.Command{
		Use:          "install",
		Short:        "Wire this forge in as the checkout's merge driver, then check it",
		Args:         cobra.NoArgs,
		Annotations:  map[string]string{skipHookActivationAnnotation: ""},
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := cmdutil.FindProjectRoot()
			if err != nil || root == "" {
				return errors.New("merge-driver install: not inside a forge project")
			}
			wireGeneratedMergeDriver(cmd.ErrOrStderr(), root, true)
			return checkMergeDriver(root).write(cmd.OutOrStdout(), root)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:          "check",
		Short:        "Fail unless forge's generated files in this checkout merge through a forge that exists",
		Args:         cobra.NoArgs,
		Annotations:  map[string]string{skipHookActivationAnnotation: ""},
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := cmdutil.FindProjectRoot()
			if err != nil || root == "" {
				return errors.New("merge-driver check: not inside a forge project")
			}
			return checkMergeDriver(root).write(cmd.OutOrStdout(), root)
		},
	})
	return cmdutil.StrictGroup(cmd)
}
