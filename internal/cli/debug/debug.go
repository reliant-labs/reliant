// Package debug holds the `forge debug` command group — a Delve-backed
// interactive debugger driver (start / break / continue / eval / ...).
//
// Dir-nested command group (the devspace idiom): the parent newCmd assembles
// the subcommands defined in this package's sibling files; the shared session
// + output helpers stay in this file. init() self-registers the group with
// internal/cli/factory so a blank import from internal/cli/groups.go attaches
// it to the root without a cycle.
//
// The package is named `debug`; it imports the debugger engine package
// github.com/reliant-labs/forge/internal/debug under the alias dbgsvc to avoid
// the name collision.
package debug

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cli/factory"
	dbgsvc "github.com/reliant-labs/forge/internal/debug"
)

func init() { factory.Register(newCmd) }

// newCmd creates the top-level debug command and registers all subcommands.
func newCmd(f *factory.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "debug",
		Short: "Debug a running service with Delve",
		Long: `Debug Go services with Delve.

Session state is persisted to .forge/debug-session.json so subsequent
commands (break, continue, eval, ...) reconnect to the same debugger.

Examples:
  forge debug start api-gateway
  forge debug break handler.go:42
  forge debug continue
  forge debug eval "req.UserID"
  forge debug stop`,
	}

	cmd.AddCommand(newStartCmd(f))
	cmd.AddCommand(newBreakCmd(f))
	cmd.AddCommand(newBreakpointsCmd(f))
	cmd.AddCommand(newClearCmd(f))
	cmd.AddCommand(newContinueCmd(f))
	cmd.AddCommand(newStepCmd(f))
	cmd.AddCommand(newStepInCmd(f))
	cmd.AddCommand(newStepOutCmd(f))
	cmd.AddCommand(newEvalCmd(f))
	cmd.AddCommand(newLocalsCmd(f))
	cmd.AddCommand(newArgsCmd(f))
	cmd.AddCommand(newStackCmd(f))
	cmd.AddCommand(newGoroutinesCmd(f))
	cmd.AddCommand(newStopCmd(f))

	return cmdutil.StrictGroup(cmd)
}

// debugSvc returns a debug.Service handle. Constructed once per call site
// because the Service is stateless (Deps is empty today).
func debugSvc() dbgsvc.Service { return dbgsvc.New(dbgsvc.Deps{}) }

// ---------------------------------------------------------------------------
// Session reconnection
// ---------------------------------------------------------------------------

func connectToSession() (*dbgsvc.DelveDebugger, error) {
	session, err := debugSvc().LoadSession(".")
	if err != nil {
		return nil, fmt.Errorf("loading debug session: %w", err)
	}
	if session == nil {
		return nil, fmt.Errorf("no active debug session (run 'forge debug start' first)")
	}
	d := dbgsvc.NewDelveDebugger()
	if err := d.Connect(session.Addr); err != nil {
		return nil, fmt.Errorf("connecting to debugger at %s: %w", session.Addr, err)
	}
	return d, nil
}

// ---------------------------------------------------------------------------
// Output helpers
// ---------------------------------------------------------------------------

func printStopState(state *dbgsvc.StopState, jsonOutput bool) {
	if jsonOutput {
		_ = json.NewEncoder(os.Stdout).Encode(state)
		return
	}
	fmt.Printf("Stopped at %s:%d (%s)\n", state.File, state.Line, state.Function)
	fmt.Printf("Reason: %s", state.Reason)
	if state.GoroutineID > 0 {
		fmt.Printf(" | Goroutine %d", state.GoroutineID)
	}
	fmt.Println()

	if len(state.Args) > 0 {
		fmt.Println("\nArguments:")
		for _, v := range state.Args {
			printVariable(v, "  ")
		}
	}
	if len(state.Locals) > 0 {
		fmt.Println("\nLocals:")
		for _, v := range state.Locals {
			printVariable(v, "  ")
		}
	}
}

func printVariable(v dbgsvc.Variable, indent string) {
	fmt.Printf("%s%-20s %-30s %s\n", indent, v.Name, v.Type, v.Value)
	for _, child := range v.Children {
		printVariable(child, indent+"  ")
	}
}

