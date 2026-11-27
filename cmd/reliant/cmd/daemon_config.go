// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/internal/diskguard"
	"github.com/reliant-labs/reliant/internal/egresslimit"
	"github.com/reliant-labs/reliant/internal/heavyjob"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/machinepool"
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
	// labels is not checked here: --label replaces it, so only the list in
	// effect is checked, by daemonMachineLabels.
	return cfg, nil
}

// labelsFlag is a repeatable key=value flag (`--label pool=pipeline --label
// gate=yes`, or `--label pool=pipeline,gate=yes`). Each use is checked as it
// is parsed, so a bad label is refused before the command runs.
type labelsFlag struct {
	name string
	args []string
}

func (f *labelsFlag) Set(s string) error {
	if _, err := machinepool.ParseLabels([]string{s}, "--"+f.name); err != nil {
		return err
	}
	f.args = append(f.args, s)
	return nil
}

func (f *labelsFlag) String() string { return strings.Join(f.args, ",") }

func (f *labelsFlag) Type() string { return "key=value" }

// labels is every use of the flag as one label map (a key given twice takes
// its last value); nil when it was not used.
func (f *labelsFlag) labels() (map[string]string, error) {
	return machinepool.ParseLabels(f.args, "--"+f.name)
}

// daemonMachineLabels are the durable labels `daemon start` declares for its
// machine: --label when given (it replaces the config's list, as a flag
// overrides its config value), else DaemonConfig.labels. Only the list in
// effect is checked, so a bad config list that --label replaces does not stop
// the daemon; a bad list in effect does, before the daemon does anything.
func daemonMachineLabels(cmd *cobra.Command, cfg *config.DaemonConfig, flag *labelsFlag) (map[string]string, error) {
	if cmd.Flags().Changed(flag.name) {
		return flag.labels()
	}
	labels, err := machinepool.ParseLabels(cfg.GetLabels(), "labels")
	if err != nil {
		return nil, fmt.Errorf("config: RELIANT_DAEMON_LABELS (labels): %w", err)
	}
	return labels, nil
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
		GateImage:     cfg.GetGateImage(),
		GateVulnDBDir: cfg.GetGateVulnDbDir(),
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
	// background_idle_reap defaults to 4h, so a zero after loading was set
	// on purpose: off. The runtime reads zero as its default, negative as off.
	idleReap := cfg.GetBackgroundIdleReap().AsDuration()
	if idleReap == 0 {
		idleReap = -1
	}
	// Like a disk threshold, a memory bound it cannot read is logged and
	// keeps its default.
	memoryHigh, err := heavyjob.ParseMemoryHigh(cfg.GetHeavyMemoryHigh())
	if err != nil {
		logging.Warn("Heavy commands keep the default memory bound", "setting", "heavy_memory_high (RELIANT_HEAVY_MEMORY_HIGH)", "error", err)
	}
	return daemonruntime.Guards{
		Disk:               disk,
		HeavyCommands:      commands,
		HeavyShare:         int(cfg.GetHeavyShare()),
		HeavyCPUWeight:     int(cfg.GetHeavyCpuWeight()),
		HeavyMemoryHigh:    memoryHigh,
		CommandThreads:     int(cfg.GetCommandThreads()),
		BackgroundIdleReap: idleReap,
	}
}
