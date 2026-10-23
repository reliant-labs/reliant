// File: internal/cli/lint/lint_component_drift.go
//
// component-drift — reports disagreement between what the project's CODE
// declares and what `deploy/kcl/workloads.k` declares.
//
// forge does not regenerate workloads.k. It is scaffolded once, appended to
// by `forge scaffold <kind>`, and owned by the project from then on — because
// deploy is where hyper-customization per environment lives, and a generator
// that rewrote it would overwrite exactly the edits it exists to enable.
//
// The cost of that choice is drift: a service can be added to the protos (or
// a worker package created by hand, or a component deleted) without the
// deploy declaration following. This lint is what pays that cost. It REPORTS,
// naming the component and printing the literal stanza to paste — it never
// writes.
//
// WARNING, NOT ERROR, and the asymmetry is deliberate:
//
//   - A component in the code with no entry here is usually an oversight, but
//     it is also how you legitimately ship a component NO environment
//     deploys — one still being built, or one deployed by something outside
//     forge entirely.
//   - An entry here naming no component in the code is usually stale, but it
//     is also how you legitimately declare infrastructure forge did not
//     generate: NATS, a cache, a third-party sidecar. Those have no Go
//     package to discover and MUST NOT be reported as errors.
//
// Neither direction can be distinguished from a deliberate choice by reading
// the tree, so neither fails a build. Forge notices; the human decides.

package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/naming"
)

// componentDriftFinding is one disagreement between the code and the deploy
// declaration. Undeclared is true when the code has a workload workloads.k
// does not (the paste-this-stanza case); false when workloads.k names one
// the code does not.
type componentDriftFinding struct {
	Name       string
	Kind       string
	Undeclared bool
	Stanza     string
}

// workloadNameInKCL matches a `name = "<x>"` field — the component-name
// declaration inside a typed literal. Anchored on the field rather than a
// bare substring so a name appearing in a command path or an image reference
// does not read as a declaration.
var workloadNameInKCL = regexp.MustCompile(`(?m)^\s*name\s*=\s*"([^"]+)"\s*$`)

// declaredWorkloadNames returns the workload names a workloads.k actually
// declares.
//
// Docstrings and comments are stripped first (codegen.StripKCLProse — the
// same rule the append path's duplicate check uses), because the scaffolded
// workloads.k DOCUMENTS the shape it expects, including a worked example
// declaring `name = "nats"`. Without this, that example reads as a real
// component and the lint reports an orphan entry for something nobody
// declared. Prose that teaches the format must not be mistaken for the format.
func declaredWorkloadNames(src string) map[string]bool {
	out := map[string]bool{}
	for _, m := range workloadNameInKCL.FindAllStringSubmatch(codegen.StripKCLProse(src), -1) {
		out[m[1]] = true
	}
	return out
}

// runsBuiltinCommand reports whether a declared workload is one of the
// project binary's OWN commands — the scaffolded `db migrate up` job, an
// all-in-one `server` — rather than a
// component. Such a workload is neither a component nor drift: nothing in
// the code is missing, and nothing about it is stale.
func runsBuiltinCommand(w codegen.DeclaredWorkload, projectName string) bool {
	return codegen.ProjectBinaryRuns(projectName, w.BuildCmd) && codegen.RunsBuiltinSubcommand(w.Args)
}

// servesAnyOf reports whether w's `serves` names a component the code has. A
// workload running several components through one subcommand is named for
// the process (`admin-api`), not for any one component, so it matches none
// by name — what it serves is what makes it current rather than stale.
func servesAnyOf(w codegen.DeclaredWorkload, inCode map[string]bool) bool {
	for name := range inCode {
		if w.ServesComponent(name) {
			return true
		}
	}
	return false
}

// runsOwnSubcommandOfAny reports whether w runs some component's own
// subcommand under a name of its own (`workspace-controller` running
// `workspace`): that workload is the component's declaration, not an orphan.
func runsOwnSubcommandOfAny(w codegen.DeclaredWorkload, projectName string, components []config.ComponentConfig) bool {
	for _, c := range components {
		if codegen.RunsOwnSubcommand(w, projectName, c) {
			return true
		}
	}
	return false
}

