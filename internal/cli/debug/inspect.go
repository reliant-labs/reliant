package debug

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/factory"
)

func newEvalCmd(_ *factory.Factory) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "eval <expression>",
		Short: "Evaluate an expression in the current scope",
		Long: `Evaluate a Go expression in the debugger's current scope.

Examples:
  forge debug eval "req.UserID"
  forge debug eval "len(items)"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dbg, err := connectToSession()
			if err != nil {
				return err
			}
			v, err := dbg.Eval(args[0])
			if err != nil {
				return fmt.Errorf("evaluating expression: %w", err)
			}
			if jsonOutput {
				_ = json.NewEncoder(os.Stdout).Encode(v)
			} else {
				printVariable(*v, "")
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func newLocalsCmd(_ *factory.Factory) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "locals",
		Short: "Show local variables in the current scope",
		RunE: func(cmd *cobra.Command, args []string) error {
			dbg, err := connectToSession()
			if err != nil {
				return err
			}
			vars, err := dbg.Locals()
			if err != nil {
				return fmt.Errorf("listing locals: %w", err)
			}
			printVariables(vars, jsonOutput)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func newArgsCmd(_ *factory.Factory) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "args",
		Short: "Show function arguments in the current scope",
		RunE: func(cmd *cobra.Command, args []string) error {
			dbg, err := connectToSession()
			if err != nil {
				return err
			}
			vars, err := dbg.Args()
			if err != nil {
				return fmt.Errorf("listing args: %w", err)
			}
			printVariables(vars, jsonOutput)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func newStackCmd(_ *factory.Factory) *cobra.Command {
	var (
		depth      int
		jsonOutput bool
	)

	cmd := &cobra.Command{
		Use:   "stack",
		Short: "Show the current call stack",
		RunE: func(cmd *cobra.Command, args []string) error {
			dbg, err := connectToSession()
			if err != nil {
				return err
			}
			frames, err := dbg.Stacktrace(depth)
			if err != nil {
				return fmt.Errorf("getting stacktrace: %w", err)
			}
			printStacktrace(frames, jsonOutput)
			return nil
		},
	}

	cmd.Flags().IntVar(&depth, "depth", 50, "Maximum stack depth")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func newGoroutinesCmd(_ *factory.Factory) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "goroutines",
		Short: "List goroutines",
		RunE: func(cmd *cobra.Command, args []string) error {
			dbg, err := connectToSession()
			if err != nil {
				return err
			}
			goroutines, err := dbg.Goroutines()
			if err != nil {
				return fmt.Errorf("listing goroutines: %w", err)
			}
			printGoroutines(goroutines, jsonOutput)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}
