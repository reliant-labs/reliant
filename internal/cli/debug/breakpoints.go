package debug

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/factory"
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
via Delve's location parser. The --func flag forces function resolution.

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

	dbg, err := connectToSession()
	if err != nil {
		return err
	}

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
		printBreakpoint(*bp, jsonOutput)
		return nil
	}

	file, line, err := parseFileLine(spec)
	if err != nil {
		return err
	}
	bp, err := dbg.SetBreakpoint(file, line, condition)
	if err != nil {
		return fmt.Errorf("setting breakpoint: %w", err)
	}
	printBreakpoint(*bp, jsonOutput)
	return nil
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

			dbg, err := connectToSession()
			if err != nil {
				return err
			}

			if err := dbg.ClearBreakpoint(id); err != nil {
				return fmt.Errorf("clearing breakpoint: %w", err)
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
