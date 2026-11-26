package config

import (
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"cel.dev/cel-go/cel"
	"github.com/bmatcuk/doublestar/v4"
)

// Bounds the validator enforces. They are deliberately tight: each is a
// value past which the setting stops meaning what the design intends.
const (
	MaxBatch          = 16
	MaxSpeculate      = 4
	MaxRerunOnRed     = 3
	MaxReviewRounds   = 10
	MaxFixAttempts    = 10
	MaxQuarantineDays = 7
	MaxRecurringPRs   = 10
	// MaxWhenLength is the complexity budget's inline expression cap (§9).
	MaxWhenLength = 200

	MinGateTimeout      = time.Minute
	MaxGateTimeout      = 6 * time.Hour
	MaxSoak             = 6 * time.Hour
	MinRecurringTimeout = time.Minute
	MaxRecurringTimeout = 12 * time.Hour
)

var (
	// GitHub: an owner is up to 39 alphanumerics or hyphens, not starting
	// with a hyphen; a repository name is up to 100 of [A-Za-z0-9._-].
	repoRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]{1,100}$`)
	// nameRe is a job, pool, env or check name.
	nameRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	envVarRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// placeholderRe finds `{token}`; a `$` before the brace (shell `${VAR}`)
	// is checked by the caller.
	placeholderRe = regexp.MustCompile(`\{([A-Za-z0-9._/-]+)\}`)
)

// nonConfigurableKinds are the §4.3 decision kinds that are requested or
// made in the app, never required by configuration.
var nonConfigurableKinds = []string{
	"hold", "release_hold", "priority", "emergency_land", "release_now",
	"release_promote", "pause_queue", "resolve_needs_person", "close_pr",
}

const reservedEnvPrefix = "RELIANT_"

type validator struct {
	pos  map[string]position
	errs Errors
}

// at finds the position of path, or of its nearest parent Parse recorded.
func (v *validator) at(path string) position {
	for p := path; p != ""; {
		if pos, ok := v.pos[p]; ok {
			return pos
		}
		i := strings.LastIndexAny(p, ".[")
		if i < 0 {
			break
		}
		p = p[:i]
	}
	return position{}
}

func (v *validator) errorf(path, format string, args ...any) {
	pos := v.at(path)
	v.errs = append(v.errs, &FieldError{Path: path, Line: pos.line, Column: pos.column, Msg: fmt.Sprintf(format, args...)})
}

func (v *validator) present(path string) bool {
	_, ok := v.pos[path]
	return ok
}

// Validate checks c against every rule of version 1. Parse calls it; call it
// yourself only on a Config built in code. The error is an [Errors].
func (c *Config) Validate() error {
	v := &validator{pos: c.pos}
	switch c.Version {
	case SupportedVersion:
	case 0:
		v.errorf("version", "is required: write `version: %d` as the first line", SupportedVersion)
	default:
		v.errorf("version", "version %d is not supported: this build of Reliant reads pipeline.yaml version %d", c.Version, SupportedVersion)
	}
	v.gate(c.Gate)
	if c.Check != nil {
		v.check(c.Check)
	}
	for i, g := range c.Generated {
		v.glob(fmt.Sprintf("generated[%d]", i), g)
	}
	if c.Regen != "" {
		v.command("regen", c.Regen)
		if len(c.Generated) == 0 {
			v.errorf("regen", "needs `generated`: list the globs regen writes, so the queue can tell a generated-only conflict from a real one")
		}
	}
	for i, a := range c.OwnerApproval {
		v.ownerApproval(fmt.Sprintf("owner_approval[%d]", i), a)
	}
	v.pins(c.Pins)
	v.consumers(c.Consumers)
	if q := c.Queue; q != nil {
		v.intRange("queue.batch_max", q.BatchMax, 1, MaxBatch)
		v.intRange("queue.speculate", q.Speculate, 0, MaxSpeculate)
		v.intRange("queue.rerun_on_red", q.RerunOnRed, 0, MaxRerunOnRed)
	}
	if r := c.Review; r != nil {
		v.intRange("review.max_rounds", r.MaxRounds, 1, MaxReviewRounds)
	}
	if f := c.Fix; f != nil {
		v.intRange("fix.attempts_per_signature", f.AttemptsPerSignature, 1, MaxFixAttempts)
	}
	if f := c.Flake; f != nil {
		if f.Policy != "" {
			oneOf(v, "flake.policy", f.Policy, []FlakePolicy{FlakeQuarantine, FlakeDelete})
		}
		v.intRange("flake.quarantine_days", f.QuarantineDays, 1, MaxQuarantineDays)
	}
	switch {
	case c.Release != nil:
		v.release(c.Release)
	case v.present("release"):
		v.errorf("release", "is empty: write `release: {}` to turn releases on with every default, or remove the key")
	}
	v.recurring(c.Recurring)
	if c.Sweep != nil {
		v.sweep(c.Sweep)
	}
	for _, src := range sortedKeys(c.Playbooks) {
		path := "playbooks." + string(src)
		if !slices.Contains(IncidentSources, src) {
			v.errorf(path, "unknown incident source %q: playbooks take %s", src, list(IncidentSources))
			continue
		}
		v.relPath(path, c.Playbooks[src])
	}
	if len(v.errs) == 0 {
		return nil
	}
	sort.SliceStable(v.errs, func(i, j int) bool {
		li, lj := v.errs[i].Line, v.errs[j].Line
		return li > 0 && (lj == 0 || li < lj)
	})
	return v.errs
}

func (v *validator) gate(g *Gate) {
	if g == nil {
		if v.present("gate") {
			v.errorf("gate", "is empty: name the full suite main must always pass, e.g. `gate: \"npm test\"`")
		} else {
			v.errorf("gate", "is required: name the full suite main must always pass, e.g. `gate: \"npm test\"`")
		}
		return
	}
	runPath := "gate"
	if v.present("gate.run") {
		runPath = "gate.run"
	}
	runner := g.Runner
	switch runner {
	case "":
		runner = effectiveRunner(g)
	case RunnerMachine, RunnerChecks:
	default:
		v.errorf("gate.runner", "must be %s, got %q", list([]Runner{RunnerMachine, RunnerChecks}), g.Runner)
		return
	}
	switch runner {
	case RunnerMachine:
		// The test stage's command is written in the file, as gate.run
		// (or the gate string) or as gate.stages.test, unless the test
		// stage is turned off. v.stages checks the second form.
		if (g.Stages == nil || g.Stages.Test == nil) && strings.TrimSpace(g.Run) == "" {
			v.errorf(runPath, "needs a command for runner machine (e.g. `gate: \"npm test\"`): the test stage's command, the full suite; to gate only on GitHub check runs, set runner: checks and list them in checks")
		}
		if g.Pool != "" {
			v.name("gate.pool", g.Pool)
		}
		if g.Stages != nil {
			v.stages(g)
		}
	case RunnerChecks:
		if g.Stages != nil {
			v.errorf("gate.stages", "applies only to runner machine: runner checks runs no stage, it only waits for the GitHub check runs")
		}
		if len(g.Checks) == 0 {
			v.errorf("gate.checks", "runner checks needs the GitHub check names the gate waits for")
		}
		if g.Run != "" {
			v.errorf(runPath, "runner checks runs no command; to run a command and also wait for checks, use runner: machine with both run and checks")
		}
		if g.Pool != "" {
			v.errorf("gate.pool", "applies only to runner machine")
		}
		if len(g.Env) > 0 {
			v.errorf("gate.env", "applies only to runner machine")
		}
	}
	v.durRange("gate.timeout", g.Timeout, MinGateTimeout, MaxGateTimeout)
	v.checkNames("gate.checks", g.Checks)
	for _, k := range sortedKeys(g.Env) {
		path := "gate.env." + k
		switch {
		case !envVarRe.MatchString(k):
			v.errorf(path, "%q is not an environment variable name (letters, digits and _, not starting with a digit)", k)
		case strings.HasPrefix(k, reservedEnvPrefix):
			v.errorf(path, "names starting %s are reserved for the variables the queue sets", reservedEnvPrefix)
		}
	}
	for i, p := range g.InfraPatterns {
		if _, err := regexp.Compile(p); err != nil {
			v.errorf(fmt.Sprintf("gate.infra_patterns[%d]", i), "is not a valid regular expression: %v", err)
		}
	}
	if g.InfraPatternsFile != "" {
		v.relPath("gate.infra_patterns_file", g.InfraPatternsFile)
	}
}

