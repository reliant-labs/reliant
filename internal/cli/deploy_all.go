package cli

// `forge env deploy <env>` with NO version: the one command that ships the
// checkout (owner decision O-15).
//
// THE OWNER'S WORDS: "the whole point is that deploy should do all the
// deployment bits, including building and pushing the release (OCI configs,
// the actual docker images, etc)." What prompted it was hitting this on
// hounders prod, from the UI:
//
//	hosted env "prod" has no promoted release, so there is no digest to
//	deploy … fix: forge env build prod --release <version> --no-build
//	&& forge env deploy prod <version>
//
// Three commands, one of which (--no-build) cuts a release over digests that
// do not exist, to do the thing the operator already asked for.
//
// So, in order:
//
//  1. CHOOSE: reuse a release whose recorded provenance matches this checkout
//     and which still covers the env (deploy_autoversion.go) — then nothing
//     is built or cut, so a retried deploy never builds or cuts twice.
//     Otherwise:
//  2. BUILD at the current checkout, by exactly `forge env build`'s path —
//     images, static artifacts, and F-DECL's shape recording — PUSH, and CUT
//     a release under the auto-version rule.
//  3. PLAN with the existing promote-plan machinery, and print it.
//  4. CONFIRM (deploy_confirm.go) — nothing is written until somebody says yes.
//  5. PROMOTE, apply and wait, by the same path `forge env deploy <env> <v>`
//     takes.
//
// WHAT THIS REPLACED, AND WHY THAT IS RIGHT. A no-version deploy used to be a
// "spec-change deploy": re-apply the env's CURRENT binding, for when the KCL
// moved and the release did not. That behaviour is NOT lost — it is what
// `forge env deploy <env> <current-version>` does, which is how §8.5's "Apply
// config" button spells it. What is gone is the spelling, because it made the
// most obvious invocation of the verb the one that could not ship the code in
// front of you. Pre-1.0: the old path is deleted, not kept beside the new one.

import (
	"context"
	"fmt"
	"os"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/release"
)

