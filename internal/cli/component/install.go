package component

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/pkg/components"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

func newInstallCmd(_ *factory.Factory) *cobra.Command {
	var targetDir string
	cmd := &cobra.Command{
		Use:   "install <component-names...>",
		Short: "Install components into your project",
		Long: `Install one or more components from the library into your project's
src/components/ui/ directory — or the directory the frontend's package.json
names in "forge": {"components_dir": "..."}. If --dir is not specified, the
command auto-detects the nearest frontend directory. A component whose file
name differs only in case from one already there (button.tsx beside
Button.tsx) is refused: on macOS and Windows that is one file.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lib := components.NewLibrary()
			dir := targetDir
			if dir == "" {
				detected := detectComponentsDir()
				if detected == "" {
					return fmt.Errorf("could not auto-detect components directory; use --dir to specify")
				}
				dir = detected
			} else {
				dir = resolveInstallDir(dir)
			}

			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("create directory %s: %w", dir, err)
			}

			requested := map[string]bool{}
			for _, name := range args {
				content, err := lib.Get(name)
				if err != nil {
					similar := lib.FindSimilar(name)
					if len(similar) > 0 {
						return fmt.Errorf("component %q not found. Did you mean: %s", name, strings.Join(similar, ", "))
					}
					return fmt.Errorf("component %q not found", name)
				}
				if existing := generator.CaseCollision(dir, name+".tsx"); existing != "" {
					return generator.CaseCollisionError(dir, name+".tsx", existing)
				}
				dest := filepath.Join(dir, name+".tsx")
				if err := atomicfile.WriteFile(dest, []byte(content), 0o644); err != nil {
					return fmt.Errorf("write component %s: %w", name, err)
				}
				requested[name] = true
				fmt.Printf("  Installed %s -> %s\n", name, dest)
			}

			// A component's `./x` imports are siblings in this directory
			// (input → form_field_context, page_header → link). Write each
			// missing one so an install into a bare directory compiles. Never
			// overwrite: a sibling already there may be the project's own
			// (scaffolds place a framework-aware link.tsx).
			for _, name := range args {
				deps, err := lib.Dependencies(name)
				if err != nil {
					return err
				}
				for _, dep := range deps {
					if requested[dep] {
						continue
					}
					requested[dep] = true
					depDest := filepath.Join(dir, dep+".tsx")
					if _, statErr := os.Stat(depDest); !os.IsNotExist(statErr) || generator.CaseCollision(dir, dep+".tsx") != "" {
						continue
					}
					depContent, err := lib.Get(dep)
					if err != nil {
						return err
					}
					if err := atomicfile.WriteFile(depDest, []byte(depContent), 0o644); err != nil {
						return fmt.Errorf("write component %s: %w", dep, err)
					}
					fmt.Printf("  Installed %s -> %s (imported by %s)\n", dep, depDest, name)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&targetDir, "dir", "d", "", "target directory (default: auto-detect nearest frontend)")
	return cmd
}
