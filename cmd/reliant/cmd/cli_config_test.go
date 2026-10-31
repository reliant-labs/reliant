// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	userconfig "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/instanceid"
)

// runInGroup runs a probe command under one of CLIConfig's command groups, so
// the group's PersistentPreRunE (configureCLIMachine) runs first, and returns
// what Execute returned. probe may be nil.
func runInGroup(t *testing.T, group string, probe func(cmd *cobra.Command) error, args ...string) error {
	t.Helper()
	root := NewRootCmd()
	groupCmd, _, err := root.Find([]string{group})
	if err != nil {
		t.Fatal(err)
	}
	groupCmd.AddCommand(&cobra.Command{Use: "cli-config-probe", RunE: func(cmd *cobra.Command, _ []string) error {
		if probe != nil {
			return probe(cmd)
		}
		return nil
	}})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(append([]string{group, "cli-config-probe"}, args...))
	return root.Execute()
}

// cliConfigGroups is every command group bindCLIConfig binds (root.go). Each
// must also be one of CLIConfig's `subtrees` (cli_config.proto), or its
// commands fail before they run: `reliant github` shipped bound but missing
// from the annotation, so every `reliant github token|gh` exited with
// "CLIConfig is declared for the commands at or below ...".
var cliConfigGroups = []string{"auth", "daemon", "github", "machine", "project", "trigger", "workflow"}

// Every end-user command group hands the machine block to the packages that
// resolve ~/.reliant and the instance id lazily, before the command runs.
func TestCLIGroupsConfigureTheMachine(t *testing.T) {
	for _, group := range cliConfigGroups {
		t.Run(group, func(t *testing.T) {
			isolateCLI(t)
			t.Cleanup(func() {
				userconfig.ConfigureDirs("", "")
				instanceid.Pin("")
			})
			userDir, dataDir := t.TempDir(), t.TempDir()
			t.Setenv("RELIANT_USER_CONFIG_DIR", userDir)
			t.Setenv("RELIANT_APP_DATA_DIR", dataDir)
			t.Setenv("RELIANT_INSTANCE_ID", "pinned-by-"+group)

			var gotUser, gotData, gotID string
			err := runInGroup(t, group, func(*cobra.Command) error {
				gotUser, gotData, gotID = userconfig.GetUserConfigDir(), userconfig.GetAppDataDir(), instanceid.ID()
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if gotUser != userDir || gotData != dataDir || gotID != "pinned-by-"+group {
				t.Errorf("command saw user config %q, app data %q, instance %q; want %q, %q, %q",
					gotUser, gotData, gotID, userDir, dataDir, "pinned-by-"+group)
			}
		})
	}
}

// The localhost OAuth helper's extra origins come from CLIConfig, for `auth
// serve` and the daemon alike.
func TestCLIWebOrigins(t *testing.T) {
	isolateCLI(t)
	t.Setenv("RELIANT_WEB_ORIGIN", "http://localhost:4321, http://localhost:4322")
	t.Setenv("FRONTEND_PORT", "3600")

	var got []string
	err := runInGroup(t, "daemon", func(cmd *cobra.Command) error {
		var err error
		got, err = cliWebOrigins(cmd)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"http://localhost:4321", "http://localhost:4322", "http://localhost:3600", "http://127.0.0.1:3600"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("web origins = %q, want %q", got, want)
	}
}

// RELIANT_SKIP_TLS_VERIFY is a typed bool now: any spelling of true turns
// verification off, and a value that is not a bool fails the command naming
// the variable instead of being silently ignored.
func TestSkipTLSVerifyIsLoaded(t *testing.T) {
	for value, want := range map[string]bool{"1": true, "true": true, "0": false, "": false} {
		t.Run("RELIANT_SKIP_TLS_VERIFY="+value, func(t *testing.T) {
			isolateCLI(t)
			t.Setenv("RELIANT_SKIP_TLS_VERIFY", value)
			conn, err := resolveWithArgs(t, "--server", "https://reliant.example.com")
			if err != nil {
				t.Fatal(err)
			}
			if conn.skipTLSVerify != want {
				t.Errorf("skipTLSVerify = %v, want %v", conn.skipTLSVerify, want)
			}
		})
	}

	isolateCLI(t)
	t.Setenv("RELIANT_SKIP_TLS_VERIFY", "sometimes")
	err := runInGroup(t, "auth", nil)
	if err == nil || !strings.Contains(err.Error(), "RELIANT_SKIP_TLS_VERIFY") {
		t.Fatalf("want a load error naming RELIANT_SKIP_TLS_VERIFY, got %v", err)
	}
}

// The connection flags belong to the end-user groups alone: a server role's
// command line does not take --server, so no CLI setting reaches its
// Deployment.
func TestServerRolesDoNotTakeTheCLIFlags(t *testing.T) {
	root := NewRootCmd()
	for _, path := range [][]string{{"server", "api"}, {"server", "worker"}, {"server", "gateway"}, {"db", "migrate", "up"}} {
		cmd, _, err := root.Find(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{flagServer, flagGateway} {
			if cmd.Flags().Lookup(name) != nil || cmd.InheritedFlags().Lookup(name) != nil {
				t.Errorf("%s takes --%s", strings.Join(path, " "), name)
			}
		}
	}
	for _, group := range cliConfigGroups {
		cmd, _, err := root.Find([]string{group})
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{flagServer, flagGateway} {
			if cmd.PersistentFlags().Lookup(name) == nil {
				t.Errorf("%s does not take --%s", group, name)
			}
		}
	}
}
