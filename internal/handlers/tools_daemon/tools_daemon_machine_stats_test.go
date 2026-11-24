// Copyright (c) 2026 Reliant Labs
package tools_daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
)

// machineStatsLine is one captured log line.
type machineStatsLine struct {
	msg   string
	attrs map[string]any
}

type machineStatsHarness struct {
	// ctx is what the stream's context carries (authz membership).
	ctx    context.Context
	svc    *Service
	conn   *daemonConnection
	now    time.Time
	lines  []machineStatsLine
	labels map[string]string
	// labelErr, when set, is what GetDaemon fails with.
	labelErr error
	reads    int
}

func newMachineStatsHarness(t *testing.T) *machineStatsHarness {
	h := &machineStatsHarness{
		ctx:    context.Background(),
		now:    time.Date(2026, 11, 20, 6, 0, 0, 0, time.UTC),
		labels: map[string]string{"monitor": "platform"},
	}
	h.svc = newTestService(&MockRepository{
		GetDaemonFunc: func(_ context.Context, id string) (*db.Daemon, error) {
			h.reads++
			if h.labelErr != nil {
				return nil, h.labelErr
			}
			return &db.Daemon{ID: id, Labels: h.labels}, nil
		},
	})
	t.Cleanup(h.svc.Close)
	h.svc.deps.MachineStats = MachineStatsSelection{Orgs: []string{"org-platform"}, Label: "monitor=platform"}
	h.svc.now = func() time.Time { return h.now }
	h.svc.logMachineStats = func(_ context.Context, msg string, args ...any) {
		m := map[string]any{}
		for i := 0; i+1 < len(args); i += 2 {
			m[args[i].(string)] = args[i+1]
		}
		h.lines = append(h.lines, machineStatsLine{msg: msg, attrs: m})
	}
	h.conn = &daemonConnection{daemonID: "d-cicd", userID: "u-1", orgID: "org-platform", name: "cicd", daemonType: "local"}
	return h
}

func load(v float64) *float64 { return &v }

// heartbeat sends one heartbeat through the inbound state worker's handler,
// as the receive loop would, and returns the lines it logged.
func (h *machineStatsHarness) heartbeat(hb *reliantv1.DaemonHeartbeat) []machineStatsLine {
	before := len(h.lines)
	h.svc.applyInboundState(h.ctx, h.conn,
		&reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_Heartbeat{Heartbeat: hb}}, make(chan struct{}))
	return h.lines[before:]
}

func fullHeartbeat() *reliantv1.DaemonHeartbeat {
	return &reliantv1.DaemonHeartbeat{
		MachineStats: &reliantv1.MachineStats{
			Load1: load(96), Load5: load(72), Load15: load(48), Cpus: 48,
			MemTotalBytes: 128 << 30, MemAvailableBytes: 64 << 30,
		},
		Disk: &reliantv1.DiskReport{
			Volumes: []*reliantv1.DiskVolume{
				{Label: "home", Paths: []string{"/home/ubuntu"}, TotalBytes: 878 << 30, FreeBytes: 175 << 30},
				{Label: "temp", Paths: []string{"/tmp"}, TotalBytes: 100, FreeBytes: 90},
			},
			Processes: &reliantv1.ProcessReport{Count: 2000, Limit: 100000},
		},
	}
}

// A selected machine — in a watched org, self-hosted, carrying the label —
// logs one machine health line and one machine volume line per volume on its
// first heartbeat, then at most once a minute.
func TestMachineStatsLinesForASelectedMachine(t *testing.T) {
	h := newMachineStatsHarness(t)

	lines := h.heartbeat(fullHeartbeat())
	require.Len(t, lines, 3)
	assert.Equal(t, "machine health", lines[0].msg)
	health := lines[0].attrs
	assert.Equal(t, "heartbeat", health["machine_source"])
	assert.Equal(t, "cicd", health["machine"])
	assert.Equal(t, "d-cicd", health["daemon_id"])
	assert.Equal(t, "org-platform", health["org_id"])
	assert.Equal(t, 2.0, health["load1_per_cpu"])
	assert.Equal(t, 1.5, health["load5_per_cpu"])
	assert.Equal(t, 0.5, health["mem_available_ratio"])
	assert.Equal(t, uint64(2000), health["processes"])
	assert.Equal(t, "home", health["disk_min_free_volume"])
	assert.Equal(t, "machine volume", lines[1].msg)
	assert.Equal(t, "home", lines[1].attrs["volume"])
	assert.Equal(t, 0.199, lines[1].attrs["free_ratio"])
	assert.Equal(t, "cicd", lines[1].attrs["machine"])
	assert.Equal(t, "machine volume", lines[2].msg)
	assert.Equal(t, "temp", lines[2].attrs["volume"])

	// Heartbeats 15s apart: nothing until a minute has passed.
	for i := 0; i < 3; i++ {
		h.now = h.now.Add(15 * time.Second)
		assert.Empty(t, h.heartbeat(fullHeartbeat()), "rate-limited at %s", h.now)
	}
	h.now = h.now.Add(15 * time.Second)
	assert.Len(t, h.heartbeat(fullHeartbeat()), 3, "a minute after the last lines")
	assert.Equal(t, 1, h.reads, "labels are read at most every five minutes")
}

