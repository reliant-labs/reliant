package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Forge bug report afa50061: reliant's `task test` runs through `reliant forge
// slot run`, and every run printed the version-skew warning, because the
// embedded forge is rarely the one a project pins. The slot commands are
// machine-level coordination that every forge shares, so in a project pinned
// to another forge they run as this one and say nothing about it.
func TestSlotCommandsRunWithoutTheSkewWarning(t *testing.T) {
	skewedProject(t)
	slotEnv(t, "1")
	const warning = "a different forge than the project's"

	// The project really is skewed: a project command warns.
	if out, err := runCheck(t, "skill", "list"); err != nil || !strings.Contains(out, warning) {
		t.Fatalf("forge skill list in the skewed project: err %v, want the skew warning, got:\n%s", err, out)
	}

	for _, args := range [][]string{
		{"slot", "status"},
		{"slot", "status", "--json"},
		{"machine", "slot", "status"},
		{"slot", "run", "--", "forge-no-such-command-xyz"},
		{"machine", "slot", "run", "--", "forge-no-such-command-xyz"},
	} {
		root := newTestRootCmd(t)
		var stdout, stderr bytes.Buffer
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		root.SetArgs(args)
		err := root.Execute()
		if args[len(args)-1] == "forge-no-such-command-xyz" {
			// The missing command proves `slot run` ran it, in a slot.
			var coded interface{ ExitCode() int }
			if !errors.As(err, &coded) || coded.ExitCode() != 127 {
				t.Errorf("forge %s = %v, want the command's exit 127", strings.Join(args, " "), err)
			}
		} else if err != nil {
			t.Errorf("forge %s: %v", strings.Join(args, " "), err)
		}
		for _, out := range []string{stdout.String(), stderr.String()} {
			if strings.Contains(out, warning) || strings.Contains(out, skewTestPin) {
				t.Errorf("forge %s printed the skew warning:\n%s", strings.Join(args, " "), out)
			}
		}
	}
}

// A pin-independent command is silent about the pin because it runs as this
// forge in any project: it must never be handed to the pinned forge, nor
// refused under skew. Every command under each group is checked, so a
// subcommand added later cannot quietly break the pairing.
func TestPinIndependentCommandsRunHere(t *testing.T) {
	root := newTestRootCmd(t)
	for _, group := range pinIndependentCommands {
		cmd, _, err := root.Find(strings.Fields(group))
		if err != nil || commandRel(cmd, root) != group {
			t.Fatalf("pinIndependentCommands names %q, which is not a command (%v)", group, err)
		}
		var walk func(c *cobra.Command)
		walk = func(c *cobra.Command) {
			rel := commandRel(c, root)
			if !pinIndependent(rel) {
				t.Errorf("%q is under %q but not pin-independent", rel, group)
			}
			if handOffModeFor(rel, false) != runHere {
				t.Errorf("%q is pin-independent but handed to the pinned forge: add it to handOffExempt", rel)
			}
			if _, gated := pinGateFor(rel); gated {
				t.Errorf("%q is pin-independent but pin-gated", rel)
			}
			for _, sub := range c.Commands() {
				walk(sub)
			}
		}
		walk(cmd)
	}
}
