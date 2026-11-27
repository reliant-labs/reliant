// Copyright (c) 2025 Reliant Labs
package gaterun

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/pipeline/config"
)

// The daemon commands the Dispatcher sends. Start and status are idempotent
// by gate id, so the daemon router may send either again after a dropped
// connection (toolexec reissuableDaemonCommands lists them).
const (
	CommandStart  = "pipeline.gate.start"
	CommandStatus = "pipeline.gate.status"
	CommandCancel = "pipeline.gate.cancel"
)

// PlanStage is one resolved stage as the daemon runs it.
type PlanStage struct {
	Name  config.StageName  `json:"name"`
	State config.StageState `json:"state"`
	Kind  config.StageKind  `json:"kind,omitempty"`
	// Command is the stage's shell command (Kind command).
	Command string `json:"command,omitempty"`
	// ScanEnv is the forge env whose vuln_gate judges a builtin scan; ""
	// is forge's default gate.
	ScanEnv string `json:"scan_env,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// Planned is whether the stage runs at all.
func (s PlanStage) Planned() bool { return s.State == config.StageOn }

// PlanFromGate turns a resolved gate into the four stages the daemon runs,
// in [config.StageNames] order. Runner checks runs no stage on a machine, so
// it is refused.
func PlanFromGate(g config.ResolvedGate) ([]PlanStage, error) {
	if g.Runner == config.RunnerChecks {
		return nil, fmt.Errorf("%w: runner checks runs no stage on a machine", ErrInvalidRequest)
	}
	out := make([]PlanStage, 0, len(config.StageNames))
	for _, n := range config.StageNames {
		s := g.Stage(n)
		p := PlanStage{Name: n, State: s.State, Kind: s.Kind, Command: s.Command, Reason: s.Reason}
		if p.State == config.StageOn {
			switch {
			case p.Kind == config.StageCommand && p.Command != "":
			case p.Kind == config.StageBuiltinScan || (p.Kind == "" && n == config.StageSBOMScan):
				p.Kind, p.Command = config.StageBuiltinScan, ""
				if s.Scan != nil && s.Scan.Policy == config.ScanPolicyVulnGate {
					p.ScanEnv = s.Scan.Env
				}
			default:
				p = PlanStage{Name: n, State: config.StageUnavailable, Reason: "no command was resolved for this stage"}
			}
		} else {
			p.Kind, p.Command = "", ""
		}
		out = append(out, p)
	}
	return out, nil
}

// validatePlan checks a plan received over the wire.
func validatePlan(stages []PlanStage) error {
	if len(stages) != len(config.StageNames) {
		return fmt.Errorf("%w: %d stages, want %d", ErrInvalidRequest, len(stages), len(config.StageNames))
	}
	for i, s := range stages {
		if s.Name != config.StageNames[i] {
			return fmt.Errorf("%w: stage %d is %q, want %q", ErrInvalidRequest, i, s.Name, config.StageNames[i])
		}
		switch s.State {
		case config.StageOff, config.StageUnavailable:
		case config.StageOn:
			if s.Kind == config.StageCommand && s.Command == "" {
				return fmt.Errorf("%w: stage %s has no command", ErrInvalidRequest, s.Name)
			}
			if s.Kind != config.StageCommand && (s.Kind != config.StageBuiltinScan || s.Name != config.StageSBOMScan) {
				return fmt.Errorf("%w: stage %s has kind %q", ErrInvalidRequest, s.Name, s.Kind)
			}
			if s.ScanEnv != "" && !scanEnvPattern.MatchString(s.ScanEnv) {
				return fmt.Errorf("%w: scan env %q", ErrInvalidRequest, s.ScanEnv)
			}
		default:
			return fmt.Errorf("%w: stage %s state %q", ErrInvalidRequest, s.Name, s.State)
		}
	}
	return nil
}

var scanEnvPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// JobState is a gate's state on its daemon.
type JobState string

const (
	JobRunning JobState = "running"
	JobDone    JobState = "done"
	// JobBusy: the member runs all the gates it takes; try another.
	JobBusy JobState = "busy"
	// JobRefused: the member cannot run this gate (no isolated backend).
	JobRefused JobState = "refused"
	// JobUnknown: the member does not know the gate id (it restarted, or the
	// result was pruned).
	JobUnknown JobState = "unknown"
)

// StartRequest is CommandStart's payload.
type StartRequest struct {
	GateID string `json:"gate_id"`
	// Repo is "owner/name"; the daemon fetches Ref from GitHub.
	Repo string `json:"repo"`
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	// BaseSHA is the batch's base: the main commit the candidate is built
	// on. The daemon fetches it beside the candidate, so `git merge-base`
	// and a diff against it work in the gate's checkout, and the gate's
	// environment names it (EnvGateBase, EnvCIBase). "" (a server that does
	// not send it, or a gate of main's own tip) fetches the candidate alone.
	// A daemon that predates the field ignores it.
	BaseSHA string            `json:"base_sha,omitempty"`
	Stages  []PlanStage       `json:"stages"`
	Env     map[string]string `json:"env,omitempty"`
	// TimeoutMs bounds the gate inside the boundary.
	TimeoutMs int64 `json:"timeout_ms"`
	// AllowNotIsolated lets the member fall back to runc. The Dispatcher
	// sets it only for a shadow gate of a repository in shadow mode.
	AllowNotIsolated bool `json:"allow_not_isolated,omitempty"`
	// WarmCache gives the gate the repository's warm cache volume
	// (Spec.CacheVolume). Off by default. It takes effect only together with
	// AllowNotIsolated, and only when the gate runs on the not-isolated runc
	// backend. The Dispatcher sets it only for a repository that set
	// gate.allow_unisolated in its pipeline.yaml. A shared cache lets one
	// gate's code poison a later gate's build, and the opt-in is the
	// repository accepting that until sysbox gate machines exist. A dedicated
	// sysbox gate machine, with a nested engine and isolation per gate, closes
	// this gap and the no-docker gap together.
	WarmCache bool `json:"warm_cache,omitempty"`
	// MaxConcurrent is how many gates the member runs at once (the pool's
	// max_runs_per_member); 0 is 1.
	MaxConcurrent int `json:"max_concurrent,omitempty"`
}

// Timeout is the request's wall-time bound.
func (r StartRequest) Timeout() time.Duration { return time.Duration(r.TimeoutMs) * time.Millisecond }

var (
	gateIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)
	repoPattern   = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)
	refPattern    = regexp.MustCompile(`^reliant-queue/[A-Za-z0-9_.-]{1,100}$`)
	shaPattern    = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// Validate checks a StartRequest a daemon received.
func (r StartRequest) Validate() error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalidRequest, fmt.Sprintf(format, a...))
	}
	switch {
	case !gateIDPattern.MatchString(r.GateID):
		return bad("gate id %q", r.GateID)
	case !repoPattern.MatchString(r.Repo):
		return bad("repo %q is not owner/name", r.Repo)
	case !refPattern.MatchString(r.Ref):
		return bad("ref %q is not reliant-queue/<batch>", r.Ref)
	case !shaPattern.MatchString(r.SHA):
		return bad("sha %q is not a full commit sha", r.SHA)
	case r.BaseSHA != "" && !shaPattern.MatchString(r.BaseSHA):
		return bad("base sha %q is not a full commit sha", r.BaseSHA)
	case r.TimeoutMs <= 0:
		return bad("no timeout")
	}
	if err := validatePlan(r.Stages); err != nil {
		return err
	}
	_, err := GateEnv(r.GateID, r.EffectiveEnv())
	return err
}

// EffectiveEnv is the request's gate.env with the base commit's variables over
// it: when the request names a base, the batch's real base wins over any
// value pipeline.yaml's gate.env sets for them.
func (r StartRequest) EffectiveEnv() map[string]string {
	if r.BaseSHA == "" {
		return r.Env
	}
	env := make(map[string]string, len(r.Env)+2)
	for k, v := range r.Env {
		env[k] = v
	}
	env[EnvGateBase] = r.BaseSHA
	env[EnvCIBase] = r.BaseSHA
	return env
}

// StartReply is CommandStart's answer.
type StartReply struct {
	State     JobState  `json:"state"`
	Isolation Isolation `json:"isolation,omitempty"`
	Reason    string    `json:"reason,omitempty"`
}

// StatusRequest is CommandStatus's (and CommandCancel's) payload.
type StatusRequest struct {
	GateID string `json:"gate_id"`
}

// StatusReply is CommandStatus's answer; Result is set when State is done.
type StatusReply struct {
	State  JobState    `json:"state"`
	Result *GateResult `json:"result,omitempty"`
}

// GateID is the gate id of a batch's attempt: "gate-<batch>-<attempt>",
// lowercased to the id alphabet.
func GateID(batchID string, attempt int) string {
	b := make([]byte, 0, len(batchID))
	for _, c := range []byte(batchID) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
			b = append(b, c)
		case c >= 'A' && c <= 'Z':
			b = append(b, c+'a'-'A')
		default:
			b = append(b, '-')
		}
	}
	id := "gate-" + string(b)
	suffix := "-" + strconv.Itoa(max(attempt, 1))
	if len(id)+len(suffix) > 63 {
		id = id[:63-len(suffix)]
	}
	return id + suffix
}

// The gate's environment, before the repository's gate.env.
var baseEnv = map[string]string{
	"PATH":   "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	"HOME":   HomeDir,
	"TMPDIR": TmpDir,
	"LANG":   "C.UTF-8",
	"TZ":     "UTC",
	"CI":     "true",
}

// EnvGateRoot and EnvVulnDB tell the stage script where the boundary's work
// volume and database are. A backend may move them (the tests' fake runs the
// script in a temporary directory).
const (
	EnvGateRoot = "RELIANT_GATE_ROOT"
	EnvVulnDB   = "RELIANT_GATE_VULNDB"
)

// EnvDockerSkips is the skip log of the stage that is running. The stage
// script sets it to a file named for the stage and the run. A test that
// cannot get the docker engine it needs appends one line to the file and
// skips with [DockerSkipMessage]. The count of those lines becomes
// StageResult.SkippedDockerTests, so a skipped container test is counted in
// the verdict and never passes silently. The path holds the run id, so Go's
// test cache never replays a skip that it did not record (the cache keys on
// the values of the variables a test reads).
//
// Gates have no docker engine: runc gates run with no nested engine, and
// the GitHub merge queue on cicd has none either (its runner users are not
// in the docker group). A dedicated sysbox gate machine, with a nested engine
// per gate, closes this gap.
const EnvDockerSkips = "RELIANT_GATE_DOCKER_SKIPS"

// EnvGateBase and EnvCIBase name the batch's base commit (StartRequest
// BaseSHA) in a gate that has one. The gate's checkout has no origin/main:
// a repository's affected-only lanes diff against this commit instead. Both
// names are set because repositories read either: reliant's scripts/gate.sh
// reads GATE_BASE; forge's ci commands and control-plane's gate read CI_BASE.
const (
	EnvGateBase = "GATE_BASE"
	EnvCIBase   = "CI_BASE"
)

// DockerSkipMessage is how a container test reports a skip in a gate.
const DockerSkipMessage = "skip: docker unavailable (gate)"

// reservedEnv are the names gate.env may not set.
var reservedEnv = map[string]bool{
	"HOME": true, "TMPDIR": true, "DOCKER_HOST": true,
	"RELIANT_GATE": true, "RELIANT_RUN_ID": true, EnvGateRoot: true, EnvVulnDB: true,
	"RELIANT_GATE_STAGE": true, "RELIANT_GATE_ARTIFACTS": true, EnvDockerSkips: true,
}

// CacheVolumeName is repo's warm cache volume: "reliant-gate-cache-", repo
// ("owner/name") lowercased with '/' as '-' for a person to read, then "-"
// and the first 12 hex digits of the SHA-256 of the lowercased repo. The
// readable part alone is not one-to-one ("a/b-c" and "a-b/c" read alike),
// so the hash is what keeps two repositories from sharing one cache. Case
// is folded first because GitHub names are case-insensitive. repo must be a
// valid StartRequest repo.
func CacheVolumeName(repo string) string {
	lower := strings.ToLower(repo)
	sum := sha256.Sum256([]byte(lower))
	return "reliant-gate-cache-" + strings.ReplaceAll(lower, "/", "-") + "-" + hex.EncodeToString(sum[:6])
}

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// GateEnv is the gate's whole environment as sorted KEY=VALUE entries: the
// base, the run's identity and layout, then gateEnv. Nothing comes from this
// process's environment.
func GateEnv(runID string, gateEnv map[string]string) ([]string, error) {
	merged := make(map[string]string, len(baseEnv)+len(gateEnv)+5)
	for k, v := range baseEnv {
		merged[k] = v
	}
	merged["RELIANT_GATE"] = "1"
	merged["RELIANT_RUN_ID"] = runID
	merged[EnvGateRoot] = WorkMount
	merged[EnvVulnDB] = VulnDBMount
	merged["RELIANT_GATE_ARTIFACTS"] = ArtifactsDir
	for k, v := range gateEnv {
		switch {
		case !envNamePattern.MatchString(k):
			return nil, fmt.Errorf("%w: gate.env name %q is not a variable name", ErrInvalidRequest, k)
		case reservedEnv[k]:
			return nil, fmt.Errorf("%w: gate.env may not set %s", ErrInvalidRequest, k)
		}
		merged[k] = v
	}
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out, nil
}
