package config

import (
	"encoding/json"
	"io/fs"
	"path"
	"slices"
)

// StageName names one of the gate's built-in stages (owner decision D13,
// 2026-11-26). The gate always runs them in the order of [StageNames]; the
// order is not configurable.
type StageName string

const (
	StageLint     StageName = "lint"
	StageTest     StageName = "test"
	StageBuild    StageName = "build"
	StageSBOMScan StageName = "sbom_scan"
)

// StageNames is the closed set of stages, in the order the gate runs them:
// a failed stage stops the later ones (a broken build has nothing to scan).
var StageNames = []StageName{StageLint, StageTest, StageBuild, StageSBOMScan}

// SettingKey is the pipeline_setting_changes.setting under which turning the
// stage off or back on is recorded: `gate.stage.<name>`.
func (n StageName) SettingKey() string { return "gate.stage." + string(n) }

// StageState is whether a stage runs.
type StageState string

const (
	// StageOn runs the stage: its Command, or its built-in Scan.
	StageOn StageState = "on"
	// StageOff is a stage the file turned off (`gate.stages.<name>: off`,
	// or runner checks, which runs no stage on a machine). Every report
	// shows it as off, never as passed.
	StageOff StageState = "off"
	// StageUnavailable is a stage left at its default whose default has
	// nothing to run in this project: a Node repository without a `lint`
	// script, a project whose kind was not detected. Nobody chose it, so it
	// is neither off (a choice, and a recorded setting) nor passed; reports
	// render it as unavailable, with its Reason.
	StageUnavailable StageState = "unavailable"
)

// StageStates is the closed set of StageState values.
var StageStates = []StageState{StageOn, StageOff, StageUnavailable}

// StageSource says where a stage's state and command came from.
type StageSource string

const (
	// StageSourceDefault is the detected default for the project's kind.
	StageSourceDefault StageSource = "default"
	// StageSourceOverride is what the file wrote: a command in
	// `gate.stages.<name>.run` (for the test stage also `gate` or
	// `gate.run`), or `off`.
	StageSourceOverride StageSource = "override"
)

// StageKind is what an on stage runs.
type StageKind string

const (
	// StageCommand runs Command from the repository root, inside the gate's
	// boundary. Exit 0 pass, 1 fail, 2 unproven.
	StageCommand StageKind = "command"
	// StageBuiltinScan is the default sbom_scan: no shell command, but
	// forge's own SBOM and vulnerability scan (syft, then grype against the
	// machine's database, mounted read-only) of the build stage's output,
	// judged by Scan. The gate runner (pipeline build plan task E9f) runs it
	// through forge. Its verdict is pass, warn, fail or unproven; no SBOM, no
	// scan or a stale database is unproven (forge's vuln_gate_unscanned).
	StageBuiltinScan StageKind = "builtin_scan"
)

// ScanPolicy is the rule a built-in scan judges findings by.
type ScanPolicy string

const (
	// ScanPolicyVulnGate is a forge project's own: the KCL vuln_gate of
	// Scan.Env and its `forge vuln exempt` exemptions.
	ScanPolicyVulnGate ScanPolicy = "vuln_gate"
	// ScanPolicyDefault is forge's default vuln_gate, for a repository with
	// none of its own: block KEV and fixable criticals, warn on high.
	ScanPolicyDefault ScanPolicy = "default"
)

// BuiltinScan is what a built-in sbom_scan judges by.
type BuiltinScan struct {
	Policy ScanPolicy
	// Env is the forge env whose vuln_gate judges; set only for
	// ScanPolicyVulnGate.
	Env string
}

// ResolvedStage is one gate stage with its default or override applied.
type ResolvedStage struct {
	Name   StageName
	State  StageState
	Source StageSource
	// Kind is what the stage runs; empty unless State is on.
	Kind StageKind
	// Command is set when Kind is command. A repo command (an override) of
	// sbom_scan follows the same 0 pass, 1 fail, 2 unproven contract.
	Command string
	// Scan is set when Kind is builtin_scan.
	Scan *BuiltinScan
	// Reason says, for reports and the enrolment screen, what a default
	// was detected from, or why the stage is unavailable or off.
	Reason string
}

// Setting is the stage's audited setting value: off when the file turned
// it off, else on. An unavailable stage is on as a setting: nobody turned it
// off, and it runs once the project has something for it to run.
func (s ResolvedStage) Setting() StageSetting {
	if s.State == StageOff {
		return StageSettingOff
	}
	return StageSettingOn
}

// Stage returns the gate's stage n. A ResolvedGate that holds no stages (the
// zero value) returns an on default with no command.
func (g ResolvedGate) Stage(n StageName) ResolvedStage {
	for _, s := range g.Stages {
		if s.Name == n {
			return s
		}
	}
	return ResolvedStage{Name: n, State: StageOn, Source: StageSourceDefault}
}

