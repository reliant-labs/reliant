// Package cmdutil holds cross-cutting helpers shared by forge's own CLI across
// MORE THAN ONE command group (internal/cli and its dir-nested subpackages).
// It is a leaf package — it imports only neutral internal packages — so any
// command group can depend on it without an import cycle back to internal/cli.
//
// Helpers used by a single command stay with that command; trivial stdlib
// wrappers (a one-line os.Stat check) are duplicated locally rather than
// shared. Only genuinely shared logic lives here.
package cmdutil

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cliutil"
)

// cmdRoute records, at execution time, how the forge CLI is mounted in
// the EXECUTING binary's cobra tree: no tokens for a standalone forge
// binary (however its file is named), ["forge"] when mounted as a
// subcommand of another root (e.g. `reliant forge`). The forge root
// command's PersistentPreRun records it via RecordCmdRoute before any
// subcommand RunE fires, so Name() and self-invocation
// (internal/cli.forgeExecCommand) reflect the REAL mount instead of
// guessing from the binary's basename.
//
// The basename guess was a silent-degradation trap: a standalone binary
// named anything but exactly "forge" (a temp build, forge-v2) both
// printed un-runnable next-step hints ("forge-v2 forge generate") and
// self-invoked `<exe> forge protoc-gen-forge`, which hit cobra's
// "unknown command" and broke descriptor generation.
var cmdRoute struct {
	recorded bool
	tokens   []string
}

// RecordCmdRoute walks from forge's root command up to the executed
// tree's real root and records the subcommand tokens between them.
// Called by the forge root PersistentPreRun on every invocation.
func RecordCmdRoute(forgeRoot *cobra.Command) {
	var tokens []string
	for c := forgeRoot; c.HasParent(); c = c.Parent() {
		tokens = append([]string{c.Name()}, tokens...)
	}
	cmdRoute.tokens = tokens
	cmdRoute.recorded = true
}

// CmdRouteTokens returns the recorded mount route (subcommand tokens
// from the executing binary to forge's root) and whether one was
// recorded this process.
func CmdRouteTokens() ([]string, bool) {
	return cmdRoute.tokens, cmdRoute.recorded
}

// ResetCmdRoute clears the recorded route. Test hook only — production
// code records exactly once per process, at dispatch.
func ResetCmdRoute() {
	cmdRoute.recorded = false
	cmdRoute.tokens = nil
}

// Name returns the command name users should type to invoke Forge. It
// prefers the recorded mount route (RecordCmdRoute): a standalone
// binary reports its own basename (users literally type that name),
// an embedded mount reports "<basename> forge" (e.g. "reliant forge").
// Without a recording it falls back to the legacy basename heuristic.
// Shared so group commands can print copy-pasteable next-step hints
// without importing internal/cli.
func Name() string {
	base := filepath.Base(os.Args[0])
	if tokens, ok := CmdRouteTokens(); ok {
		return strings.Join(append([]string{base}, tokens...), " ")
	}
	if base == "forge" {
		return "forge"
	}
	return base + " " + "forge"
}

// ErrProjectConfigNotFound is returned when forge.yaml does not exist. The
// canonical sentinel lives here (the shared leaf package) so both internal/cli
// and the dir-nested command groups compare against the same value;
// internal/cli's config.ErrProjectConfigNotFound aliases this.
var ErrProjectConfigNotFound = errors.New("forge.yaml not found in current directory (run 'forge project new' to create a project)")

// ProjectRoot finds the project root by looking for forge.yaml in the
// resolution root — --project-dir when set, else the cwd (NOT a walk-up; see
// FindProjectRoot for that). Returns a user-facing error when forge.yaml is
// absent from that directory.
func ProjectRoot() (string, error) {
	cwd, err := ResolutionRoot()
	if err != nil {
		return "", err
	}
	configPath := filepath.Join(cwd, "forge.yaml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return "", cliutil.UserErr("forge",
			"forge.yaml not found in current directory",
			"",
			"cd into your project root, or run 'forge project new <name>' to scaffold a new project")
	}
	return cwd, nil
}

// FindProjectRoot walks upward from the resolution root (--project-dir when
// set, else the cwd) looking for a forge.yaml. Returns the directory or "" when
// no project is found. Mirrors the loadProjectConfig walk-up behavior in
// config.go.
func FindProjectRoot() (string, error) {
	dir, err := ResolutionRoot()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "forge.yaml")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}
