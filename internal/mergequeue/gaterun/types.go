// Copyright (c) 2025 Reliant Labs
package gaterun

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/reliant-labs/reliant/internal/pipeline/config"
)

// Class is a gate's verdict (pipeline redesign §3.4).
type Class string

const (
	ClassPass     Class = "pass"
	ClassFail     Class = "fail"
	ClassUnproven Class = "unproven"
)

// ExitCode is the gate exit contract's code: 0 pass, 1 fail, 2 unproven.
func (c Class) ExitCode() int {
	switch c {
	case ClassPass:
		return 0
	case ClassFail:
		return 1
	default:
		return 2
	}
}

// ClassifyExit maps an exit code onto the contract: 0 pass, 2 unproven, any
// other code fail.
func ClassifyExit(code int) Class {
	switch code {
	case 0:
		return ClassPass
	case 2:
		return ClassUnproven
	default:
		return ClassFail
	}
}

// Isolation is the backend a gate ran in, in queue_batches.isolation_backend
// terms.
type Isolation string

const (
	// IsolationSysbox is the sysbox-runc boundary (credsandbox gate backend,
	// E9a).
	IsolationSysbox Isolation = "sysbox"
	// IsolationVM is a per-gate microVM (planned, E9e).
	IsolationVM Isolation = "vm"
	// IsolationRunc is plain runc: contained (no writable host mounts, an
	// env allowlist, limits) but not isolated. Shadow gates only.
	IsolationRunc Isolation = "runc"
)

// Isolated reports whether a gate in i ran inside the isolation boundary
// pipeline redesign §3.3 requires.
func (i Isolation) Isolated() bool { return i == IsolationSysbox || i == IsolationVM }

// StageState is one stage's outcome in a gate result.
type StageState string

const (
	StagePass     StageState = "pass"
	StageWarn     StageState = "warn" // sbom_scan only: nothing blocks, a finding warns
	StageFail     StageState = "fail"
	StageUnproven StageState = "unproven"
	// StageOff: the repository turned the stage off (or forge's vuln_gate
	// mode is off). Shown as off, never as passed (D13).
	StageOff StageState = "off"
	// StageUnavailable: the default has nothing to run in this project.
	StageUnavailable StageState = "unavailable"
	// StageSkipped: an earlier stage did not pass, so this one never ran.
	StageSkipped StageState = "skipped"
)

// StageResult is one stage of a gate.
type StageResult struct {
	Name    config.StageName `json:"name"`
	Verdict StageState       `json:"verdict"`
	// Exit is the stage command's exit, -1 when it did not run.
	Exit     int           `json:"exit"`
	Duration time.Duration `json:"duration"`
	// Summary says why a stage is off, unavailable, skipped or not passed.
	Summary string `json:"summary,omitempty"`
	// LogTail is the end of the stage's output (at most logTailBytes). The
	// whole log stays in the boundary and goes with its teardown.
	LogTail string `json:"log_tail,omitempty"`
	// SkippedDockerTests counts the container tests that skipped in this
	// stage because the gate has no docker engine (EnvDockerSkips). Such a
	// test reports a skip, never a pass, and this count is how the verdict
	// shows it.
	SkippedDockerTests int `json:"skipped_docker_tests,omitempty"`
}

// ScanReport is the sbom_scan stage's forge document (`forge vuln gate
// --json`), kept as data for the release record.
type ScanReport struct {
	Verdict  string          `json:"verdict"`
	Document json.RawMessage `json:"document,omitempty"`
}

// Reason codes: why a gate is unproven when no stage decided it.
const (
	ReasonNoIsolatedMachine = "no_isolated_gate_machine"
	ReasonNoMember          = "no_gate_member"
	ReasonPoolBusy          = "gate_pool_busy"
	ReasonFetchFailed       = "fetch_failed"
	ReasonCandidateMoved    = "candidate_moved"
	ReasonBackendFailed     = "backend_failed"
	ReasonLimitExceeded     = "limit_exceeded"
	ReasonLost              = "gate_lost"
	ReasonResultInvalid     = "result_invalid"
	ReasonCanceled          = "canceled"
	// ReasonStage: a stage's own exit decided the verdict.
	ReasonStage = "stage"
)

