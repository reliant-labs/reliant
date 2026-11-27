package config

// FilePath is where a repository keeps the file, relative to its root.
const FilePath = ".reliant/pipeline.yaml"

// SupportedVersion is the only `version` this build reads.
const SupportedVersion = 1

// Config is pipeline.yaml as written. Optional scalars whose default is not
// their zero value are pointers, so "absent" and "written as the zero value"
// stay distinct until [Config.Resolve] applies the defaults. Read settings
// through [Resolved], not through these fields.
type Config struct {
	// Version must be 1.
	Version int `yaml:"version"`
	// Check is the optional fast tier, run on each PR head on push. Nil: no
	// check tier; the PR is reviewed and gated in the queue.
	Check *Check `yaml:"check"`
	// Gate is the full suite every batch candidate must pass before it lands.
	// It is the only required field besides Version.
	Gate *Gate `yaml:"gate"`
	// Generated lists doublestar globs (repository-relative) of generated
	// files. A candidate whose diff touches only these, after Regen, is
	// mergeable without a fix.
	Generated []string `yaml:"generated"`
	// Regen regenerates Generated. It requires Generated.
	Regen string `yaml:"regen"`
	// OwnerApproval lists the decisions a person must record in the app
	// before a matching head lands.
	OwnerApproval []OwnerApproval `yaml:"owner_approval"`
	// Pins are the producer repositories this repository pins.
	Pins []Pin `yaml:"pins"`
	// Consumers are the repositories that pin this one.
	Consumers []Consumer `yaml:"consumers"`
	Queue     *Queue     `yaml:"queue"`
	Review    *Review    `yaml:"review"`
	Fix       *Fix       `yaml:"fix"`
	Flake     *Flake     `yaml:"flake"`
	// Release turns releases on. Nil (the key absent): no releases. Write
	// `release: {}` for every default.
	Release   *Release       `yaml:"release"`
	Recurring []RecurringJob `yaml:"recurring"`
	// Sweep configures the daily sweep. Absent: the sweep still runs daily
	// with every built-in check, which is the pipeline-health digest.
	Sweep *Sweep `yaml:"sweep"`
	// Playbooks overrides the responder's prompt per incident source: a
	// repository-relative path to a prompt file.
	Playbooks map[IncidentSource]string `yaml:"playbooks"`

	// pos maps a YAML path to where Parse found it, for validation errors.
	pos map[string]position
}

// Runner is where a gate's command runs.
type Runner string

const (
	// RunnerMachine runs Gate.Run on an isolated member of Gate.Pool. It may
	// also wait for Gate.Checks; every part must pass.
	RunnerMachine Runner = "machine"
	// RunnerChecks runs nothing itself: the gate is the named GitHub check
	// runs on the candidate sha, started by the repository's own CI.
	RunnerChecks Runner = "checks"
)

// Gate is the `gate` field. The string form `gate: "<cmd>"` is
// `gate: {run: "<cmd>"}`.
type Gate struct {
	// Run is the test stage's command (the full suite): the same as
	// `stages.test.run`, and the gate's only command before the gate had
	// stages. Exit 0 pass, 1 fail, 2 unproven. Writing both is an error.
	Run string `yaml:"run"`
	// Stages overrides or turns off the gate's built-in stages, which
	// always run in the order lint, test, build, sbom_scan. A stage not
	// written keeps its detected default. Machine runner only.
	Stages *Stages `yaml:"stages"`
	// Runner defaults to machine when Run is set, and to checks when only
	// Checks is.
	Runner Runner `yaml:"runner"`
	// Pool names the gate machine pool. Default "gate". Machine runner only.
	Pool string `yaml:"pool"`
	// Timeout bounds one gate run. Default 60m. An exceeded timeout is
	// unproven, never red.
	Timeout *Duration `yaml:"timeout"`
	// Checks names GitHub check runs that must also pass on the candidate.
	Checks []string `yaml:"checks"`
	// Env holds non-secret variables for the gate's environment. The file is
	// in git: never put a secret here. Names starting RELIANT_ are reserved.
	// Machine runner only.
	Env EnvVars `yaml:"env"`
	// InfraPatterns are regular expressions (Go RE2) over failing step logs;
	// a match makes the failure infra (unproven), never red.
	InfraPatterns []string `yaml:"infra_patterns"`
	// InfraPatternsFile is a repository-relative file of more patterns, one
	// per line (see [ParseInfraPatterns]). It is read from the same base
	// commit as this file.
	InfraPatternsFile string `yaml:"infra_patterns_file"`
	// AllowUnisolated lets a batch land on a gate that ran outside an
	// isolation boundary (plain runc), until isolated gate machines exist
	// (owner, 2026-11-26). Default false: such a gate is unproven and never
	// lands. A batch that lands this way is marked unisolated.
	AllowUnisolated *bool `yaml:"allow_unisolated"`
}

