// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// quotedCommandRef matches a command named in help text — 'workflow watch',
// 'reliant daemon status' — up to the first character that cannot be part of
// a command path (a flag, a placeholder, the closing quote).
var quotedCommandRef = regexp.MustCompile(`'(?:reliant )?((?:auth|daemon|db|forge|preview-url|project|server|trigger|version|workflow)(?: [a-z][a-z-]*)*)`)

// walkCommands calls fn on cmd and every command under it.
func walkCommands(cmd *cobra.Command, fn func(*cobra.Command)) {
	fn(cmd)
	for _, sub := range cmd.Commands() {
		walkCommands(sub, fn)
	}
}

// Help text that names a command must name one that exists. `workflow status
// --help` pointed at a 'workflow ps' that never shipped.
func TestHelpTextNamesOnlyCommandsThatExist(t *testing.T) {
	root := NewRootCmd()
	walkCommands(root, func(cmd *cobra.Command) {
		if strings.HasPrefix(cmd.CommandPath(), "reliant forge") {
			return // forge's help is forge's own, naming forge's commands
		}
		for _, m := range quotedCommandRef.FindAllStringSubmatch(cmd.Long+"\n"+cmd.Example, -1) {
			words := strings.Fields(m[1])
			found, rest, err := root.Find(words)
			require.NoError(t, err, "%s: help names '%s'", cmd.CommandPath(), m[1])
			if found.Name() == "forge" {
				continue
			}
			require.False(t, len(rest) > 0 && found.HasSubCommands(),
				"%s: help names '%s', but %q has no subcommand %q",
				cmd.CommandPath(), m[1], found.CommandPath(), strings.Join(rest, " "))
		}
	})
}

// Every `trigger create` example must run as written, and --daemon is
// required: a trigger names the machine its runs execute on.
func TestTriggerCreateExamplesPassDaemon(t *testing.T) {
	create, _, err := NewRootCmd().Find([]string{"trigger", "create"})
	require.NoError(t, err)
	examples := strings.Split(strings.ReplaceAll(create.Example, "\\\n", " "), "\n")
	n := 0
	for _, line := range examples {
		if strings.Contains(line, "reliant trigger create") {
			n++
			require.Contains(t, line, "--daemon", "example omits the required --daemon: %s", line)
		}
	}
	require.Positive(t, n)
}

// With no stored login, `auth status` says so once — not "Not logged in (not
// logged in)".
func TestAuthStatusNotLoggedInSaysItOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("RELIANT_TOKEN", "")
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"auth", "status", "--server", testServerURL})
	require.Error(t, root.Execute())
	require.Contains(t, out.String(), "Not logged in")
	require.NotContains(t, out.String(), "(not logged in)")

	// The same message, from any command that needs a login.
	root = NewRootCmd()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"project", "list", "--server", testServerURL})
	err := root.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "not logged in to")
	require.NotContains(t, err.Error(), "(not logged in)")
}
