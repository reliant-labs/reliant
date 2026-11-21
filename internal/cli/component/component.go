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
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/cliutil"
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

// detectComponentsDir picks the frontend to install into when --dir is not
// given, from the project's frontend inventory (the same set `forge lint`
// and `forge build` read), so it works from the project root, from inside a
// frontend, and from any directory below either.
//
//   - cwd inside a frontend: that frontend.
//   - exactly one web frontend: that one.
//   - several: an error listing the --dir value for each, rather than
//     silently taking the first in directory order.
//   - none, or no forge.yaml above cwd: the directory itself when it looks
//     like a frontend root, else an error naming the flag.
//
// React Native frontends are not candidates: the library's components are
// DOM components.
func detectComponentsDir() (string, error) {
	cwd, err := cmdutil.ResolutionRoot()
	if err != nil {
		return "", err
	}
	root, err := cmdutil.FindProjectRoot()
	if err != nil {
		return "", err
	}

	var frontends []string // absolute frontend roots
	if root != "" {
		// A forge.yaml that does not load is an error to surface, not an
		// empty inventory: treating it as "no frontend" would fall through to
		// installing into whatever frontend-shaped directory cwd happens to be.
		cfg, err := config.LoadProjectDir(root)
		if err != nil {
			return "", cliutil.WrapUserErr("forge component install",
				"could not load the project to find its frontends",
				filepath.Join(root, "forge.yaml"),
				"fix forge.yaml, or pass the frontend directory: --dir <path/to/frontend>",
				err)
		}
		for _, fe := range cfg.ToolchainFrontends() {
			if fe.Type == "react-native" {
				continue
			}
			rel, ok := fe.Dir(root)
			if !ok {
				continue
			}
			frontends = append(frontends, filepath.Join(root, filepath.FromSlash(rel)))
		}
	}

	componentsDir := func(feDir string) string {
		return filepath.Join(feDir, filepath.FromSlash(config.FrontendComponentsDir(feDir)))
	}

	// Inside a frontend: the innermost one containing cwd. A root frontend
	// (".") contains everything, so prefer the longest match.
	best := ""
	for _, fe := range frontends {
		if within(cwd, fe) && len(fe) > len(best) {
			best = fe
		}
	}
	if best != "" {
		return componentsDir(best), nil
	}

	switch len(frontends) {
	case 1:
		return componentsDir(frontends[0]), nil
	case 0:
		if looksLikeFrontendRoot(cwd) {
			return componentsDir(cwd), nil
		}
		where := "no forge.yaml in this directory or above"
		if root != "" {
			where = "the project at " + root + " has no web frontend under frontends/"
		}
		return "", cliutil.UserErr("forge component install",
			"could not find a frontend to install into: "+where,
			"",
			"pass the frontend directory: --dir <path/to/frontend> (components go in its src/components/ui/)")
	default:
		lines := make([]string, 0, len(frontends))
		for _, fe := range frontends {
			rel, err := filepath.Rel(cwd, fe)
			if err != nil {
				rel = fe
			}
			lines = append(lines, "  --dir "+filepath.ToSlash(rel))
		}
		return "", cliutil.UserErr("forge component install",
			fmt.Sprintf("this project has %d frontends; pick one", len(frontends)),
			"",
			"re-run with one of:\n"+strings.Join(lines, "\n"))
	}
}

// within reports whether path is dir or below it.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// looksLikeFrontendRoot is the same marker test resolveInstallDir uses for
// --dir: a package.json and a src/ directory.
func looksLikeFrontendRoot(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, "src"))
	return err == nil && info.IsDir()
}
