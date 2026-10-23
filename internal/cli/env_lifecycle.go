package cli

// Bundle.lifecycle — WHO APPLIES an environment.
//
// An env either is applied to DIRECTLY by forge, or is reconciled from a
// bundle by something else (the Flux path). Nothing else in a Bundle answers
// that: `control_plane` says whether anything is hosted, and the derived
// pkg/release EnvKind says what KIND of hosting. Neither is the apply path —
// a hosted env can still be a developer's throwaway cluster, and an env with
// no control plane at all can still be a real environment nobody should
// kubectl-apply into by hand.
//
// So the env DECLARES it, in one field, and this file is the Go half: the
// vocabulary, and the one predicate every consumer asks.
const (
	// lifecycleLocal is a developer's own cluster. Direct apply is the
	// point: the inner loop is `forge env up` against a cluster only this
	// machine can see.
	lifecycleLocal = "local"
	// lifecycleEphemeral is a throwaway per-run cluster (CI / e2e), created
	// and deleted inside one run. Routing it through a reconciler would add
	// a control loop with nothing to converge to.
	lifecycleEphemeral = "ephemeral"

	// applierForge is Bundle.applier = "forge": a REAL environment that forge
	// applies, by declaration, and that no delivery unit of has moved onto the
	// hub yet. It is a TRANSITIONAL state, not a destination: every real env,
	// the platform's own included, is moving onto build -> OCI bundle -> ledger
	// -> Flux, one delivery unit at a time (Bundle.hub_converges), and direct
	// apply narrows to dev and ephemeral envs. applier = "forge" is the
	// starting position of that move, the same declaration as
	// hub_converges = [].
	applierForge = "forge"
)

// DirectApplyAllowed reports whether forge may apply to this environment
// DIRECTLY, rather than recording a bundle for a reconciler to converge.
//
// True in two cases, and they are different reasons:
//
//  1. the env DECLARES `lifecycle = "local" | "ephemeral"` — it is a cluster
//     forge creates and can delete, so applying to it is the intended path.
//     KCL has already checked that claim against every cluster the env
//     targets, so a GKE context cannot reach here by declaring itself local;
//  2. the env TARGETS NO CLUSTER AT ALL. A host-only or compose-only env has
//     nothing to apply, so "may forge apply directly" is vacuously true.
//     Answering false here would describe such an env as needing a
//     reconciler, which is the opposite of true: there is no cluster for one
//     to reconcile against.
//
// An env that declares nothing AND targets a cluster is a REAL environment,
// and this returns false for it. That default is deliberate: an env whose
// author forgot to declare it is treated as production, not as scratch.
//
//  3. the env DECLARES who applies it: `applier = "forge"`, or
//     `hub_converges = [...]` for the units forge still applies. The cluster
//     is not scratch (lifecycle would be a false claim, and KCL checks it),
//     and the declaration is the env saying it is mid-move onto the hub, so
//     direct apply is allowed for now. It is a transitional allowance: it
//     shrinks as units move, and ends when the list names every unit.
//
// TODAY NOTHING REFUSES ON THIS. It drives a notice and the JSON
// projections. The refusal (F-REFUSE-DIRECT) will land for an env that
// declares none of the three, and because the predicate is already here and
// already correct, that change is a flag flip rather than a migration.
func DirectApplyAllowed(e *KCLEntities) bool {
	if e == nil {
		// No render, so no declaration and no target. Nothing is known to
		// need a reconciler, and the callers here are notice/projection
		// paths that must not invent one.
		return true
	}
	if e.Applier == applierForge || e.HubConverges != nil {
		return true
	}
	switch e.Lifecycle {
	case lifecycleLocal, lifecycleEphemeral:
		return true
	}
	return !kclEntitiesHaveK8sCluster(e)
}

// lifecycleNoticeLine is the ONE line a direct cluster apply prints for an
// env that declares neither a lifecycle nor an applier.
//
// It is a NOTICE, not a warning, and it changes no behaviour: the apply it
// annotates still runs, identically. Its whole job is to make the coming
// change visible at the place it will land, so the day direct apply is
// refused is not the day anyone first hears about it. It names the ways out:
// lifecycle for scratch clusters; applier or hub_converges for a real env that
// is mid-move onto the hub.
//
// Stdout is deliberate and it is safe: `forge env render`'s manifest stream
// and every --json document divert stdout for their duration (see
// runEnvRender and runDeployReported), so this cannot land in a YAML stream a
// kubectl is reading or ahead of a document a jq is parsing. The apply paths
// that print it already report their progress on stdout, and a notice about
// an apply belongs with the apply.
const lifecycleNoticeLine = "Note: this env declares none of `lifecycle` (a scratch cluster), `applier = \"forge\"` " +
	"(a real env forge applies until its units move) or `hub_converges` (the units the hub applies); direct apply to it " +
	"will be refused once F-REFUSE-DIRECT lands. Declare the one that is true"