// collectComponentDrift compares the project's discovered components against
// the names declared in deploy/kcl/workloads.k.
//
// A project with no workloads.k yields NO findings: that is a project whose
// deploy is not scaffolded (features.deploy off, or a CLI/library kind), and
// reporting every component as undeclared there would be noise.
//
// A declared workload is judged by what it RUNS. One that runs a built-in
// command of the project binary (runsBuiltinCommand) matches no component by
// design, so it is never reported as an orphan — which is what kept every
// fresh scaffold warning about its own `migrate` job. Likewise a component
// another workload runs (codegen.ServingWorkload: its `serves`, an
// all-in-one `server`, or its own subcommand under another name) is not
// undeclared, and a workload whose `serves` names a live component is not an
// orphan. Nor, in a project that runs a kind of component in processes it
// composes itself (codegen.ComposedProcesses — control-plane's `public-api`),
// is a component of that kind undeclared or such a process an orphan: that
// is the reading that makes `forge scaffold` declare nothing for it.
func collectComponentDrift(projectDir string, cfg *config.ProjectConfig) ([]componentDriftFinding, error) {
	if cfg == nil {
		return nil, nil
	}
	path := filepath.Join(projectDir, codegen.WorkloadsKCLRelPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	declared := declaredWorkloadNames(string(raw))
	runs := codegen.DeclaredWorkloads(string(raw))
	// A component and its workload match in any spelling: `engine_shadow`
	// is declared `name = "engine-shadow"` (codegen.WorkloadName).
	declaredAs := map[string]bool{}
	for name := range declared {
		declaredAs[naming.KCLIdentifier(name)] = true
	}

	inCode := map[string]bool{}
	// composedBy names the workloads ComposedProcesses reads as the
	// project's own composed processes: those are not orphans.
	composedBy := map[string]bool{}
	var findings []componentDriftFinding
	components := codegen.DiscoverProjectComponents(projectDir, cfg.Name)
	for _, c := range components {
		inCode[naming.KCLIdentifier(c.Name)] = true
		if declaredAs[naming.KCLIdentifier(c.Name)] {
			continue
		}
		// Run by another workload's process — named in its `serves`, one
		// `server` that runs everything, or its own subcommand under
		// another name. Declaring it again would deploy it twice, so its
		// absence is not drift.
		if _, served := codegen.ServingWorkload(runs, cfg.Name, c); served {
			continue
		}
		// The project runs this kind in processes it composes itself
		// (control-plane's `public-api`), not one workload each — the same
		// reading that makes `forge scaffold` declare nothing for it.
		if composed := codegen.ComposedProcesses(runs, cfg.Name, components, c); len(composed) > 0 {
			for _, name := range composed {
				composedBy[name] = true
			}
			continue
		}
		findings = append(findings, componentDriftFinding{
			Name:       c.Name,
			Kind:       c.EffectiveKind(),
			Undeclared: true,
			Stanza:     codegen.WorkloadStanza(cfg.ModulePath, cfg.Name, c),
		})
	}

	// The other direction. Entries with no Go component behind them are
	// reported far more gently: declaring infrastructure forge never
	// generated is a SUPPORTED use of this file, not a mistake.
	var orphans []string
	for name := range declared {
		if inCode[naming.KCLIdentifier(name)] {
			continue
		}
		if composedBy[name] {
			continue
		}
		if w, ok := runs[name]; ok && (runsBuiltinCommand(w, cfg.Name) || servesAnyOf(w, inCode) || runsOwnSubcommandOfAny(w, cfg.Name, components)) {
			continue
		}
		orphans = append(orphans, name)
	}
	sort.Strings(orphans)
	for _, name := range orphans {
		findings = append(findings, componentDriftFinding{Name: name})
	}
	return findings, nil
}

// collectComponentDriftJSON is the JSON arm. Every finding is a warning and
// the step never gates, matching text mode.
func collectComponentDriftJSON(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
	findings, err := collectComponentDrift(rc.cwd, rc.cfg)
	if err != nil {
		return nil, false, err
	}
	out := make([]lintJSONFinding, 0, len(findings))
	for _, f := range findings {
		if !f.Undeclared {
			out = append(out, lintJSONFinding{
				File:     codegen.WorkloadsKCLRelPath,
				Severity: "warning",
				Rule:     "component-drift/orphan-entry",
				Message: fmt.Sprintf("%s declares %q, which no Go component matches",
					codegen.WorkloadsKCLRelPath, f.Name),
				FixHint: "expected if it is infrastructure forge did not generate; if its subcommand runs several components, list them in its `serves`; otherwise delete the entry",
			})
			continue
		}
		out = append(out, lintJSONFinding{
			File:     codegen.WorkloadsKCLRelPath,
			Severity: "warning",
			Rule:     "component-drift/undeclared-component",
			Message: fmt.Sprintf("%s %q has no entry in %s, so no environment deploys it. %s",
				f.Kind, f.Name, codegen.WorkloadsKCLRelPath, codegen.ServesHint(f.Name)),
			FixHint: strings.TrimRight(f.Stanza, "\n"),
		})
	}
	return out, false, nil
}