// effectiveRunner is the runner an unset gate.runner means: machine when
// the gate has a command or stages, checks when it names only check runs.
func effectiveRunner(g *Gate) Runner {
	if g.Run == "" && g.Stages == nil && len(g.Checks) > 0 {
		return RunnerChecks
	}
	return RunnerMachine
}

// stages checks gate.stages on a machine gate.
func (v *validator) stages(g *Gate) {
	s := g.Stages
	off := 0
	for i, st := range s.entries() {
		if st == nil {
			continue
		}
		path := "gate.stages." + string(StageNames[i])
		switch {
		case st.Off:
			off++
		case st.Run == "" && !v.present(path+".run"):
			v.errorf(path, "needs run: write {run: <command>} to replace the stage's command, or off to turn it off")
		default:
			v.command(path+".run", st.Run)
		}
	}
	if s.Test != nil && g.Run != "" {
		where := "gate.run"
		if pos := v.at("gate.run"); pos.line > 0 && v.present("gate.run") {
			where = fmt.Sprintf("gate.run (line %d)", pos.line)
		}
		v.errorf("gate.stages.test", "and %s both set the test stage: gate.run is the test stage's command, so keep one of them", where)
	}
	if off == len(StageNames) {
		v.errorf("gate.stages", "turns every stage off, so the gate would run nothing: turn a stage on, or set runner: checks to gate only on GitHub check runs")
	}
}