// Stages is `gate.stages` (owner decision D13): one optional entry per
// built-in stage. Its keys are the closed set [StageNames]; their order in
// the file means nothing, because the gate always runs lint, test, build,
// sbom_scan.
type Stages struct {
	Lint     *Stage `yaml:"lint"`
	Test     *Stage `yaml:"test"`
	Build    *Stage `yaml:"build"`
	SBOMScan *Stage `yaml:"sbom_scan"`
}

// Stage is one `gate.stages.<name>` entry: the mapping `{run: "<cmd>"}`,
// which replaces the stage's command, or the exact string `off`, which turns
// the stage off. No other string is accepted, so `off` can never be read as
// a command, nor a command as `off`.
type Stage struct {
	// Run replaces the stage's default. For sbom_scan it replaces the
	// built-in scan with a repo command: exit 0 pass, 1 fail, 2 unproven.
	Run string `yaml:"run"`
	// Off is the string form `off`.
	Off bool `yaml:"-"`
}

// EnvVars is gate.env: variable names to values. A value may be written as
// any YAML scalar but null; a number or boolean is kept as written
// (`PORT: 8080` is "8080").
type EnvVars map[string]string

// Check is the optional `check` tier. The string form `check: "<cmd>"` is
// `check: {run: "<cmd>"}`. At least one of Run and Checks is set.
type Check struct {
	Run    string   `yaml:"run"`
	Checks []string `yaml:"checks"`
}

// ApprovalKind is a pipeline decision kind that can be required of a head
// (design §4.3). The other decision kinds are requested or made in the app,
// never configured.
type ApprovalKind string

const (
	ApproveBreakingProto  ApprovalKind = "approve_breaking_proto"
	ApproveBreaksRollback ApprovalKind = "approve_breaks_rollback"
	ApproveProdFix        ApprovalKind = "approve_prod_fix"
)

// ApprovalKinds is the closed set of OwnerApproval kinds.
var ApprovalKinds = []ApprovalKind{ApproveBreakingProto, ApproveBreaksRollback, ApproveProdFix}

// OwnerApproval requires a recorded decision of Kind before a matching head
// lands. Its conditions combine with AND: Paths (the diff touches one of
// them), WhenCmd (the command, run in the credentials-none sandbox on the
// head, exits 0) and When (a CEL expression over the subject, such as
// `incident.source in ['prod_alert']`, at most 200 characters). An entry with
// no condition applies to every subject of its kind. Several entries of one
// kind combine with OR.
type OwnerApproval struct {
	Kind    ApprovalKind `yaml:"kind"`
	Paths   []string     `yaml:"paths"`
	WhenCmd string       `yaml:"when_cmd"`
	When    string       `yaml:"when"`
}

// Pin is a producer repository this repository pins (design §3.5).
type Pin struct {
	// Repo is the producer, as owner/name.
	Repo string `yaml:"repo"`
	// File is where the pin lives, repository-relative. Informational.
	File string `yaml:"file"`
	// Bump moves the pin. `{owner/name}` (or `{name}`) placeholders, for
	// Repo and each LockstepWith repo, are replaced by the sha being pinned
	// (see [Pin.BumpCommand]).
	Bump string `yaml:"bump"`
	// LockstepWith lists pins that must move in the same PR as this one.
	LockstepWith []string `yaml:"lockstep_with"`
}

