// File: internal/cli/scaffold/variant.go
//
// `forge scaffold variant <pkg> <Name> [--root <root>]` scaffolds one more
// configuration of an existing internal package: a wrapper type over its
// Service plus the provider that builds it, in a new file under
// internal/app.
//
// The composition graph's identity is the Go type, so a component two roots
// (or two consumers) wire differently cannot be told apart by name: each
// configuration is a type of its own. A consumer that wants this
// configuration takes a `*<Name>` field; with --root the provider is
// `//forge:root-only <root>`, so no other root can reach it and resolution
// never offers it to an interface field it merely satisfies. The idiom is
// documented in the architecture/graph-checks skill.

package scaffold

import (
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/naming"
)

// newVariantCmd is the cobra surface for `forge scaffold variant <pkg> <Name>`.
func newVariantCmd(_ *factory.Factory) *cobra.Command {
	var root string
	cmd := &cobra.Command{
		Use:   "variant <pkg> <Name>",
		Short: "Scaffold one more configuration of an internal package as its own type (wrapper + provider in internal/app)",
		Long: `Scaffold a variant of internal/<pkg>: a configuration of the same component
that some roots or consumers need wired differently (a client with and
without a signer, a record-only and a syncing checker).

The composition graph identifies a node by its Go type, so a variant is a
type of its own. This writes internal/app/variant_<name>.go holding

  type <Name> struct{ <pkg>.Service }      the variant's type
  type <Name>Deps struct{ ... }            what its configuration needs
  func New<Name>(<Name>Deps) (*<Name>, error)

and never touches an existing file. A consumer that wants this variant
takes a ` + "`*app.<Name>`" + `-shaped field of that very type (declare the
type where the consumer can import it if internal/app is out of reach).
With --root, the provider is marked //forge:root-only <root>: only that
root may reach it, and it never fills an interface field it merely
satisfies — the other configuration keeps those.

Run 'forge generate' afterwards: the provider joins the graph.

Example:
  forge scaffold variant controlplane SigningControlPlane --root worker
  forge scaffold variant workflowcheck RecordOnlyChecker`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVariant(args[0], args[1], root)
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "mark the provider //forge:root-only <root> (comma-separate several roots)")
	return cmd
}