// GateResult is a gate's verdict and everything report read.
type GateResult struct {
	GateID string `json:"gate_id"`
	// Machine is the pool member (daemon id) that ran it.
	Machine   string    `json:"machine,omitempty"`
	Isolation Isolation `json:"isolation,omitempty"`
	Class     Class     `json:"class"`
	// ReasonCode is one of the Reason* codes, Reason the words for a person.
	ReasonCode string `json:"reason_code,omitempty"`
	Reason     string `json:"reason,omitempty"`
	// Stages has every stage the gate was planned with, by name.
	Stages map[config.StageName]StageResult `json:"stages,omitempty"`
	Scan   *ScanReport                      `json:"scan,omitempty"`
	// FailingTests and InfraMatches come from the repository's own
	// .reliant/gate-result.json (`failing_tests`, `infra_matches`), and
	// InfraMatches also from gate.infra_patterns matched against the log tail
	// of the stage that did not pass.
	FailingTests []string `json:"failing_tests,omitempty"`
	InfraMatches []string `json:"infra_matches,omitempty"`
	// RepoResult is that file, raw: data only.
	RepoResult json.RawMessage `json:"repo_result,omitempty"`
	// SkippedDockerTests is the sum of the stages' SkippedDockerTests: the
	// container tests this gate did not run because it had no docker engine.
	SkippedDockerTests int           `json:"skipped_docker_tests,omitempty"`
	Duration           time.Duration `json:"duration"`
}

// StageList is Stages in the order the gate runs them.
func (r GateResult) StageList() []StageResult {
	var out []StageResult
	for _, n := range config.StageNames {
		if s, ok := r.Stages[n]; ok {
			out = append(out, s)
		}
	}
	return out
}

// ExitCode is the result's exit contract code.
func (r GateResult) ExitCode() int { return r.Class.ExitCode() }

// Landable is the hard rule the lander checks: a batch lands only on a pass
// produced inside an isolation boundary. A runc (not isolated) gate is never
// landable, whatever it says (pipeline redesign §3.3; owner, 2026-11-26).
func (r GateResult) Landable() bool { return r.Class == ClassPass && r.Isolation.Isolated() }

// Errors Dispatch returns when no gate verdict was reached. Every one means
// unproven: [ResultFromError] turns it into the GateResult to record.
var (
	// ErrInvalidRequest: the request cannot be gated as given.
	ErrInvalidRequest = errors.New("invalid gate request")
	// ErrNoIsolatedGateMachine: the member had no isolated backend that
	// passed Detect and Preflight (and the gate was not allowed runc).
	ErrNoIsolatedGateMachine = errors.New("unproven: no isolated gate machine")
	// ErrNoGateMember: the pool has no connected, eligible member.
	ErrNoGateMember = errors.New("unproven: no gate pool member is available")
	// ErrGatePoolBusy: every eligible member is running all the gates it
	// takes.
	ErrGatePoolBusy = errors.New("unproven: every gate pool member is busy")
	// ErrGateLost: the member stopped knowing the gate (it restarted), or
	// never answered before the deadline.
	ErrGateLost = errors.New("unproven: the gate was lost")
)

// ResultFromError is the unproven GateResult for a Dispatch error, so a
// caller always records a result.
func ResultFromError(gateID string, err error) GateResult {
	code := ReasonBackendFailed
	switch {
	case errors.Is(err, ErrNoIsolatedGateMachine):
		code = ReasonNoIsolatedMachine
	case errors.Is(err, ErrNoGateMember):
		code = ReasonNoMember
	case errors.Is(err, ErrGatePoolBusy):
		code = ReasonPoolBusy
	case errors.Is(err, ErrGateLost):
		code = ReasonLost
	case errors.Is(err, errCanceled), errors.Is(err, context.Canceled):
		code = ReasonCanceled
	}
	return GateResult{GateID: gateID, Class: ClassUnproven, ReasonCode: code, Reason: err.Error()}
}

var errCanceled = errors.New("gate canceled")