// ConsumerMode says whether a consumer's result gates a producer batch.
type ConsumerMode string

const (
	// ConsumerAdvisory posts a display check and opens an incident on red.
	ConsumerAdvisory ConsumerMode = "advisory"
	// ConsumerRequired makes a producer batch also pass the consumer's
	// command against the candidate.
	ConsumerRequired ConsumerMode = "required"
)

// Consumer is a repository that pins this one.
type Consumer struct {
	Repo string `yaml:"repo"`
	// Mode defaults to advisory.
	Mode ConsumerMode `yaml:"mode"`
	// Command builds the consumer against the producer candidate. The
	// `{candidate}` placeholder is the candidate sha (see
	// [Consumer.CommandFor]).
	Command string `yaml:"command"`
}

// Queue tunes the merge queue.
type Queue struct {
	// BatchMax caps entries per batch. Default 4, 1–16.
	BatchMax *int `yaml:"batch_max"`
	// Speculate is how many batches may build and gate on top of the one in
	// flight. Default 0, 0–4.
	Speculate *int `yaml:"speculate"`
	// RerunOnRed reruns a red gate on the same tree this many times before
	// bisecting. Default 1, 0–3.
	RerunOnRed *int `yaml:"rerun_on_red"`
	// Preempt lets an urgent entry abort the batch in flight. Default false.
	Preempt *bool `yaml:"preempt"`
}

// Review tunes the review loop.
type Review struct {
	// MaxRounds caps review rounds per PR. Default 3, 1–10.
	MaxRounds *int `yaml:"max_rounds"`
}

// Fix tunes the fixer.
type Fix struct {
	// AttemptsPerSignature caps fix attempts per failure signature before
	// the PR needs a person. Default 3, 1–10.
	AttemptsPerSignature *int `yaml:"attempts_per_signature"`
}

// FlakePolicy is what the responder does with a flaky test.
type FlakePolicy string

const (
	// FlakeQuarantine passes the test to the gate as quarantined, then opens
	// an incident when the quarantine expires.
	FlakeQuarantine FlakePolicy = "quarantine"
	// FlakeDelete deletes the test and opens an issue; the quarantine covers
	// the gap until that PR lands.
	FlakeDelete FlakePolicy = "delete"
)

// Flake is the flake policy.
type Flake struct {
	// Policy defaults to quarantine.
	Policy FlakePolicy `yaml:"policy"`
	// QuarantineDays is how long a flaky test stays quarantined. Default 7,
	// 1–7.
	QuarantineDays *int `yaml:"quarantine_days"`
}

// OnFail is what a release does when an env's health check regresses.
type OnFail string

const (
	// OnFailRollback redeploys the env's previous release at once. A
	// rollback floor refusal falls back to fix_forward.
	OnFailRollback OnFail = "rollback"
	// OnFailHold stops the chain and leaves the env as it is for a person.
	OnFailHold OnFail = "hold"
	// OnFailFixForward stops the chain and opens a high-severity release
	// incident whose fix is emergency-eligible.
	OnFailFixForward OnFail = "fix_forward"
)

// Release turns on releases and tunes them (design §6.5).
type Release struct {
	// Schedule is a 5-field cron line (UTC) or `every <duration>`, either
	// optionally followed by ` if main moved`. Default
	// "every 2h if main moved". On demand is always available as the app's
	// release_now decision.
	Schedule string `yaml:"schedule"`
	// OnLand also releases after every landed batch. Default false.
	OnLand *bool `yaml:"on_land"`
	// Pool is the machine pool that builds the release. Default: any of the
	// project's machines.
	Pool string `yaml:"pool"`
	// Envs is the promotion chain, in order. Omitted: derived from the
	// project's forge HealthGate order.
	Envs []ReleaseEnv `yaml:"envs"`
	// OnFail defaults to rollback.
	OnFail OnFail `yaml:"on_fail"`
}

