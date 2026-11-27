package config

import (
	"regexp"
	"slices"
	"time"
)

// The §10.1 defaults. [Defaults] and [Config.Resolve] are the only readers;
// they are exported so the enrolment screen and docs can show them.
const (
	DefaultGatePool         = "gate"
	DefaultGateTimeout      = 60 * time.Minute
	DefaultBatchMax         = 4
	DefaultSpeculate        = 0
	DefaultRerunOnRed       = 1
	DefaultReviewMaxRounds  = 3
	DefaultFixAttempts      = 3
	DefaultQuarantineDays   = 7
	DefaultReleaseSchedule  = "every 2h if main moved"
	DefaultSweepSchedule    = "0 6 * * *"
	DefaultRecurringTimeout = 60 * time.Minute
	DefaultRecurringMaxPRs  = 1
	DefaultFlakePolicy      = FlakeQuarantine
	DefaultConsumerMode     = ConsumerAdvisory
	DefaultOnFail           = OnFailRollback
	DefaultEnvApproval      = EnvApprovalProjectSetting
	DefaultInconclusive     = InconclusiveProbes
	DefaultStrategy         = StrategyAllAtOnce
	DefaultRecurringOutput  = OutputReport
)

// Resolved is a validated pipeline.yaml with every default applied. Slices
// and maps are copies; a caller may keep or change them without touching the
// Config they came from.
type Resolved struct {
	Version int
	// Check is nil when the repository has no check tier.
	Check *Check
	Gate  ResolvedGate
	// Generated and Regen are empty when the repository declares none.
	Generated     []string
	Regen         string
	OwnerApproval []OwnerApproval
	Pins          []Pin
	// Consumers have Mode filled.
	Consumers []Consumer
	Queue     ResolvedQueue
	Review    ResolvedReview
	Fix       ResolvedFix
	Flake     ResolvedFlake
	// Release is nil when releases are off (the `release` key absent).
	Release   *ResolvedRelease
	Recurring []ResolvedRecurringJob
	Sweep     ResolvedSweep
	// Playbooks holds only the sources the file overrides; the responder
	// uses its built-in prompt for the rest.
	Playbooks map[IncidentSource]string
}

// ResolvedGate is the gate with Runner, Pool, Timeout and Stages filled.
type ResolvedGate struct {
	// Run is the test stage's command, kept for readers of the gate as one
	// command: the test stage's Command when it is on, else empty.
	Run    string
	Runner Runner
	// Stages is every built-in stage in the order the gate runs them
	// ([StageNames]): always four entries, each on, off or unavailable,
	// with its source. Runner checks runs none, so all four are off.
	Stages []ResolvedStage
	// Pool is empty for runner checks. Which machines are in a pool is the
	// org's state, not this file's: machines (the project's own, or cloud
	// machines, priced like any other machine) that passed the gate
	// preflight and carry the pool's label.
	Pool    string
	Timeout time.Duration
	Checks  []string
	Env     map[string]string
	// InfraPatterns are compiled from the file's infra_patterns. Patterns
	// in InfraPatternsFile are read from the repository by the caller
	// ([ParseInfraPatterns]) and added to these.
	InfraPatterns     []*regexp.Regexp
	InfraPatternsFile string
	// AllowUnisolated is gate.allow_unisolated: a pass outside an isolation
	// boundary may land.
	AllowUnisolated bool
}

type ResolvedQueue struct {
	BatchMax   int
	Speculate  int
	RerunOnRed int
	Preempt    bool
}

type ResolvedReview struct{ MaxRounds int }

type ResolvedFix struct{ AttemptsPerSignature int }

type ResolvedFlake struct {
	Policy         FlakePolicy
	QuarantineDays int
}

// ResolvedRelease is the release configuration with defaults applied.
type ResolvedRelease struct {
	Schedule Schedule
	OnLand   bool
	// Pool is empty for "any of the project's machines".
	Pool string
	// Envs is nil when the file names no chain: the caller derives it from
	// the project's forge HealthGate order (every env in deploy/kcl ordered
	// by HealthGate.after, else the only env).
	Envs   []ResolvedReleaseEnv
	OnFail OnFail
}

// ResolvedReleaseEnv is one chain step with defaults applied.
type ResolvedReleaseEnv struct {
	Name string
	// Soak is 0 for "the env's release_health after window", the floor a
	// soak never goes below.
	Soak time.Duration
	// Approval is project_setting unless the file said none or required.
	Approval EnvApproval
	// EarlyPass is 0 for no early pass.
	EarlyPass    time.Duration
	Inconclusive Inconclusive
	// Probes is never empty: the env's own, else every built-in probe.
	Probes   []Probe
	Strategy StrategyKind
}

