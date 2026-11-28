// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/reliant-labs/forge/pkg/hostslot"

	"github.com/reliant-labs/reliant/internal/diskguard"
	"github.com/reliant-labs/reliant/internal/diskwatch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/machinehealth"
	"github.com/reliant-labs/reliant/internal/machinepreflight"
)

func init() {
	RegisterCommand(machinepreflight.Command, handleMachinePreflight)
}

// preflightDiskGuard is the daemon's disk guard, whose refusal of heavy work
// fails a node's preflight (SetPreflightDiskGuard). Nil until the daemon
// built one: the refusal check is then skipped.
var preflightDiskGuard atomic.Pointer[diskguard.Guard]

// SetPreflightDiskGuard sets the disk guard machine.preflight reads. Safe for
// concurrent use.
func SetPreflightDiskGuard(g *diskguard.Guard) {
	preflightDiskGuard.Store(g)
}

// preflightEnv is the machine machine.preflight checks; a test replaces it.
var preflightEnv = systemPreflightEnv

func systemPreflightEnv() machinepreflight.Env {
	var refusal func() string
	if g := preflightDiskGuard.Load(); g != nil {
		refusal = g.HeavyRefusal
	}
	return machinepreflight.SystemEnv(
		func(path string) (machinepreflight.Disk, error) {
			u, err := diskwatch.StatUsage(path)
			return machinepreflight.Disk{FreeBytes: u.FreeBytes, TotalBytes: u.TotalBytes}, err
		},
		refusal,
		func() (float64, bool) { return machinehealth.Load5("/proc") },
		hostslot.DetectMachine().CPUs,
	)
}

// handleMachinePreflight answers machinepreflight.Command: the node's machine
// self-check, run before the node's work starts. A failed check is an answer
// (Result.OK false, with reasons), never an error: the error path is for a
// request this daemon cannot read.
func handleMachinePreflight(ctx context.Context, payload []byte) ([]byte, error) {
	var req machinepreflight.Request
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid %s payload: %w", machinepreflight.Command, err)
	}
	res := machinepreflight.Check(preflightEnv(), req.Spec)
	if !res.OK {
		// The machine-health signal on the machine's own log: the gateway's
		// and the worker's records say which run it failed.
		p := res.Primary()
		logging.WarnContext(ctx, "machine preflight failed", "reason", string(p.Reason), "failures", len(res.Failures), "message", res.Message())
	}
	return json.Marshal(res)
}
