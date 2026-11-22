// Copyright (c) 2025 Reliant Labs
package app

import (
	"github.com/reliant-labs/reliant/internal/handlers/tools_daemon"
	"github.com/reliant-labs/reliant/pkg/config"
)

// machineStatsSelection is the gateway's MACHINE_STATS_ORGS and
// MACHINE_STATS_LABEL as the tools_daemon selection of the self-hosted
// machines whose heartbeat stats it logs. An empty org list selects nothing.
func machineStatsSelection(cfg *config.GatewayConfig) tools_daemon.MachineStatsSelection {
	return tools_daemon.MachineStatsSelection{
		Orgs:  cfg.GetMachineStatsOrgs(),
		Label: cfg.GetMachineStatsLabel(),
	}
}