// runDeployEverything is the no-version form.
func runDeployEverything(ctx context.Context, envName string, f deployCmdFlags) error {
	projectDir := projectDirForKCL()
	rollout := cluster.RolloutPolicy{
		Mode:     cluster.RolloutMode(f.rolloutMode),
		Timeout:  f.rolloutTimeout,
		FailFast: f.rolloutFailFast,
		Order:    f.rolloutOrder,
	}
	if err := rollout.Validate(); err != nil {
		return err
	}

	// The capacity pre-flight, before the build: a no-plan org must learn it
	// in seconds, not after the images are built and pushed. Read-only, and first of all.
	capacity, err := hostedCapacityPreflightForEnv(ctx, projectDir, envName, true, progressWriter(f.jsonOut))
	if err != nil {
		return err
	}

	// The ledger FIRST, before anything is built: it decides where the
	// release is cut and who applies the promotion, and an env whose
	// declaration cannot be resolved must fail before it burns a build.
	ledger, err := resolveReleaseLedger(ctx, projectDir, envName)
	if err != nil {
		return err
	}

	// Step 1 and 2. The version is decided from the checkout's provenance
	// BEFORE the build, so the build can push under it and the cut can
	// record what the push produced.
	//
	// UNDER --json THE BUILD'S OUTPUT GOES TO STDERR. `--json` promises
	// stdout carries exactly ONE document, and the build phase is a few
	// hundred lines of `[build] …` written with fmt.Printf — which would
	// land above the document and make it unparseable. Diverting
	// os.Stdout for the duration is the same mechanism runEnvRender uses
	// for the same reason; the build log stays fully readable on stderr.
	cut, err := func() (deployCutResult, error) {
		if f.jsonOut {
			real := os.Stdout
			os.Stdout = os.Stderr
			defer func() { os.Stdout = real }()
			return buildAndCutForDeploy(ctx, projectDir, envName, f, ledger)
		}
		return buildAndCutForDeploy(ctx, projectDir, envName, f, ledger)
	}()
	if err != nil {
		return err
	}

	// Steps 3-5 are the versioned deploy, verbatim: plan, confirm, promote,
	// apply, wait. That is the point — the no-version form decides WHICH
	// bytes and then hands over, so the two forms cannot diverge in how they
	// plan, confirm, compare-and-set, publish or gate.
	//
	// The apply rides the follow-through, INCLUDING a pure hosted env's
	// client-side publish, which followPromote now performs
	// (applyHostedPublish). Before O-15 that publish was reachable only from
	// the no-version form, so shipping a hosted env took two commands —
	// `deploy <env> <v> --no-wait` to record, then `deploy <env>` to
	// publish. Moving it into the follow-through is what lets ONE command do
	// it, and keeps the versioned form able to do it too rather than
	// stranding the publish on a spelling this change removes.
	p := f.promote
	p.version = cut.Version
	if cut.Reused {
		// A reused release was built by no step of THIS deploy, so it gets
		// what `forge env deploy <env> <version>` gives an existing release:
		// the hosted platform guard, and a bundle sealed over the release's
		// OWN pins. For a hosted ledger ensureHostedReleaseBundle below
		// writes it; any other ledger is written here, because no build
		// wrote it on the way.
		if err := checkReleaseHostedPlatforms(ctx, progressWriter(f.jsonOut), projectDir, envName, cut.Version, ledger.Releases); err != nil {
			return err
		}
		if !ledger.Hosted {
			if err := writeReleaseBundle(ctx, projectDir, envName, cut.Version, ledger, p.rerecordBundle, progressWriter(f.jsonOut)); err != nil {
				return err
			}
		}
	}
	if err := ensureHostedReleaseBundle(ctx, projectDir, envName, cut.Version, ledger, p.rerecordBundle, progressWriter(f.jsonOut)); err != nil {
		return err
	}
	return runPromote(ctx, cut.Version, envName, promoteOptions{
		Ledger:        ledger,
		Capacity:      capacity,
		JSON:          f.jsonOut,
		ProjectDir:    projectDir,
		Note:          p.note,
		Actor:         p.actor,
		ExpectCurrent: p.expectCurrent,
		Supersede:     p.supersede,
		Gates:         p.gates,
		Run:           p.run,
		// AutoVersion is stated so the gate's refusal and its --plan-only
		// line can say "release X was cut and pushed; approving it needs no
		// rebuild" — without it a CI author reads "pass --yes" and re-runs
		// the no-version form, paying for the build twice.
		Confirm: newDeployConfirm(p, cut.Version),
		// The SERVER-BINDING half (O-13). The bundle this plan is
		// computed against was written by step 1's build, which is why
		// the plan is resolved HERE and not before it.
		DeployPlan: planForDeploy(ctx, projectDir, envName, cut.Version, f.targets, ledger, progressWriter(f.jsonOut)),
		Approval: deployApproval{
			Digest:               p.approve,
			AcknowledgedFindings: p.acknowledgeDestructive,
		},
		Follow: &promoteFollowOptions{
			NoWait:       p.noWait,
			Wait:         p.wait,
			jsonOut:      f.jsonOut,
			Timeout:      p.timeout,
			FailFast:     p.failFast,
			skipHubCheck: p.skipHubCheck,
			clientDeploy: deployOptions{
				imageTag:      f.tag,
				namespace:     f.namespace,
				targetArch:    f.targetArch,
				prune:         f.prune,
				targets:       f.targets,
				skipFrontend:  f.skipFrontend,
				frontendsOnly: f.frontendsOnly,
				skipPreflight: f.skipPreflight,
				noDigest:      f.noDigest,
				rollout:       rollout,
			},
		},
	})
}

// deployCutResult is the release a no-version deploy will promote.
type deployCutResult struct {
	Version string
	// Reused is true when an existing release's recorded provenance matched
	// this checkout and it still covers the env, so nothing was built or
	// cut.
	Reused bool
}

