// Tests for the user-vs-maintainer CLI surface split (cmdutil.HideDevFlags).
//
// The visible flag set of `forge generate` is pinned here on purpose: a new
// flag must consciously pick a side (visible user surface vs hidden
// --help-dev surface) or these tests fail. The `forge lint` half of the
// split moved with the command to internal/cli/lint/help_surface_test.go.

package cli

import (
	"bytes"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// visibleFlagNames returns the sorted names of all non-hidden local
// flags on cmd — exactly what cobra renders under "Flags:" in --help.
func visibleFlagNames(cmd *cobra.Command) []string {
	var names []string
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if !f.Hidden {
			names = append(names, f.Name)
		}
	})
	sort.Strings(names)
	return names
}

// hiddenFlagNames returns the sorted names of all hidden local flags.
func hiddenFlagNames(cmd *cobra.Command) []string {
	var names []string
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			names = append(names, f.Name)
		}
	})
	sort.Strings(names)
	return names
}

func assertStringSlicesEqual(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s mismatch:\n  got:  %v\n  want: %v\n(new flags must consciously pick a side — see help_dev.go)", what, got, want)
	}
}

func TestGenerateHelpSurface(t *testing.T) {
	cmd := newGenerateCmd()

	assertStringSlicesEqual(t, "generate visible flags", visibleFlagNames(cmd), []string{
		"check",
		// VISIBLE on purpose: --dry-run is the spelling every other forge
		// verb uses for "show me, don't do it", and a user who has to
		// discover that generate calls it something else has already lost
		// the round trip the flag exists to save.
		"dry-run",
		"explain",
		"force",
		// VISIBLE on purpose: a run that keeps hand-edited stale files
		// names this flag as the way to delete them, so --help must show it.
		"force-cleanup",
		"heal",
		"help-dev",
		// VISIBLE on purpose: a skipped run says "--no-cache runs every
		// step", and the flag it names must be one --help shows.
		"no-cache",
		"verbose",
		"watch",
	})

	assertStringSlicesEqual(t, "generate hidden flags", hiddenFlagNames(cmd), []string{
		"explain-drift",
		"no-revert",
		"plan",
		"skip-config-check",
		"skip-pre-checks",
		"skip-validate",
		"steps",
		"strict",
		"templates-only",
		"timings",
	})

	usage := cmd.UsageString()
	for _, name := range hiddenFlagNames(cmd) {
		if strings.Contains(usage, "--"+name) {
			t.Errorf("hidden generate flag --%s leaked into --help usage output", name)
		}
	}
	if !strings.Contains(cmd.Long, "--help-dev") {
		t.Error("generate Long help must mention --help-dev so hidden flags stay discoverable")
	}
}

// TestHiddenFlagsStillParse proves hiding is help-only: hidden flags
// must still parse and set their values exactly as before.
func TestHiddenFlagsStillParse(t *testing.T) {
	cases := []struct {
		name string
		cmd  *cobra.Command
		args []string
		want map[string]string // flag name -> expected parsed value
	}{
		{
			name: "generate hidden bool and string",
			cmd:  newGenerateCmd(),
			args: []string{"--skip-validate", "--steps=mocks", "--plan"},
			want: map[string]string{"skip-validate": "true", "steps": "mocks", "plan": "true"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cmd.Flags().Parse(tc.args); err != nil {
				t.Fatalf("Parse(%v) error = %v (hidden flags must remain functional)", tc.args, err)
			}
			for name, want := range tc.want {
				f := tc.cmd.Flags().Lookup(name)
				if f == nil {
					t.Fatalf("flag --%s not registered", name)
				}
				if got := f.Value.String(); got != want {
					t.Errorf("--%s parsed value = %q, want %q", name, got, want)
				}
			}
		})
	}
}

// TestHelpDevListsHiddenFlags runs `<cmd> --help-dev` end-to-end and
// asserts it exits cleanly, lists every hidden flag, and does NOT run
// the underlying command.
func TestHelpDevListsHiddenFlags(t *testing.T) {
	for _, newCmd := range []func() *cobra.Command{newGenerateCmd} {
		cmd := newCmd()
		hidden := hiddenFlagNames(cmd)

		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"--help-dev"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("forge %s --help-dev: unexpected error: %v", cmd.Name(), err)
		}
		for _, name := range hidden {
			if !strings.Contains(out.String(), "--"+name) {
				t.Errorf("forge %s --help-dev output missing hidden flag --%s\noutput:\n%s", cmd.Name(), name, out.String())
			}
		}
	}
}