func printBreakpoint(bp dbgsvc.BreakpointInfo, jsonOutput bool) {
	if jsonOutput {
		_ = json.NewEncoder(os.Stdout).Encode(bp)
		return
	}
	loc := fmt.Sprintf("%s:%d", bp.File, bp.Line)
	if bp.FunctionName != "" {
		loc = fmt.Sprintf("%s (%s)", loc, bp.FunctionName)
	}
	extra := ""
	if bp.Condition != "" {
		extra = fmt.Sprintf(" [cond: %s]", bp.Condition)
	}
	fmt.Printf("Breakpoint %d: %s  hits=%d%s\n", bp.ID, loc, bp.HitCount, extra)
}

func printVariables(vars []dbgsvc.Variable, jsonOutput bool) {
	if jsonOutput {
		_ = json.NewEncoder(os.Stdout).Encode(vars)
		return
	}
	if len(vars) == 0 {
		fmt.Println("(none)")
		return
	}
	for _, v := range vars {
		printVariable(v, "")
	}
}

func printStacktrace(frames []dbgsvc.StackFrame, jsonOutput bool) {
	if jsonOutput {
		_ = json.NewEncoder(os.Stdout).Encode(frames)
		return
	}
	for i, f := range frames {
		fmt.Printf("#%-3d %s\n     %s:%d\n", i, f.Function, f.File, f.Line)
	}
}

func printGoroutines(goroutines []dbgsvc.GoroutineInfo, jsonOutput bool) {
	if jsonOutput {
		_ = json.NewEncoder(os.Stdout).Encode(goroutines)
		return
	}
	fmt.Printf("%-8s %-12s %-50s %s\n", "ID", "STATUS", "FUNCTION", "LOCATION")
	for _, g := range goroutines {
		loc := fmt.Sprintf("%s:%d", g.CurrentFile, g.CurrentLine)
		fmt.Printf("%-8d %-12s %-50s %s\n", g.ID, g.Status, g.Function, loc)
	}
}

// ---------------------------------------------------------------------------
// Shared parsing / function-name resolution helpers
// ---------------------------------------------------------------------------

// isFileLineSpec reports whether s looks like a "file:line" breakpoint
// location (the part after the LAST colon parses as an integer line number).
// Everything else — "main.Foo", "runtime.gopark", "(*Server).Serve", a bare
// "handleRequest" — is treated as a function spec for Delve's location
// parser. A Windows-style "C:\path\handler.go:42" still ends in :<int>, so the
// last-colon rule classifies it correctly as file:line.
func isFileLineSpec(s string) bool {
	idx := strings.LastIndex(s, ":")
	if idx < 0 || idx == len(s)-1 {
		return false
	}
	_, err := strconv.Atoi(s[idx+1:])
	return err == nil
}

// parseFileLine splits "file.go:42" into file and line.
func parseFileLine(s string) (string, int, error) {
	idx := strings.LastIndex(s, ":")
	if idx < 0 {
		return "", 0, fmt.Errorf("expected file:line format (e.g. handler.go:42), got %q", s)
	}
	file := s[:idx]
	line, err := strconv.Atoi(s[idx+1:])
	if err != nil {
		return "", 0, fmt.Errorf("invalid line number in %q: %w", s, err)
	}
	file, err = filepath.Abs(file)
	if err != nil {
		return "", 0, fmt.Errorf("resolving absolute path for %q: %w", s[:idx], err)
	}
	return file, line, nil
}

// readModulePath reads the module path from go.mod in the given directory.
func readModulePath(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimPrefix(line, "module ")
		}
	}
	return ""
}

// resolveShortFuncName searches internal/handlers/ subdirectories for a Go file that
// defines a method matching shortName on a *Service receiver, and returns the
// fully-qualified Delve function name.
func resolveShortFuncName(modulePath, shortName string) string {
	entries, err := os.ReadDir(filepath.Join("internal", "handlers"))
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		files, _ := filepath.Glob(filepath.Join("internal", "handlers", entry.Name(), "*.go"))
		for _, f := range files {
			content, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			if strings.Contains(string(content), "func (s *Service) "+shortName+"(") ||
				strings.Contains(string(content), "func (s *service) "+shortName+"(") {
				return modulePath + "/handlers/" + entry.Name() + ".(*Service)." + shortName
			}
		}
	}
	return ""
}
