package debug

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/factory"
	dbgsvc "github.com/reliant-labs/forge/internal/debug"
)

func newBreakCmd(_ *factory.Factory) *cobra.Command {
	var (
		funcName   string
		condition  string
		jsonOutput bool
	)

	cmd := &cobra.Command{
		Use:   "break <file:line | function>",
		Short: "Set a breakpoint",
		Long: `Set a breakpoint at a file:line location or on a function by name.

The positional argument is auto-detected: a "file.go:42" spec sets a
source-line breakpoint, while anything else (e.g. "main.handleRequest",
"runtime.gopark", "(*Server).Serve") is resolved as a function breakpoint
via Delve's location parser. The --func flag forces function resolution. A
bare method name (e.g. "CreateTask") resolves to the *Service method of that
name under internal/handlers/.

A file path is relative to the project root. It resolves whether the binary
was built with -trimpath (as air and the Reliant image build) or not, and a
bare file name matches by suffix when it is unique.

Setting a breakpoint on a running target halts it for the instant it takes,
then lets it run on.

Examples:
  forge debug break handler.go:42
  forge debug break main.handleRequest
  forge debug break runtime.gopark
  forge debug break '(*Server).Serve'
  forge debug break --func main.handleRequest
  forge debug break handler.go:42 --cond "id > 5"`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDebugBreak(args, funcName, condition, jsonOutput)
		},
	}

	cmd.Flags().StringVar(&funcName, "func", "", "Set breakpoint on a function by name")
	cmd.Flags().StringVar(&condition, "cond", "", "Conditional expression for the breakpoint")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")

	return cmd
}

func runDebugBreak(args []string, funcName, condition string, jsonOutput bool) error {
	// The --func flag and the positional argument are two spellings of the
	// same intent. Decide which kind of breakpoint to set BEFORE connecting:
	//   - explicit --func        => function breakpoint
	//   - positional "file.go:42" => source-line breakpoint
	//   - any other positional    => function breakpoint (main.Foo,
	//                                 runtime.gopark, (*T).Method, ...)
	// Delve natively resolves function specs via its location parser; the
	// wrapper used to reject everything that wasn't file:line, which made
	// `forge debug break runtime.gopark` impossible.
	spec := funcName
	asFunc := funcName != ""
	if spec == "" {
		if len(args) == 0 {
			return fmt.Errorf("provide a file:line or function argument, or use --func")
		}
		spec = args[0]
		asFunc = !isFileLineSpec(spec)
	}

	dbg, session, err := connectWithSession()
	if err != nil {
		return err
	}
	defer dbg.Disconnect()

	if asFunc {
		// If the user passed a short name (e.g. "Create" without a package
		// qualifier), try to resolve it to a fully-qualified Delve function
		// name from the project's handler packages.
		if !strings.Contains(spec, "/") && !strings.Contains(spec, ".") {
			modPath := readModulePath(".")
			if modPath != "" {
				if resolved := resolveShortFuncName(modPath, spec); resolved != "" {
					spec = resolved
				}
			}
		}
		bp, err := dbg.SetFunctionBreakpoint(spec, condition)
		if err != nil {
			return fmt.Errorf("setting function breakpoint %q: %w", spec, err)
		}
		recordBreakpoint(session, bp.ID)
		printBreakpoint(*bp, jsonOutput)
		return nil
	}

	projectDir, err := os.Getwd()
	if err != nil {
		return err
	}
	ls, err := parseLineSpec(spec, projectDir, readModulePath(projectDir))
	if err != nil {
		return err
	}
	bp, err := dbg.SetLineBreakpoint(ls.files, ls.locExpr, ls.line, condition)
	if err != nil {
		return fmt.Errorf("setting breakpoint at %s: %w", spec, err)
	}
	recordBreakpoint(session, bp.ID)
	printBreakpoint(*bp, jsonOutput)
	return nil
}

// recordBreakpoint adds id to the breakpoints this session created
// (SessionInfo.BreakpointIDs).
func recordBreakpoint(session *dbgsvc.SessionInfo, id int) {
	if slices.Contains(session.BreakpointIDs, id) {
		return
	}
	session.BreakpointIDs = append(session.BreakpointIDs, id)
	if err := debugSvc().SaveSession(".", session); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: saving session: %v\n", err)
	}
}

func newBreakpointsCmd(_ *factory.Factory) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "breakpoints",
		Short: "List all breakpoints",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDebugBreakpoints(jsonOutput)
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func runDebugBreakpoints(jsonOutput bool) error {
	dbg, err := connectToSession()
	if err != nil {
		return err
	}

	bps, err := dbg.ListBreakpoints()
	if err != nil {
		return fmt.Errorf("listing breakpoints: %w", err)
	}

	if jsonOutput {
		_ = json.NewEncoder(os.Stdout).Encode(bps)
		return nil
	}

	if len(bps) == 0 {
		fmt.Println("No breakpoints set.")
		return nil
	}
	for _, bp := range bps {
		printBreakpoint(bp, false)
	}
	return nil
}

func newClearCmd(_ *factory.Factory) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "clear <id>",
		Short: "Clear a breakpoint by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid breakpoint ID %q: %w", args[0], err)
			}

			if err := runDebugClear(id); err != nil {
				return err
			}

			if jsonOutput {
				_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"id": id, "cleared": true})
			} else {
				fmt.Printf("Breakpoint %d cleared.\n", id)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func runDebugClear(id int) error {
	dbg, session, err := connectWithSession()
	if err != nil {
		return err
	}
	defer dbg.Disconnect()
	if err := dbg.ClearBreakpoint(id); err != nil {
		return fmt.Errorf("clearing breakpoint: %w", err)
	}
	if i := slices.Index(session.BreakpointIDs, id); i >= 0 {
		session.BreakpointIDs = slices.Delete(session.BreakpointIDs, i, i+1)
		if err := debugSvc().SaveSession(".", session); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: saving session: %v\n", err)
		}
	}
	return nil
}