// ResolvedRecurringJob is a recurring job with defaults applied.
type ResolvedRecurringJob struct {
	Name     string
	Schedule Schedule
	Prompt   string
	Run      string
	Output   RecurringOutput
	// MaxPRs is 0 unless Output is pr.
	MaxPRs  int
	Tools   []string
	Timeout time.Duration
}

// ResolvedSweep is the daily sweep with defaults applied.
type ResolvedSweep struct {
	Schedule Schedule
	Checks   []SweepCheck
}

// Defaults returns the configuration a file with nothing but `version` and
// `gate` resolves to, minus the gate command itself, which has no default
// ([DetectGate] proposes one), for a project whose kind is not known: its
// stages are [Project.DefaultStages] of the zero Project.
func Defaults() Resolved {
	sweepChecks := make([]SweepCheck, len(SweepBuiltins))
	for i, b := range SweepBuiltins {
		sweepChecks[i] = SweepCheck{Name: string(b)}
	}
	return Resolved{
		Version: SupportedVersion,
		Gate: ResolvedGate{
			Runner:  RunnerMachine,
			Pool:    DefaultGatePool,
			Timeout: DefaultGateTimeout,
			Stages:  Project{}.DefaultStages(),
		},
		Queue: ResolvedQueue{
			BatchMax:   DefaultBatchMax,
			Speculate:  DefaultSpeculate,
			RerunOnRed: DefaultRerunOnRed,
		},
		Review: ResolvedReview{MaxRounds: DefaultReviewMaxRounds},
		Fix:    ResolvedFix{AttemptsPerSignature: DefaultFixAttempts},
		Flake:  ResolvedFlake{Policy: DefaultFlakePolicy, QuarantineDays: DefaultQuarantineDays},
		Sweep: ResolvedSweep{
			Schedule: mustSchedule(DefaultSweepSchedule),
			Checks:   sweepChecks,
		},
	}
}

// DefaultProbes is the probes of an env that names none: every built-in.
func DefaultProbes() []Probe {
	out := make([]Probe, len(ProbeBuiltins))
	for i, b := range ProbeBuiltins {
		out[i] = Probe{Name: string(b)}
	}
	return out
}

// DefaultRelease is what `release: {}` resolves to.
func DefaultRelease() ResolvedRelease {
	return ResolvedRelease{
		Schedule: mustSchedule(DefaultReleaseSchedule),
		OnFail:   DefaultOnFail,
	}
}

// Resolve validates c and applies every default, for a project whose kind is
// not known: ResolveFor with the zero [Project]. Its lint and build stages
// are unavailable unless the file overrides them; a caller that has the
// repository's tree resolves with [Config.ResolveFor] and [DetectProject].
func (c *Config) Resolve() (Resolved, error) {
	return c.ResolveFor(Project{})
}

// ResolveFor validates c and applies every default, the gate stages' from
// p's kind and facts (see [Project.DefaultStages]).
func (c *Config) ResolveFor(p Project) (Resolved, error) {
	if err := c.Validate(); err != nil {
		return Resolved{}, err
	}
	r := Defaults()
	if c.Check != nil {
		r.Check = &Check{Run: c.Check.Run, Checks: slices.Clone(c.Check.Checks)}
	}
	if c.Release != nil && len(c.Release.Envs) > 0 {
		// The release chain's first env is the one the build stage builds
		// and whose vuln_gate judges the scan.
		p.ForgeEnv = c.Release.Envs[0].Name
	}
	r.Gate = resolveGate(c.Gate, r.Gate, p)
	r.Generated = slices.Clone(c.Generated)
	r.Regen = c.Regen
	r.OwnerApproval = make([]OwnerApproval, len(c.OwnerApproval))
	for i, a := range c.OwnerApproval {
		a.Paths = slices.Clone(a.Paths)
		r.OwnerApproval[i] = a
	}
	r.Pins = make([]Pin, len(c.Pins))
	for i, p := range c.Pins {
		p.LockstepWith = slices.Clone(p.LockstepWith)
		r.Pins[i] = p
	}
	r.Consumers = make([]Consumer, len(c.Consumers))
	for i, cs := range c.Consumers {
		cs.Mode = or(cs.Mode, DefaultConsumerMode)
		r.Consumers[i] = cs
	}
	if q := c.Queue; q != nil {
		r.Queue.BatchMax = deref(q.BatchMax, r.Queue.BatchMax)
		r.Queue.Speculate = deref(q.Speculate, r.Queue.Speculate)
		r.Queue.RerunOnRed = deref(q.RerunOnRed, r.Queue.RerunOnRed)
		r.Queue.Preempt = deref(q.Preempt, r.Queue.Preempt)
	}
	if c.Review != nil {
		r.Review.MaxRounds = deref(c.Review.MaxRounds, r.Review.MaxRounds)
	}
	if c.Fix != nil {
		r.Fix.AttemptsPerSignature = deref(c.Fix.AttemptsPerSignature, r.Fix.AttemptsPerSignature)
	}
	if f := c.Flake; f != nil {
		r.Flake.Policy = or(f.Policy, r.Flake.Policy)
		r.Flake.QuarantineDays = deref(f.QuarantineDays, r.Flake.QuarantineDays)
	}
	if c.Release != nil {
		rel := resolveRelease(c.Release)
		r.Release = &rel
	}
	r.Recurring = make([]ResolvedRecurringJob, len(c.Recurring))
	for i, j := range c.Recurring {
		r.Recurring[i] = resolveRecurring(j)
	}
	if s := c.Sweep; s != nil {
		if s.Schedule != "" {
			r.Sweep.Schedule = mustSchedule(s.Schedule)
		}
		if s.Checks != nil {
			r.Sweep.Checks = slices.Clone(s.Checks)
		}
	}
	if len(c.Playbooks) > 0 {
		r.Playbooks = make(map[IncidentSource]string, len(c.Playbooks))
		for k, p := range c.Playbooks {
			r.Playbooks[k] = p
		}
	}
	return r, nil
}

