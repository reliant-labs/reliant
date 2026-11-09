package debug

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/factory"
)

func newContinueCmd(_ *factory.Factory) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "continue",
		Short: "Resume execution until the next breakpoint",
		RunE: func(cmd *cobra.Command, args []string) error {
			dbg, err := connectToSession()
			if err != nil {
				return err
			}
			state, err := dbg.Continue()
			if err != nil {
				return fmt.Errorf("continuing: %w", err)
			}
			printStopState(state, jsonOutput)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func newStepCmd(_ *factory.Factory) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "step",
		Short: "Step over the current line",
		RunE: func(cmd *cobra.Command, args []string) error {
			dbg, err := connectToSession()
			if err != nil {
				return err
			}
			state, err := dbg.StepOver()
			if err != nil {
				return fmt.Errorf("stepping over: %w", err)
			}
			printStopState(state, jsonOutput)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func newStepInCmd(_ *factory.Factory) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "step-in",
		Short: "Step into the current function call",
		RunE: func(cmd *cobra.Command, args []string) error {
			dbg, err := connectToSession()
			if err != nil {
				return err
			}
			state, err := dbg.StepInto()
			if err != nil {
				return fmt.Errorf("stepping in: %w", err)
			}
			printStopState(state, jsonOutput)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func newStepOutCmd(_ *factory.Factory) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "step-out",
		Short: "Step out of the current function",
		RunE: func(cmd *cobra.Command, args []string) error {
			dbg, err := connectToSession()
			if err != nil {
				return err
			}
			state, err := dbg.StepOut()
			if err != nil {
				return fmt.Errorf("stepping out: %w", err)
			}
			printStopState(state, jsonOutput)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}
