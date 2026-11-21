// Copyright (c) 2025 Reliant Labs
package app

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/forgetest"
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

// TestMachineStatsSelectionFromKCL checks the deployment projection as well as
// the loader: dropping either env mapping must fail even if Go defaults match.
func TestMachineStatsSelectionFromKCL(t *testing.T) {
	forge := forgetest.Pinned(t) // KCL evaluation is slow and skips under -short.
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	cmd := forge.Command(t.Context(), "kcl", "eval", "deploy/kcl/testdata/machine_stats_gateway.k", "-S", "cases")
	cmd.Dir = root
	out, err := cmd.Output()
	require.NoError(t, err, "evaluate gateway deployment env: %s", out)
	var cases map[string]map[string]any
	require.NoError(t, json.Unmarshal(out, &cases))
	for _, tc := range []struct {
		name  string
		orgs  string
		label string
		want  tools_daemon.MachineStatsSelection
	}{
		{"default", "", "monitor=platform", tools_daemon.MachineStatsSelection{Label: "monitor=platform"}},
		{"single", "org-platform", "monitor=platform", tools_daemon.MachineStatsSelection{Orgs: []string{"org-platform"}, Label: "monitor=platform"}},
		{"multiple", "org-platform,org-ci", "pool=ci", tools_daemon.MachineStatsSelection{Orgs: []string{"org-platform", "org-ci"}, Label: "pool=ci"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := cases[tc.name]
			require.Contains(t, env, "MACHINE_STATS_ORGS")
			require.Contains(t, env, "MACHINE_STATS_LABEL")
			require.Equal(t, tc.orgs, env["MACHINE_STATS_ORGS"])
			require.Equal(t, tc.label, env["MACHINE_STATS_LABEL"])
			orgs, ok := env["MACHINE_STATS_ORGS"].(string)
			require.True(t, ok)
			label, ok := env["MACHINE_STATS_LABEL"].(string)
			require.True(t, ok)
			t.Setenv("MACHINE_STATS_ORGS", orgs)
			t.Setenv("MACHINE_STATS_LABEL", label)
			require.Equal(t, tc.want, machineStatsSelection(loadGatewayConfig(t)))
		})
	}
}

// The fast loader coverage stays active under -short independently of KCL.
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
