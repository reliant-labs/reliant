// File: internal/cli/lint/lint_frontend_deps.go
//
// What the frontend lanes (eslint and typecheck) do about a frontend's
// dependencies: they read them, and never install them unless the run asks.
//
// ── Why lint does not install ─────────────────────────────────────────────
//
// The lanes used to install a frontend with no node_modules first (#540), so
// a fresh worktree linted instead of reporting "could not run". That made
// `forge lint` mutate the checkout it was judging: a read-only inventory of a
// project ran `pnpm install`, and the package manager's output ("Done in
// 415ms using pnpm v12.15.0") leaked through --quiet, which promises only
// what failed plus the verdict. A linter is a gate; it reports on the tree it
// is given. Preparing the tree is `forge env up`'s, `forge build`'s and the
// CI workflow's job — each installs before it lints (the generated ci.yml
// installs every frontend in its setup steps; `task ci`'s deps-<frontend> rows
// run before its lint rows).
//
// So a frontend whose dependencies are not installed is a lane that could
// NOT run (lint_skip.go): the lane is named in the verdict with the exact
// install command, and — a skipped check is not a passed one — it fails the
// run the same way a missing buf or compiler does, in CI and locally alike,
// unless the run accepts it (--allow-skip / lint.allow_skip) or drops the
// frontend lanes outright (--skip-frontends). One policy for every lane that
// could not run, rather than a second, frontend-only, CI-only mode.
//
// `forge lint --install` keeps the old behaviour reachable: it installs what
// is missing (and refreshes a tree older than its lockfile) with the
// frontend's own package manager, frozen to the lockfile, before the lanes
// run. Its progress and the package manager's output go to stderr, and
// nowhere under --quiet; a failed install still reports the package
// manager's output, in the lane's could-not-run finding.
//
// A tree that IS installed but older than its lockfile or package.json is
// linted as it stands — failing a gate on an mtime heuristic would turn every
// branch switch red — with an info note naming the command that brings it
// current.
//
// ── Warn first (lint_rollout.go) ──────────────────────────────────────────
//
// Refusing to install turns a pipeline that relied on lint's install red —
// a scaffolded release.yml lints on a fresh runner with no install step — so
// the refusal rolls out like a new rule, under ruleFrontendNoInstall. While
// the project's pin is below the rule's GatesFrom (or lint.rules dials it to
// warn), lint still installs a frontend with no node_modules, as it did, and
// reports a warning carrying the rollout note that names the change to make.
// The install's output stays off stdout either way: stderr in a human run,
// nowhere under --quiet, which promises only failures and the verdict; a
// failed install still reports the package manager's output in the lane's
// could-not-run finding. From GatesFrom, or with lint.rules
// {forge-frontend-no-install: error}, lint refuses. `off` keeps installing
// and says nothing. An unpinned project, and forge's own tree, refuse: the
// rollout reads nothing in such a pin that says the project is behind.

package lint

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/mod/semver"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/frontenddeps"
	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/internal/linter/suppress"
)

// ruleFrontendNoInstall is the rollout of "forge lint does not install
// frontend dependencies". Its finding is the warning a run that still
// installed carries; once it gates, a missing node_modules is the lane's
// could-not-run finding instead.
const ruleFrontendNoInstall = "forge-frontend-no-install"

// frontendDepsPolicy is how one run's frontend lanes treat dependencies.
type frontendDepsPolicy struct {
	// install is `forge lint --install`: install missing dependencies (and
	// refresh stale ones) before a lane runs.
	install bool
	// implicit is the pre-rollout behaviour, while ruleFrontendNoInstall
	// does not gate for this project: install a frontend with NO
	// node_modules (nothing more), then report it with warn.
	implicit bool
	// warn, when set, is what an implicit install reports (nil: lint.rules
	// turned the rule off, so it installs silently).
	warn *implicitInstallWarning
	// out receives an install's progress and the package manager's output;
	// nil discards it (--quiet).
	out io.Writer
}