// StageSetting is the value of a `gate.stage.<name>` setting.
type StageSetting string

const (
	StageSettingOn  StageSetting = "on"
	StageSettingOff StageSetting = "off"
)

// StageChange is a stage turned off or back on between two configurations:
// one pipeline_setting_changes row, with Setting as its setting and Old and
// New as its values.
type StageChange struct {
	Stage   StageName
	Setting string
	Old     StageSetting
	New     StageSetting
}

// StageChanges lists the stages whose setting (on or off) differs from old to
// new, in stage order. The projector calls it when a pipeline.yaml change
// lands on the base branch (pipeline build plan task R8), and records one
// setting change per entry.
//
// Only turning a stage off or on is a setting change. A changed command, or
// a stage moving between on and unavailable because the project gained or
// lost something for it to run, is not: the first is in the reviewed
// pipeline.yaml diff, the second is not anybody's setting. A Resolved with no
// stages (the zero value: no configuration before this one) counts every
// stage as on, the default.
func StageChanges(old, new Resolved) []StageChange {
	var out []StageChange
	for _, n := range StageNames {
		o, nw := old.Gate.Stage(n).Setting(), new.Gate.Stage(n).Setting()
		if o != nw {
			out = append(out, StageChange{Stage: n, Setting: n.SettingKey(), Old: o, New: nw})
		}
	}
	return out
}

// Project is what the stages' defaults depend on outside pipeline.yaml: the
// project's kind and a few facts of its tree. [DetectProject] reads them from
// a repository's root; a caller may fill or correct a field it knows better
// (ForgeEnv, from the project's forge HealthGate order).
type Project struct {
	// Kind is empty when it was not detected: lint and build are then
	// unavailable unless the file overrides them.
	Kind RepoKind
	// Test is the detected test command ([DetectGate]'s proposal). The
	// file still names the test stage's command: Test is what the
	// enrolment screen proposes for it.
	Test string
	// ForgeEnv is the forge env the build stage builds (`reliant forge env
	// build <env>`, never pushed) and whose vuln_gate judges the scan: the
	// first env of the release chain. A file's `release.envs` decides it
	// when it names a chain. DetectProject sets it only when deploy/kcl
	// declares exactly one env; with several, the caller sets the first in
	// HealthGate order, the same derivation as the release chain's.
	ForgeEnv string
	// GolangciLint: the root has a golangci-lint configuration, so a Go
	// repository's lint stage also runs golangci-lint.
	GolangciLint bool
	// LintScript and BuildScript: package.json declares a `lint` / `build`
	// script.
	LintScript  bool
	BuildScript bool
}

var golangciConfigs = []string{".golangci.yml", ".golangci.yaml", ".golangci.toml", ".golangci.json"}

// DetectProject reads the facts the stage defaults depend on from a
// repository's root, as [DetectGate] does (no includes, no workspaces).
// Kind is DetectGate's: forge, else Node, else Go.
func DetectProject(fsys fs.FS) Project {
	var p Project
	if prop, ok := DetectGate(fsys); ok {
		p.Kind, p.Test = prop.Kind, prop.Gate
	}
	if data, err := fs.ReadFile(fsys, "package.json"); err == nil {
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		if json.Unmarshal(data, &pkg) == nil {
			p.LintScript = pkg.Scripts["lint"] != ""
			p.BuildScript = pkg.Scripts["build"] != ""
		}
	}
	p.GolangciLint = slices.ContainsFunc(golangciConfigs, func(name string) bool { return exists(fsys, name) })
	if p.Kind == RepoForge {
		if envs := forgeEnvs(fsys); len(envs) == 1 {
			p.ForgeEnv = envs[0]
		}
	}
	return p
}

// forgeEnvs lists the envs deploy/kcl declares (a directory with a main.k).
func forgeEnvs(fsys fs.FS) []string {
	entries, err := fs.ReadDir(fsys, "deploy/kcl")
	if err != nil {
		return nil
	}
	var envs []string
	for _, e := range entries {
		if e.IsDir() && exists(fsys, path.Join("deploy/kcl", e.Name(), "main.k")) {
			envs = append(envs, e.Name())
		}
	}
	return envs
}

// DefaultStages is every stage at its default for p, in stage order: what a
// file that overrides no stage gets, and what the enrolment screen shows.
// The test stage's default is p.Test, which the file then writes.
func (p Project) DefaultStages() []ResolvedStage {
	return []ResolvedStage{p.defaultLint(), p.defaultTest(), p.defaultBuild(), p.defaultScan()}
}

func onCommand(n StageName, cmd, reason string) ResolvedStage {
	return ResolvedStage{Name: n, State: StageOn, Source: StageSourceDefault, Kind: StageCommand, Command: cmd, Reason: reason}
}