// runVariant validates inputs and writes internal/app/variant_<name>.go.
func runVariant(pkg, name, roots string) error {
	ctxLabel := fmt.Sprintf("forge scaffold variant %s %s", pkg, name)

	if !token.IsIdentifier(pkg) || strings.ToLower(pkg) != pkg {
		return cliutil.UserErr(ctxLabel, fmt.Sprintf("invalid package name %q", pkg), "",
			"name the Go package under internal/ (lower-case, e.g. controlplane)")
	}
	if !token.IsIdentifier(name) || !token.IsExported(name) {
		return cliutil.UserErr(ctxLabel, fmt.Sprintf("invalid variant name %q", name), "",
			"use an exported Go type name, e.g. SigningControlPlane")
	}
	var rootList []string
	for _, r := range strings.Split(roots, ",") {
		if r = strings.TrimSpace(r); r != "" {
			rootList = append(rootList, r)
		}
	}

	root, err := projectRoot()
	if err != nil {
		return err
	}
	if err := requireServiceKind(root, "variant"); err != nil {
		return err
	}
	cfg, err := generator.ReadProjectConfig(filepath.Join(root, "forge.yaml"))
	if err != nil || cfg.ModulePath == "" {
		return cliutil.UserErr(ctxLabel, "forge.yaml declares no module_path", "",
			"set module_path in forge.yaml")
	}

	if _, err := os.Stat(filepath.Join(root, "internal", pkg)); err != nil {
		return cliutil.UserErr(ctxLabel,
			fmt.Sprintf("there is no package internal/%s", pkg), "",
			fmt.Sprintf("run `forge scaffold package %s` first, or name an existing internal package", pkg))
	}

	appDir := filepath.Join(root, "internal", "app")
	target := filepath.Join(appDir, "variant_"+naming.ToSnakeCase(name)+".go")
	if _, err := os.Stat(target); err == nil {
		return cliutil.UserErr(ctxLabel, fmt.Sprintf("file %s already exists", target), "",
			"pick a different <Name> (or delete the existing file first)")
	}
	// A second New<Name> in package app would not compile.
	if existing, _ := filepath.Glob(filepath.Join(appDir, "*.go")); len(existing) > 0 {
		for _, f := range existing {
			b, err := os.ReadFile(f)
			if err == nil && (strings.Contains(string(b), "func New"+name+"(") || strings.Contains(string(b), "type "+name+" ")) {
				return cliutil.UserErr(ctxLabel,
					fmt.Sprintf("internal/app already declares %s or New%s (%s)", name, name, filepath.Base(f)), "",
					"pick a different <Name>")
			}
		}
	}

	if err := os.MkdirAll(appDir, 0o755); err != nil {
		return cliutil.WrapUserErr(ctxLabel, "create internal/app", appDir, "verify the directory is writable", err)
	}
	content := buildVariantFile(cfg.ModulePath, pkg, name, rootList)
	if err := atomicfile.WriteFile(target, []byte(content), 0o644); err != nil {
		return cliutil.WrapUserErr(ctxLabel, "write variant file", target, "verify the directory is writable", err)
	}

	fmt.Printf("✅ Created %s\n", target)
	fmt.Printf("   Fill %sDeps and the %s.Deps literal in New%s with this configuration.\n", name, pkg, name)
	fmt.Printf("   A consumer that wants it takes a field of type *%s; run `forge generate` to wire it.\n", name)
	return nil
}

// buildVariantFile renders the wrapper type and its provider. Kept pure so
// the test can assert on the content without a filesystem.
func buildVariantFile(module, pkg, name string, roots []string) string {
	var b strings.Builder
	b.WriteString("// yours: scaffolded once by `forge scaffold variant`, never touched again\n")
	b.WriteString("package app\n\n")
	fmt.Fprintf(&b, "import \"%s/internal/%s\"\n\n", module, pkg)
	fmt.Fprintf(&b, "// %s is one configuration of %s.Service. The composition graph\n", name, pkg)
	b.WriteString("// identifies a node by its Go type, so a configuration some consumers need\n")
	b.WriteString("// wired differently is a type of its own: a consumer that wants this one\n")
	fmt.Fprintf(&b, "// takes a field of type *%s (`forge skill load architecture/graph-checks`).\n", name)
	fmt.Fprintf(&b, "type %s struct{ %s.Service }\n\n", name, pkg)
	fmt.Fprintf(&b, "// %sDeps is what this configuration needs, resolved by type.\n", name)
	fmt.Fprintf(&b, "type %sDeps struct {\n\t// Add the dependencies this configuration of %s needs.\n}\n\n", name, pkg)
	fmt.Fprintf(&b, "// New%s builds %s with this variant's configuration.\n", name, pkg)
	if len(roots) > 0 {
		b.WriteString("//\n// Root-only: no other root may reach it, and it never fills an interface\n")
		fmt.Fprintf(&b, "// field it merely satisfies — those keep %s's default configuration.\n", pkg)
		fmt.Fprintf(&b, "//\n//forge:root-only %s\n", strings.Join(roots, ","))
	}
	fmt.Fprintf(&b, "func New%s(d %sDeps) (*%s, error) {\n", name, name, name)
	fmt.Fprintf(&b, "\tsvc, err := %s.New(%s.Deps{\n\t\t// This variant's configuration, from d.\n\t})\n", pkg, pkg)
	b.WriteString("\tif err != nil {\n\t\treturn nil, err\n\t}\n")
	fmt.Fprintf(&b, "\treturn &%s{Service: svc}, nil\n}\n", name)
	return b.String()
}
