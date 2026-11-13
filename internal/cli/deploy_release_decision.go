package cli

// The release decision as something an operator SEES before it costs them:
// the dry-run preview of it, and the confirmation a dirty cut on a shared env
// has to get before the build starts.
//
// WHY THIS EXISTS. On 2026-10-12 a bare `forge env deploy prod` began cutting
// and building a whole new release because the checkout counted as dirty (an
// untracked nested worktree). The `--dry-run` run just before it printed
// nothing about releases, so the preview did not predict the real run, and the
// operator learned about the build from its codegen output.
//
//   - decideDeployRelease (deploy_autoversion.go) is the ONE decision. The real
//     run and the preview both call it, so they cannot disagree.
//   - A dirty cut on a SHARED env asks first. A build from uncommitted bytes
//     is not reproducible and the release it cuts describes no commit, so the
//     operator confirms it (or passes --yes) before any build starts.

import (
	"context"
	"fmt"
	"io"
)

// envIsShared reports whether e deploys somewhere other than the developer's
// own machine: anything hosted, or a cluster workload on a cluster that is not
// a local dev cluster. Fail-safe: a cluster workload that names no cluster is
// shared, because an unknown destination must not be assumed to be a laptop.
func envIsShared(e *KCLEntities) bool {
	if e == nil {
		return false
	}
	if e.HasHosted() {
		return true
	}
	for _, w := range e.WorkloadsOn(RuntimeCluster) {
		if w.Runtime.Cluster == nil || !isLocalCluster(w.Runtime.Cluster.Cluster) {
			return true
		}
	}
	return false
}

// dirtyCutNeedsConfirmation is whether choice, a cut from uncommitted bytes,
// must be confirmed before building on this env. Rendering the env is the cost
// of the question, so it is asked only of a dirty cut; a render that fails
// answers yes (the build would fail on it, and refusing first is the cheaper
// way to find out).
func dirtyCutNeedsConfirmation(ctx context.Context, projectDir, envName string, choice deployReleaseChoice) bool {
	if choice.Reuse != nil || !choice.Dirty {
		return false
	}
	entities, err := RenderKCL(ctx, projectDir, envName)
	if err != nil {
		return true
	}
	return envIsShared(entities)
}

// gateDirtyCut asks before a dirty cut on a shared env builds anything.
//
// --yes and --approve are consent, as everywhere on this verb; --plan-only is
// the explicit request to cut and stop, so it proceeds. A terminal is
// prompted, defaulting to no. Anything else is refused (exit 5) BEFORE the
// build, where the plan gate would have refused it after the build had been
// paid for.
func gateDirtyCut(env string, choice deployReleaseChoice, c deployConfirm, out io.Writer) error {
	if c.Yes || c.Approve != "" || c.PlanOnly {
		return nil
	}
	question := fmt.Sprintf("Build and cut release %s for env %q from a checkout with uncommitted changes?", choice.Version, env)
	if c.Interactive {
		ask := c.prompt
		if ask == nil {
			ask = promptYesNo
		}
		ok, err := ask(question)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(out, "Cancelled — nothing was built.")
			return exitCodeError{code: exitPlanUnconfirmed, msg: "cancelled: nothing was built or cut"}
		}
		return nil
	}
	return exitCodeError{code: exitPlanUnconfirmed, msg: fmt.Sprintf(
		"refusing to build: env %s is shared and this checkout has uncommitted changes, so the release would describe no commit (dirty_cut_unconfirmed).\n"+
			"  Nothing was built, pushed or cut. There is no terminal to confirm on and no --yes was given.\n"+
			"  fix: commit the changes — a clean tree reuses an existing release for it where one exists;\n"+
			"  or deploy a release that already exists: forge env deploy %s <version>;\n"+
			"  or accept the dirty cut: forge env deploy %s --yes\n"+
			"  (preview this decision any time with: forge env deploy %s --dry-run)",
		env, env, env, env)}
}

// previewDeployDecision is `forge env deploy <env> --dry-run`'s answer to the
// question a preview exists for: what would the real run do about the release.
// It prints the line the real run prints, in the conditional.
//
// A preview that cannot reach the ledger says so and goes on: the manifests
// it was asked for do not depend on the answer, and the real run would fail
// at the same place.
func previewDeployDecision(ctx context.Context, envName string, f deployCmdFlags, report *deployReport) {
	// The real run refuses a scoped deploy with no version before deciding.
	if scope, _ := scopeOfDeploy(f); scope != "" {
		return
	}
	out := progressWriter(f.jsonOut)
	projectDir := projectDirForKCL()
	ledger, err := resolveReleaseLedger(ctx, projectDir, envName)
	if err != nil {
		fmt.Fprintf(out, "[deploy] release decision unavailable: %v\n", err)
		return
	}
	choice := decideDeployRelease(ctx, projectDir, envName, ledger)
	choice.announce(out, envName, true)
	needsConfirm := dirtyCutNeedsConfirmation(ctx, projectDir, envName, choice)
	if needsConfirm {
		fmt.Fprintf(out, "[deploy] env %s is shared, so a real run asks you to confirm this dirty build before it starts (or pass --yes)\n", envName)
	}
	report.setReleaseDecision(choice, needsConfirm)
}