func (v *validator) check(c *Check) {
	path := "check"
	if v.present("check.run") {
		path = "check.run"
	}
	if c.Run == "" && len(c.Checks) == 0 {
		v.errorf("check", "needs run, checks, or both; remove the key for no check tier")
		return
	}
	if c.Run != "" {
		v.command(path, c.Run)
	}
	v.checkNames("check.checks", c.Checks)
}

func (v *validator) ownerApproval(path string, a OwnerApproval) {
	switch {
	case a.Kind == "":
		v.errorf(path+".kind", "is required: one of %s", list(ApprovalKinds))
	case slices.Contains(nonConfigurableKinds, string(a.Kind)):
		v.errorf(path+".kind", "%q is requested or decided in the app, not required by configuration; owner_approval takes %s", a.Kind, list(ApprovalKinds))
	case !slices.Contains(ApprovalKinds, a.Kind):
		v.errorf(path+".kind", "unknown kind %q: owner_approval takes %s", a.Kind, list(ApprovalKinds))
	}
	for i, p := range a.Paths {
		v.glob(fmt.Sprintf("%s.paths[%d]", path, i), p)
	}
	if v.present(path+".when_cmd") || a.WhenCmd != "" {
		v.command(path+".when_cmd", a.WhenCmd)
	}
	if v.present(path+".when") || a.When != "" {
		v.celExpr(path+".when", a.When)
	}
}

func (v *validator) celExpr(path, expr string) {
	if strings.TrimSpace(expr) == "" {
		v.errorf(path, "is empty: write a CEL expression or remove the key")
		return
	}
	if len(expr) > MaxWhenLength {
		v.errorf(path, "is %d characters; the limit is %d: move the logic into when_cmd (a script with its own tests)", len(expr), MaxWhenLength)
		return
	}
	env, err := cel.NewEnv()
	if err != nil {
		v.errorf(path, "cannot check the expression: %v", err)
		return
	}
	if _, iss := env.Parse(expr); iss != nil && iss.Err() != nil {
		v.errorf(path, "is not a valid CEL expression: %v", iss.Err())
	}
}

func (v *validator) pins(pins []Pin) {
	seen := map[string]bool{}
	for i, p := range pins {
		path := fmt.Sprintf("pins[%d]", i)
		if v.repo(path+".repo", p.Repo) {
			if seen[strings.ToLower(p.Repo)] {
				v.errorf(path+".repo", "%s is pinned twice", p.Repo)
			}
			seen[strings.ToLower(p.Repo)] = true
		}
		if p.File != "" {
			v.relPath(path+".file", p.File)
		}
		lock := map[string]bool{}
		for j, r := range p.LockstepWith {
			lp := fmt.Sprintf("%s.lockstep_with[%d]", path, j)
			if !v.repo(lp, r) {
				continue
			}
			switch {
			case strings.EqualFold(r, p.Repo):
				v.errorf(lp, "names the pin's own repo")
			case lock[strings.ToLower(r)]:
				v.errorf(lp, "%s is listed twice", r)
			}
			lock[strings.ToLower(r)] = true
		}
		if v.present(path+".bump") || p.Bump != "" {
			v.command(path+".bump", p.Bump)
			if _, err := p.expand(nil); err != nil {
				v.errorf(path+".bump", "%v", err)
			}
		}
	}
}