// buildAndCutForDeploy runs the choose-build-cut half: decide from the
// checkout's provenance whether a release already holds it, and when none
// does, build and push by `forge env build`'s own path and cut.
//
// A REUSED RELEASE IS NOT REBUILT. chooseDeployRelease asks the registry for
// every image the release pins, which answers "are the bytes still there"
// without producing new ones; a rebuild would (see its comment — 2026-10-09,
// prod). The choice is printed first, either way.
func buildAndCutForDeploy(ctx context.Context, projectDir, envName string, f deployCmdFlags, ledger envLedger) (deployCutResult, error) {
	choice := decideDeployRelease(ctx, projectDir, envName, f.renderOptions, ledger)
	choice.announce(os.Stdout, envName, false)
	if dirtyCutNeedsConfirmation(ctx, projectDir, envName, choice) {
		if err := gateDirtyCut(envName, choice, *newDeployConfirm(f.promote, choice.Version), progressWriter(f.jsonOut)); err != nil {
			return deployCutResult{}, err
		}
	}
	if choice.Reuse != nil {
		return deployCutResult{Version: choice.Reuse.Version, Reused: true}, nil
	}

	// The BUILD, by `forge env build <env> --release <version>`'s own path:
	// --release implies --push and implies a docker build, and runBuild is
	// what records F-DECL's declaration, builds every artifact the env
	// declares, pushes each to its own declared reference, and cuts.
	opts := buildOptions{
		env:         envName,
		outputDir:   "bin",
		buildTarget: "all",
		parallel:    true,
		buildDocker: true,
		// pushIfDeclared, NOT push. Both push every image whose workload
		// declares a pushable reference; they differ on an env that declares
		// NONE, and that difference decides whether this verb works at all
		// for a whole class of project.
		//
		// --push treats "nothing to push" as a usage error, which is right
		// for `forge env build --push`: the author asked to publish and
		// there is nothing to publish. It is wrong here. A hosted env whose
		// images CI pushes — a third-party image, a workload built in
		// another pipeline — declares no reference forge builds, and such an
		// env is perfectly deployable: its release records the declared
		// images' digests (harvestHostedBackendArtifacts) and the deploy
		// pins them. Refusing it would mean `forge env deploy <env>` could
		// not ship an env that `forge env deploy <env> <v>` ships fine.
		//
		// So: push what is declared, and let an env with nothing to push
		// proceed to the cut. This is the mode `forge env up` already uses,
		// for the same reason.
		pushIfDeclared: true,
		// The deploy ran the capacity pre-flight before it got here.
		capacityChecked: true,
		release:         choice.Version,
		targetArch:      f.targetArch,
		targets:         f.targets,
		run:             f.promote.run,
	}
	if err := runDeployBuild(ctx, opts); err != nil {
		return deployCutResult{}, fmt.Errorf("build env %s for deploy: %w", envName, err)
	}
	return deployCutResult{Version: choice.Version}, nil
}

// runDeployBuild is runBuild, as a var so a test can state the build's
// outcome instead of needing a toolchain, a registry and a docker daemon.
//
// A SEAM AND NOT A MOCKED INTERFACE, because the thing under test is the
// ORDER — build, then cut, then plan, then confirm, then promote — and the
// order is observable from what the seam records. A test that replaced the
// whole build-and-cut half would be asserting its own structure.
var runDeployBuild = runBuild

// errUnknownReleaseForDeploy is `forge env deploy <env> <v>` naming a version
// nobody cut.
//
// THE FIX IS NEVER --no-build. That is what the owner hit: `forge env build
// <env> --release <v> --no-build` cuts a release over digests an earlier push
// left in .forge/state, and a version that was never cut has none — so the
// advice produced a release with no images in it, or failed its own
// completeness gate. A version that does not exist cannot be conjured from
// state that does not exist either. The honest remedy is the verb that builds
// it: `forge env deploy <env>`.
func errUnknownReleaseForDeploy(envName, version string, known []release.Release) error {
	msg := fmt.Sprintf("env %q: release %s was never cut, so there is nothing to deploy.\n", envName, version)
	if len(known) > 0 {
		msg += "  Releases this project has: " + releaseVersionList(known) + "\n"
	}
	msg += fmt.Sprintf("  fix: ship the current checkout — forge env deploy %s\n"+
		"       (builds, pushes, cuts a release named for this tree, then plans and deploys it)\n"+
		"  or name one of the releases above: forge env deploy %s <version>", envName, envName)
	return exitCodeError{code: exitWrong, msg: msg}
}

// releaseVersionList is the newest few versions, for the error above. Bounded
// because a project with hundreds of releases should not print all of them
// into a message whose subject is one typo.
func releaseVersionList(known []release.Release) string {
	const max = 5
	out := ""
	for i, rel := range known {
		if i == max {
			out += fmt.Sprintf(", … (%d more)", len(known)-max)
			break
		}
		if i > 0 {
			out += ", "
		}
		out += rel.Version
	}
	return out
}
