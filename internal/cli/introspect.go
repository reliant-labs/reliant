// Package cli — `forge project introspect` command group.
//
// Introspect surfaces "what the binary will actually register" without
// requiring the binary to be running. The current leaf is `handlers`,
// which walks the project's proto services and prints every RPC path in
// the canonical Connect form `/<package>.<service>/<method>`.
//
// Use case: you wired a new service and want to confirm at a glance that
// every RPC will be reachable at the expected URL — catches "this
// service isn't wired" issues in seconds rather than via curl-debugging
// a running server.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/codegen"
)

// HandlerPath is a single RPC route the binary will register.
// Service is the fully-qualified proto service name (package.Service);
// Method is the RPC method name; Path is the canonical Connect URL
// (`/<Service>/<Method>`).
type HandlerPath struct {
	Service string `json:"service"`
	Method  string `json:"method"`
	Path    string `json:"path"`
}

// newIntrospectCmd builds the `forge project introspect` command group. Today
// it has one leaf — `handlers` — but the group exists so other
// "what would the binary do" inspectors (routes, middleware chain,
// scheduled workers, …) can live alongside without polluting the root.
func newIntrospectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "introspect",
		Short: "Inspect what the assembled binary will expose at runtime",
		Long: `Inspect what the assembled binary will expose at runtime.

Subcommands answer "what would the binary do" questions without
requiring it to be running. Useful for catching wiring mistakes early.

Subcommands:
  handlers   Print every RPC path the binary will register.`,
	}
	cmd.AddCommand(newIntrospectHandlersCmd())
	return cmdutil.StrictGroup(cmd)
}

// newIntrospectHandlersCmd builds `forge project introspect handlers`.
//
// Reads the project's proto services via the same descriptor used by
// `forge generate` / `forge project audit`, then emits one line per RPC in the
// canonical Connect path form. Sorted by service then method so output
// is stable across runs (diffable in CI).
func newIntrospectHandlersCmd() *cobra.Command {
	var (
		protoDir string
		format   string
	)
	cmd := &cobra.Command{
		Use:   "handlers",
		Short: "Print every RPC path the binary will register",
		Long: `Print every RPC path the binary will register.

Walks the project's proto service definitions and prints one line per
RPC in the canonical Connect form: /<package>.<Service>/<Method>.
Output is sorted by service then method for stable diffs.

Examples:
  forge project introspect handlers
  forge project introspect handlers --format json
  forge project introspect handlers --proto-dir proto/services`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIntrospectHandlers(cmd.OutOrStdout(), protoDir, format)
		},
	}
	cmd.Flags().StringVar(&protoDir, "proto-dir", "proto/services", "Directory containing service proto files")
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text or json")
	return cmd
}

// runIntrospectHandlers is the command body, split out so tests can
// exercise it against any io.Writer without spawning a real cobra
// invocation.
func runIntrospectHandlers(w io.Writer, protoDir, format string) error {
	switch format {
	case "text", "json":
	default:
		return fmt.Errorf("unknown --format %q (want text or json)", format)
	}

	// Resolve project dir the same way map/audit do: walk up from cwd
	// looking for forge.yaml. Falls back to cwd so the command still
	// works in repos without forge.yaml (the descriptor is still
	// addressable relative to wherever the user invoked from).
	projectDir := "."
	if path, err := findProjectConfigFile(); err == nil {
		projectDir = filepath.Dir(path)
	} else if !errors.Is(err, ErrProjectConfigNotFound) {
		return fmt.Errorf("locate project config: %w", err)
	}

	absProtoDir := protoDir
	if !filepath.IsAbs(absProtoDir) {
		absProtoDir = filepath.Join(projectDir, protoDir)
	}

	defs, err := codegen.ParseServicesFromProtos(absProtoDir, projectDir)
	if err != nil {
		return fmt.Errorf("parse services: %w", err)
	}

	// The command's contract is "paths the BINARY will register" — drop
	// services with no serviceRow in the user-owned pkg/app/services.go:
	// their RPCs exist in the protos but this binary does not mount
	// them. Best-effort: a missing/broken registry serves everything
	// (pre-registration behavior).
	reg, regErr := loadServiceRegistry(projectDir)
	if regErr != nil {
		reg = &serviceRegistry{Exists: false}
	}
	var served []codegen.ServiceDef
	for _, d := range defs {
		if reg.registered(d.Name) {
			served = append(served, d)
		}
	}

	paths := handlerPathsFromServices(served)
	return writeHandlerPaths(w, paths, format)
}

// handlerPathsFromServices builds the sorted list of RPC paths from a
// slice of ServiceDef. Pure function — kept separate from the I/O so
// tests can feed it synthetic input.
//
// Sort order: by Service (case-sensitive), then by Method. Stable
// output matters for CI diffs and for the "did anything change?"
// agent-loop use case.
func handlerPathsFromServices(defs []codegen.ServiceDef) []HandlerPath {
	var out []HandlerPath
	for _, d := range defs {
		// Skip services with no package — would emit a bare leading
		// dot in the path which is never what the user wants. This
		// shouldn't happen with well-formed protos but guard anyway.
		if d.Package == "" || d.Name == "" {
			continue
		}
		fq := d.Package + "." + d.Name
		for _, m := range d.Methods {
			if m.Name == "" {
				continue
			}
			out = append(out, HandlerPath{
				Service: fq,
				Method:  m.Name,
				Path:    "/" + fq + "/" + m.Name,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		return out[i].Method < out[j].Method
	})
	return out
}

// writeHandlerPaths emits paths in the requested format. text mode is
// one path per line; json mode is an indented array so it pipes
// cleanly into jq.
func writeHandlerPaths(w io.Writer, paths []HandlerPath, format string) error {
	if format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		// Always emit a list (never null) so consumers can
		// unconditionally `.[]` over it.
		if paths == nil {
			paths = []HandlerPath{}
		}
		return enc.Encode(paths)
	}
	for _, p := range paths {
		if _, err := fmt.Fprintln(w, p.Path); err != nil {
			return err
		}
	}
	return nil
}