func (v *validator) consumers(cs []Consumer) {
	seen := map[string]bool{}
	for i, c := range cs {
		path := fmt.Sprintf("consumers[%d]", i)
		if v.repo(path+".repo", c.Repo) {
			if seen[strings.ToLower(c.Repo)] {
				v.errorf(path+".repo", "%s is listed twice", c.Repo)
			}
			seen[strings.ToLower(c.Repo)] = true
		}
		if c.Mode != "" {
			oneOf(v, path+".mode", c.Mode, []ConsumerMode{ConsumerAdvisory, ConsumerRequired})
		}
		if c.Command == "" {
			v.errorf(path+".command", "is required: the command that builds %s against the candidate ({candidate} is its sha)", orDefault(c.Repo, "the consumer"))
		} else {
			v.command(path+".command", c.Command)
		}
	}
}

func (v *validator) release(r *Release) {
	if r.Schedule != "" || v.present("release.schedule") {
		if _, err := ParseSchedule(r.Schedule); err != nil {
			v.errorf("release.schedule", "%v", err)
		}
	}
	if r.Pool != "" {
		v.name("release.pool", r.Pool)
	}
	if r.OnFail != "" {
		oneOf(v, "release.on_fail", r.OnFail, []OnFail{OnFailRollback, OnFailHold, OnFailFixForward})
	}
	if v.present("release.envs") && len(r.Envs) == 0 {
		v.errorf("release.envs", "lists no env: list the chain in order, or remove the key to derive it from the project's forge HealthGate order")
	}
	seen := map[string]bool{}
	for i, e := range r.Envs {
		path := fmt.Sprintf("release.envs[%d]", i)
		if e.Name == "" {
			v.errorf(path+".name", "is required: a forge env (deploy/kcl/<name>)")
		} else if v.name(path+".name", e.Name) {
			if seen[e.Name] {
				v.errorf(path+".name", "%s appears twice in the chain", e.Name)
			}
			seen[e.Name] = true
		}
		v.durRange(path+".soak", e.Soak, 0, MaxSoak)
		if e.EarlyPass != nil {
			v.durRange(path+".early_pass", e.EarlyPass, time.Minute, MaxSoak)
			if e.Soak != nil && *e.Soak > 0 && *e.EarlyPass > *e.Soak {
				v.errorf(path+".early_pass", "is longer than soak (%s)", formatDuration(e.Soak.Std()))
			}
		}
		switch e.Approval {
		case "", EnvApprovalProjectSetting, EnvApprovalNone, EnvApprovalRequired:
		case "prod_only", "per_env":
			v.errorf(path+".approval", "%q is a project setting (Project settings → Pipeline → release approval); per env write none or required, or omit it to follow that setting", e.Approval)
		default:
			v.errorf(path+".approval", "must be %s, got %q", list([]EnvApproval{EnvApprovalProjectSetting, EnvApprovalNone, EnvApprovalRequired}), e.Approval)
		}
		switch e.Inconclusive {
		case "", InconclusiveProbes:
		case "hold", "pass":
			v.errorf(path+".inconclusive", "%q is not an option: an inconclusive check is neither a pass nor a block; the env's probes judge it (inconclusive: probes)", e.Inconclusive)
		default:
			oneOf(v, path+".inconclusive", e.Inconclusive, Inconclusives)
		}
		v.probes(path+".probes", e.Probes)
		if s := e.Strategy; s != nil {
			sp := path + ".strategy"
			switch s.Kind {
			case "":
				v.errorf(sp+".kind", "is required: %s", StrategyAllAtOnce)
			case StrategyAllAtOnce:
				if len(s.Steps) > 0 {
					v.errorf(sp+".steps", "applies only to a canary strategy")
				}
			case StrategyCanary:
				v.errorf(sp+".kind", "canary is not available yet: no runtime implements traffic splitting (a forge `deploy --weight` or a hosted traffic split); deploy the env with %s for now", StrategyAllAtOnce)
			default:
				v.errorf(sp+".kind", "unknown strategy %q: the only strategy available is %s", s.Kind, StrategyAllAtOnce)
			}
		}
	}
}

