// Copyright (c) 2025 Reliant Labs
package app

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/handlers/tools_daemon"
	"github.com/reliant-labs/reliant/pkg/config"
)

// gatewayReach is the GatewayConfig reach the `server gateway` root loads in
// production (pkg/config's ROOTS projection, the same list
// deploy/kcl/config_gen.k's gateway root and cmd/reliant/cmd's loader use).
func gatewayReach(t *testing.T) []string {
	t.Helper()
	for _, root := range config.Roots() {
		for _, c := range root.Configs {
			if _, ok := c.Message.(*config.GatewayConfig); ok {
				return c.Reach
			}
		}
	}
	t.Fatal("no root reaches GatewayConfig")
	return nil
}

// loadGatewayConfig loads GatewayConfig the way `reliant server gateway`
// does: flags registered on that subcommand, the environment the KCL env map
// (gatewayConfigEnvMap) renders, and the production reach.
func loadGatewayConfig(t *testing.T) *config.GatewayConfig {
	t.Helper()
	root := &cobra.Command{Use: "reliant"}
	server := &cobra.Command{Use: "server"}
	gateway := &cobra.Command{Use: "gateway"}
	root.AddCommand(server)
	server.AddCommand(gateway)
	config.RegisterGatewayConfigFlags(gateway)
	cfg, err := config.LoadGatewayConfigReach(gateway, gatewayReach(t))
	require.NoError(t, err)
	return cfg
}

// MACHINE_STATS_ORGS and MACHINE_STATS_LABEL, as the KCL env map renders them
// (one comma-separated string each), reach the gateway's selection through
// the production loader and composition: an omitted or misparsed allowlist
// would silently watch nothing.
func TestMachineStatsSelectionFromGatewayEnv(t *testing.T) {
	require.Contains(t, gatewayReach(t), "machine_stats_orgs")
	require.Contains(t, gatewayReach(t), "machine_stats_label")

	t.Run("unset is off, with the default label", func(t *testing.T) {
		t.Setenv("MACHINE_STATS_ORGS", "")
		t.Setenv("MACHINE_STATS_LABEL", "")
		sel := machineStatsSelection(loadGatewayConfig(t))
		require.Empty(t, sel.Orgs)
		require.Equal(t, "monitor=platform", sel.Label)
	})

	t.Run("one org and the default label", func(t *testing.T) {
		t.Setenv("MACHINE_STATS_ORGS", "org-platform")
		t.Setenv("MACHINE_STATS_LABEL", "")
		require.Equal(t, tools_daemon.MachineStatsSelection{Orgs: []string{"org-platform"}, Label: "monitor=platform"},
			machineStatsSelection(loadGatewayConfig(t)))
	})

	t.Run("several orgs and a custom label", func(t *testing.T) {
		t.Setenv("MACHINE_STATS_ORGS", "org-platform,org-ci")
		t.Setenv("MACHINE_STATS_LABEL", "pool=ci")
		require.Equal(t, tools_daemon.MachineStatsSelection{Orgs: []string{"org-platform", "org-ci"}, Label: "pool=ci"},
			machineStatsSelection(loadGatewayConfig(t)))
	})
}
