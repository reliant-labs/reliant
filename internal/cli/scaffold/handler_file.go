// File: internal/cli/scaffold/handler_file.go
//
// `forge scaffold handler-file <svc> <name>` scaffolds an additional RPC-group
// file inside an existing handler directory. Splitting a fat handlers.go
// into per-RPC-group files (handlers_admin.go, handlers_billing.go, ...)
// is a common refactor once a service grows past ~10 RPCs; before this
// subcommand the user had to: (a) create the file by hand, (b) remember
// the right package declaration, (c) remember that mock_gen reads
// methods across every non-test .go file in the directory so nothing
// extra needs to be registered.
//
// All three steps fit a one-line cobra command, so they are.

package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/naming"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// newHandlerFileCmd is the cobra surface for `forge scaffold handler-file <svc> <name>`.
func newHandlerFileCmd(_ *factory.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "handler-file <svc> <name>",
		Short: "Scaffold an additional RPC-group file in an existing handler directory",
		Long: `Scaffold a new .go file under handlers/<svc>/ to hold a subset of the
service's RPC implementations. Useful when a single handlers.go has
grown unwieldy and you want to split RPCs into per-feature files
(handlers_billing.go, handlers_admin.go, ...).

The new file is a one-line stub: just the package declaration plus a
comment noting the convention. mock_gen.go discovers methods across
every non-test .go file in the package, so no extra registration is
required — copy the method bodies you want to split out from
handlers.go (or your generated stub file) into the new file and you
are done.

Run 'forge generate' after splitting to refresh mock_gen.go.

Example:
  forge scaffold handler-file billing payment_methods
  forge scaffold handler-file admin user_admin`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHandlerFile(args[0], args[1])
		},
	}
	return cmd
}

// runHandlerFile validates inputs and writes the stub file under
// handlers/<svc>/<name>.go.
//
// Pre-conditions enforced here, in order:
//
//  1. <svc> is a valid identifier (same rule as `forge scaffold service`).
//  2. <name> is a valid identifier — it becomes a Go filename + sits
//     in the same package, so the same rules apply.
//  3. The handler directory exists. We do not auto-create it; if the
//     service hasn't been scaffolded yet, the user should run
//     `forge scaffold service <svc>` first.
//  4. The target file does not already exist. Overwriting a hand-edited
//     RPC file would silently destroy code; the right answer is to
//     pick a different name (or delete the old file first).
func runHandlerFile(svc, name string) error {
	ctxLabel := fmt.Sprintf("forge scaffold handler-file %s %s", svc, name)

	if err := validateIdentifier(svc); err != nil {
		return cliutil.WrapUserErr(ctxLabel, "invalid service name", "",
			"use a name starting with a letter, containing letters/digits/_/-", err)
	}
	// Tolerate a trailing ".go" before identifier validation — the
	// subcommand owns the file extension, so seeing one is a habit-typo
	// rather than a user error worth rejecting. validateIdentifier
	// itself would refuse the dot.
	fileName := strings.TrimSuffix(name, ".go")
	if err := validateIdentifier(fileName); err != nil {
		return cliutil.WrapUserErr(ctxLabel, "invalid file name", "",
			"use a name starting with a letter, containing letters/digits/_/-", err)
	}

	root, err := projectRoot()
	if err != nil {
		return err
	}
	if err := requireServiceKind(root, "handler-file"); err != nil {
		return err
	}

	// Resolve the on-disk handler dir from <svc>. naming.ServicePackage
	// matches the convention `forge scaffold service` uses to derive a Go
	// package name from the proto-shaped service name (hyphens → underscores).
	pkg := naming.ServicePackage(svc)
	handlerDir := filepath.Join(root, "internal", "handlers", pkg)
	if _, err := os.Stat(handlerDir); os.IsNotExist(err) {
		return cliutil.UserErr(ctxLabel,
			fmt.Sprintf("service %q has no handler directory at %s", svc, handlerDir),
			"",
			fmt.Sprintf("run `forge scaffold service %s` first to scaffold the service", svc))
	}

	// fileName was sanitized above (trailing .go stripped + identifier
	// validated). Re-derive the on-disk path with the canonical extension.
	targetPath := filepath.Join(handlerDir, fileName+".go")
	if _, err := os.Stat(targetPath); err == nil {
		return cliutil.UserErr(ctxLabel,
			fmt.Sprintf("file %s already exists", targetPath),
			"",
			"pick a different <name> (or delete the existing file first if you really want to start over)")
	}

	content := buildHandlerFileStub(pkg, fileName)
	if err := atomicfile.WriteFile(targetPath, []byte(content), 0o644); err != nil {
		return cliutil.WrapUserErr(ctxLabel, "write handler file", targetPath,
			"verify the directory is writable", err)
	}

	fmt.Printf("✅ Created %s\n", targetPath)
	fmt.Println("   Move the RPC method implementations you want to group here from handlers.go.")
	fmt.Println("   Run `forge generate` to refresh mock_gen.go for the package.")
	return nil
}

// buildHandlerFileStub produces the one-line stub written to disk. The
// generated file is deliberately minimal — just the package declaration
// and a comment explaining the convention — because the whole point of
// `forge scaffold handler-file` is to give the user an empty file to paste
// hand-written RPC bodies into. A larger scaffold would force the user
// to delete boilerplate before getting useful work done.
//
// Kept package-level (rather than inlined) so the test can assert on
// the exact content without re-running the filesystem path.
func buildHandlerFileStub(pkg, fileName string) string {
	return fmt.Sprintf(`package %s

// %s.go is one of several RPC-implementation files in the %s handler
// package. mock_gen.go (regenerated by `+"`forge generate`"+`) discovers
// RPC methods across every non-test .go file in the directory, so this
// file does not need to be registered anywhere.
//
// Add RPC method implementations here.
`, pkg, fileName, pkg)
}
