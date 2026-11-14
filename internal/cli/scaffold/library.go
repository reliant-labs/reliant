// File: internal/cli/scaffold/library.go
//
// `forge scaffold library <name>` scaffolds a library-shaped package — a
// utility helper, a thin wrapper around a third-party API, or any
// internal/<name>/ that genuinely doesn't fit the Service/Deps/New
// contract pattern. Two things distinguish it from `forge scaffold package`:
//
//  1. No contract.go. The whole point of a library package is that the
//     contract pattern is overkill — small surface, often function-style
//     calls instead of an interface, sometimes just a few constants.
//
//  2. The path is pre-registered in forge.yaml's `contracts.exclude`
//     list so the contract-required-linter doesn't fire on it. Without
//     this, every library-shaped package in a strict-contracts project
//     produces a manual three-step setup (mkdir, write file, edit yaml)
//     that every cp-forge / control-plane migration repeats.
//
// Behavior:
//
//	forge scaffold library httputil           # internal/httputil/httputil.go + exclude
//	forge scaffold library crypto --path pkg/crypto
//	forge scaffold library fooutil --no-exclude   # skip the yaml edit
//	forge scaffold library bar --force        # overwrite an existing dir
//
// The forge.yaml mutation uses yaml.Node round-tripping (via
// appendToContractsExclude in this file) so user comments and any
// non-canonical fields are preserved.

package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/naming"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// newLibraryCmd is the cobra surface for `forge scaffold library <name>`.
func newLibraryCmd(_ *factory.Factory) *cobra.Command {
	var (
		path      string
		force     bool
		noExclude bool
	)

	cmd := &cobra.Command{
		Use:   "library <name>",
		Short: "Scaffold a library-shaped Go package (no contract.go) and pre-register the contracts.exclude entry",
		Long: `Scaffold a library-shaped Go package into an existing Forge project.

A library package is utility code — a thin wrapper around a third-party
API, a helper module, anything where the Service/Deps/New(Deps) Service
contract pattern is overkill. It deliberately skips contract.go and is
pre-registered in forge.yaml's contracts.exclude so the contract-
required linter won't fire on it.

This is distinct from 'forge scaffold package <name>', which scaffolds a
contract.go and wires the package into bootstrap. Use 'scaffold library'
when the code is genuinely library-shaped; use 'scaffold package' when it
belongs in the Deps graph.

Default path is internal/<name>/. Override with --path to put the
package under pkg/<name>/ (or anywhere else).

Example:
  forge scaffold library httputil
  forge scaffold library crypto --path pkg/crypto
  forge scaffold library legacy --no-exclude   # skip the forge.yaml edit
  forge scaffold library httputil --force      # overwrite an existing dir`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLibrary(args[0], path, force, noExclude)
		},
	}

	cmd.Flags().StringVar(&path, "path", "", "Target directory (default: internal/<name>/)")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite the target directory if it already exists")
	cmd.Flags().BoolVar(&noExclude, "no-exclude", false, "Skip adding the path to forge.yaml's contracts.exclude")

	return cmd
}

