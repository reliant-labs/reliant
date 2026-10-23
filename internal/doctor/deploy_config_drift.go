package doctor

// deploy_config_drift.go — "does every frontend the deploy graph
// declares have code forge can actually build?"
//
// internal/cli/generate_config_check.go walks the other way: for each
// frontend forge.yaml DECLARES, does its path exist on disk. Its header
// names the principle — a silent skip was "the #1 source of 'I added a
// service but generate did nothing' friction" — but it can only
// cross-check what forge.yaml still declares, so a frontend that exists
// in the deploy graph and NOT in the project config falls in the blind
// spot that file was written to close.
//
// This check used to ask a NARROWER question, and the narrow one is now
// gone. It asked "is the frontend absent from forge.yaml", on the
// premise that `forge build --target <name>` resolved its targets from
// cfg.Frontends and would answer:
//
//	Error: target "reliant-web" not found in project config
//
// That premise was itself the bug. forge.yaml's `frontends:` is a
// CODEGEN inventory (config.EffectiveFrontends) — "whose TypeScript does
// this repo generate" — and it deliberately excludes a frontend pinned
// to another repository. Using it to answer a BUILD question conflated
// two different sets, and the remedy this check printed ("add it to
// forge.yaml frontends[]") was the one action that would have been
// actively harmful, since it would project control-plane's generated
// TypeScript into the sibling reliant working tree. build.go's
// resolveNamedBuildTarget now consults the rendered env for the build
// set, so absence from forge.yaml no longer stops anything and is no
// longer worth reporting.
//
// What is left is the case where there is genuinely no code to build:
// the declaration names a path INSIDE this repository and nothing is
// there. `npm run build` cannot run in a directory that does not exist,
// and no other check sees it — the forge.yaml→filesystem walk above only
// covers what forge.yaml lists, which by construction this does not.
//
// Everything else has code forge can reach, and is deliberately silent:
//
//   - a cross-repo `source:` pin is materialized from the pin at build
//     time, which is the entire point of the pin;
//   - a path outside the tree (`../reliant/web`) names a checkout whose
//     presence is a property of the machine, not of this repository's
//     configuration. Reporting it would fail every CI run and pass on
//     every developer's machine, and a verdict that flips with the
//     working copy trains the reader to ignore the whole report;
//   - a frontend in forge.yaml that no environment declares is the
//     ordinary shape of a dev-only frontend.
//
// Severity splits on what the declaration CLAIMS:
//
//   - a runtime that ships (OnHosted, OnBucket, OnFirebase) is an ERROR.
//     It says "ship this", and there is no source tree to produce the
//     artifact from.
//   - forge.OnHost / forge.BuildOnly is a WARNING. Neither makes a
//     shipping claim, so missing code is a likely oversight — often a
//     declaration written ahead of `forge scaffold frontend` — rather
//     than a contradiction.
//
// Frontends are the whole check because they are the only component kind
// with a source tree forge shells into. A service in KCL that names no
// code is already reported by lint's component-drift rule against
// deploy/kcl/workloads.k.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
)

// frontendDrift is one frontend the deploy graph declares with no code
// in this repository, accumulated across every environment declaring it.
type frontendDrift struct {
	name string
	// dir is the in-repo path the declaration resolves to, named in the
	// finding so the reader knows exactly where forge looked.
	dir string
	// envs are the environments declaring it, sorted, so the finding can
	// name deploy/kcl/{preprod,prod}/main.k rather than repeat itself
	// once per environment.
	envs []string
	// deployEnvs are the subset whose runtime ships the build.
	// Non-empty promotes the finding to an error.
	deployEnvs []string
	// deployTypes are the distinct shipping runtimes seen ("hosted",
	// "bucket", "firebase"), sorted — named in the evidence so the reader
	// knows what the project believes it is shipping. Collected
	// generically from the discriminator, so a new runtime flows through
	// without a change here.
	deployTypes []string
}

// shipsSomewhere reports whether any environment binds a shipping runtime,
// which is what separates the error case from the warning.
func (d frontendDrift) shipsSomewhere() bool { return len(d.deployEnvs) > 0 }