func unavailable(n StageName, reason string) ResolvedStage {
	return ResolvedStage{Name: n, State: StageUnavailable, Source: StageSourceDefault, Reason: reason}
}

const undetected = "the project's kind was not detected (no forge.yaml, package.json test script or go.mod)"

func (p Project) defaultLint() ResolvedStage {
	switch p.Kind {
	case RepoForge:
		return onCommand(StageLint, "reliant forge lint", "a forge project")
	case RepoGo:
		if p.GolangciLint {
			return onCommand(StageLint, "go vet ./... && golangci-lint run ./...", "a Go module with a golangci-lint configuration")
		}
		return onCommand(StageLint, "go vet ./...", "a Go module")
	case RepoNode:
		if p.LintScript {
			return onCommand(StageLint, "npm run lint", "package.json has a lint script")
		}
		return unavailable(StageLint, "package.json has no lint script: add one, set gate.stages.lint.run, or turn the stage off")
	}
	return unavailable(StageLint, undetected+": set gate.stages.lint.run, or turn the stage off")
}

func (p Project) defaultTest() ResolvedStage {
	if p.Test == "" {
		return unavailable(StageTest, "no test command was detected: the file names it (gate, gate.run or gate.stages.test.run)")
	}
	return onCommand(StageTest, p.Test, "detected for a "+string(p.Kind)+" project")
}

func (p Project) defaultBuild() ResolvedStage {
	switch p.Kind {
	case RepoForge:
		if p.ForgeEnv == "" {
			return unavailable(StageBuild, "a forge project whose env to build is unknown: name the chain in release.envs (the first env is built), or set gate.stages.build.run")
		}
		return onCommand(StageBuild, "reliant forge env build "+p.ForgeEnv, "a forge project: the artifacts of "+p.ForgeEnv+", the release chain's first env, built and never pushed")
	case RepoGo:
		return onCommand(StageBuild, "go build ./...", "a Go module")
	case RepoNode:
		if p.BuildScript {
			return onCommand(StageBuild, "npm run build", "package.json has a build script")
		}
		return unavailable(StageBuild, "package.json has no build script: add one, set gate.stages.build.run, or turn the stage off")
	}
	return unavailable(StageBuild, undetected+": set gate.stages.build.run, or turn the stage off")
}

// defaultScan is never unavailable: a scan with nothing to scan is unproven
// at run time (no SBOM is vuln_gate_unscanned), which the queue never lands
// on, rather than a stage nobody runs.
func (p Project) defaultScan() ResolvedStage {
	s := ResolvedStage{Name: StageSBOMScan, State: StageOn, Source: StageSourceDefault, Kind: StageBuiltinScan}
	switch {
	case p.Kind == RepoForge && p.ForgeEnv != "":
		s.Scan = &BuiltinScan{Policy: ScanPolicyVulnGate, Env: p.ForgeEnv}
		s.Reason = "forge's SBOM and scan of the build, judged by " + p.ForgeEnv + "'s vuln_gate and exemptions"
	case p.Kind == RepoForge:
		s.Scan = &BuiltinScan{Policy: ScanPolicyDefault}
		s.Reason = "forge's SBOM and scan of the build, judged by forge's default vuln_gate: the env whose vuln_gate applies is unknown (name release.envs)"
	default:
		s.Scan = &BuiltinScan{Policy: ScanPolicyDefault}
		s.Reason = "a syft SBOM of the build output scanned by grype, judged by forge's default vuln_gate (block KEV and fixable criticals, warn on high)"
	}
	return s
}

// resolveStages applies the file's stage overrides over p's defaults. g has
// been validated.
func resolveStages(g *Gate, runner Runner, p Project) []ResolvedStage {
	out := p.DefaultStages()
	if runner == RunnerChecks {
		for i := range out {
			out[i] = ResolvedStage{Name: out[i].Name, State: StageOff, Source: StageSourceOverride,
				Reason: "runner checks: the gate only waits for GitHub check runs and runs no stage on a machine"}
		}
		return out
	}
	var written Stages
	if g.Stages != nil {
		written = *g.Stages
	}
	if g.Run != "" {
		written.Test = &Stage{Run: g.Run}
	}
	for i, st := range written.entries() {
		switch {
		case st == nil:
		case st.Off:
			out[i] = ResolvedStage{Name: out[i].Name, State: StageOff, Source: StageSourceOverride, Reason: "turned off in pipeline.yaml"}
		default:
			out[i] = ResolvedStage{Name: out[i].Name, State: StageOn, Source: StageSourceOverride, Kind: StageCommand, Command: st.Run, Reason: "set in pipeline.yaml"}
		}
	}
	return out
}

// entries lists s's stages in stage order.
func (s *Stages) entries() []*Stage {
	return []*Stage{s.Lint, s.Test, s.Build, s.SBOMScan}
}