func (v *validator) recurring(jobs []RecurringJob) {
	seen := map[string]bool{}
	for i, j := range jobs {
		path := fmt.Sprintf("recurring[%d]", i)
		if j.Name == "" {
			v.errorf(path+".name", "is required")
		} else if v.name(path+".name", j.Name) {
			if seen[j.Name] {
				v.errorf(path+".name", "%s is defined twice", j.Name)
			}
			seen[j.Name] = true
		}
		v.schedule(path+".schedule", j.Schedule, true)
		switch {
		case j.Prompt != "" && j.Run != "":
			v.errorf(path, "sets both prompt and run: a job is an agent (prompt) or a command (run), not both")
		case j.Prompt == "" && j.Run == "":
			v.errorf(path, "needs prompt (an agent job) or run (a command)")
		case j.Prompt != "":
			v.relPath(path+".prompt", j.Prompt)
		default:
			v.command(path+".run", j.Run)
		}
		if j.Output != "" {
			oneOf(v, path+".output", j.Output, []RecurringOutput{OutputPR, OutputIncident, OutputReport, OutputNone})
		}
		if j.MaxPRs != nil {
			if j.Output != OutputPR {
				v.errorf(path+".max_prs", "applies only to output pr")
			} else {
				v.intRange(path+".max_prs", j.MaxPRs, 1, MaxRecurringPRs)
			}
		}
		if len(j.Tools) > 0 && j.Prompt == "" {
			v.errorf(path+".tools", "applies only to a prompt job; a run job uses no agent")
		}
		for k, t := range j.Tools {
			if strings.TrimSpace(t) == "" {
				v.errorf(fmt.Sprintf("%s.tools[%d]", path, k), "is empty: write a tool name")
			}
		}
		v.uniqueStrings(path+".tools", j.Tools, "tool")
		v.durRange(path+".timeout", j.Timeout, MinRecurringTimeout, MaxRecurringTimeout)
	}
}

func (v *validator) sweep(s *Sweep) {
	if s.Schedule != "" || v.present("sweep.schedule") {
		v.schedule("sweep.schedule", s.Schedule, false)
	}
	seen := map[string]bool{}
	for i, c := range s.Checks {
		path := fmt.Sprintf("sweep.checks[%d]", i)
		if c.Builtin() {
			if !slices.Contains(SweepBuiltins, SweepBuiltin(c.Name)) {
				v.errorf(path, "unknown built-in check %q: the built-ins are %s; a repo command check is {name, run}", c.Name, list(SweepBuiltins))
				continue
			}
		} else {
			if c.Name == "" {
				v.errorf(path+".name", "is required")
				continue
			}
			if slices.Contains(SweepBuiltins, SweepBuiltin(c.Name)) {
				v.errorf(path+".name", "%q is a built-in check: give the command check another name", c.Name)
				continue
			}
			if !v.name(path+".name", c.Name) {
				continue
			}
			v.command(path+".run", c.Run)
		}
		if seen[c.Name] {
			v.errorf(path, "%s is listed twice", c.Name)
		}
		seen[c.Name] = true
	}
}

func (v *validator) probes(path string, probes []Probe) {
	if probes != nil && len(probes) == 0 {
		v.errorf(path, "lists no probe: an inconclusive check needs something to judge it; remove the key for every built-in probe")
		return
	}
	seen := map[string]bool{}
	for i, p := range probes {
		ip := fmt.Sprintf("%s[%d]", path, i)
		if p.Builtin() {
			if !slices.Contains(ProbeBuiltins, ProbeBuiltin(p.Name)) {
				v.errorf(ip, "unknown built-in probe %q: the built-ins are %s; a repo command probe is {name, run}", p.Name, list(ProbeBuiltins))
				continue
			}
		} else {
			switch {
			case p.Name == "":
				v.errorf(ip+".name", "is required")
				continue
			case slices.Contains(ProbeBuiltins, ProbeBuiltin(p.Name)):
				v.errorf(ip+".name", "%q is a built-in probe: give the command probe another name", p.Name)
				continue
			case !v.name(ip+".name", p.Name):
				continue
			}
			v.command(ip+".run", p.Run)
		}
		if seen[p.Name] {
			v.errorf(ip, "%s is listed twice", p.Name)
		}
		seen[p.Name] = true
	}
}

