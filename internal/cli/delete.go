// Package cli — `forge project delete service` — the inverse of `forge scaffold service`.
//
// Where `forge scaffold service` scaffolds a handlers/<svc>/ dir and (after
// the user lists its serviceRow line) serves it, `forge project delete service`
// walks that back:
//
//  1. Removes the handler scaffold directory (handlers/<svc>/).
//  2. Leaves a TYPES-ONLY tombstone comment in pkg/app/services.go in
//     place of the serviceRow line. The comment is load-bearing: the
//     registry treats any mention of the service name in that file as
//     "deliberately not served here", so the proto types / Connect client
//     / frontend hooks keep generating for callers while the handler
//     scaffold, wiring, and auth registration are gated off.
//     (See generate_serve.go's package doc for the registry semantics.)
//
// This addresses the orphan-stub hazard (FORGE_SHAPE_REDESIGN §7f): a
// service added but never implemented previously had no inverse command,
// so it lingered as an Unimplemented CRUD stub with no consumers. Delete
// is the explicit retirement path.
//
// Destructive (it removes a directory), so it confirms before acting
// unless --yes; --dry-run prints the plan and changes nothing.
package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/naming"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

func newDeleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete (retire) a service from the project — the inverse of `forge scaffold service`",
		Long: `Delete a component from an existing Forge project.

Subcommands:
  forge project delete service <name>   Retire a service: remove the handlers/<name>/
                                scaffold and leave a types-only tombstone in
                                pkg/app/services.go so the proto types / Connect
                                client keep generating for callers while the
                                handler is no longer served.`,
	}
	cmd.AddCommand(cmdutil.ConfiguresClone(newDeleteServiceCmd()))
	return cmdutil.StrictGroup(cmd)
}

