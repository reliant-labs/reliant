package debug

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/factory"
	dbgsvc "github.com/reliant-labs/forge/internal/debug"
)

func newContinueCmd(_ *factory.Factory) *cobra.Command {
	var (
		jsonOutput bool
		timeout    time.Duration
		noWait     bool
	)

	cmd := &cobra.Command{
		Use:   "continue",
		Short: "Resume execution until the next breakpoint",
		Long: `Resume the target and wait for its next stop.

A running target (resumed by start/attach, 'continue --no-wait' or an earlier
timed-out continue) is not resumed again: continue waits for the stop that is
coming. A stop the target reached while nobody was waiting is reported, not
skipped.

--timeout bounds the wait (0 = until it stops): on timeout continue exits
non-zero and the target keeps running. --no-wait resumes and returns at once.

Examples:
  forge debug continue --timeout 60s
  forge debug continue --no-wait`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContinue(timeout, noWait, jsonOutput)
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "Give up waiting after this long, leaving the target running (0 = wait until it stops)")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "Resume the target and return without waiting for a stop")
	return cmd
}

func runContinue(timeout time.Duration, noWait, jsonOutput bool) error {
	dbg, session, err := connectWithSession()
	if err != nil {
		return err
	}
	defer dbg.Disconnect()

	if noWait {
		if err := dbg.Resume(); err != nil {
			return fmt.Errorf("resuming: %w", err)
		}
		setResumed(session, true)
		if jsonOutput {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"running": true})
		} else {
			fmt.Println("Target resumed (running). 'forge debug continue --timeout <d>' waits for the next stop.")
		}
		return nil
	}

	if st, err := dbg.State(); st == dbgsvc.TargetExited {
		return fmt.Errorf("target has exited: %w", err)
	} else if err != nil {
		return err
	}
	// An earlier command resumed the target and returned, so its next stop
	// is still unobserved: only wait for it. WaitForStop never resumes, so
	// a stop that lands at any moment — before this command or during it —
	// is reported, not skipped. Otherwise resume to the next stop.
	wait := dbg.ContinueFor
	if session.Resumed {
		wait = dbg.WaitForStop
	}
	state, err := wait(timeout)
	if errors.Is(err, dbgsvc.ErrStillRunning) {
		setResumed(session, true)
		return fmt.Errorf("no stop within %s: the target is still running (the next 'forge debug continue' waits for its next stop)", timeout)
	}
	if err != nil {
		return fmt.Errorf("continuing: %w", err)
	}
	setResumed(session, false)
	printStopState(state, jsonOutput)
	return nil
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
			markObserved()
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
			markObserved()
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
			markObserved()
			printStopState(state, jsonOutput)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}