// runLibrary executes the library scaffold. Kept package-level (not a
// method on a struct) to match the shape of every other run* in this
// package, so the cobra wiring in newScaffoldCmd stays uniform.
func runLibrary(name, pathFlag string, force, noExclude bool) error {
	ctxLabel := fmt.Sprintf("forge scaffold library %s", name)

	if err := validatePackageName(name); err != nil {
		return cliutil.WrapUserErr(ctxLabel, "invalid library name", "",
			"use a name starting with a letter, containing letters/digits/_/-; not a Go keyword", err)
	}

	root, err := projectRoot()
	if err != nil {
		return err
	}

	// Resolve target path. The default lives under internal/<pkg>/; --path
	// lets users put the package under pkg/<name>/ or anywhere else they
	// prefer. The directory name doubles as the Go package name, so we
	// run it through naming.ServicePackage to normalize hyphens to
	// underscores when defaulting ("http-util" -> "http_util").
	pkg := naming.ServicePackage(name)
	relPath := pathFlag
	if relPath == "" {
		relPath = filepath.Join("internal", pkg)
	}
	// Normalize: clean and force forward slashes for the forge.yaml
	// entry (slashes are the on-disk separator on every platform forge
	// targets, and contracts.exclude matches on slash-form paths).
	relPath = filepath.ToSlash(filepath.Clean(relPath))
	if strings.HasPrefix(relPath, "/") || strings.HasPrefix(relPath, "..") {
		return cliutil.UserErr(ctxLabel,
			fmt.Sprintf("--path must be a project-relative path, got %q", pathFlag),
			"",
			"pass a path like 'pkg/<name>' or 'internal/util/<name>'")
	}

	// Derive the on-disk package identifier from the last segment of the
	// resolved path so a --path override drives the package name. Falling
	// back to pkg keeps the default path's behavior unchanged.
	pkgIdent := naming.ServicePackage(filepath.Base(relPath))

	absDir := filepath.Join(root, relPath)
	if _, err := os.Stat(absDir); err == nil {
		if !force {
			return cliutil.UserErr(ctxLabel,
				fmt.Sprintf("directory already exists: %s", relPath),
				absDir,
				"use --force to overwrite, or pick a different name / --path")
		}
		// --force: wipe the directory contents so the starter file replaces
		// whatever was there. We don't os.RemoveAll the whole tree because
		// that would also remove subdirectories the user might have nested
		// in (rare but conceivable). Removing only files inside the dir
		// keeps the behavior predictable: --force re-stamps the starter
		// without nuking anything else under it.
		if err := os.RemoveAll(absDir); err != nil {
			return cliutil.WrapUserErr(ctxLabel,
				"remove existing directory", absDir,
				"check filesystem permissions and try again", err)
		}
	} else if !os.IsNotExist(err) {
		return cliutil.WrapUserErr(ctxLabel, "stat target directory", absDir,
			"check filesystem permissions", err)
	}

	if err := os.MkdirAll(absDir, 0o755); err != nil {
		return cliutil.WrapUserErr(ctxLabel, "create target directory", absDir,
			"check filesystem permissions and parent directory existence", err)
	}

	// Starter file: bare package declaration + a TODO so `go build` is
	// happy out of the box but the user sees a clear "fill me in" marker.
	starterPath := filepath.Join(absDir, pkgIdent+".go")
	starter := fmt.Sprintf(`// Package %s is library-shaped: utility code that intentionally
// skips the Service/Deps/New contract pattern. The package path is
// registered in forge.yaml's contracts.exclude so the contract-required
// linter won't fire on it.
package %s

// TODO: add your library code here.
//
// Library packages are the right shape for thin wrappers around a
// third-party API, small utility helpers, or pure functions that
// don't need a test-seam interface. If you find yourself growing
// Deps-style construction, exported state, or a stable surface that
// callers test against, promote this package to a contract-style
// service via 'forge scaffold package %s' instead.
`, pkgIdent, pkgIdent, name)

	if err := atomicfile.WriteFile(starterPath, []byte(starter), 0o644); err != nil {
		return cliutil.WrapUserErr(ctxLabel, "write starter file", starterPath,
			"check filesystem permissions", err)
	}

	// Mutate forge.yaml unless the user opted out. yaml.Node round-trip
	// preserves comments and any non-canonical keys the user added.
	if !noExclude {
		configPath := filepath.Join(root, "forge.yaml")
		added, err := appendToContractsExclude(configPath, relPath)
		if err != nil {
			return cliutil.WrapUserErr(ctxLabel, "update contracts.exclude in forge.yaml", configPath,
				"verify forge.yaml is valid YAML, then re-run; or pass --no-exclude to skip", err)
		}
		if added {
			fmt.Printf("Adding library '%s' at %s/\n", name, relPath)
			fmt.Printf("  - %s\n", filepath.Join(relPath, pkgIdent+".go"))
			fmt.Printf("  - forge.yaml (contracts.exclude += %q)\n", relPath)
		} else {
			fmt.Printf("Adding library '%s' at %s/\n", name, relPath)
			fmt.Printf("  - %s\n", filepath.Join(relPath, pkgIdent+".go"))
			fmt.Printf("  - forge.yaml (contracts.exclude already contains %q; left as-is)\n", relPath)
		}
	} else {
		fmt.Printf("Adding library '%s' at %s/\n", name, relPath)
		fmt.Printf("  - %s\n", filepath.Join(relPath, pkgIdent+".go"))
		fmt.Printf("  - forge.yaml (--no-exclude: contracts.exclude not modified)\n")
	}

	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Printf("  1. Edit %s to add your library code.\n", filepath.Join(relPath, pkgIdent+".go"))
	fmt.Println("  2. Per the migration-service skill, library code skips the")
	fmt.Println("     contract.go pattern but still benefits from forge lint.")
	return nil
}