// implicitInstallWarning is the rollout's say on an implicit install: the
// grace note, when the pin is what keeps the rule from gating.
type implicitInstallWarning struct {
	grace *ruleGrace
}

// frontendDeps is the run's dependency policy.
func (rc *lintRunCtx) frontendDeps() frontendDepsPolicy {
	p := frontendDepsPolicy{install: rc.install, out: rc.installOut}
	if !rc.install {
		p.implicit, p.warn = implicitInstallRollout(lintRollouts, rc.cfg)
	}
	return p
}

// implicitInstallRollout decides whether lint still installs implicitly for
// the project cfg describes, and what it says when it does. lint.rules wins
// (error refuses, warn installs and warns, off installs silently); otherwise
// the pin decides, exactly as it softens a new rule's findings (graceFor).
func implicitInstallRollout(table map[string]ruleRollout, cfg *config.ProjectConfig) (bool, *implicitInstallWarning) {
	rules := effectiveRules(cfg)
	_, exact := rules[ruleFrontendNoInstall]
	_, wild := rules[suppress.Wildcard]
	if exact || wild {
		adjusted, keep := rules.Override(finding.Finding{Rule: ruleFrontendNoInstall, Severity: finding.SeverityError})
		switch {
		case !keep:
			return true, nil
		case adjusted.Severity == finding.SeverityError:
			return false, nil
		default:
			return true, &implicitInstallWarning{}
		}
	}
	ro, declared := table[ruleFrontendNoInstall]
	pin, known := comparablePin(cfg)
	if !declared || ro.GatesFrom == "" || !known {
		return false, nil
	}
	// The pin the project came from matters only once the pin itself is
	// past GatesFrom, so history is read only then.
	prev, prevKnown := "", false
	if semver.Compare(pin, ro.GatesFrom) >= 0 {
		prev, prevKnown = previousPin(cfg, pin)
	}
	g := graceFor(ro, pin, prev, prevKnown, cfg.ForgeVersion)
	if g == nil {
		return false, nil
	}
	return true, &implicitInstallWarning{grace: g}
}

// installFrontendDeps is lint's install: the frontend's own package manager,
// frozen to its lockfile. A variable so tests observe the decision without a
// package manager.
var installFrontendDeps = func(ctx context.Context, out io.Writer, name, dir string) error {
	return frontenddeps.EnsureTo(ctx, out, "[lint]", name, dir, "", true)
}

// frontendInstallMu serializes installs: the typecheck lane runs its
// frontends concurrently, and two members of one workspace install at the
// same root.
var frontendInstallMu sync.Mutex

func (p frontendDepsPolicy) runInstall(ctx context.Context, name, dir string) error {
	frontendInstallMu.Lock()
	defer frontendInstallMu.Unlock()
	return installFrontendDeps(ctx, p.out, name, dir)
}

// frontendDepsState is what a lane may do with a frontend's dependencies.
type frontendDepsState struct {
	// missing: no node_modules in the frontend or any parent directory, so
	// the lane could NOT run. reason completes "<frontend>: <reason>"; fix
	// is the command that installs them.
	missing     bool
	reason, fix string
	// note, when set, is an info line for a lane that runs anyway (a stale
	// tree, or a refresh --install could not complete).
	note string
	// warning, when set, is the rollout's finding for an implicit install
	// (ruleFrontendNoInstall); the lane adds it to its findings.
	warning *lintJSONFinding
}

