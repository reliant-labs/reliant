// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/internal/diskguard"
	"github.com/reliant-labs/reliant/internal/egresslimit"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonruntime"
	"github.com/reliant-labs/reliant/pkg/config"
)

// loadDaemonConfig loads `daemon start`'s config (proto/reliant/config/v1 DaemonConfig)
// and refuses one the daemon cannot run with. The loader refuses a value that
// does not parse or lies outside a field's allowed set (DAEMON_TLS_MODE,
// RELIANT_DAEMON_TYPE); this adds a port that is not a port. So a typo in any
// daemon setting stops the daemon here, before it claims its data directory or
// dials anything.
func loadDaemonConfig(cmd *cobra.Command) (*config.DaemonConfig, error) {
	cfg, err := config.LoadDaemonConfig(cmd)
	if err != nil {
		return nil, err
	}
	for _, p := range []struct {
		name     string
		port     int32
		optional bool
	}{
		{"TOOLS_DAEMON_PORT (--port)", cfg.GetGatewayPort(), false},
		{"DAEMON_LISTEN_PORT (--listen-port)", cfg.GetListenPort(), false},
		{"DAEMON_FRONTEND_PORT", cfg.GetFrontendPort(), true},
	} {
		if p.optional && p.port == 0 {
			continue
		}
		if p.port < 1 || p.port > 65535 {
			return nil, fmt.Errorf("config: %s = %d is not a TCP port (1-65535)", p.name, p.port)
		}
	}
	return cfg, nil
}

// daemonHost is what the platform declared about where this daemon runs, in
// the shape the daemon runtime takes.
func daemonHost(cfg *config.DaemonConfig) daemonruntime.Host {
	return daemonruntime.Host{
		DaemonType:   cfg.GetDaemonType(),
		WorkingDir:   cfg.GetWorkingDir(),
		RuntimeType:  cfg.GetRuntimeType(),
		FrontendPort: int(cfg.GetFrontendPort()),
		ProjectPath:  cfg.GetProjectPath(),

		AllowPrivateFetch: cfg.GetFetchAllowPrivateNetwork(),
		Egress: egresslimit.Limits{
			UserPerMinute:     int(cfg.GetEgressUserPerMinute()),
			UserBurst:         int(cfg.GetEgressUserBurst()),
			UserMaxConcurrent: int(cfg.GetEgressUserMaxConcurrent()),
			HostMaxConcurrent: int(cfg.GetEgressHostMaxConcurrent()),
		},
	}
}

// daemonGuards is the daemon's heavy-command and disk-guard policy. A disk
// threshold it cannot read is logged and keeps its default — a misconfigured
// guard never stops the daemon (unlike every other DaemonConfig value).
func daemonGuards(cfg *config.DaemonConfig) daemonruntime.Guards {
	disk, err := diskguard.ParseThresholds(cfg.GetDiskReclaimAt(), cfg.GetDiskRefuseAt())
	if err != nil {
		logging.Warn("Disk guard keeps its default thresholds", "error", err)
	}
	// heavy_commands defaults to heavyjob.DefaultCommands, so an empty list
	// after loading was set empty on purpose: nothing is heavy by pattern.
	// The getter returns nil for it, which the gate would read as the
	// default, so hand it a non-nil empty slice.
	commands := cfg.GetHeavyCommands()
	if commands == nil {
		commands = []string{}
	}
	return daemonruntime.Guards{Disk: disk, HeavyCommands: commands}
}