// EnvApproval says whether promoting into an env needs a release_promote
// decision.
type EnvApproval string

const (
	// EnvApprovalProjectSetting follows the project's release_approval
	// setting (none | per_env | prod_only; prod_only by default). The default.
	EnvApprovalProjectSetting EnvApproval = "project_setting"
	// EnvApprovalNone promotes without a decision.
	EnvApprovalNone EnvApproval = "none"
	// EnvApprovalRequired always waits for a decision.
	EnvApprovalRequired EnvApproval = "required"
)

// Inconclusive is what a release does when `obs check` cannot decide
// (exit 2: too little traffic in the window). By owner decision
// (2026-11-25) an inconclusive check is neither a pass nor a block: the
// env's probes judge it. The set is closed so it can grow.
type Inconclusive string

const (
	// InconclusiveProbes falls back to the env's Probes: the step passes
	// when every probe passes and fails (on_fail) when one fails. The
	// default, and today the only value.
	InconclusiveProbes Inconclusive = "probes"
)

// Inconclusives is the closed set of Inconclusive values.
var Inconclusives = []Inconclusive{InconclusiveProbes}

// ProbeBuiltin names a built-in release probe.
type ProbeBuiltin string

const (
	// ProbeHealth checks the env's declared health endpoints.
	ProbeHealth ProbeBuiltin = "health"
	// ProbeSynthetic runs the env's synthetic checks.
	ProbeSynthetic ProbeBuiltin = "synthetic"
)

// ProbeBuiltins is the closed set of built-in probes, and the default
// probes of an env that names none.
var ProbeBuiltins = []ProbeBuiltin{ProbeHealth, ProbeSynthetic}

// Probe judges an env when its health check is inconclusive: a built-in
// probe (the string form: its name) or a repo command (`{name, run}`) that
// exits 0 when the env is healthy.
type Probe struct {
	Name string `yaml:"name"`
	// Run is the command; empty for a built-in probe.
	Run string `yaml:"run"`
}

// Builtin reports whether p is a built-in probe.
func (p Probe) Builtin() bool { return p.Run == "" }

// StrategyKind is how a release env takes the new release.
type StrategyKind string

const (
	// StrategyAllAtOnce deploys the release to the whole env. The default,
	// and the only kind a runtime implements today.
	StrategyAllAtOnce StrategyKind = "all_at_once"
	// StrategyCanary is reserved (design §6.5 extension point): validation
	// refuses it until a runtime implements traffic splitting.
	StrategyCanary StrategyKind = "canary"
)

// ReleaseEnv is one step of the promotion chain.
type ReleaseEnv struct {
	// Name is a forge env (deploy/kcl/<name>).
	Name string `yaml:"name"`
	// Soak is how long the env soaks before the next env. It is at least the
	// env's release_health after window; omitted or 0 means that window.
	Soak *Duration `yaml:"soak"`
	// Approval defaults to project_setting.
	Approval EnvApproval `yaml:"approval"`
	// EarlyPass is `obs check --early-pass`. Omitted: no early pass.
	EarlyPass *Duration `yaml:"early_pass"`
	// Inconclusive defaults to probes.
	Inconclusive Inconclusive `yaml:"inconclusive"`
	// Probes judge an inconclusive check. Omitted: every built-in probe
	// (health, synthetic).
	Probes []Probe `yaml:"probes"`
	// Strategy defaults to all_at_once.
	Strategy *Strategy `yaml:"strategy"`
}

// Strategy is a release env's rollout strategy.
type Strategy struct {
	Kind StrategyKind `yaml:"kind"`
	// Steps are the canary traffic weights, in percent. Canary only.
	Steps []int `yaml:"steps"`
}

// RecurringOutput is what a recurring job's result becomes.
type RecurringOutput string