func newDeleteServiceCmd() *cobra.Command {
	var (
		dryRun    bool
		assumeYes bool
		keepTypes bool
	)

	cmd := &cobra.Command{
		Use:   "service <name>",
		Short: "Retire a service (inverse of `forge scaffold service`)",
		Long: `Retire a service from this project.

What it does:
  - deletes the handlers/<name>/ scaffold directory
  - leaves a types-only tombstone comment in pkg/app/services.go (so the
    proto types, Connect client, and frontend hooks keep generating for
    callers — the service is no longer SERVED, but its contract survives)

Pass --no-keep-types to omit the tombstone comment entirely; the service
then reverts to "unlisted" and forge will re-scaffold its handler on the
next generate if it's still declared in proto.

This is destructive (it removes a directory). It prompts for confirmation
unless --yes; --dry-run prints the plan and changes nothing.

Example:
  forge project delete service reporting`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeleteService(args[0], dryRun, assumeYes, keepTypes, cmd.InOrStdin())
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print what would change without touching any files")
	cmd.Flags().BoolVar(&assumeYes, "yes", false, "Skip the confirmation prompt")
	// keep-types defaults true (the safe, caller-preserving behavior); the
	// negatable form (--keep-types=false) is how a user opts out.
	cmd.Flags().BoolVar(&keepTypes, "keep-types", true, "Leave a types-only tombstone in pkg/app/services.go so proto types + Connect client keep generating (default; --keep-types=false to fully unlist)")

	return cmd
}

// deleteServicePlan is the resolved set of mutations runDeleteService will
// apply — computed once so dry-run and the real run share one description.
type deleteServicePlan struct {
	handlerDir string // project-relative; "" when no scaffold dir on disk
	// declared reports whether the project's own sources still declare this
	// component — its proto, worker package or operator package. Deleting the
	// handler scaffold does not retract that declaration, so the plan says so.
	declared       bool
	registryState  serviceRegistration
	servicesGoPath string // project-relative; "" when no services.go
}

// runDeleteService is the cobra RunE body, split out so tests drive it
// directly. `in` is the confirmation-prompt input (cmd.InOrStdin()).
func runDeleteService(name string, dryRun, assumeYes, keepTypes bool, in io.Reader) error {
	const ctxLabel = "forge project delete service"

	root, err := projectRoot()
	if err != nil {
		return err
	}

	store, err := loadProjectStore()
	if err != nil {
		return cliutil.WrapUserErr(ctxLabel, "failed to load project config", "",
			"verify forge.yaml is valid YAML", err)
	}
	cfg := store.Config()

	// Resolve the service against what the project's sources declare.
	_, declared := codegen.DiscoverProjectComponents(root, cfg.Name).Named(name)

	// Resolve the on-disk handler dir (disk-first — it may use a spelling
	// the naming rules wouldn't synthesize).
	res, resErr := codegen.ResolveServiceComponent(root, name)
	handlerDirRel := ""
	if resErr == nil && res.FromDisk {
		handlerDirRel = "internal/handlers/" + res.ImportLeaf
	}

	// Registry state, so we know whether there's a serviceRow line to
	// rewrite into a tombstone.
	reg, _ := loadServiceRegistry(root)
	regState := registrationUnlisted
	if reg != nil {
		regState = reg.state(name)
	}
	servicesGoRel := ""
	if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(serviceRegistryRelPath))); statErr == nil {
		servicesGoRel = serviceRegistryRelPath
	}

	if !declared && handlerDirRel == "" {
		return cliutil.UserErr(ctxLabel,
			fmt.Sprintf("service %q not found — nothing in the project declares it, and there is no scaffold under handlers/", name),
			"",
			"run `forge project audit` to list the project's services, or check the spelling")
	}

	plan := deleteServicePlan{
		handlerDir:     handlerDirRel,
		declared:       declared,
		registryState:  regState,
		servicesGoPath: servicesGoRel,
	}

	// Describe the plan.
	fmt.Printf("Plan to delete service %q:\n", name)
	if plan.declared {
		fmt.Printf("  - the project still declares %q (proto / package); delete that declaration too to retire it fully\n", name)
	}
	if plan.handlerDir != "" {
		fmt.Printf("  - remove scaffold directory %s/\n", plan.handlerDir)
	} else {
		fmt.Printf("  - (no handlers/ scaffold directory on disk)\n")
	}
	if plan.servicesGoPath != "" {
		switch {
		case !keepTypes:
			fmt.Printf("  - remove the serviceRow line from %s (fully unlist)\n", plan.servicesGoPath)
		case plan.registryState == registrationRegistered:
			fmt.Printf("  - replace the serviceRow line in %s with a types-only tombstone comment\n", plan.servicesGoPath)
		default:
			fmt.Printf("  - leave a types-only tombstone comment in %s\n", plan.servicesGoPath)
		}
	}

	if dryRun {
		fmt.Println("\n(dry-run — no files changed)")
		return nil
	}

	if !assumeYes {
		fmt.Printf("\nProceed? This removes the directory above. [y/N]: ")
		reader := bufio.NewReader(in)
		line, _ := reader.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
		default:
			fmt.Println("Aborted.")
			return nil
		}
	}

	// `forge project delete` removes the CODE (handlers / owned files); the
	// component stops being discovered on the next read because its sources
	// are gone.

	// services.go — rewrite the serviceRow line into a tombstone (or
	//    remove it entirely under --keep-types=false). User-owned file, so
	//    we do a minimal line-targeted edit, never a rewrite.
	if plan.servicesGoPath != "" {
		changed, editErr := rewriteServicesGoForDelete(root, name, keepTypes)
		if editErr != nil {
			return cliutil.WrapUserErr(ctxLabel,
				fmt.Sprintf("failed to update %s", serviceRegistryRelPath), "",
				"the registration file is user-owned — edit it by hand: delete the serviceRow line and (to keep types-only) leave a comment naming the service", editErr)
		}
		switch {
		case changed && keepTypes:
			fmt.Printf("✓ tombstoned %q in %s (types-only — proto types + Connect client still generate)\n", name, serviceRegistryRelPath)
		case changed:
			fmt.Printf("✓ removed the %q serviceRow line from %s\n", name, serviceRegistryRelPath)
		}
	}

	// 3. handler scaffold dir.
	if plan.handlerDir != "" {
		abs := filepath.Join(root, filepath.FromSlash(plan.handlerDir))
		if err := os.RemoveAll(abs); err != nil {
			return cliutil.WrapUserErr(ctxLabel,
				fmt.Sprintf("failed to remove %s", plan.handlerDir), "",
				"check write permissions; remove the directory by hand if needed", err)
		}
		fmt.Printf("✓ removed %s/\n", plan.handlerDir)
	}

	fmt.Printf("\nService %q retired. Run `forge generate` to sweep generated artifacts (gen/ types, mocks, wiring) and `forge project audit` to confirm.\n", name)
	return nil
}