// check decides, for the lane `tool` ("eslint", "typecheck"), whether the
// frontend at dir has its dependencies. root bounds the walk up to a
// workspace root that hoists them (anyAncestorHasNodeModules).
func (p frontendDepsPolicy) check(ctx context.Context, name, dir, root, tool string) frontendDepsState {
	// The node_modules the lane reads: the frontend's own, or — in a
	// workspace that hoists its members' dependencies — the nearest
	// ancestor's. Staleness is judged against THAT directory's manifests and
	// lockfile, and a refresh installs there: a hoisted member has no
	// node_modules of its own, so judging only the frontend's missed every
	// workspace-root lockfile change.
	nmDir, installed := nearestNodeModulesDir(dir, root)
	stale := installed && frontenddeps.Stale(nmDir)
	installDir := dir
	if !installed {
		installDir = frontenddeps.WorkspaceInstallDir(dir, root)
	}
	switch {
	case p.install && (!installed || stale):
		target := installDir
		if stale {
			target = nmDir
		}
		if err := p.runInstall(ctx, name, target); err != nil {
			if !installed {
				return frontendDepsState{
					missing: true,
					reason:  fmt.Sprintf("dependencies are not installed and installing them (--install) failed — %s did NOT run: %v", tool, err),
					fix:     npmInstallFix(target),
				}
			}
			return frontendDepsState{note: fmt.Sprintf("%s: refreshing the stale node_modules in %s (--install) failed, so %s ran against the installed tree: %v",
				name, filepath.ToSlash(nmDir), tool, err)}
		}
		installed = anyAncestorHasNodeModules(dir, root)
		stale = false
	case p.implicit && !installed:
		// The pre-rollout behaviour, exactly: install what is missing,
		// never refresh.
		if err := p.runInstall(ctx, name, dir); err != nil {
			return frontendDepsState{
				missing: true,
				reason:  fmt.Sprintf("dependencies are not installed and installing them failed — %s did NOT run: %v", tool, err),
				fix:     npmInstallFix(dir),
			}
		}
		installed = anyAncestorHasNodeModules(dir, root)
		if installed && p.warn != nil {
			w := p.warn.finding(name, dir)
			return frontendDepsState{warning: &w}
		}
	}
	if !installed {
		return frontendDepsState{
			missing: true,
			reason: fmt.Sprintf("dependencies are not installed (node_modules not found in %s or any parent directory) — %s did NOT run; forge lint does not install them",
				filepath.ToSlash(dir), tool),
			fix: depsNotInstalledFix(installDir),
		}
	}
	if stale {
		return frontendDepsState{note: fmt.Sprintf("%s: the node_modules in %s is older than its lockfile or package.json, so %s ran against the installed tree — %s",
			name, filepath.ToSlash(nmDir), tool, depsNotInstalledFix(nmDir))}
	}
	return frontendDepsState{}
}

// finding is the warning an implicit install reports: what lint did, that it
// will stop, and what to change before it does.
func (w implicitInstallWarning) finding(name, dir string) lintJSONFinding {
	when := "a later forge release"
	if w.grace != nil {
		when = w.grace.GatesFrom
		if w.grace.GatesAfter != "" {
			when = "the forge pin after " + w.grace.GatesAfter
		}
	}
	return lintJSONFinding{
		Severity: lintSevWarning,
		Rule:     ruleFrontendNoInstall,
		Message: fmt.Sprintf("%s: node_modules was missing, so forge lint installed it — from %s forge lint will stop installing frontend dependencies, and this lane will be one that could NOT run",
			name, when),
		FixHint: fmt.Sprintf("add `--install` to this `forge lint`, or an install step before it (%s; e.g. in .github/workflows/release.yml)",
			frontenddeps.InstallCommand(dir, "", true)),
		Grace: w.grace,
	}
}

// depsNotInstalledFix is the remediation when lint found no dependencies: the
// frontend's own install command, or the flag that has lint run it.
func depsNotInstalledFix(dir string) string {
	return npmInstallFix(dir) + " — or run `forge lint --install`, which installs them first"
}

// installOutput is where an install reports: stderr for a human run (stdout
// carries the verdict, and --json's report), nowhere under --quiet, which
// prints only what failed plus the verdict.
func installOutput(quiet bool) io.Writer {
	if quiet {
		return io.Discard
	}
	return os.Stderr
}
