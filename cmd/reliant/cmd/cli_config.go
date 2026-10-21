// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/internal/auth/oauthhelper"
	userconfig "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/instanceid"
	"github.com/reliant-labs/reliant/pkg/config"
)

// bindCLIConfig makes each end-user command group a CLIConfig subtree
// (proto/config/v1/cli_config.proto): its flags (--server, --gateway) are
// persistent on the group, and every command in it configures this machine's
// Reliant state from the loaded config before it runs. The groups must match
// the message's `subtrees`; LoadCLIConfig refuses a command outside them.
func bindCLIConfig(groups ...*cobra.Command) {
	for _, group := range groups {
		config.RegisterCLIConfigFlags(group)
		group.PersistentPreRunE = configureCLIMachine
	}
}

// configureCLIMachine hands CLIConfig's machine block to the packages that
// resolve this machine's Reliant state lazily, far below any command: where
// ~/.reliant and the app data live (internal/config) and the pinned instance
// id (internal/instanceid). They are set once, here, rather than threaded to
// every reader, as the root config's environment tier is (loadRootConfig).
func configureCLIMachine(cmd *cobra.Command, _ []string) error {
	cfg, err := config.LoadCLIConfig(cmd)
	if err != nil {
		return err
	}
	machine := cfg.GetMachine()
	userconfig.ConfigureDirs(machine.GetUserConfigDir(), machine.GetAppDataDir())
	instanceid.Pin(machine.GetInstanceId())
	return nil
}

// cliWebOrigins is the web origins CLIConfig names (RELIANT_WEB_ORIGIN and
// FRONTEND_PORT's loopback origins), which every localhost OAuth helper this
// process opens accepts.
func cliWebOrigins(cmd *cobra.Command) ([]string, error) {
	cfg, err := config.LoadCLIConfig(cmd)
	if err != nil {
		return nil, err
	}
	web := cfg.GetWeb()
	return oauthhelper.WebOrigins(web.GetOrigins(), web.GetFrontendPort()), nil
}
