// Package component holds the `forge component` command group — list, search,
// and install UI components from forge's built-in component library.
//
// It is the first dir-nested command group in forge's own CLI (the devspace
// idiom forge ships in generated apps). The parent newCmd assembles the
// subcommands defined in this package's sibling files (list.go, search.go,
// install.go); init() self-registers the group with internal/cli/factory so a
// blank import from internal/cli/groups.go attaches it to the root without a
// group↔root import cycle.
package component

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/config"
)

func init() { factory.Register(newCmd) }

// newCmd builds the `component` parent command and attaches its subcommands.
func newCmd(f *factory.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "component",
		Short: "Manage UI components from the component library",
		Long:  "List, search, and install UI components from Forge's built-in component library.",
	}
	cmd.AddCommand(newListCmd(f))
	cmd.AddCommand(newSearchCmd(f))
	cmd.AddCommand(newInstallCmd(f))
	return cmdutil.StrictGroup(cmd)
}

// detectComponentsDir looks for a frontends/*/src/components/ui/ directory
// relative to the current working directory.
// resolveInstallDir turns a user-supplied --dir into the directory components
// are actually written to.
//
// The command's contract is "install into your project's src/components/ui/",
// and auto-detect has always appended that suffix. --dir used to be taken
// literally, so `--dir .` from a frontend root scattered 74 loose .tsx files
// next to package.json instead — same command, same documented promise, two
// different layouts depending on whether a flag was passed.
//
// So --dir now names the FRONTEND, matching how the flag reads at a call site
// (`--dir web`), and the suffix is appended for it. A caller who wants a
// literal directory still gets one: if the path already ends in components/ui,
// or does not look like a frontend root, it is used verbatim. That keeps the
// escape hatch open for installing into a scratch dir.
func resolveInstallDir(dir string) string {
	cleaned := filepath.Clean(dir)

	// Already pointed at a components/ui directory — use it as given.
	if filepath.Base(cleaned) == "ui" && filepath.Base(filepath.Dir(cleaned)) == "components" {
		return cleaned
	}

	// A frontend root is identified by the things every frontend has. Checking
	// for these rather than assuming keeps `--dir /tmp/scratch` literal.
	for _, marker := range []string{"package.json", "src"} {
		if _, err := os.Stat(filepath.Join(cleaned, marker)); err == nil {
			return filepath.Join(cleaned, filepath.FromSlash(config.FrontendComponentsDir(cleaned)))
		}
	}

	return cleaned
}

func detectComponentsDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}

	// Check frontends directory
	frontendsDir := filepath.Join(cwd, "frontends")
	entries, err := os.ReadDir(frontendsDir)
	if err != nil {
		return ""
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		candidate := filepath.Join(frontendsDir, e.Name(), filepath.FromSlash(config.FrontendComponentsDir(filepath.Join(frontendsDir, e.Name()))))
		if info, err := os.Stat(filepath.Join(frontendsDir, e.Name(), "src")); err == nil && info.IsDir() {
			return candidate
		}
	}
	return ""
}