// appendToContractsExclude reads forge.yaml at configPath, ensures the
// contracts: mapping exists, ensures contracts.exclude: is a sequence,
// and appends entry if it's not already present. Returns true when the
// list was modified, false when entry was already there.
//
// The function uses yaml.Node round-tripping so user comments, key
// ordering, and any non-canonical fields under contracts: survive
// untouched. This is the same shape the in-place forge.yaml setters use
// for top-level keys, but parameterized for the nested
// contracts.exclude path.
func appendToContractsExclude(configPath, entry string) (bool, error) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return false, fmt.Errorf("read project config: %w", err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return false, fmt.Errorf("parse project config: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return false, fmt.Errorf("project config %s: expected a YAML document", configPath)
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return false, fmt.Errorf("project config %s: expected top-level mapping", configPath)
	}

	// Find or create the contracts: mapping node.
	contractsNode := findMappingValue(root, "contracts")
	if contractsNode == nil {
		contractsNode = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "contracts"},
			contractsNode,
		)
	} else if contractsNode.Kind != yaml.MappingNode {
		// contracts: present but null/scalar — replace with a fresh mapping.
		contractsNode.Kind = yaml.MappingNode
		contractsNode.Tag = "!!map"
		contractsNode.Value = ""
		contractsNode.Content = nil
	}

	// Find or create the exclude: sequence under contracts:.
	excludeNode := findMappingValue(contractsNode, "exclude")
	if excludeNode == nil {
		excludeNode = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		contractsNode.Content = append(contractsNode.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "exclude"},
			excludeNode,
		)
	} else if excludeNode.Kind != yaml.SequenceNode {
		// exclude: present but null/scalar — replace with a fresh sequence.
		excludeNode.Kind = yaml.SequenceNode
		excludeNode.Tag = "!!seq"
		excludeNode.Value = ""
		excludeNode.Content = nil
	}

	// Idempotency: skip if the entry is already in the list. Compare on
	// the rendered scalar value; quoting style differences don't matter.
	for _, child := range excludeNode.Content {
		if child.Kind == yaml.ScalarNode && child.Value == entry {
			return false, nil
		}
	}

	excludeNode.Content = append(excludeNode.Content, &yaml.Node{
		Kind:  yaml.ScalarNode,
		Tag:   "!!str",
		Value: entry,
	})

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return false, fmt.Errorf("marshal project config: %w", err)
	}
	if err := atomicfile.WriteFile(configPath, out, 0o644); err != nil {
		return false, fmt.Errorf("write project config: %w", err)
	}
	return true, nil
}

// findMappingValue returns the value node for the given key in a
// yaml.MappingNode, or nil if the key is absent. Mapping nodes encode
// keys and values as alternating children.
func findMappingValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i]
		if k.Kind == yaml.ScalarNode && k.Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}