// schedule checks a recurring or sweep schedule. The `if main moved` form is
// a release schedule's alone: nothing else is tied to the base branch moving.
func (v *validator) schedule(path, s string, required bool) {
	if s == "" && !required && !v.present(path) {
		return
	}
	if s == "" {
		v.errorf(path, "is required: a 5-field cron line such as \"0 6 * * *\", or \"every 6h\"")
		return
	}
	sched, err := ParseSchedule(s)
	if err != nil {
		v.errorf(path, "%v", err)
		return
	}
	if sched.IfMainMoved {
		v.errorf(path, "`if main moved` applies only to release.schedule")
	}
}

func (v *validator) command(path, cmd string) {
	if strings.TrimSpace(cmd) == "" {
		v.errorf(path, "is empty: write the command, or remove the key")
	}
}

func (v *validator) relPath(path, p string) {
	if !fs.ValidPath(p) || p == "." || strings.Contains(p, `\`) {
		v.errorf(path, "%q must be a repository-relative path with / separators, such as .reliant/prompts/pentest.md (no leading /, no . or .. elements)", p)
	}
}

func (v *validator) glob(path, g string) {
	switch {
	case strings.TrimSpace(g) == "":
		v.errorf(path, "is empty")
	case strings.HasPrefix(g, "/"):
		v.errorf(path, "%q must be relative to the repository root (no leading /)", g)
	case slices.Contains(strings.Split(g, "/"), ".."):
		v.errorf(path, "%q must not leave the repository (..)", g)
	case !doublestar.ValidatePattern(g):
		v.errorf(path, "%q is not a valid glob", g)
	}
}

// repo reports whether r is a valid owner/name, recording an error if not.
func (v *validator) repo(path, r string) bool {
	switch {
	case r == "":
		v.errorf(path, "is required: owner/name, e.g. reliant-labs/forge")
		return false
	case !repoRe.MatchString(r) || strings.HasSuffix(r, "/.") || strings.HasSuffix(r, "/.."):
		v.errorf(path, "%q is not a GitHub repository as owner/name, e.g. reliant-labs/forge", r)
		return false
	}
	return true
}

func (v *validator) name(path, n string) bool {
	if !nameRe.MatchString(n) {
		v.errorf(path, "%q must be lowercase letters, digits, - and _ (at most 63, starting with a letter or digit)", n)
		return false
	}
	return true
}

func (v *validator) checkNames(path string, names []string) {
	for i, n := range names {
		if strings.TrimSpace(n) == "" {
			v.errorf(fmt.Sprintf("%s[%d]", path, i), "is empty: write the GitHub check run name")
		}
	}
	v.uniqueStrings(path, names, "check")
}

// uniqueStrings reports repeated entries; callers report empty ones.
func (v *validator) uniqueStrings(path string, items []string, noun string) {
	seen := map[string]bool{}
	for i, s := range items {
		if strings.TrimSpace(s) == "" {
			continue
		}
		if seen[s] {
			v.errorf(fmt.Sprintf("%s[%d]", path, i), "%s %q is listed twice", noun, s)
		}
		seen[s] = true
	}
}

func (v *validator) intRange(path string, p *int, lo, hi int) {
	if p != nil && (*p < lo || *p > hi) {
		v.errorf(path, "must be between %d and %d, got %d", lo, hi, *p)
	}
}

func (v *validator) durRange(path string, p *Duration, lo, hi time.Duration) {
	if p == nil {
		return
	}
	if d := p.Std(); d < lo || d > hi {
		v.errorf(path, "must be between %s and %s, got %s", formatDuration(lo), formatDuration(hi), formatDuration(d))
	}
}

func oneOf[T ~string](v *validator, path string, got T, allowed []T) {
	if !slices.Contains(allowed, got) {
		v.errorf(path, "must be %s, got %q", list(allowed), got)
	}
}

func list[T ~string](items []T) string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = string(s)
	}
	if len(out) <= 1 {
		return strings.Join(out, "")
	}
	return strings.Join(out[:len(out)-1], ", ") + " or " + out[len(out)-1]
}

func sortedKeys[K ~string, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