// CheckFrontendCode reports frontends the rendered deploy graph declares
// whose code this repository claims to hold and does not, so there is
// nothing for `forge build --target <name>` to build.
//
// SKIPs when the project declares no environments (--kind cli /
// library). Unlike the question this check used to ask, it needs no
// forge.yaml at all: the deploy graph and the filesystem are the two
// sources, and a project with neither a config nor a frontend has
// nothing to answer for.
//
// The Skip is DOWNGRADED to UNDETERMINED when any environment failed to
// render (see [renderScope.fold]) — with a hole in the deploy graph,
// "the question does not apply" is a stronger claim than the facts
// support.
func CheckFrontendCode(_ context.Context, env *Environment) CheckResult {
	return examineRendered(env, "frontend declarations", func(renders []envRender) CheckResult {
		drift := collectFrontendDrift(env.ProjectDir, renders)
		if len(drift) == 0 {
			return CheckResult{
				Status: StatusPass,
				Message: fmt.Sprintf("%d env(s): every KCL-declared frontend has code forge can build",
					len(renders)),
			}
		}

		var problems []string
		worst := StatusWarn
		shipping := 0
		for _, d := range drift {
			problems = append(problems, frontendDriftMessage(d))
			if d.shipsSomewhere() {
				worst = StatusFail
				shipping++
			}
		}

		summary := fmt.Sprintf("%d frontend(s) declared in KCL with no code in this repository", len(drift))
		if shipping > 0 {
			summary = fmt.Sprintf("%s — %d on a runtime that ships, so they claim to ship and cannot be built",
				summary, shipping)
		}
		return CheckResult{Status: worst, Message: summary, Evidence: strings.Join(problems, "\n")}
	})
}

// collectFrontendDrift folds every environment's rendered frontends into
// one finding per NAME. A frontend is reported once however many
// environments declare it — the fix is one directory, so a
// per-environment finding would be the same instruction repeated.
//
// The admission test is config.KCLFrontend.MissingInRepoCode: the
// declaration resolves to a path inside this project and there is no
// directory there. It is the exact complement of the OwnsFrontendCode
// test the codegen inventory uses, which is what keeps the two answers
// from drifting apart — a frontend forge generates into is by
// construction never reported here.
//
// Only environments that RENDERED are folded in — which is a property of
// the input rather than a filter here: examineRendered hands the check
// body nothing else. That matters because "this env does not declare the
// frontend" and "this env did not evaluate" are indistinguishable in a
// failed render, and the difference is the whole question. The unread
// environments are named by the caller's scope instead, where they
// downgrade the verdict.
func collectFrontendDrift(projectDir string, renders []envRender) []frontendDrift {
	byName := map[string]*frontendDrift{}
	var order []string

	for _, r := range renders {
		for _, fe := range r.frontends {
			kf := config.KCLFrontend{Name: fe.Name, Path: fe.Path, HasSource: fe.HasSource()}
			if !kf.MissingInRepoCode(projectDir) {
				continue
			}
			d, seen := byName[fe.Name]
			if !seen {
				dir, _ := kf.InRepoDir(projectDir)
				d = &frontendDrift{name: fe.Name, dir: dir}
				byName[fe.Name] = d
				order = append(order, fe.Name)
			}
			d.envs = append(d.envs, r.env)
			if fe.Runtime.Ships() {
				d.deployEnvs = append(d.deployEnvs, r.env)
				if t := fe.Runtime.Type; !contains(d.deployTypes, t) {
					d.deployTypes = append(d.deployTypes, t)
				}
			}
		}
	}

	sort.Strings(order)
	out := make([]frontendDrift, 0, len(order))
	for _, name := range order {
		d := byName[name]
		sort.Strings(d.envs)
		sort.Strings(d.deployEnvs)
		sort.Strings(d.deployTypes)
		out = append(out, *d)
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// frontendDriftMessage renders one finding in the batched,
// named-and-remediated style the sibling config check uses: name the
// entity, name the files that declare it, name where forge looked, and
// give the two ways out.
//
// The remedy is deliberately NOT "add it to forge.yaml". That key is the
// codegen inventory, adding a name to it creates no code, and for a
// frontend owned by another repository it is the harmful action. Either
// the source tree exists or the declaration should not.
func frontendDriftMessage(d frontendDrift) string {
	if !d.shipsSomewhere() {
		return fmt.Sprintf(
			"frontends[name=%s] declared in %s (not shipped: forge.OnHost / forge.BuildOnly) but %s/ does not exist — "+
				"`forge build --target %s` has no source tree to build. "+
				"Run `forge scaffold frontend %s`, or remove the KCL declaration.",
			d.name, kclPathsPhrase(d.envs), d.dir, d.name, d.name)
	}
	target := "a shipping runtime"
	if len(d.deployTypes) > 0 {
		target = fmt.Sprintf("the %s runtime", strings.Join(d.deployTypes, "/"))
	}
	return fmt.Sprintf(
		"frontends[name=%s] declared in %s with %s but %s/ does not exist — "+
			"it claims to ship and `forge build --target %s` has no source tree to build. "+
			"Run `forge scaffold frontend %s`, or remove the KCL declaration.",
		d.name, kclPathsPhrase(d.deployEnvs), target, d.dir, d.name, d.name)
}

// kclPathsPhrase renders the environments as a brace-expanded path so a
// frontend declared in four environments reads as one location rather
// than four.
func kclPathsPhrase(envs []string) string {
	if len(envs) == 1 {
		return fmt.Sprintf("deploy/kcl/%s/main.k", envs[0])
	}
	return fmt.Sprintf("deploy/kcl/{%s}/main.k", strings.Join(envs, ","))
}