func resolveGate(g *Gate, def ResolvedGate, p Project) ResolvedGate {
	out := ResolvedGate{
		Runner:            or(g.Runner, effectiveRunner(g)),
		Timeout:           def.Timeout,
		Checks:            slices.Clone(g.Checks),
		InfraPatternsFile: g.InfraPatternsFile,
		AllowUnisolated:   g.AllowUnisolated != nil && *g.AllowUnisolated,
	}
	out.Stages = resolveStages(g, out.Runner, p)
	if test := out.Stage(StageTest); test.State == StageOn {
		out.Run = test.Command
	}
	if out.Runner == RunnerMachine {
		out.Pool = or(g.Pool, def.Pool)
	}
	if g.Timeout != nil {
		out.Timeout = g.Timeout.Std()
	}
	if len(g.Env) > 0 {
		out.Env = make(map[string]string, len(g.Env))
		for k, v := range g.Env {
			out.Env[k] = v
		}
	}
	for _, p := range g.InfraPatterns {
		out.InfraPatterns = append(out.InfraPatterns, regexp.MustCompile(p)) // validated
	}
	return out
}

func resolveRelease(rel *Release) ResolvedRelease {
	out := DefaultRelease()
	if rel.Schedule != "" {
		out.Schedule = mustSchedule(rel.Schedule)
	}
	out.OnLand = deref(rel.OnLand, false)
	out.Pool = rel.Pool
	out.OnFail = or(rel.OnFail, out.OnFail)
	if rel.Envs != nil {
		out.Envs = make([]ResolvedReleaseEnv, len(rel.Envs))
	}
	for i, e := range rel.Envs {
		env := ResolvedReleaseEnv{
			Name:         e.Name,
			Approval:     or(e.Approval, DefaultEnvApproval),
			Inconclusive: or(e.Inconclusive, DefaultInconclusive),
			Strategy:     DefaultStrategy,
		}
		if e.Probes != nil {
			env.Probes = slices.Clone(e.Probes)
		} else {
			env.Probes = DefaultProbes()
		}
		if e.Soak != nil {
			env.Soak = e.Soak.Std()
		}
		if e.EarlyPass != nil {
			env.EarlyPass = e.EarlyPass.Std()
		}
		if e.Strategy != nil {
			env.Strategy = e.Strategy.Kind
		}
		out.Envs[i] = env
	}
	return out
}

func resolveRecurring(j RecurringJob) ResolvedRecurringJob {
	out := ResolvedRecurringJob{
		Name:     j.Name,
		Schedule: mustSchedule(j.Schedule),
		Prompt:   j.Prompt,
		Run:      j.Run,
		Output:   or(j.Output, DefaultRecurringOutput),
		Tools:    slices.Clone(j.Tools),
		Timeout:  DefaultRecurringTimeout,
	}
	if out.Output == OutputPR {
		out.MaxPRs = deref(j.MaxPRs, DefaultRecurringMaxPRs)
	}
	if j.Timeout != nil {
		out.Timeout = j.Timeout.Std()
	}
	return out
}

// mustSchedule parses a schedule the validator already accepted.
func mustSchedule(s string) Schedule {
	sched, err := ParseSchedule(s)
	if err != nil {
		panic("pipeline config: unvalidated schedule " + s + ": " + err.Error())
	}
	return sched
}

func deref[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

func or[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}
	return v
}
