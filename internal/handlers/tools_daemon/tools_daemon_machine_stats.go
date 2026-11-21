// Copyright (c) 2026 Reliant Labs
package tools_daemon

import (
	"context"
	"slices"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/machinehealth"
)

// A self-hosted machine ships no logs anywhere, but its heartbeat carries
// its load, memory and disk. For the machines the platform runs itself (the
// pipeline boxes), the gateway — whose own logs land in tenant zero — writes
// those as the same `machine health` / `machine volume` lines a managed
// machine logs, with machine_source=heartbeat under ServiceName
// daemon-gateway. control-plane's heartbeat rules (MachineDiskFull,
// MachineDiskFilling, MachineMemoryLow, MachineLoadSustained,
// MachineNotReporting) and the machine-health dashboard read exactly those;
// the managed-workspace rules (ws-* services) do not (internal/machinehealth).

const (
	// machineStatsInterval is how often a watched machine's lines are logged.
	machineStatsInterval = time.Minute
	// machineStatsLabelRefresh is how long a read of the machine's durable
	// labels is trusted: a label added or removed takes effect within it.
	machineStatsLabelRefresh = 5 * time.Minute
	// machineStatsLabelRetry is how soon a label read that failed is retried.
	machineStatsLabelRetry = time.Minute
	// defaultMachineStatsLabel is MACHINE_STATS_LABEL's default, used when
	// the selection names none.
	defaultMachineStatsLabel = "monitor=platform"
)

// MachineStatsSelection picks the self-hosted machines whose heartbeat stats
// the gateway logs (GatewayConfig machine_stats_orgs / machine_stats_label).
// A machine is selected when it belongs to one of Orgs, is not a managed
// workspace (its pod's own lines already reach tenant zero) and carries Label
// among its durable labels. No Orgs selects none.
type MachineStatsSelection struct {
	Orgs []string
	// Label is key=value; a bare key matches any value. Empty is
	// monitor=platform.
	Label string
}

// matchesLabel reports whether labels carry the selection's label.
func (sel MachineStatsSelection) matchesLabel(labels map[string]string) bool {
	label := strings.TrimSpace(sel.Label)
	if label == "" {
		label = defaultMachineStatsLabel
	}
	key, want, hasValue := strings.Cut(label, "=")
	got, ok := labels[strings.TrimSpace(key)]
	if !ok {
		return false
	}
	return !hasValue || got == strings.TrimSpace(want)
}

// machineStatsState is one connection's machine-stats bookkeeping. Only the
// inbound state worker touches it.
type machineStatsState struct {
	// labelled is whether the machine carried the label at the last read.
	labelled bool
	// labelsNext is when the labels are read again; zero before the first.
	labelsNext time.Time
	// labelErrLogged: a failed label read has been logged on this connection.
	labelErrLogged bool
	// loggedAt is when this connection's lines were last logged.
	loggedAt time.Time
}

// logMachineStatsLines logs hb's machine health line, and one machine volume
// line per sized volume, when conn's machine is selected and a minute has
// passed since its last lines (its first heartbeat logs at once). A heartbeat
// from a daemon that sends no machine stats still logs what it has: its
// identity, disk and process count.
func (s *Service) logMachineStatsLines(ctx context.Context, conn *daemonConnection, hb *reliantv1.DaemonHeartbeat, now time.Time) {
	sel := s.deps.MachineStats
	if hb == nil || len(sel.Orgs) == 0 || conn.daemonType == "cloud" ||
		conn.orgUnresolved || conn.orgID == "" || !slices.Contains(sel.Orgs, conn.orgID) {
		return
	}
	st := &conn.machineStats
	if !st.loggedAt.IsZero() && now.Sub(st.loggedAt) < machineStatsInterval {
		return
	}
	if !s.machineStatsLabelled(ctx, conn, now) {
		return
	}
	st.loggedAt = now

	name := conn.name
	if name == "" {
		name = conn.hostname
	}
	health, volumes := machinehealth.HeartbeatLines(machinehealth.Identity{
		Machine: name, DaemonID: conn.daemonID, OrgID: conn.orgID,
	}, hb.GetMachineStats(), hb.GetDisk(), now)
	s.logMachineStats(ctx, machinehealth.HealthMessage, health...)
	for _, v := range volumes {
		s.logMachineStats(ctx, machinehealth.VolumeMessage, v...)
	}
}

// machineStatsLabelled reports whether conn's machine carries the selection
// label, reading its durable labels (the daemon row: they hold while the
// machine sleeps, unlike the ones it registers with) at most every
// machineStatsLabelRefresh. A read that fails leaves the machine unselected
// until a retry a minute later, and is logged once per connection.
func (s *Service) machineStatsLabelled(ctx context.Context, conn *daemonConnection, now time.Time) bool {
	st := &conn.machineStats
	if !st.labelsNext.IsZero() && now.Before(st.labelsNext) {
		return st.labelled
	}
	d, err := s.deps.Repo.GetDaemon(ctx, conn.daemonID)
	if err != nil {
		if !st.labelErrLogged {
			st.labelErrLogged = true
			logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Failed to read machine labels for machine stats; retrying",
				"error", err, "daemonID", conn.daemonID)
		}
		st.labelled, st.labelsNext = false, now.Add(machineStatsLabelRetry)
		return false
	}
	st.labelled = d != nil && s.deps.MachineStats.matchesLabel(d.Labels)
	st.labelsNext = now.Add(machineStatsLabelRefresh)
	return st.labelled
}