// A daemon that predates machine stats still logs its identity, disk and
// processes, so the machine is not reported as silent.
func TestMachineStatsLinesForAnOldDaemon(t *testing.T) {
	h := newMachineStatsHarness(t)
	hb := fullHeartbeat()
	hb.MachineStats = nil
	lines := h.heartbeat(hb)
	require.Len(t, lines, 3)
	assert.Equal(t, "cicd", lines[0].attrs["machine"])
	assert.NotContains(t, lines[0].attrs, "load1")
	assert.NotContains(t, lines[0].attrs, "mem_available_ratio")
	assert.Equal(t, uint64(2000), lines[0].attrs["processes"])
}

// Nothing is logged for a machine that is not selected.
func TestMachineStatsLinesOnlyForSelectedMachines(t *testing.T) {
	for name, setup := range map[string]func(h *machineStatsHarness){
		"feature off":        func(h *machineStatsHarness) { h.svc.deps.MachineStats.Orgs = nil },
		"another org":        func(h *machineStatsHarness) { h.conn.orgID = "org-customer" },
		"org unresolved":     func(h *machineStatsHarness) { h.conn.orgUnresolved = true },
		"no label":           func(h *machineStatsHarness) { h.labels = map[string]string{"pool": "ci"} },
		"label, other value": func(h *machineStatsHarness) { h.labels = map[string]string{"monitor": "off"} },
		"managed workspace":  func(h *machineStatsHarness) { h.conn.daemonType = "cloud" },
	} {
		t.Run(name, func(t *testing.T) {
			h := newMachineStatsHarness(t)
			setup(h)
			assert.Empty(t, h.heartbeat(fullHeartbeat()))
		})
	}
}

// A machine with no known org — paired with an unbound credential, as the
// platform's pipeline boxes are — is selected in the org it acts in: its
// owner's default org. Before, it was never selected, and prod wrote no
// line for cicd or ovh-build-1.
func TestMachineStatsLinesForAMachineWithNoKnownOrg(t *testing.T) {
	h := newMachineStatsHarness(t)
	h.conn.orgID = ""
	h.ctx = authz.WithMembership(context.Background(), defaultOrgMembership{def: "org-platform"})
	lines := h.heartbeat(fullHeartbeat())
	require.Len(t, lines, 3)
	assert.Equal(t, "org-platform", lines[0].attrs["org_id"], "the line names the org it was selected in")
	assert.Equal(t, "org-platform", lines[1].attrs["org_id"])

	for name, ctx := range map[string]context.Context{
		"owner's default org not watched": authz.WithMembership(context.Background(), defaultOrgMembership{def: "org-customer"}),
		"no membership wired":             context.Background(),
	} {
		t.Run(name, func(t *testing.T) {
			h := newMachineStatsHarness(t)
			h.conn.orgID = ""
			h.ctx = ctx
			assert.Empty(t, h.heartbeat(fullHeartbeat()))
		})
	}
}

// A failed read of the owner's default org selects nothing and is retried a
// minute later, like a failed label read.
func TestMachineStatsDefaultOrgReadError(t *testing.T) {
	h := newMachineStatsHarness(t)
	h.conn.orgID = ""
	m := &failingDefaultOrg{err: errors.New("membership down")}
	h.ctx = authz.WithMembership(context.Background(), m)
	assert.Empty(t, h.heartbeat(fullHeartbeat()))
	m.err = nil
	h.now = h.now.Add(30 * time.Second)
	assert.Empty(t, h.heartbeat(fullHeartbeat()), "retried after a minute, not every heartbeat")
	h.now = h.now.Add(30 * time.Second)
	assert.Len(t, h.heartbeat(fullHeartbeat()), 3)
}

type failingDefaultOrg struct{ err error }

func (*failingDefaultOrg) IsMember(context.Context, string, string) (bool, bool, error) {
	return true, true, nil
}

func (m *failingDefaultOrg) DefaultOrg(context.Context, string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return "org-platform", nil
}

// A label added while the machine is connected takes effect within five
// minutes; a label read that fails selects nothing and is retried a minute
// later.
func TestMachineStatsLabelRefreshAndReadErrors(t *testing.T) {
	h := newMachineStatsHarness(t)
	h.labels = nil
	assert.Empty(t, h.heartbeat(fullHeartbeat()))
	h.labels = map[string]string{"monitor": "platform"}
	h.now = h.now.Add(4 * time.Minute)
	assert.Empty(t, h.heartbeat(fullHeartbeat()), "the read is trusted for five minutes")
	h.now = h.now.Add(time.Minute)
	assert.Len(t, h.heartbeat(fullHeartbeat()), 3)

	h = newMachineStatsHarness(t)
	h.labelErr = errors.New("db down")
	assert.Empty(t, h.heartbeat(fullHeartbeat()))
	h.labelErr = nil
	h.now = h.now.Add(30 * time.Second)
	assert.Empty(t, h.heartbeat(fullHeartbeat()), "retried after a minute, not every heartbeat")
	h.now = h.now.Add(30 * time.Second)
	assert.Len(t, h.heartbeat(fullHeartbeat()), 3)
	assert.Equal(t, 2, h.reads)
}

func TestMachineStatsSelectionMatchesLabel(t *testing.T) {
	labels := map[string]string{"monitor": "platform", "pool": "ci"}
	assert.True(t, MachineStatsSelection{}.matchesLabel(labels), "empty is monitor=platform")
	assert.True(t, MachineStatsSelection{Label: "pool=ci"}.matchesLabel(labels))
	assert.True(t, MachineStatsSelection{Label: "pool"}.matchesLabel(labels), "a bare key matches any value")
	assert.False(t, MachineStatsSelection{Label: "pool=build"}.matchesLabel(labels))
	assert.False(t, MachineStatsSelection{Label: "monitor=platform"}.matchesLabel(nil))
}