// rewriteServicesGoForDelete edits the user-owned pkg/app/services.go to
// retire a service. When keepTypes is true, the matching `serviceRow<X>(...)`
// line is REPLACED with a tombstone comment (which the registry reads as
// "types-only — deliberately not served"); when false, the line is removed
// outright (the service reverts to unlisted). Returns whether a change was
// made.
//
// The edit is line-targeted, not an AST rewrite: services.go is user-owned
// and may carry hand-written comments / ordering forge must not disturb.
func rewriteServicesGoForDelete(root, name string, keepTypes bool) (bool, error) {
	path := filepath.Join(root, filepath.FromSlash(serviceRegistryRelPath))
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	rowFunc := codegen.ServiceRowFuncName(name) // e.g. serviceRowReporting
	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines))
	changed := false
	for _, line := range lines {
		// Match the registration line: a serviceRow<X>( call. Tolerate the
		// collision-renamed "Svc"-prefixed spelling too.
		trimmed := strings.TrimSpace(line)
		if !changed && (strings.HasPrefix(trimmed, rowFunc+"(") || strings.HasPrefix(trimmed, collisionRowFunc(name)+"(")) {
			changed = true
			if keepTypes {
				indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
				out = append(out, fmt.Sprintf("%s// %s: retired via `forge project delete service` — types-only (proto types + Connect client still generate; not served here).", indent, name))
			}
			// keepTypes==false: drop the line entirely.
			continue
		}
		out = append(out, line)
	}

	if !changed {
		// No serviceRow line found. If keepTypes, ensure a tombstone
		// mention exists so the registry classifies the service as
		// types-only rather than re-scaffolding it as unlisted.
		if keepTypes {
			canonical := naming.ServicePackage(name)
			if !strings.Contains(string(data), canonical) && !strings.Contains(string(data), name) {
				// Append a tombstone comment at end of file (before any
				// trailing newline) — harmless and read by the registry.
				body := strings.TrimRight(string(data), "\n")
				body += fmt.Sprintf("\n\n// %s: retired via `forge project delete service` — types-only (not served here).\n", name)
				if werr := atomicfile.WriteFile(path, []byte(body), 0o644); werr != nil {
					return false, werr
				}
				return true, nil
			}
		}
		return false, nil
	}

	if werr := atomicfile.WriteFile(path, []byte(strings.Join(out, "\n")), 0o644); werr != nil {
		return false, werr
	}
	return true, nil
}

// collisionRowFunc returns the "Svc"-prefixed serviceRow spelling forge
// emits when a service package collides with an internal package (see
// ResolveCollisionNaming). Used so the delete edit also matches the
// collision-renamed registration line.
func collisionRowFunc(name string) string {
	field := naming.ToPascalCase(strings.TrimSuffix(name, "Service"))
	if field == "" {
		field = naming.ToPascalCase(name)
	}
	return codegen.ServiceRowPrefix + "Svc" + field
}