const (
	// OutputPR opens a PR, which enters the pr loop.
	OutputPR RecurringOutput = "pr"
	// OutputIncident opens an incident of source recurring.
	OutputIncident RecurringOutput = "incident"
	// OutputReport files an Inbox item. The default.
	OutputReport RecurringOutput = "report"
	// OutputNone discards it.
	OutputNone RecurringOutput = "none"
)

// RecurringJob is one scheduled job (design §6.3). Exactly one of Prompt
// (an agent job) and Run (a command, no model) is set.
type RecurringJob struct {
	// Name is unique in the file: lowercase letters, digits, - and _.
	Name string `yaml:"name"`
	// Schedule is a 5-field cron line (UTC) or `every <duration>`.
	Schedule string `yaml:"schedule"`
	// Prompt is a repository-relative prompt file for an agent.
	Prompt string `yaml:"prompt"`
	// Run is a command.
	Run string `yaml:"run"`
	// Output defaults to report.
	Output RecurringOutput `yaml:"output"`
	// MaxPRs caps the job's open PRs. Output pr only; default 1, 1–10.
	MaxPRs *int `yaml:"max_prs"`
	// Tools restricts the agent's tools. Prompt jobs only.
	Tools []string `yaml:"tools"`
	// Timeout bounds one run. Default 60m, 1m–12h.
	Timeout *Duration `yaml:"timeout"`
}

// SweepBuiltin names a built-in sweep check.
type SweepBuiltin string

const (
	SweepStaleNeedsPerson        SweepBuiltin = "stale_needs_person"
	SweepStalePR                 SweepBuiltin = "stale_pr"
	SweepFlakeQuarantineExpiring SweepBuiltin = "flake_quarantine_expiring"
	SweepOrphanQueueBranches     SweepBuiltin = "orphan_queue_branches"
	SweepPinDrift                SweepBuiltin = "pin_drift"
	SweepUnprovenRate            SweepBuiltin = "unproven_rate"
	SweepEscapes                 SweepBuiltin = "escapes"
)

// SweepBuiltins is the closed set of built-in sweep checks, in report order.
var SweepBuiltins = []SweepBuiltin{
	SweepStaleNeedsPerson, SweepStalePR, SweepFlakeQuarantineExpiring,
	SweepOrphanQueueBranches, SweepPinDrift, SweepUnprovenRate, SweepEscapes,
}

// Sweep configures the daily sweep (design §6.4).
type Sweep struct {
	// Schedule is a 5-field cron line (UTC) or `every <duration>`. Default
	// "0 6 * * *".
	Schedule string `yaml:"schedule"`
	// Checks replaces the default list (every built-in). An explicit empty
	// list keeps only the digest.
	Checks []SweepCheck `yaml:"checks"`
}

// SweepCheck is a built-in check (the string form: its name) or a repo
// command check (`{name, run}`) whose JSON output lists findings.
type SweepCheck struct {
	Name string `yaml:"name"`
	// Run is the command; empty for a built-in check.
	Run string `yaml:"run"`
}

// Builtin reports whether c is a built-in check.
func (c SweepCheck) Builtin() bool { return c.Run == "" }

// IncidentSource is where an incident came from (design §6.2).
type IncidentSource string

const (
	SourceMain      IncidentSource = "main"
	SourceRelease   IncidentSource = "release"
	SourceProdAlert IncidentSource = "prod_alert"
	SourceObsIssue  IncidentSource = "obs_issue"
	SourceNightly   IncidentSource = "nightly"
	SourceFlake     IncidentSource = "flake"
	SourceInfra     IncidentSource = "infra"
	SourceRecurring IncidentSource = "recurring"
)

// IncidentSources is the closed set of playbook keys.
var IncidentSources = []IncidentSource{
	SourceMain, SourceRelease, SourceProdAlert, SourceObsIssue,
	SourceNightly, SourceFlake, SourceInfra, SourceRecurring,
}
