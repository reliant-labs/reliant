// Package cluster owns the render-KCL → kubectl-apply → wait-rollouts
// pipeline that `forge env deploy`, `forge cluster reload`, and the
// deploy phase of `forge env up` all execute. Before this package existed,
// the pipeline was duplicated across three call sites:
//
//   - runDeploy           (internal/cli/deploy.go)
//   - runDevClusterReload (internal/cli/dev_cluster.go)
//   - reconcileCluster    (internal/cli/up.go)
//
// All three drove the same kubectl invocations against the same KCL
// renderer; they differed only in the pre-flight (context guard vs
// context pin), the dev-cluster bootstrap (deploy-only), and the
// per-call defaults (prune, host-skip, one-shot Job wait).
//
// This package intentionally does NOT own:
//
//   - the kubectl-context guard (verifyKubectlContext) — that's the
//     deploy command's affordance and applies to non-dev envs the dev
//     cluster reload doesn't reach;
//   - the k3d cluster bootstrap (ensureDevCluster, buildAndPushLocal)
//     — that's a deploy-time concern for the dev env that the reload
//     deliberately skips;
//   - the typed KCLEntities schema (still in internal/cli/).
//
// The shape mirrors internal/hostlaunch: a small Opts struct
// expressing the differences between call sites, plus a single Apply
// entry point and the kubectl/KCL helpers exported for callers that
// need them piecewise.
//
//forge:lint-disable-next-line forge-exclude-contract-outbound-io: the oras registry probe (RegistryImageChecker, preflight_registry.go) already sits behind the consumer-side ImageChecker/CredentialedImageChecker interfaces (preflight.go); the kubectl/KCL calls are the pipeline itself, and a contract.go split of the whole package is CONTRACTS follow-up F2
//forge:exclude-contract: CLI-internal deploy-pipeline glue (render KCL → kubectl apply → wait rollouts); its exported methods are the pipeline's own API, not a bootstrap-wired Service
package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/pkg/deploy"
	"github.com/reliant-labs/forge/pkg/deploy/openbao"
	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/stsresize"
)

// RolloutMode is what Apply does after the manifests land.
type RolloutMode string

const (
	// RolloutWait waits for every managed Deployment (and one-shot Job) and
	// FAILS the deploy if any of them does not become ready. The default,
	// and the only correct behavior for a real environment: "kubectl apply
	// exited 0" says the API server accepted the objects, not that anything
	// is running.
	RolloutWait RolloutMode = "wait"

	// RolloutWarn waits and REPORTS failures without failing the deploy.
	// For the local inner loop (`forge cluster reload`), where a
	// half-rolled cluster is something you look at rather than a build
	// result — and where the previous Deployment is still serving anyway.
	// Never appropriate for CI or a cloud env.
	RolloutWarn RolloutMode = "warn"

	// RolloutSkip applies and returns immediately, waiting for nothing.
	// For "push the manifests and let the cluster converge" — a
	// fire-and-forget deploy, or a huge env where the caller does its own
	// verification afterwards.
	RolloutSkip RolloutMode = "skip"
)

// RolloutPolicy governs the post-apply wait: whether to wait at all,
// whether a stuck rollout fails the deploy, how long to give it, and
// whether to stop at the first failure or report every one.
//
// WHY THIS TYPE EXISTS
// ====================
//
// The wait used to be unconditional and its result was DISCARDED — a
// failed rollout printed "Warning: rollout for x" and Apply returned nil,
// so `forge env deploy` exited 0 with nothing running. That is the worst
// available failure mode: a green deploy over a broken environment. It is
// not hypothetical — a control-plane prod deploy reported success while
// three Deployments sat on a stale image for hours, and the CI pipeline
// that noticed it had to re-implement the whole wait in shell, correctly,
// beside the forge call that was silently swallowing it.
//
// So: waiting is a POLICY with a safe default, not a hardcoded gesture.
type RolloutPolicy struct {
	// Mode selects wait / warn / skip. Empty means RolloutWait.
	Mode RolloutMode

	// Timeout bounds EACH resource's wait (per Deployment, per Job), not
	// the whole set — a 12-service env is not 12x more likely to be broken
	// than a 1-service one, and a shared budget would make the last
	// service's verdict depend on how slow the first was.
	//
	// Zero means DefaultRolloutTimeout. The old hardcoded 60s was a
	// local-k3d value applied to cloud: a cold cloud rollout that pulls a
	// fresh image routinely exceeds a minute, so it produced spurious
	// failures on exactly the deploys that mattered most.
	Timeout time.Duration

	// FailFast stops at the FIRST resource that fails instead of waiting
	// for the rest.
	//
	// Default (false) is deliberate: when a deploy goes wrong you usually
	// want the whole picture — "these 3 of 12 never came up" — and the
	// remaining waits cost nothing on a healthy resource, because a ready
	// Deployment returns immediately. Turn it on when the failures are
	// expected to cascade and you would rather see the first one now than
	// wait out eleven timeouts.
	FailFast bool

	// Order, when non-empty, is a prefix ordering for the Deployment wait:
	// these names are awaited first, in this order, before everything else
	// (which follows in its natural order).
	//
	// This is a WAIT ordering, not an apply ordering — kubectl applies the
	// stream in one shot and Kubernetes converges concurrently, so forge
	// cannot serialize the rollouts themselves without lying about what it
	// controls. What it CAN do is decide what to report first, which is
	// what makes a phased deploy legible: put the database migration or the
	// API server first and its failure surfaces before a dozen dependents
	// time out waiting on the thing that was already broken.
	//
	// Names not present in the namespace are ignored, so an ordering can
	// name a resource a --target filter excluded without erroring.
	Order []string
}

// DefaultRolloutTimeout bounds each resource's readiness wait when a
// policy does not set one. Five minutes covers a cold cloud rollout that
// pulls a fresh image on a new node; the previous 60s did not.
const DefaultRolloutTimeout = 5 * time.Minute

// Normalize fills in the zero value's defaults: wait-and-fail, with
// DefaultRolloutTimeout. Keeping this in one place is what lets every
// caller leave the field unset and still get the safe behavior.
func (p RolloutPolicy) Normalize() RolloutPolicy {
	if p.Mode == "" {
		p.Mode = RolloutWait
	}
	if p.Timeout <= 0 {
		p.Timeout = DefaultRolloutTimeout
	}
	return p
}

// Validate rejects an unrecognized mode.
//
// A typo'd `--rollout=wan` must not silently fall through to some default:
// the whole failure class this policy exists to remove is "the wrong
// behavior was chosen quietly". A caller that asked for something forge
// does not understand gets an error naming the valid values, not a guess.
func (p RolloutPolicy) Validate() error {
	switch p.Mode {
	case "", RolloutWait, RolloutWarn, RolloutSkip:
		return nil
	default:
		return fmt.Errorf("unknown rollout mode %q: want one of %q, %q, %q",
			p.Mode, RolloutWait, RolloutWarn, RolloutSkip)
	}
}

// classifyApplyResult decides what an apply error MEANS under this policy,
// and is the single place an incomplete apply is graded.
//
// An *IncompleteApplyError — objects that were rendered but never reached
// the cluster — is the same class of problem as a Deployment that never
// became ready, so it is graded by the same policy rather than by a knob
// of its own: fatal under the default RolloutWait, reported-and-tolerated
// under RolloutWarn, where a half-converged cluster is something you look
// at rather than a build result.
//
// RolloutSkip does NOT exempt it. Skip means "do not WAIT for the cluster
// to converge", which is a statement about time; an object that was
// rejected is never going to converge no matter how long nobody waits, and
// it is exactly the evidence a fire-and-forget deploy has no other way to
// get. A caller who asked forge not to wait still asked it to apply.
//
// Every other error passes through untouched: a genuine apply failure
// (a bad manifest, an unreachable API server) is fatal in every mode, and
// warn mode was never a licence to ignore those.
func (p RolloutPolicy) classifyApplyResult(err error) error {
	var incomplete *IncompleteApplyError
	if !errors.As(err, &incomplete) {
		return err
	}
	if p.Mode == RolloutWarn {
		fmt.Printf("Warning: %v\n", incomplete)
		return nil
	}
	return err
}

// waitRank is name's position in Order — its first occurrence — or len(Order)
// for a name Order does not mention, which sorts it after every named one. A
// stable sort by rank is the whole of the ordering rule: anything named in
// Order comes first, in Order's sequence, and the remainder follows in its
// original order. Unknown names in Order are simply never matched. Ranking
// rather than filtering a name list is what lets the wait order every
// cluster's Deployments together (see orderWaitQueue) without collapsing two
// clusters' same-named Deployments into one.
func (p RolloutPolicy) waitRank(name string) int {
	for i, n := range p.Order {
		if n == name {
			return i
		}
	}
	return len(p.Order)
}

// ApplyOpts expresses the differences between the three existing call
// sites. Every field has a sensible zero value so callers that don't
// care about a knob can leave it unset.
type ApplyOpts struct {
	// MainK is the path to deploy/kcl/<env>/main.k — the KCL entrypoint
	// that renders the cluster manifests. Required.
	MainK string

	// ImageTag is the value bound to KCL's `image_tag` -D variable.
	// Required (callers default to gitShortSHA at the call site).
	ImageTag string

	// ImageDigests is the per-image content-addressed digest map bound to
	// KCL's `image_digests` -D variable. When set, each rendered service's
	// manifest image resolves to ITS image's digest rather than the env-wide
	// ImageTag — the structural fix for a multi-image env pinning every
	// service to one digest.
	//
	// Keys are BOTH the bare image name (`reliant`) and the tag-qualified
	// name the build pushed (`reliant:e2e`), because the render looks up
	// whichever one the workload declares: an unpinned image takes the env
	// tag and is keyed bare, while an image that pins its own tag must match
	// a digest captured for THAT tag or stay unpinned. A tag-pinned image
	// renders as `<image>:<tag>@sha256:…`.
	//
	// May be nil/empty (the no-digest path), in which case every image stays
	// on ImageTag, byte-identical to before.
	ImageDigests map[string]string

	// Namespace is the namespace this apply WAITS in and prunes: the
	// rollout wait lists Deployments and Jobs here, and a Job naming no
	// namespace of its own is waited for here. Required.
	//
	// It is NOT a render input when Render is set. A deploy spanning several
	// namespaces applies one group per (cluster, namespace), and re-rendering
	// the env once per group with that group's namespace bound to KCL's
	// `namespace` option changes what the env DECLARES — every override and
	// URL keyed on the env namespace — so a group's render could disagree
	// with `forge env render` about which objects exist. Without Render (the
	// single-group callers: cluster reload, a direct apply) it is also the
	// namespace the env is rendered with.
	Namespace string

	// Render, when set, supplies the env's manifest stream instead of
	// RenderManifests(Namespace, …). A deploy renders the env ONCE, with the
	// env namespace `forge env render` uses, and every group's apply
	// partitions that one stream by cluster (ClusterScope) — the groups
	// differ in where they apply and wait, never in what the env declares.
	Render func(ctx context.Context) (string, error)

	// EnvConfigKV is the per-env config map projected as additional
	// `-D key=value` bindings to KCL. May be nil — the dev cluster
	// reload doesn't project per-env config (it would force a re-deploy
	// pipeline rebuild, defeating the inner-loop purpose).
	EnvConfigKV map[string]string

	// DryRun skips kubectl apply and prints the rendered manifests
	// instead. With DryRunFramed, the output is wrapped in
	// "--- Generated Manifests (dry-run) ---" / "--- End Manifests ---"
	// markers (the forge env deploy convention). Without it, raw manifests
	// are printed (the forge cluster reload convention).
	DryRun       bool
	DryRunFramed bool

	// Prune deletes forge-managed Deployments in the namespace that the
	// just-applied KCL render no longer produces. Opt-in — pruning is
	// destructive (see deploy.go's pruneOrphanDeployments docstring).
	Prune bool

	// Project is the forge project name. With Env it names the owner of
	// the CRDs this apply renders (CRDOwner), which is what makes CRD
	// pruning provable. Empty disables the CRD stamp and prune.
	Project string
	// PruneCRDs deletes the CRDs this (project, env) previously rendered
	// and no longer does, provided no custom resource of them remains —
	// see crd_prune.go. It runs only on a FULL apply: never with Targets,
	// never on a dry run. The env up and deploy paths set it, because a
	// CRD the render dropped is otherwise served forever.
	PruneCRDs bool

	// Rollout governs what the post-apply wait DOES, and — critically —
	// whether a rollout that never becomes ready fails the deploy.
	//
	// The zero value is RolloutPolicy{}, which Normalize() turns into the
	// safe default: wait for every managed Deployment, and FAIL if any of
	// them does not become ready. See RolloutPolicy.
	Rollout RolloutPolicy

	// Quiet suppresses the section-header banners ("Applying
	// manifests...", "Waiting for rollouts...") and emits the matching
	// per-resource warnings in the bare format ("Warning: <msg>" with
	// no leading indent) — the shape `forge cluster reload` used
	// pre-extraction. Off by default; the deploy and up call sites
	// keep the framed banners.
	Quiet bool

	// Env is the environment name (e.g. "dev", "dev-host", "prod")
	// passed to KCL as `-D env=<env>`. User main.k files can read it via
	// `option("env")` to conditionally include manifests — typical use
	// is skipping in-cluster infra (NATS, Temporal, LiteLLM) on dev-host
	// envs where docker-compose provides the same services.
	Env string

	// Context, when non-empty, is the kubectl context every kubectl
	// invocation in the apply/wait path runs against — passed as
	// `--context <ctx>` per command rather than mutating the global
	// active context (`kubectl config use-context`). This is what makes
	// concurrent multi-cluster `forge env deploy` safe: two deploys sharing
	// one kubeconfig but targeting different clusters no longer race on
	// the single global context. Empty = use kubectl's current/default
	// context (unchanged for single-cluster users).
	Context string

	// Targets, when non-empty, is the EXCLUSIVE set of KCL-declared service
	// GROUPs (`app.kubernetes.io/name` values) this apply keeps. The whole
	// env bundle is still rendered (KCL renders the env as a unit), but after
	// RenderManifests and before KubectlApply the multi-doc YAML is filtered
	// to EXACTLY the manifests whose group ∈ Targets — and EXACTLY the helm
	// charts whose Name ∈ Targets are rendered. The filter is purely
	// mechanical: it includes nothing implicitly. There is no shared-base
	// auto-keep, and no synthetic group: the env-shared manifests (Namespace,
	// ConfigMap, RuntimeClass, NetworkPolicy) and bundle-level
	// `additional_manifests` carry NO group, so they apply ONLY on a bare
	// deploy and are NEVER selected by a service Target. To make a manifest
	// ride a service's Target, declare it on that service's `manifests`.
	// Empty Targets means "apply EVERYTHING" — every manifest and every
	// declared platform dep (the full declarative reconcile). See
	// SelectManifestsByGroup and selectHelmChartsByGroup.
	Targets []string

	// HelmCharts are the env's declared platform dependencies, rendered
	// with helm-as-a-RENDERER (helm template --skip-crds) and folded into
	// THIS apply stream. Each is a renderable whose NAME is its GROUP — the
	// SAME exclusive Targets filter selects it: a chart is rendered + applied
	// iff (no Targets) OR (its Name ∈ Targets), the identical rule every other
	// manifest obeys (each chart's manifests are stamped
	// `app.kubernetes.io/name = Name`). There is NO chart opt-in special case
	// — a bare `forge env deploy <env>` (no Targets) reconciles every declared
	// platform dep too. Apply renders each selected chart, stamps every
	// manifest with its group, and applies it in CRD-first order (the chart's
	// forge-supplied CRDs → wait Established → the chart's controllers) so the
	// apply leaves CRDs Established + controllers Deployed. Empty => no
	// platform deps. See helm.go.
	HelmCharts []HelmChartSpec

	// ClusterScope, when non-nil, scopes the rendered env bundle to ONE
	// deploy group's cluster before applying — declared-cluster-only
	// multi-cluster routing. KCL renders the whole env as a unit (every
	// service's manifests in one stream), but each manifest must land ONLY
	// on the cluster of its OWNING service (identified by its
	// `app.kubernetes.io/name` label), and no other cluster may receive it.
	// Without this, every group applied the entire bundle to its own
	// `--context`, so a two-cluster env cross-contaminated both clusters
	// (the secondary got the whole stack and hard-failed on missing CRDs).
	// ScopeManifestsToGroup does the per-doc partition by owner; nil leaves
	// the stream untouched (the single-cluster path, byte-identical to the
	// pre-scoping behaviour). See ScopeManifestsToGroup for the ownership
	// rule.
	//
	// nil does NOT mean "write everything to Context": an object stamped
	// for a different cluster (ClusterRoutingLabel) is refused, scoped or
	// not — see refuseForeignClusterDocs.
	ClusterScope *GroupScope

	// OnStream, when non-nil, is handed the FINAL manifest stream — after
	// the --target filter and the multi-cluster scope, with every rendered
	// chart folded in — immediately before it is either applied or printed
	// as a dry run.
	//
	// This is the only place that stream exists. A caller cannot reproduce
	// it by re-rendering: it would have to re-apply both filters and
	// re-template every chart, and a second implementation of that is free
	// to disagree with this one about what ships. Reporting it from here
	// means a `--dry-run` preview and a real apply describe the same bytes
	// by construction.
	//
	// Called for dry runs too, BEFORE the early return — previewing what
	// would be applied is the primary use. Purely observational: the
	// callback's result is ignored and it cannot affect the apply.
	OnStream func(manifests string)

	// SkipUnits are the delivery units whose objects this apply LEAVES to the
	// hub: Bundle.hub_converges, resolved by the caller. The objects are
	// dropped from what is sent and their Deployments from what is awaited,
	// and nothing else changes: pruning still reads the full stream, because
	// pruning from a filtered one would delete every skipped Deployment as an
	// orphan. See delivery_unit_filter.go.
	SkipUnits []string

	// OnRollout, when non-nil, receives one observation per resource whose
	// readiness was awaited, as each verdict arrives.
	//
	// Apply's own return value stays the aggregate error, which is what
	// fails the deploy; this reports the per-resource detail that the
	// aggregate necessarily loses — in particular WHICH resource, and
	// whether its budget expired rather than a failure being observed. See
	// rollout_observer.go for why that third state cannot be reconstructed
	// from the outside.
	//
	// Not called under RolloutSkip: forge asks the cluster nothing in that
	// mode, so it has nothing to report, and inventing "not waited" entries
	// here would mean synthesizing observations Apply never made.
	OnRollout func(RolloutObservation)
}

// GroupScope describes how to filter the env's rendered manifest stream
// down to ONE deploy group's cluster. It is the input to
// ScopeManifestsToGroup, applied by Apply before the kubectl apply when
// ApplyOpts.ClusterScope is set.
//
// Routing is DECLARED-CLUSTER-ONLY: there is no "primary cluster". A
// manifest lands on the cluster of its OWNING service, identified by the
// service's `app.kubernetes.io/name` label, which forge stamps on every
// workload AND on every per-service owned manifest (forge.Service.manifests).
// See ScopeManifestsToGroup for the precise per-document keep/drop rule.
type GroupScope struct {
	// Cluster is THIS group's cluster name (forge.K8sCluster.cluster). It
	// is the value a manifest's first-class `forge.dev/cluster` routing
	// label is matched against: a manifest carrying that label lands on
	// this group iff the label equals Cluster, and is dropped otherwise —
	// no app-label indirection. Empty disables the first-class match (the
	// stream is routed purely by OwnApps/OtherApps, the pre-existing
	// behaviour). See ClusterRoutingLabel and ScopeManifestsToGroup.
	Cluster string

	// OwnApps is the set of `app.kubernetes.io/name` values belonging to
	// THIS group's services — their workloads (Deployment / Service /
	// per-service RBAC / HPA) AND the raw manifests those services own (a
	// CRD, env-level infra pinned to this cluster via an image-less infra
	// service). All carry the service's app label. These land on this
	// group's cluster.
	OwnApps map[string]struct{}

	// OtherApps is the set of app-name labels owned by OTHER k8s groups —
	// services that target a DIFFERENT cluster. These are dropped from this
	// group so a manifest never lands on a cluster its owner doesn't declare.
	OtherApps map[string]struct{}
}

// AppNameLabel is the KCL-declared GROUP key — the single `--target`
// selector. The group is ALWAYS the SERVICE: forge's KCL renderer stamps it
// with the service/component name on workloads / RBAC (`_managed_labels`,
// kcl/lib/services.k, kcl/lib/rbac.k), gateways/routes, and on a service's
// owned manifests (`Service.manifests`, which inherit THAT service's name).
// Env-shared resources that belong to no service — Namespace, ConfigMap,
// RuntimeClass, NetworkPolicy, and bundle-level `additional_manifests` —
// carry NO `app.kubernetes.io/name`. There is no synthetic env/namespace
// group.
//
// SelectManifestsByGroup is therefore a pure mechanical include filter —
// keep iff group ∈ targets — with no Go-side "always keep shared infra"
// policy. An ungrouped manifest matches no service `--target`, so it applies
// only on a bare `forge env deploy` (no `--target`). To make a manifest ride a
// service's `--target`, declare it on that service's `manifests`.
const AppNameLabel = "app.kubernetes.io/name"

// WorkloadLabel is forge's OWN routing key. It names the workload that owns a
// manifest forge did not build itself — a `RenderedWorkload.manifests` entry
// (kcl/lib/labels.k owner_labels) or a helm chart's output (stampAppLabel) —
// and it takes priority over AppNameLabel when present. See ManifestGroup.
//
// It exists because AppNameLabel is not forge's to take. Chart output and
// hand-written controllers set `app.kubernetes.io/name` for their own reasons,
// most often as the key a Service or DaemonSet SELECTOR matches on. Forcing it
// to the owning workload's name breaks those selectors; deferring to it lets
// the manifest's own label pick which `--target` ships it (control-plane's
// kata pre-pull DaemonSet fell out of `--target kata-prepull` that way). A key
// nothing else reads resolves both without choosing a loser.
//
// Objects forge's own builders render carry no WorkloadLabel: their
// AppNameLabel already is the workload, and adding a second label to every pod
// template would roll every Deployment in every project for no routing gain.
const WorkloadLabel = "forge.dev/workload"

// ManifestGroup is the deploy GROUP a manifest belongs to — the value
// `--target` selects on and multi-cluster routing attributes by. WorkloadLabel
// when the manifest carries one, else AppNameLabel, else "" (an env-shared
// object that belongs to no workload).
//
// It is the ONE reader of that fact. SelectManifestsByGroup,
// ScopeManifestsToGroup, `forge env render --list`, the `--target` vocabulary
// and doctor's routing model all call it, so the name `--list` prints for an
// object is by construction the name `--target` accepts and routes by.
func ManifestGroup(labels map[string]string) string {
	if g := labels[WorkloadLabel]; g != "" {
		return g
	}
	return labels[AppNameLabel]
}

// ManifestGroups returns every distinct non-empty ManifestGroup in a
// `---`-separated stream, sorted. It is the manifest half of the `--target`
// vocabulary: every name an object in the rendered env is attributed to.
func ManifestGroups(manifests string) []string {
	seen := map[string]struct{}{}
	for _, doc := range splitDocs(manifests) {
		m, ok := parseDoc(doc)
		if !ok {
			continue
		}
		if g := ManifestGroup(m.Metadata.Labels); g != "" {
			seen[g] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for g := range seen {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// ClusterRoutingLabel is the FIRST-CLASS per-manifest cluster-attribution
// key. forge's KCL gateway/route builders stamp it
// (`forge.dev/cluster: k3d-<name>`) when an ingress entity (Gateway /
// HTTPRoute / GRPCRoute) declares `cluster = <forge.Cluster>` — the
// builder denormalizes the referenced Cluster's kubectl CONTEXT
// (`k3d-<name>`) into the label, which is exactly the value
// GroupScope.Cluster (== forge.K8sCluster.cluster, the kubectl context)
// is matched against. ScopeManifestsToGroup reads it directly: a manifest
// carrying this label routes to the named cluster ONLY, no
// `app.kubernetes.io/name` indirection. It is the
// replacement for the older label-piggyback trick (stamping an unrelated
// service's app label so the manifest rode that service's group routing).
// A manifest WITHOUT this label still routes by app label exactly as
// before, so existing consumers are unaffected.
const ClusterRoutingLabel = "forge.dev/cluster"

// reportRolloutListFailure handles the case where the rollout wait cannot
// enumerate Deployments at all.
//
// Failing to LIST is not the same as finding nothing wrong: it means the wait
// never ran, so under a failing policy it must not pass silently. Only
// RolloutWarn downgrades it to a warning.
func reportRolloutListFailure(lerr error, quiet bool, mode RolloutMode) error {
	indent := "  "
	if quiet {
		indent = ""
	}
	fmt.Printf("%sWarning: list deployments: %v\n", indent, lerr)
	if mode == RolloutWarn {
		return nil
	}
	return fmt.Errorf("list deployments for rollout wait: %w", lerr)
}

// printDryRunManifests writes what a real apply would send, charts included.
//
// framed adds the banner the deploy/up path prints; reload passes false and
// gets the bare stream, which is what its callers pipe into kubectl.
func printDryRunManifests(manifests string, charts []renderedChart, framed bool) {
	all := manifests
	for _, rc := range charts {
		all = joinNonEmpty(all, rc.crds, rc.manifests, rc.extra)
	}
	all = RedactSecretValues(all)
	if !framed {
		fmt.Println(all)
		return
	}
	fmt.Println("\n--- Generated Manifests (dry-run) ---")
	fmt.Println(all)
	fmt.Println("--- End Manifests ---")
	fmt.Println("\nDry run complete. No changes applied.")
}

// chartStreams flattens each rendered chart's three parts — its CRDs, its
// templated manifests, and any consumer-declared riding manifests — in the same
// order printDryRunManifests folds them. Sharing the order is the point: the
// human dry run and an observing caller must be shown the same stream, not two
// arrangements of the same documents.
func chartStreams(charts []renderedChart) []string {
	out := make([]string, 0, len(charts)*3)
	for _, rc := range charts {
		out = append(out, rc.crds, rc.manifests, rc.extra)
	}
	return out
}

// RenderedChartStream is one platform dep's contribution to a deploy's
// manifest stream: the chart's name, the kubectl context it installs into
// ("" = the env's primary), and its documents in apply order (CRDs, then the
// templated chart, then the riding manifests).
type RenderedChartStream struct {
	Name    string
	Cluster string
	Stream  string
}

// RenderChartStreams renders each chart EXACTLY as a deploy does — the same
// renderSelectedCharts call, the same CRD bundle, the same --skip-crds
// template, post-hook drop, synthesized Namespace and app-label stamp — and
// returns each chart's stream in chartStreams order.
//
// It exists so `forge env render` can show chart output without modelling it
// a second time. A preview that re-implemented the chart render would drift
// from the apply the first time either changed, and a preview that disagrees
// with the deploy is worse than none: it is what an audit trusts.
func RenderChartStreams(ctx context.Context, specs []HelmChartSpec) ([]RenderedChartStream, error) {
	charts, err := renderSelectedCharts(ctx, specs)
	if err != nil {
		return nil, err
	}
	out := make([]RenderedChartStream, 0, len(charts))
	for _, rc := range charts {
		out = append(out, RenderedChartStream{
			Name:    rc.spec.Name,
			Cluster: strings.TrimSpace(rc.spec.Cluster),
			Stream:  joinNonEmpty(chartStreams([]renderedChart{rc})...),
		})
	}
	return out, nil
}

// ApplyHelmChart renders and applies ONE platform dep into one cluster, CRDs
// first and Established-gated — the same pipeline a declared
// `forge.HelmChart` flows through on a `--target` deploy.
//
// WHY A SINGLE-CHART ENTRY POINT EXISTS. Every other chart apply starts from
// an env's rendered Bundle, which is right for a platform dep the env
// declares. The cluster phase installs a declared chart through this function
// too, rather than its own helm shell-out, which is what keeps the two
// identical — in particular the CRD-first ordering: an object applied before
// its CRD is Established fails on a kind the apiserver has never registered.
//
// `quiet` is false: this is an install a human asked for, and the per-resource
// kubectl lines are the evidence it happened.
func ApplyHelmChart(ctx context.Context, kctx string, spec HelmChartSpec) error {
	charts, err := renderSelectedCharts(ctx, []HelmChartSpec{spec})
	if err != nil {
		return err
	}
	return applyRenderedCharts(ctx, kctx, charts, false)
}

// renderSelectedCharts helm-templates each selected platform dep into the
// manifests, CRDs and consumer-declared extras the apply pipeline needs.
//
// Rendering happens BEFORE the dry-run branch on purpose, so `--dry-run` shows
// the chart manifests too — they flow through the same pipeline as everything
// else rather than appearing only on a real apply.
func renderSelectedCharts(ctx context.Context, specs []HelmChartSpec) ([]renderedChart, error) {
	out := make([]renderedChart, 0, len(specs))
	for _, spec := range specs {
		rendered, rerr := RenderHelmChart(ctx, spec)
		if rerr != nil {
			return nil, rerr
		}
		// Consumer-declared raw manifests riding this chart's --target
		// (GatewayClass / ClusterIssuers): stamp them with the chart's
		// app-label exactly like the chart's own output, so they select +
		// route as part of the same named platform dep.
		var extra string
		if strings.TrimSpace(spec.Manifests) != "" {
			extra = stampAppLabel(spec.Manifests, spec.Name, spec.Owner)
		}
		// The FULL CRD set: forge's pinned forge-supplied bundle (spec.CRDs)
		// PLUS the chart's OWN CRDs that forge does not own (the chart's
		// `gateway.envoyproxy.io` CRDs the envoy controller starts informers
		// on; the main render is --skip-crds so they'd otherwise never land
		// and the controller crashloops on cache-sync). chartOwnCRDs filters
		// out the standard Gateway API group forge pins separately.
		ownCRDs, cerr := chartOwnCRDs(ctx, spec)
		if cerr != nil {
			return nil, cerr
		}
		out = append(out, renderedChart{
			spec: spec, manifests: rendered,
			crds: joinNonEmpty(spec.CRDs, ownCRDs), extra: extra,
		})
	}
	return out, nil
}

// applyRenderedCharts applies each rendered platform dep in CRD-first order,
// then its riding manifests once its controllers are Available.
//
// Split out of Apply so the CHART-NAMESPACE WIRING is testable without a KCL
// render or a helm pull. That matters more than it looks: the
// `default`-namespace bug was not a broken helper, it was a namespace that
// existed in the spec and never reached the apply. A test that calls
// applyCRDsThenRest directly cannot see that — it passes the namespace in
// itself — so it would stay green with the wiring severed. This is the seam
// where `rc.spec.Namespace` is read, so it is the seam a regression test has
// to drive.
func applyRenderedCharts(ctx context.Context, kctx string, charts []renderedChart, quiet bool) error {
	// One id for this run's inventory writes (chart_prune.go): how a
	// concurrent apply's merge tells an entry this run recorded from one it
	// only read.
	applyID := newChartApplyID()
	for _, rc := range charts {
		// A chart may declare its OWN cluster (HelmChartSpec.Cluster), which
		// overrides the env's primary context for THIS chart alone. Resolved
		// once here and used for every kubectl in the chart's sequence: the
		// applies, the CRD-Established wait, the controller-Available wait and
		// the riding-manifest applies. They must all agree — waiting for
		// Established against the PRIMARY apiserver would report a CRD the
		// target cluster does not have, which is exactly the "installed
		// nowhere useful and reported success" failure this closes.
		chartCtx := chartContext(kctx, rc.spec)
		if !quiet {
			if rc.spec.Cluster != "" {
				fmt.Printf("Applying platform dependency %q (helm-rendered) to cluster %q...\n",
					rc.spec.Name, chartCtx)
			} else {
				fmt.Printf("Applying platform dependency %q (helm-rendered)...\n", rc.spec.Name)
			}
		}
		// One External Secrets Operator per cluster: refuse before any write
		// when another env's install already owns the cluster-scoped CRDs.
		if err := checkComponentOwner(ctx, chartCtx, rc.spec); err != nil {
			return err
		}
		// Serialize owned charts across apply, inventory commit and prune.
		// Another writer fails before ANY chart write; locks never expire.
		if err := applyLockedChart(ctx, chartCtx, rc, applyID, quiet); err != nil {
			return err
		}
	}
	return nil
}

// One chart owns its non-expiring lock until all child commands have exited.
// An abnormal exit leaves the lock for explicit, operator-controlled recovery.
func applyLockedChart(ctx context.Context, chartCtx string, rc renderedChart, applyID string, quiet bool) (result error) {
	prune, err := beginChartPrune(ctx, chartCtx, rc, applyID)
	if prune != nil {
		defer func() {
			if result != nil || ctx.Err() != nil {
				// A transport error/cancellation may leave an API request in
				// flight. Never admit another writer by automatically unlocking
				// an uncertain outcome. Recovery requires the operator to stop
				// this writer and reconcile its outstanding requests first.
				result = errors.Join(result, fmt.Errorf("chart lock retained: %s/%s; explicit recovery required (see deploy/flux)", chartStateNamespace, chartLockName(rc.spec)))
				return
			}
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			result = errors.Join(result, prune.release(cleanup))
		}()
	}
	if err != nil {
		return err
	}
	if prune != nil {
		ctx = context.WithValue(ctx, chartPruneContextKey{}, prune)
	}
	// rc.spec.Namespace is the chart's DECLARED namespace, threaded into
	// the apply as `-n` so a chart object with no `metadata.namespace`
	// lands there instead of in `default` (see KubectlApplyNamespaced).
	if err := applyCRDsThenRest(ctx, chartCtx, rc.spec.Namespace, rc.crds, rc.manifests); err != nil {
		// Never record intended but unconfirmed UIDs after a failed apply.
		// The prior inventory remains intact; retain the lock for recovery.
		return fmt.Errorf("platform dependency %q: %w", rc.spec.Name, err)
	}
	// Consumer-declared manifests riding this chart's --target
	// (GatewayClass / ClusterIssuers) — applied AFTER the chart's
	// controllers so the controller (envoy-gateway / cert-manager) is up
	// before the instances it reconciles. applyCRDsThenRest two-passes
	// any CRD-then-rest within them too (harmless: these carry none).
	if strings.TrimSpace(rc.extra) != "" {
		if err := applyChartRidingManifests(ctx, chartCtx, rc, quiet); err != nil {
			return err
		}
	}
	// The chart is fully applied: only now is its render a proven account
	// of what it declares, so only now can an object it dropped be pruned.
	// A failed security-object prune fails the deploy and retains its lock;
	// success must not hide an outstanding grant that should have been removed.
	if _, err := prune.afterApply(ctx); err != nil {
		return fmt.Errorf("platform dependency %q prune: %w", rc.spec.Name, err)
	}
	return nil
}

// applyChartRidingManifests applies a chart's riding manifests once its
// controllers are Available.
func applyChartRidingManifests(ctx context.Context, chartCtx string, rc renderedChart, quiet bool) error {
	// WAIT for the chart's Deployments to be Available before applying its
	// riding manifests. cert-manager ships a ValidatingWebhook (failurePolicy:
	// Fail) that REJECTS ClusterIssuers until the webhook Deployment is Ready
	// + cainjector has injected the caBundle — without this gate the issuer
	// apply fails `failed calling webhook ... no endpoints available`. A
	// namespace with no Deployments returns immediately.
	if rc.spec.Namespace != "" {
		if !quiet {
			fmt.Printf("Waiting for %q controller Deployments to be Available...\n", rc.spec.Name)
		}
		if err := waitChartDeploymentsAvailable(ctx, chartCtx, rc.spec.Namespace, 180*time.Second); err != nil {
			return fmt.Errorf("platform dependency %q: wait controllers Available: %w", rc.spec.Name, err)
		}
	}
	if !quiet {
		fmt.Printf("Applying platform dependency %q owned manifests...\n", rc.spec.Name)
	}
	// Bounded retry: the webhook Service's endpoints can lag a few seconds
	// after the Deployment reports Available.
	if err := applyRidingManifestsWithRetry(ctx, chartCtx, rc.spec.Namespace, rc.extra); err != nil {
		return fmt.Errorf("platform dependency %q manifests: %w", rc.spec.Name, err)
	}
	return nil
}

// renderApplyManifests renders the env's KCL bundle, wrapping a failure in the
// phrasing that call site expects.
//
// The two messages are not interchangeable: reload's pre-extraction form used
// the short "KCL render:" wrap and the framed deploy/up path used the longer
// one, and tests pin both.
func renderApplyManifests(ctx context.Context, opts ApplyOpts) (string, error) {
	var (
		manifests string
		err       error
	)
	if opts.Render != nil {
		manifests, err = opts.Render(ctx)
	} else {
		manifests, err = RenderManifests(ctx, opts.MainK, opts.ImageTag, opts.Namespace, opts.Env, opts.EnvConfigKV, opts.ImageDigests)
	}
	if err == nil {
		WarnBlockingPDBs(manifests)
		return manifests, nil
	}
	if opts.Quiet {
		return "", fmt.Errorf("KCL render: %w", err)
	}
	return "", fmt.Errorf("KCL manifest generation failed: %w", err)
}

// Apply runs the render-KCL → kubectl-apply → wait-rollouts pipeline.
// It is the single entry point for the three call sites this package
// collapses. Behavior matches the pre-extraction `runDeploy` /
// `runDevClusterReload` shapes exactly (including stdout framing,
// warning messages, and ordering); per-call differences are expressed
// through ApplyOpts fields.
//
// Apply is ApplyNoWait followed by WaitRollouts of that one apply. A caller
// deploying to SEVERAL clusters must not call it once per cluster: see
// ApplyNoWait.
func Apply(ctx context.Context, opts ApplyOpts) error {
	pending, err := ApplyNoWait(ctx, opts)
	if err != nil {
		return err
	}
	return WaitRollouts(ctx, pending)
}

// ApplyNoWait is Apply without the rollout wait: it renders, selects, scopes
// and applies — the pre-rollout Job gate included — and returns what is left
// to await as a PendingRollout. Hand it, with every other cluster's, to ONE
// WaitRollouts call.
//
// WHY THE WAIT IS SEPARABLE. One env may span several clusters, and a
// workload on one can be unable to become ready until a workload on another
// is running (control-plane's workspace-proxy-bridge cannot pass readiness
// until workspace-proxy, on the daemon cluster, answers it). Applying and
// awaiting each cluster in turn deadlocks that env on every fresh deploy: the
// first cluster's wait expires on a dependency the second cluster has not been
// sent yet, the deploy fails, and the second cluster is never applied at all.
// Kubernetes converges concurrently — RolloutPolicy.Order is documented as a
// wait ordering, not an apply ordering, for exactly this reason — so the
// deploy applies every cluster first and then waits on all of them together.
//
// The pre-rollout gate stays INSIDE the apply, per cluster: it holds back a
// cluster's own workloads until that cluster's migration Jobs complete, which
// is an ordering within one apply, not a wait on convergence.
//
// A nil PendingRollout with a nil error means there is nothing to wait for (a
// dry run, a platform-only apply); WaitRollouts accepts it.
func ApplyNoWait(ctx context.Context, opts ApplyOpts) (*PendingRollout, error) {
	manifests, err := renderApplyManifests(ctx, opts)
	if err != nil {
		return nil, err
	}
	return applyRenderedNoWait(ctx, opts, manifests)
}

// selectAndScope narrows the rendered env stream to what THIS apply owns and
// stamps its CRDs. It returns the helm charts the same selection keeps, the
// narrowed stream, and the CRD owner label it stamped.
func selectAndScope(opts ApplyOpts, manifests string) (selectedCharts []HelmChartSpec, scoped, crdOwner string) {
	// Exclusive --target selection — ONE uniform mechanical filter over the
	// KCL-declared service GROUP (`app.kubernetes.io/name`).
	//
	//   - No targets  → keep EVERYTHING (full declarative reconcile of the
	//     whole env bundle + every declared platform dep).
	//   - Targets set → keep EXACTLY the manifests whose group ∈ Targets, and
	//     render EXACTLY the helm charts whose Name ∈ Targets. Nothing
	//     implicit: no shared-base auto-kept, no chart opt-in/opt-out
	//     asymmetry, no "no application matched" special case. The group is
	//     always the service; env-shared manifests (Namespace, ConfigMap,
	//     RuntimeClass, NetworkPolicy) and bundle-level additional_manifests
	//     carry NO group, so a service Target drops them — they apply only on
	//     a bare deploy. To make a shared resource an app needs ride that
	//     app's Target, declare it on the service's `manifests` so it inherits
	//     the service group (decided in KCL, never patched in by Go policy).
	//
	// A helm chart is helm-templated iff (no targets) OR (its Name ∈ Targets)
	// — the SAME rule as any manifest, since each chart's manifests are
	// stamped with the chart Name as their group. selectHelmChartsByGroup
	// applies that rule to the chart specs; SelectManifestsByGroup applies it
	// to the rendered env stream.
	selectedCharts = selectHelmChartsByGroup(opts.HelmCharts, opts.Targets)
	if len(opts.Targets) > 0 {
		manifests = SelectManifestsByGroup(manifests, opts.Targets)
	}

	// Multi-cluster scope: a service whose `deploy` targets a SECOND cluster
	// must land ONLY on that cluster. KCL renders the whole env as one
	// stream, so each per-group apply scopes that stream to its own cluster
	// here — the secondary cluster gets only its services' workloads (+ its
	// Namespace), the primary gets everything else. nil = single-cluster
	// (whole bundle), unchanged. Applied AFTER the --target filter so a
	// `forge env deploy <env> --target <svc>` in a multi-cluster env still scopes
	// to the right cluster.
	if opts.ClusterScope != nil {
		manifests = ScopeManifestsToGroup(manifests, *opts.ClusterScope)
	}

	// Stamp every CRD with this env's ownership label, so a later apply that
	// stops rendering it can prove the CRD was its own (crd_prune.go).
	crdOwner = CRDOwner(opts.Project, opts.Env)
	return selectedCharts, StampCRDOwnership(manifests, crdOwner), crdOwner
}

// applyRendered is Apply after the KCL render: select, scope, apply and wait.
//
// Split out so the apply ORDERING — the part a production deploy's safety
// rests on — is testable against a recorded kubectl without a KCL toolchain.
// A test that could only reach it through a render would be testing the
// renderer too, and would be skipped wherever KCL is unavailable.
func applyRendered(ctx context.Context, opts ApplyOpts, manifests string) error {
	pending, err := applyRenderedNoWait(ctx, opts, manifests)
	if err != nil {
		return err
	}
	return WaitRollouts(ctx, pending)
}

// applyRenderedNoWait is ApplyNoWait after the KCL render: select, scope and
// apply, returning the rollout still to be awaited.
func applyRenderedNoWait(ctx context.Context, opts ApplyOpts, manifests string) (*PendingRollout, error) {
	selectedCharts, manifests, crdOwner := selectAndScope(opts, manifests)
	// What the render holds for this cluster, before the hub's units are taken
	// out: the prunes below compare the cluster against THIS, never against
	// what this apply chose to send.
	desired := manifests
	manifests = DropDeliveryUnits(manifests, opts.SkipUnits)
	if len(opts.SkipUnits) > 0 && !opts.Quiet {
		fmt.Printf("Leaving delivery unit(s) %s to the hub (Bundle.hub_converges): not applied from here\n", strings.Join(opts.SkipUnits, ", "))
	}

	// The WHOLE scoped stream, checked once before any pass is sent: an object
	// still stamped for another cluster here would otherwise be refused only
	// by the pass that carries it, after the earlier passes had already
	// written. Refused for a dry run too, so a preview never shows a stream a
	// real apply would refuse. See refuseForeignClusterDocs.
	if err := refuseForeignClusterDocs(opts.Context, manifests); err != nil {
		return nil, err
	}

	// Split the stream into its apply passes NOW, before the dry-run return
	// and before any chart is fetched: an unknown deploy-phase declaration
	// is refused for a preview exactly as for a real apply, and before
	// anything has touched the cluster. See prerollout.go.
	//
	// CRDs come out FIRST, into their own early batch: a RenderedWorkload
	// that ships a CRD together with an instance of it (an operator and its
	// default CR, CNPG's CRDs and webhooks) otherwise sends both in one
	// server-side apply, and the instance races the CRD's registration. The
	// chart path has always done this; the env stream now uses the SAME
	// primitive (applyEarlyBatch). The stream's Namespaces ride that batch
	// too, so a namespaced object the config pass sends never lands before
	// its namespace. A stream with no CRD keeps its historical passes
	// exactly: early is then only the Namespaces the config pass would have
	// applied first anyway.
	earlyCRDs, earlyNS, remainder := partitionEarlyBatch(manifests)
	if strings.TrimSpace(earlyCRDs) == "" {
		earlyNS, remainder = "", manifests
	}
	config, rest := PartitionConfigManifests(remainder)
	// The secret stages come out of the non-config remainder BEFORE the
	// release's own phases are cut: a store component's Jobs and workloads, and
	// the env's SecretStores and ExternalSecrets, are applied and proven ahead
	// of everything the release rolls (secret_order.go). A stream with no
	// store-backed secret leaves stages empty and rest untouched.
	stages, err := partitionSecretStages(rest)
	if err != nil {
		return nil, err
	}
	if stages.active() {
		rest = stages.rest
	}
	phases, err := partitionRolloutPhases(rest)
	if err != nil {
		return nil, err
	}

	// Render the selected platform deps (helm-as-a-RENDERER). Each chart's
	// manifests are stamped `app.kubernetes.io/name = <name>` so they read
	// as ONE named app, and carry their forge-supplied CRDs for the
	// CRD-first apply below. Rendered here (before dry-run) so `--dry-run`
	// shows the chart manifests too — they flow through the SAME pipeline.
	renderedCharts, err := renderSelectedCharts(ctx, selectedCharts)
	if err != nil {
		return nil, err
	}

	// Report the final stream — filtered, scoped, charts folded in — to an
	// observing caller. Placed BEFORE the dry-run return so a preview and a
	// real apply report identical bytes; this is what makes `--dry-run
	// --json` a trustworthy preview of `--json`.
	if opts.OnStream != nil {
		opts.OnStream(joinNonEmpty(append([]string{manifests}, chartStreams(renderedCharts)...)...))
	}

	if opts.DryRun {
		printDryRunManifests(manifests, renderedCharts, opts.DryRunFramed)
		return nil, nil
	}

	// Apply the platform deps FIRST: a `--target=<platform>` apply must
	// leave the cluster with the chart's CRDs Established + controllers
	// Deployed (so a later `forge env deploy` app finds the CRDs present). Each
	// chart applies in CRD-first order (forge-supplied CRDs → wait
	// Established → the --skip-crds controllers). When this apply also
	// carries app manifests (a mixed --target), the charts land before them.
	if err := applyRenderedCharts(ctx, opts.Context, renderedCharts, opts.Quiet); err != nil {
		return nil, err
	}

	// A platform-only apply (every --target named a chart) is done once the
	// charts are applied — there are no env/app manifests to apply or wait on.
	if strings.TrimSpace(manifests) == "" && len(renderedCharts) > 0 {
		return nil, nil
	}

	// Every object of this cluster is in a unit the hub converges: nothing is
	// sent, and there is nothing to wait for. kubectl refuses an empty stream,
	// and an apply that reached it would fail a deploy that had correctly
	// left everything to the hub.
	if strings.TrimSpace(manifests) == "" && len(opts.SkipUnits) > 0 {
		if !opts.Quiet {
			fmt.Println("Nothing to apply from here: every object is in a unit the hub converges.")
		}
		return nil, nil
	}

	if !opts.Quiet {
		fmt.Println("Applying manifests...")
	}
	// Two-pass apply to close the ConfigMap/Secret ordering race: a
	// single `kubectl apply --server-side -f -` admits documents as it
	// streams them, but the apiserver can schedule a workload pod before
	// the ConfigMaps/Secrets it references are readable across every
	// apiserver cache — the pod then wedges on CreateContainerConfigError
	// / "secret not found" and the rollout wait below expires as a
	// spurious timeout (seen on a real GKE launch where the config was
	// fine). Splitting the stream so the config kinds (Namespace,
	// ConfigMap, Secret) are applied AND returned BEFORE the workloads
	// gives a real happens-before: the second pass's pods only schedule
	// once their referenced config already exists. apply --server-side is
	// idempotent, so re-sending any doc is harmless. When the bundle has
	// no config kinds, config is empty and we fall through to a single
	// apply of rest — identical to the pre-split behaviour.
	//
	// Normalized here rather than at the rollout wait below because the
	// apply itself now has a verdict to render: an incomplete apply is
	// governed by the same policy as a failed rollout (see
	// classifyApplyResult).
	policy := opts.Rollout.Normalize()
	if err := applyEarlyBatch(ctx, opts.Context, "", earlyCRDs, earlyNS); err != nil {
		return nil, policy.classifyApplyResult(err)
	}
	if strings.TrimSpace(config) != "" {
		if err := policy.classifyApplyResult(KubectlApply(ctx, opts.Context, config)); err != nil {
			if opts.Quiet {
				return nil, fmt.Errorf("kubectl apply (config): %w", err)
			}
			return nil, fmt.Errorf("kubectl apply failed (config): %w", err)
		}
	}
	// The secret ordering: the store, then the SecretStores and ExternalSecrets,
	// each proven before the next. Held in every rollout mode (see
	// secret_order.go), and before the migration Jobs: a Job that reads a
	// store-backed Secret needs it as much as a Deployment does.
	if stages.active() {
		if err := applySecretStages(ctx, opts, policy, stages); err != nil {
			return nil, err
		}
	}
	// The pre-rollout gate. When the stream carries a pre-rollout Job (a
	// migration, by default every one-shot Job), the support objects and the
	// Jobs are applied and the Jobs must COMPLETE before a single workload is
	// sent. A Job that fails or times out returns here, and the workloads
	// are never applied — the previous release keeps serving. It holds in
	// every rollout mode; see prerollout.go for why skip is no exemption.
	//
	// A stream with no pre-rollout Job skips all of this and is applied in
	// the one pass it always was.
	if phases.gated() {
		if err := applyPreRolloutGate(ctx, opts, policy, phases); err != nil {
			return nil, err
		}
		// Everything but the held-back workloads has landed. A stream that
		// was only support objects and pre-rollout Jobs has nothing left.
		rest = phases.workloads
		if strings.TrimSpace(rest) != "" && !opts.Quiet {
			fmt.Println("Pre-rollout Jobs complete; applying workloads...")
		}
	}
	// A stream the secret stages consumed whole has nothing left to send: kubectl
	// refuses an empty stream, as it always did here for a gated one.
	if strings.TrimSpace(rest) != "" || (!phases.gated() && !stages.active()) {
		if err := policy.classifyApplyResult(KubectlApply(ctx, opts.Context, rest)); err != nil {
			// Reload uses the shorter "kubectl apply:" wrap; the framed
			// deploy/up path uses the longer "kubectl apply failed:" form.
			if opts.Quiet {
				return nil, fmt.Errorf("kubectl apply: %w", err)
			}
			return nil, fmt.Errorf("kubectl apply failed: %w", err)
		}
	}

	// A workload that dropped to one replica stops rendering its PDB and
	// apply never deletes it; a stale one can block node drains. Not gated
	// on --prune: PDBs hold no state.
	if err := PrunePDBs(ctx, opts.Context, desired, opts.Namespace); err != nil {
		fmt.Printf("Warning: prune PodDisruptionBudgets: %v\n", err)
	}
	if opts.Prune {
		if err := Prune(ctx, opts.Context, desired, opts.Namespace); err != nil {
			fmt.Printf("Warning: prune: %v\n", err)
		}
	}
	// Hash-named one-shot Jobs pile up: every spec change is a new Job and
	// nothing deletes the old one. Keep the last few finished ones per base
	// name. Not gated on --prune (a finished Job holds no state, and a
	// Deployment prune is the destructive one), but a FULL apply only: a
	// --target subset says nothing about what the env stopped rendering.
	if len(opts.Targets) == 0 {
		if _, err := PruneSupersededJobs(ctx, opts.Context, manifests, opts.Namespace, opts.Env); err != nil {
			fmt.Printf("Warning: prune superseded Jobs: %v\n", err)
		}
	}
	// A --target apply renders a subset of the env, so a CRD missing from
	// it proves nothing: the CRD prune runs on a full apply only.
	if opts.PruneCRDs && len(opts.Targets) == 0 && crdOwner != "" {
		if _, _, err := PruneCRDs(ctx, opts.Context, desired, crdOwner); err != nil {
			fmt.Printf("Warning: %v\n", err)
		}
	}

	if policy.Mode == RolloutSkip {
		if !opts.Quiet {
			fmt.Println("Skipping rollout wait (rollout mode: skip)")
		}
		return nil, nil
	}

	// Wait set = every `kind: Job` in the stream this apply just sent,
	// and nothing else. See oneShotWaitSet for why a caller-supplied
	// list is not unioned in any more. A pre-rollout Job already completed
	// at the gate — the workloads would not have been applied otherwise —
	// so only the post-rollout Jobs are left to wait on.
	var jobs []string
	stageJobs := stages.awaitedJobs()
	for _, name := range oneShotWaitSet(manifests) {
		if !phases.awaited(name) && !stageJobs[name] {
			jobs = append(jobs, name)
		}
	}
	return &PendingRollout{opts: opts, policy: policy, jobs: jobs, skipDeployments: DeploymentsInUnits(desired, opts.SkipUnits)}, nil
}

// oneShotWaitSet is the set of Jobs Apply blocks on after applying:
// every `kind: Job` in the stream it just sent, de-duped, in render
// order. The applied manifests are the ONLY source.
//
// It used to be that set UNIONED with a caller-supplied list of names.
// That union failed a production deploy that had entirely succeeded
// (run 33217453299): 201 objects applied, all 13 Deployments ready,
// both one-shot Jobs `complete` — and forge reported failure. forge
// names a one-shot Job by its spec hash, so the applied objects were
// `control-plane-migrate-aed30b6854` and
// `control-plane-idp-provision-e8b66e53c9`, while the caller derived
// the unhashed entity names from the KCL entity list. Those two names
// had never been applied and matched no object in the namespace, so
// `kubectl wait` errored on each — and a Job wait that errors correctly
// means "the migration did not run", so the deploy went red.
//
// The union could only ever hurt. A name in the caller's list that IS
// applied is already here, derived from the manifest; a name that is
// NOT applied has no object to wait on and can produce nothing but a
// false failure. Reconciling the two sets (resolving a supplied stem to
// its hashed name) would restore correctness but keep a second,
// weaker derivation alive to drift again the next time naming changes.
//
// Nor does dropping it lose the ability to notice a Job that failed to
// apply. That check is verifyApplyComplete, which is kind-agnostic:
// every rendered object, Jobs included, must appear in kubectl's apply
// output or the apply itself fails, before this wait is ever reached.
//
// Scheduled CronJobs render as `kind: CronJob`, not `kind: Job`, so
// they are excluded — they run on their own cadence and the deploy is
// done once they are applied.
func oneShotWaitSet(manifests string) []string {
	names := RenderedJobNames(manifests)
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n == "" {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}

// RenderManifests shells `kcl run <mainK> -D image_tag=<tag>
// -D namespace=<ns> [-D env=<env>] [-D <key>=<val>]...` and returns
// the rendered `manifests:` list as a `---`-separated YAML document
// stream (the shape `kubectl apply -f -` consumes).
//
// KCL emits the program's top-level variables wrapped in a YAML
// object, so we unwrap the canonical `manifests` key. All other
// top-level KCL vars MUST be declared private (underscore-prefix) or
// they'll be dropped with a warning to stderr — only `manifests` is
// part of the contract.
//
// env (when non-empty) is passed as `-D env=<env>` so main.k can do
// `option("env")` and conditionally include manifests per-env.
// projectRootFromMainK recovers the project root from a
// `<root>/deploy/kcl/<env>/main.k` path by stripping the four trailing
// components. Returns "" for a path that doesn't match that shape (a
// relative or unexpected mainK) so the caller leaves cmd.Dir unset and
// inherits the current cwd (which forge runs from the project root).
func projectRootFromMainK(mainK string) string {
	if mainK == "" || !filepath.IsAbs(mainK) {
		return ""
	}
	// main.k -> <env> -> kcl -> deploy -> root
	dir := filepath.Dir(mainK) // <root>/deploy/kcl/<env>
	if filepath.Base(filepath.Dir(dir)) != "kcl" {
		return ""
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(dir))) // <root>
}

// renderDArgs builds the `-D key=value` top-level bindings for the KCL
// manifest render. The forge-controlled string args (image_tag, namespace,
// env) are passed as QUOTED KCL string literals via strconv.Quote so KCL
// types them as `str` — without the quotes an all-digit value like a
// numeric git-describe tag ("3826648") is coerced to `int`, which fails
// RenderEnv.image_tag's `str` type (the forge-deploy-prod regression).
// envCfgKV values are left unquoted: they are project config whose KCL
// type (int/bool/str) is intentional.
func renderDArgs(imageTag, namespace, env string, envCfgKV map[string]string, imageDigests map[string]string) []string {
	dArgs := []string{
		"image_tag=" + strconv.Quote(imageTag),
		"namespace=" + strconv.Quote(namespace),
	}
	if env != "" {
		dArgs = append(dArgs, "env="+strconv.Quote(env))
	}
	// image_digests is the per-image name→digest map forge env deploy pins so
	// each service resolves to ITS image's digest (not one env-wide digest
	// stamped onto every image). Passed as a QUOTED JSON string so KCL types
	// it as `str`; `forge.image_digests()` json.decodes it. Omitted entirely
	// when empty (the local-registry / no-digest path) so a plain `kcl run`
	// renders byte-identically — `option("image_digests")` then yields None
	// and the helper returns {}.
	if len(imageDigests) > 0 {
		if b, err := json.Marshal(imageDigests); err == nil {
			dArgs = append(dArgs, "image_digests="+strconv.Quote(string(b)))
		}
	}
	keys := make([]string, 0, len(envCfgKV))
	for k := range envCfgKV {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		dArgs = append(dArgs, k+"="+envCfgKV[k])
	}
	// option("worktree") / option("branch") are NOT added here. They are
	// bound for EVERY render inside kclrender.Run (withDevStackDArgs),
	// because a project keys its namespace on them and a render that omits
	// them resolves a different namespace than the deploy that applied the
	// objects. Binding them per-caller is what let the doctor's cluster
	// render drift from this one and report running workloads as "NO PODS".
	return dArgs
}

// RenderManifests renders the project's KCL for env into a Kubernetes
// manifest document, applying the given image tag, namespace, per-env
// config overrides, and image digest pins. It runs from the project root
// so deploy-as-data file reads resolve.
func RenderManifests(ctx context.Context, mainK, imageTag, namespace, env string, envCfgKV map[string]string, imageDigests map[string]string) (string, error) {
	stream, _, err := RenderManifestsWithOverrides(ctx, mainK, imageTag, namespace, env, envCfgKV, imageDigests)
	return stream, err
}

// RenderManifestsWithOverrides is the one render: `forge env render`,
// `forge env deploy` and the preflight all reach KCL through it, so they
// cannot disagree about what the env declares. It additionally reports which
// Bundle.overrides landed — what `forge env render` summarizes.
func RenderManifestsWithOverrides(ctx context.Context, mainK, imageTag, namespace, env string, envCfgKV map[string]string, imageDigests map[string]string) (string, []AppliedOverride, error) {
	dArgs := append(renderDArgs(imageTag, namespace, env, envCfgKV, imageDigests), kclrender.ReleaseDArgs(ctx)...)
	workDir := projectRootFromMainK(mainK)
	if workDir == "" {
		if wd, err := os.Getwd(); err == nil {
			workDir = wd
		}
	}
	out, err := kclrender.Run(workDir, mainK, dArgs)
	if err != nil {
		return "", nil, err
	}
	return extractManifestsIn(ctx, workDir, out)
}

// ExtractManifests is the manifest stream a render's KCL output becomes on
// its way to kubectl: `output.manifests`, with every forge.dev record
// expanded through pkg/deploy, as `---`-separated YAML. It is exported so
// tests and tooling can assert against exactly what is applied, not against
// an intermediate.
//
// ONE ENTRYPOINT. main.k ends with `output = forge.render(bundle)`, and the
// applyable stream is `output.manifests`. A top-level `manifests` var is an
// ERROR, not a fallback: the stream carries Workload records only forge can
// expand, so a second top-level stream that a bare `kcl run -S manifests |
// kubectl apply` could consume would be a path that looks right and silently
// is not. Any other public top-level var warns.
func ExtractManifests(kclOutput []byte) (string, error) {
	stream, _, err := ExtractManifestsWithOverrides(kclOutput)
	return stream, err
}

// ExtractManifestsWithOverrides is ExtractManifests, additionally reporting
// which Bundle.overrides landed and on what. Only the surfaces that SHOW the
// overrides need the second return (`forge env render`'s summary and its
// --json); every other caller wants the stream and uses ExtractManifests.
func ExtractManifestsWithOverrides(kclOutput []byte) (string, []AppliedOverride, error) {
	return extractManifestsIn(context.Background(), "", kclOutput)
}

// extractManifestsIn is the one extraction. projectDir is where a
// forge.Generated command runs from ("" = the process cwd).
func extractManifestsIn(ctx context.Context, projectDir string, kclOutput []byte) (string, []AppliedOverride, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(kclOutput, &doc); err != nil {
		return "", nil, fmt.Errorf("parse kcl output: %w", err)
	}
	if _, legacy := doc["manifests"]; legacy {
		return "", nil, fmt.Errorf("kcl output has a top-level `manifests` var: main.k must end with `output = forge.render(bundle)`, " +
			"the one entrypoint — the applyable stream is output.manifests, and forge expands the Workload records in it")
	}
	out, ok := doc["output"].(map[string]any)
	if !ok {
		return "", nil, fmt.Errorf("kcl output has no top-level `output` object; main.k must end with `output = forge.render(bundle)` " +
			"and other top-level vars must be private (underscore-prefix)")
	}
	for k := range doc {
		if k == "output" {
			continue
		}
		fmt.Fprintf(os.Stderr, "warning: ignoring extra top-level KCL var %q (mark as private with `_%s = ...` to suppress)\n", k, k)
	}
	raw, present := out["manifests"]
	var items []any
	if present && raw != nil {
		if items, ok = raw.([]any); !ok {
			return "", nil, fmt.Errorf("`output.manifests` is not a list (got %T)", raw)
		}
	}
	network, err := decodeNetworkPolicy(out["network_policy"])
	if err != nil {
		return "", nil, err
	}
	pullSecrets, err := decodePullSecrets(out["workloads"])
	if err != nil {
		return "", nil, err
	}

	items, err = expandTierDeclarations(items, network, pullSecrets)
	if err != nil {
		return "", nil, err
	}

	// The pre-rollout Job that converges a StatefulSet whose declared volume
	// grew. It sits BEFORE the overrides below so an override can address it
	// like any object forge renders, and after expansion so it sees the
	// StatefulSets the Workload records expanded into. See
	// statefulset_resize_job.go.
	resizeDisabled, resizeImage, err := decodeStatefulSetResize(out["statefulset_resize"])
	if err != nil {
		return "", nil, err
	}
	lifecycle, _ := out["lifecycle"].(string)
	items, err = expandStatefulSetResizeJobs(items, resizeJobOpts{
		Lifecycle: lifecycle, Disabled: resizeDisabled, Image: resizeImage,
		Namespace: resizeDefaultNamespace(out), ImagePullSecrets: pullSecrets,
	})
	if err != nil {
		return "", nil, err
	}

	// Bring-your-own YAML (an app chart with delivery = "bundle", a
	// forge.Generated command) joins the stream HERE, so every consumer of
	// it — render, shape, direct apply, the bundle — sees the same objects
	// and an override can address them. See byo_yaml.go.
	byo, err := expandBYOSources(ctx, projectDir, out, decodeHostedNames(out["workloads"]))
	if err != nil {
		return "", nil, err
	}
	items = append(items, byo...)

	// Bundle.overrides, applied HERE: after expansion, so a Deployment that
	// only exists because forge expanded a Workload record is addressable,
	// and before anything reads the stream, so `forge env render`, the deploy,
	// `forge env shape` and the bundle all see post-override objects from the
	// one hook. See overrides.go.
	overrides, err := decodeOverrides(out["overrides"])
	if err != nil {
		return "", nil, err
	}
	items, applied, err := applyOverrides(items, overrides, decodeHostedNames(out["workloads"]))
	if err != nil {
		return "", nil, err
	}

	// A pinned store-backed Secret's version rides its consumers' pod templates,
	// so moving the pin rolls them (secret_pins.go). After the overrides, so an
	// override that rewrites a pod template cannot drop it, and so the
	// annotation is part of the same bytes every consumer of the stream sees.
	items = stampSecretPins(items)

	var sb strings.Builder
	for i, it := range items {
		if i > 0 {
			sb.WriteString("---\n")
		}
		b, err := yaml.Marshal(it)
		if err != nil {
			return "", nil, fmt.Errorf("marshal manifest item %d: %w", i, err)
		}
		sb.Write(b)
	}
	return sb.String(), applied, nil
}

// decodePullSecrets reads the image-pull Secrets each Cluster runtime in
// `output.workloads` declares (`forge.Cluster.image_pull_secrets`), keyed by
// the (cluster, namespace) group its records render in. A pull Secret is a
// fact about where a workload runs, not about the workload, so it rides the
// runtime and reaches pkg/deploy as Context.ImagePullSecrets. Workloads that
// share a group share one render call, so the group's set is the union of
// its runtimes' declarations, in first-declared order.
func decodePullSecrets(raw any) (map[workloadGroupKey][]string, error) {
	if raw == nil {
		return nil, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("output.workloads: %w", err)
	}
	var ws []struct {
		Runtime struct {
			Type             string   `json:"type"`
			Cluster          string   `json:"cluster"`
			Namespace        string   `json:"namespace"`
			ImagePullSecrets []string `json:"image_pull_secrets"`
		} `json:"runtime"`
	}
	if err := json.Unmarshal(b, &ws); err != nil {
		return nil, fmt.Errorf("output.workloads: %w", err)
	}
	out := map[workloadGroupKey][]string{}
	seen := map[workloadGroupKey]map[string]bool{}
	for _, w := range ws {
		rt := w.Runtime
		if rt.Type != "cluster" {
			continue
		}
		key := workloadGroupKey{cluster: rt.Cluster, namespace: rt.Namespace}
		for _, s := range rt.ImagePullSecrets {
			if s == "" || seen[key][s] {
				continue
			}
			if seen[key] == nil {
				seen[key] = map[string]bool{}
			}
			seen[key][s] = true
			out[key] = append(out[key], s)
		}
	}
	return out, nil
}

// decodeNetworkPolicy reads `output.network_policy`: the env-wide
// NetworkPolicy bundle (default-deny plus the DNS / same-namespace /
// external-egress / telemetry / ingress-controller allows). nil means the env
// declares none. It is passed to every cluster group's RenderWorkloads, which
// is the one renderer that emits it.
func decodeNetworkPolicy(raw any) (*deploy.EnvNetworkPolicy, error) {
	if raw == nil {
		return nil, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var np struct {
		EgressPorts        []int32 `json:"egress_ports"`
		TelemetryNamespace string  `json:"telemetry_namespace"`
		IngressNamespace   string  `json:"ingress_namespace"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&np); err != nil {
		return nil, fmt.Errorf("decode output.network_policy: %w", err)
	}
	return &deploy.EnvNetworkPolicy{EgressPorts: np.EgressPorts, TelemetryNamespace: np.TelemetryNamespace, IngressNamespace: np.IngressNamespace}, nil
}

// workloadGroupKey is one RenderWorkloads call: the Workload records that
// share a cluster and namespace. `before` gating, cycle checks and
// cross-workload references are relations inside ONE such set, so a set is
// never split and never rendered one record at a time.
type workloadGroupKey struct{ cluster, namespace string }

// workloadGroup is one set's records, in stream order, plus the position its
// first record held (where the rendered objects are spliced in).
type workloadGroup struct {
	first   int
	labels  map[string]string // the first record's labels: cluster, part-of, env
	records []deployv1alpha1.Workload
}

// unitOf is the delivery unit a rendered object inherits: that of the group
// record whose name is the object's app.kubernetes.io/name, "" when none.
func (g *workloadGroup) unitOf(app string) string {
	for _, r := range g.records {
		if r.Name == app {
			return r.Labels[release.LabelDeliveryUnit]
		}
	}
	return ""
}

// expandTierDeclarations replaces every forge.dev record in the stream with
// the Kubernetes objects pkg/deploy produces for it.
//
//   - Workload records are GROUPED by (forge.dev/cluster, namespace) and each
//     group is rendered by ONE deploy.RenderWorkloads(group, ProfileFull)
//     call, emitted at the position of the group's first record. The env's
//     NetworkPolicy bundle (network) rides every group, because each group
//     is a namespace the env deploys into; the group's image-pull Secrets
//     (pullSecrets, from its Cluster runtimes) ride its own call.
//   - ManagedDatabase / StaticSite records render one at a time through
//     deploy.Render, in place.
//
// This is how a Cluster-bound workload reaches the cluster: the KCL layer
// emits the declaration (the same forge.dev/v1alpha1 object the hosted path
// publishes as a CR), and the ONE Go renderer, shared with the control
// plane's operator, decides what it becomes. The cluster never sees the
// record itself: no CRD is installed and no controller runs there.
//
// The record's routing and ownership labels (forge.dev/cluster, and the env
// stamp) are copied onto every object the group renders — including the env
// NetworkPolicies, which carry no app label — so multi-cluster scoping and
// env-scoped prune/status see them exactly as they saw the record.
func expandTierDeclarations(items []any, network *deploy.EnvNetworkPolicy, pullSecrets map[workloadGroupKey][]string) ([]any, error) {
	groups := map[workloadGroupKey]*workloadGroup{}
	var order []workloadGroupKey
	leaders := map[int]workloadGroupKey{}
	skip := map[int]bool{}
	for i, it := range items {
		m, ok := it.(map[string]any)
		if !ok || m["apiVersion"] != deployv1alpha1.GroupVersion.String() || m["kind"] != "Workload" {
			continue
		}
		var w deployv1alpha1.Workload
		if err := decodeStrict(m, &w); err != nil {
			return nil, fmt.Errorf("manifest item %d: %w", i, err)
		}
		if w.Namespace == "" {
			return nil, fmt.Errorf("manifest item %d: %s has no namespace: a Cluster workload record must carry its runtime's namespace", i, describeManifest(m))
		}
		key := workloadGroupKey{cluster: w.Labels[ClusterRoutingLabel], namespace: w.Namespace}
		g, seen := groups[key]
		if !seen {
			g = &workloadGroup{first: i, labels: w.Labels}
			groups[key] = g
			order = append(order, key)
			leaders[i] = key
		} else {
			skip[i] = true
			// The group renders with ONE part-of and ONE env stamp, so
			// every record in it must carry the same ones. Taking the
			// first record's would silently relabel the rest — part-of is
			// the handle a CD pipeline selects a release's workloads by.
			for _, label := range []string{deploy.LabelPartOf, deploy.LabelEnv} {
				if got, want := w.Labels[label], g.labels[label]; got != want {
					return nil, fmt.Errorf("manifest item %d: workload %q has %s=%q but %q in the same group (cluster %q, namespace %s) has %q: every workload record in a group must agree (set Bundle.project / Bundle.env once, not per record)",
						i, w.Name, label, got, g.records[0].Name, key.cluster, key.namespace, want)
				}
			}
		}
		g.records = append(g.records, w)
	}
	rendered := map[workloadGroupKey][]any{}
	for _, key := range order {
		g := groups[key]
		objs, err := deploy.RenderWorkloads(g.records, deployv1alpha1.ProfileFull, deploy.Context{
			Namespace:        key.namespace,
			PartOf:           g.labels[deploy.LabelPartOf],
			Env:              g.labels[deploy.LabelEnv],
			Network:          network,
			ImagePullSecrets: pullSecrets[key],
		})
		if err != nil {
			return nil, fmt.Errorf("render workloads (cluster %q, namespace %s): %w", key.cluster, key.namespace, err)
		}
		for _, o := range objs {
			if key.cluster != "" {
				labels := o.GetLabels()
				if labels == nil {
					labels = map[string]string{}
				}
				labels[ClusterRoutingLabel] = key.cluster
				o.SetLabels(labels)
			}
			// The delivery unit of the workload that rendered the object. An
			// object no workload owns (the env's NetworkPolicies) stays in
			// the default unit.
			if unit := g.unitOf(o.GetLabels()[deploy.LabelName]); unit != "" {
				labels := o.GetLabels()
				labels[release.LabelDeliveryUnit] = unit
				o.SetLabels(labels)
			}
			rendered[key] = append(rendered[key], o.Object)
		}
	}

	out := make([]any, 0, len(items))
	for i, it := range items {
		if key, ok := leaders[i]; ok {
			out = append(out, rendered[key]...)
			continue
		}
		if skip[i] {
			continue
		}
		m, ok := it.(map[string]any)
		if !ok || m["apiVersion"] != deployv1alpha1.GroupVersion.String() {
			out = append(out, it)
			continue
		}
		var objs []any
		var err error
		switch m["kind"] {
		case deploy.ObservabilityAgentKind:
			objs, err = expandObservabilityAgent(i, m)
		case openbao.Kind:
			objs, err = expandOpenBao(i, m)
		default:
			objs, err = expandTierRecord(i, m)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, objs...)
	}
	return out, nil
}

// expandTierRecord renders one ManagedDatabase / StaticSite record in place,
// copying the record's labels (managed set, env stamp, cluster route) onto
// every rendered object.
func expandTierRecord(i int, m map[string]any) ([]any, error) {
	var obj runtime.Object
	switch m["kind"] {
	case "StaticSite":
		obj = &deployv1alpha1.StaticSite{}
	case "ManagedDatabase":
		obj = &deployv1alpha1.ManagedDatabase{}
	default:
		return nil, fmt.Errorf("manifest item %d: %s is not a forge.dev deploy kind (Workload, ManagedDatabase, StaticSite)", i, describeManifest(m))
	}
	if err := decodeStrict(m, obj); err != nil {
		return nil, fmt.Errorf("manifest item %d: %w", i, err)
	}
	md, _ := m["metadata"].(map[string]any)
	namespace, _ := md["namespace"].(string)
	labels, _ := md["labels"].(map[string]any)
	partOf, _ := labels[deploy.LabelPartOf].(string)
	rendered, err := deploy.Render(obj, deploy.Context{Namespace: namespace, PartOf: partOf})
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", describeManifest(m), err)
	}
	out := make([]any, 0, len(rendered))
	for _, r := range rendered {
		objLabels := r.GetLabels()
		if objLabels == nil {
			objLabels = map[string]string{}
		}
		for k, v := range labels {
			if s, ok := v.(string); ok {
				objLabels[k] = s
			}
		}
		r.SetLabels(objLabels)
		out = append(out, r.Object)
	}
	return out, nil
}

// expandOpenBao renders one OpenBao record (Bundle.openbao) into the secret
// store's objects, copying the record's labels (cluster route, env stamp) onto
// every one of them. The component's own owner and part-of labels win: they
// are what the single-owner check reads.
func expandOpenBao(i int, m map[string]any) ([]any, error) {
	var rec struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name      string            `json:"name"`
			Namespace string            `json:"namespace"`
			Labels    map[string]string `json:"labels"`
		} `json:"metadata"`
		Spec openbao.Spec `json:"spec"`
	}
	if err := decodeStrict(m, &rec); err != nil {
		return nil, fmt.Errorf("manifest item %d: %w", i, err)
	}
	objs, err := openbao.Render(rec.Spec)
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", describeManifest(m), err)
	}
	out := make([]any, 0, len(objs))
	for _, o := range objs {
		labels := o.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		for _, k := range []string{ClusterRoutingLabel, deploy.LabelEnv} {
			if v := rec.Metadata.Labels[k]; v != "" {
				labels[k] = v
			}
		}
		o.SetLabels(labels)
		out = append(out, o.Object)
	}
	return out, nil
}

// expandObservabilityAgent renders one ObservabilityAgent record (the env's
// observability declaration, one per cluster) into the node agent's objects,
// copying the record's labels (cluster route, env stamp) onto every one of
// them, the cluster-scoped Namespace and ClusterRole included.
func expandObservabilityAgent(i int, m map[string]any) ([]any, error) {
	var rec struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name      string            `json:"name"`
			Namespace string            `json:"namespace"`
			Labels    map[string]string `json:"labels"`
		} `json:"metadata"`
		Spec deploy.ObservabilityAgentSpec `json:"spec"`
	}
	if err := decodeStrict(m, &rec); err != nil {
		return nil, fmt.Errorf("manifest item %d: %w", i, err)
	}
	objs, err := deploy.RenderObservabilityAgent(rec.Spec, deploy.ObservabilityContext{
		Namespace: rec.Metadata.Namespace,
		PartOf:    rec.Metadata.Labels[deploy.LabelPartOf],
		Env:       rec.Metadata.Labels[deploy.LabelEnv],
	})
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", describeManifest(m), err)
	}
	out := make([]any, 0, len(objs))
	for _, o := range objs {
		labels := o.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		if c := rec.Metadata.Labels[ClusterRoutingLabel]; c != "" {
			labels[ClusterRoutingLabel] = c
		}
		o.SetLabels(labels)
		out = append(out, o.Object)
	}
	return out, nil
}

// decodeStrict decodes one declaration record into its typed object,
// STRICTLY. A key the Go type does not know is an error rather than silently
// dropped, because a dropped field is exactly the drift this whole design
// exists to make impossible.
func decodeStrict(m map[string]any, obj any) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(obj); err != nil {
		return fmt.Errorf("decode %s: %w", describeManifest(m), err)
	}
	return nil
}

func describeManifest(m map[string]any) string {
	md, _ := m["metadata"].(map[string]any)
	return fmt.Sprintf("%v %v/%v", m["kind"], md["namespace"], md["name"])
}

// KubectlArgs prepends `--context <kctx>` to a kubectl argument list
// when kctx is non-empty, and returns the args unchanged otherwise.
// Threading the context PER COMMAND (rather than mutating the global
// active context via `kubectl config use-context`) is what makes
// concurrent multi-cluster `forge env deploy` safe — two deploys sharing
// one kubeconfig can target different clusters without racing on the
// single global context. An empty kctx means "use kubectl's
// current/default context", the unchanged single-cluster behaviour.
//
// `--context` is a global kubectl flag, so it's valid as the leading
// argument before any subcommand (apply / wait / rollout / get / …).
func KubectlArgs(kctx string, args ...string) []string {
	if kctx == "" {
		return args
	}
	out := make([]string, 0, len(args)+2)
	out = append(out, "--context", kctx)
	return append(out, args...)
}

// kubectlCmd builds an *exec.Cmd for `kubectl <args>` with the context
// flag threaded in via KubectlArgs. The single construction point keeps
// the per-command `--context` invariant in one place (and testable).
func kubectlCmd(ctx context.Context, kctx string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "kubectl", KubectlArgs(kctx, args...)...)
}

// KubectlApply pipes the rendered YAML document stream into
// `kubectl [--context <kctx>] apply --server-side --force-conflicts -f -`.
// Stdout/stderr are inherited so the user sees the per-resource
// `created`/`configured`/`unchanged` lines kubectl emits. kctx (when
// non-empty) targets a specific kubectl context for this command only.
//
// --force-conflicts is unconditional and deliberate: forge is the
// declarative source of truth, so its Server-Side Apply field manager
// always wins. Without it, any resource previously touched by a plain
// `kubectl apply` (manager `kubectl-client-side-apply`, common after
// manual debugging or an older bootstrap) makes SSA abort the whole
// deploy with "Apply failed with N conflicts ... conflicts with
// kubectl-client-side-apply" / `exit status 1`. Forcing forge to take
// ownership of those fields overrides the stale manager and keeps the
// deploy idempotent. (--force-conflicts is an SSA-only flag — it has no
// effect without --server-side, which we always pass.)
//
// An empty kctx is a HARD ERROR, never a fall-through to kubectl's
// current/default context. The target cluster is declarative —
// forge.K8sCluster.cluster in the env's KCL IS the context — so an empty
// value here means some group failed to carry its declared cluster. Applying
// to whatever context happens to be active is the footgun where an unrelated
// tool (e.g. `k3d cluster create`, which silently flips current-context) makes
// a deploy land in the WRONG cluster. Writes must fail LOUDLY instead. (Reads
// /waits via KubectlArgs may still default; only the destructive apply is
// gated here.)
func KubectlApply(ctx context.Context, kctx, manifests string) error {
	return KubectlApplyNamespaced(ctx, kctx, "", manifests)
}

// KubectlApplyNamespaced is KubectlApply with an explicit DEFAULT namespace
// for the objects in the stream that do not name one themselves: it passes
// `-n <namespace>` to kubectl, which is the namespace kubectl assigns to any
// namespaced object whose `metadata.namespace` is absent.
//
// WHY THIS EXISTS — the `default`-namespace bug. `helm template -n <ns>`
// renders a chart FOR a namespace, but a chart is not obliged to stamp
// `metadata.namespace` onto its output, and many do not: Helm's own install
// path supplies the namespace from the client at apply time, so the field is
// redundant there and charts legitimately omit it. Rendering such a chart and
// applying it with NO namespace flag sends every namespace-less object to
// kubectl's default namespace — `default` — regardless of what the chart
// declared.
//
// That is not hypothetical, and it is chart-specific rather than universal:
//
//	fluxcd-community/flux2 2.19.1   40 objects rendered, 0 carry metadata.namespace
//	envoyproxy/gateway-helm v1.7.2  18 objects rendered, 12 carry it; the 6 that
//	                                do not are all cluster-scoped, so they need none
//
// So Flux's six controllers landed in `default` while `flux-system` sat empty,
// and Envoy — declared the same way, in the same bundle, for months — was
// unaffected the whole time, because its templates stamp the field. A chart
// "working" therefore proved nothing about the next one; the namespace has to
// come from forge's declaration, not from the chart's generosity.
//
// An empty namespace reproduces the old behaviour exactly (no flag passed),
// which is what every non-chart caller wants: forge's own KCL render stamps
// `metadata.namespace` on everything it emits.
//
// WHY `-n` AND NOT STAMPING `metadata.namespace` DURING RENDER. Stamping
// produces a self-describing manifest, which is genuinely nicer, but it
// requires knowing each object's SCOPE — stamping a namespace onto a
// cluster-scoped object is an error, not a no-op. That scope is not in the
// manifest and cannot be derived from a static kind list, because a chart's
// own CRDs DEFINE new cluster-scoped kinds: on this cluster GatewayClass,
// XMesh and ETCDSnapshotFile are all cluster-scoped, and forge cannot know
// that for a CRD it is applying in the same batch. Only the apiserver knows,
// and `-n` is precisely how kubectl asks it: measured, the flag is applied to
// namespaced objects and IGNORED for cluster-scoped ones (a bare ClusterRole
// applied with `-n` comes back with `metadata.namespace` empty). So `-n`
// delegates the scope decision to the one component that can make it
// correctly, and no forge-side kind table can drift out of date.
//
// A doc that DECLARES its own namespace keeps it: kubectl refuses to relocate
// it — `the namespace from the provided object "kube-system" does not match
// the namespace "flux-system"` — and it fails the whole stream rather than
// applying some of it. So those docs are separated out and applied WITHOUT
// the flag (see splitByTargetNamespace), which both honours the chart's
// intent and keeps the mismatch from breaking the deploy.
func KubectlApplyNamespaced(ctx context.Context, kctx, namespace, manifests string) error {
	if strings.TrimSpace(kctx) == "" {
		return fmt.Errorf("refusing to apply manifests without an explicit kubectl context: " +
			"the target cluster is declarative (forge.K8sCluster.cluster in the env's KCL) — " +
			"forge never falls back to the current context for a write")
	}
	// The same discipline for every object in the stream: one stamped for
	// another cluster is never written to this one, whichever path brought it
	// here. Checked before the namespace split, so a refusal writes nothing.
	if err := refuseForeignClusterDocs(kctx, manifests); err != nil {
		return err
	}
	// Docs that name a DIFFERENT namespace than the target cannot ride the
	// `-n` flag (kubectl rejects the whole stream), so they apply separately
	// with no flag and keep the namespace they declared. In the common case
	// (every doc either declares the target namespace or declares none) this
	// is empty and there is exactly ONE apply, as before.
	onTarget, elsewhere := splitByTargetNamespace(manifests, namespace)
	if strings.TrimSpace(elsewhere) != "" {
		if err := applyStreamInNamespace(ctx, kctx, "", elsewhere); err != nil {
			return err
		}
	}
	if strings.TrimSpace(onTarget) == "" {
		return nil
	}
	return applyStreamInNamespace(ctx, kctx, namespace, onTarget)
}

// applyStreamInNamespace runs ONE server-side apply of a manifest stream with
// an optional default namespace, including the immutable-field recovery, the
// rollout-strategy recovery, and the apply-completeness verification every
// forge apply is gated on.
func applyStreamInNamespace(ctx context.Context, kctx, namespace, manifests string) error {
	// The strategy recovery wraps the INNER apply, so it heals a
	// stale-rollingUpdate wedge on the immutable recovery's re-applies too,
	// not just the first attempt. Both recoveries are no-ops until an apply
	// has already failed with their own specific error.
	apply := func() (string, string, error) {
		return applyWithStrategyRecovery(
			manifests,
			func() (string, string, error) { return applyOnce(ctx, kctx, namespace, manifests) },
			func(t strategyTarget) error {
				return kubectlPatchStrategy(ctx, kctx, t, namespace)
			},
		)
	}
	// The volume-resize recovery wraps the strategy-healed apply the same
	// way, for a StatefulSet whose declared volume size grew: it grows the
	// claims and orphan-recreates the object (volume_resize_recovery.go).
	// StatefulSet is not recreatable, so the immutable recovery outside never
	// touches one.
	withStrategy := apply
	apply = func() (string, string, error) {
		return applyWithVolumeResizeRecovery(manifests, namespace, withStrategy,
			func(desired *appsv1.StatefulSet) (stsresize.Plan, error) {
				return stsresize.Converge(ctx, kubectlStatefulSets{kctx: kctx}, desired)
			},
		)
	}
	// The recovery's delete/get must be scoped the SAME way the apply was.
	// immutableTarget.Namespace comes from the offending doc's
	// `metadata.namespace`, which is empty for exactly the charts this fix is
	// about (flux renders a namespace-less `flux-flux-check` Job) — and an
	// unscoped delete resolves to `default`, i.e. it would look for the
	// object in the wrong namespace and silently recover nothing. Defaulting
	// the target to the apply's namespace keeps the two in agreement.
	applyStdout, err := applyWithImmutableRecovery(
		manifests,
		apply,
		func(t immutableTarget) error {
			return kubectlDeleteResource(ctx, kctx, withDefaultNamespace(t, namespace))
		},
		func(t immutableTarget) error {
			return kubectlWaitResourceGone(ctx, kctx, withDefaultNamespace(t, namespace))
		},
	)
	if err != nil {
		return err
	}
	// The apply exited 0 — which says the batch was accepted, NOT that
	// every object in it was. Prove it before letting the caller call this
	// a success. See apply_completeness.go for the incident this closes.
	return verifyApplyComplete(manifests, applyStdout)
}

// immutableRecoveryAttempts bounds how many delete→wait-gone→re-apply
// cycles the recovery runs before giving up. A warm redeploy whose only
// conflict is the immutable migrate Job heals on the FIRST cycle; the
// extra attempts exist purely to absorb the residual race where the
// re-apply still observes the not-yet-finalized Job (see
// applyWithImmutableRecovery). Two is enough in practice — the explicit
// wait-gone poll already closes the window — but a small bound keeps a
// genuinely stuck resource from looping forever.
const immutableRecoveryAttempts = 3

// applyWithImmutableRecovery runs apply and, on the recoverable
// immutable-field failure only, deletes the one offending resource,
// WAITS for it to actually disappear, then re-applies — retrying that
// delete→wait-gone→re-apply cycle a bounded number of times if the
// re-apply STILL hits the immutable error. The kubectl-touching steps are
// passed as closures so the sequencing is unit-testable without a live
// cluster; KubectlApply wires the real applyOnce / kubectlDeleteResource /
// kubectlWaitResourceGone.
//
// A k8s Job's spec.template is immutable, so a warm re-deploy whose image
// tag changed (every commit does) makes the migrate Job's apply fail
// "is invalid: ... field is immutable" even though cold (fresh namespace)
// works.
//
// Why a LOOP and an explicit wait-gone, not a single delete+re-apply:
// `kubectl delete --wait --cascade=foreground` blocks until the API
// reports the object deleted, but the recovery still observed a flake on
// CLOUD where the SAME code healed to exit 0 on staging yet exited 1 on
// prod. The cause is a finalization race — under load the Job (and its GC)
// can still be settling when the re-apply runs, so the re-apply re-reads
// the not-yet-gone Job and re-hits `field is immutable`. The single-shot
// recovery then surfaced that second immutable error as the deploy's exit
// code even though the resource WAS on its way out. Polling the resource
// to NotFound before re-applying, and retrying the whole cycle if the
// re-apply still races, makes a warm cloud redeploy whose only conflict is
// the immutable Job deterministically exit 0.
//
// Scope is kept to the immutable-Job case: the loop only re-enters while
// the re-apply keeps returning the SAME immutable-field error for the same
// resource. Any failure that ISN'T the immutable-field case — a delete
// error, a re-apply error that isn't immutable, or attempts exhausted —
// surfaces the relevant error unchanged. Unrelated apply failures are
// never masked.
//
// The apply closure returns (stdout, stderr, err): stderr is what the
// recovery classifies on, and stdout is handed back to the caller for the
// apply-completeness check. On a successful recovery that stdout is the
// WINNING re-apply's, never the failed first attempt's — the first attempt
// aborted partway and its object list is not what ended up on the cluster.
func applyWithImmutableRecovery(
	manifests string,
	apply func() (stdout, stderr string, err error),
	del func(immutableTarget) error,
	waitGone func(immutableTarget) error,
) (string, error) {
	stdout, stderr, err := apply()
	if err == nil {
		return stdout, nil
	}
	targets := immutableResources(stderr, manifests)
	if len(targets) == 0 {
		// Not the recoverable immutable case — surface unchanged.
		return stdout, err
	}

	// Recovery loop: delete EVERY offending immutable resource the batch
	// reported, wait for each to actually be gone, then re-apply the whole
	// batch. Repeat (bounded) only while every re-apply failure is itself a
	// (possibly different) set of recoverable immutable conflicts.
	//
	// Why "every" and not "the same one": forge applies the workload batch
	// (the migrate Job + the Deployments) in a SINGLE server-side apply, so a
	// warm redeploy can report MORE THAN ONE immutable conflict — and even
	// when the first apply names only the migrate Job, a re-apply under load
	// can surface a DIFFERENT immutable resource. The old loop only re-entered
	// for the SAME Kind/Name; any other immutable resource was misclassified
	// as "unrelated" and its non-zero status propagated to the deploy's exit
	// code (exit 1) even though it was equally recoverable and every pod ended
	// healthy. This is the staging-0 (1 immutable Job) / prod-1 (a second
	// immutable conflict slipped through) split. We now recover the full
	// immutable set each cycle, so the deploy exits 0 once — and only once —
	// every immutable conflict has been deleted+re-applied successfully.
	lastErr := err
	for attempt := 1; attempt <= immutableRecoveryAttempts; attempt++ {
		for _, res := range targets {
			if delErr := del(res); delErr != nil {
				// A delete failure is its own problem — surface it rather than
				// the original immutable error so the cause is visible.
				return stdout, fmt.Errorf("recovering immutable %s %q: delete: %w", res.Kind, res.Name, delErr)
			}
			// Poll the resource to NotFound so the re-apply has a real
			// happens-before. Best-effort: a wait error (e.g. kubectl flake)
			// shouldn't abort the recovery — the re-apply below is the real
			// arbiter of success — so it's logged and we proceed.
			if waitGone != nil {
				if wErr := waitGone(res); wErr != nil {
					fmt.Printf("[deploy]   Note: wait-for-deletion of %s %q: %v (re-applying anyway)\n", res.Kind, res.Name, wErr)
				}
			}
		}
		reStdout, reStderr, reErr := apply()
		// The re-apply is the authoritative account of what is on the
		// cluster now: the first attempt aborted partway, so its object
		// list would under-report and make the completeness check fire on
		// a recovery that in fact healed everything.
		stdout = reStdout
		if reErr == nil {
			// Every immutable conflict recovered and the re-apply is clean —
			// the overall apply is a success (exit 0), regardless of the stale
			// non-zero status the ORIGINAL batched apply returned.
			return stdout, nil
		}
		// Keep looping only while the re-apply failure is ENTIRELY composed of
		// recoverable immutable conflicts (the same one still racing us, a
		// different immutable resource, or both). Any failure that ISN'T fully
		// immutable-recoverable is a genuine error and surfaces unchanged.
		reTargets := immutableResources(reStderr, manifests)
		if len(reTargets) == 0 {
			return stdout, reErr
		}
		lastErr = reErr
		targets = reTargets
	}
	// Bounded attempts exhausted and a resource is still immutable — recovery
	// genuinely failed. Surface the last immutable error.
	return stdout, lastErr
}

// applyOnce runs a single `kubectl [--context] apply --server-side
// --force-conflicts -f -` over the manifest stream. Both streams are
// tee'd to the real os.Stdout/os.Stderr (so the user still sees the
// per-resource created/configured/unchanged lines and any error as they
// happen) AND captured, because the apply's own output is the only
// evidence forge has about what actually landed: stderr drives the
// immutable-field recovery, and stdout drives the apply-completeness
// check (see apply_completeness.go — a server-side apply can reject one
// object and still exit 0, and the rejected object simply has no line).
// kubectlApplyArgs builds the argv (after the `--context` pair KubectlArgs
// prepends) for a server-side apply, threading `-n <namespace>` when a default
// namespace was supplied.
//
// A separate function so the argv is assertable without exec'ing kubectl, and
// so EVERY apply pass shares one construction point — the bug this closes was
// a namespace present at render time and absent at apply time, which is
// exactly what two independent argv builders produce.
func kubectlApplyArgs(namespace string) []string {
	args := []string{"apply", "--server-side", "--force-conflicts"}
	if strings.TrimSpace(namespace) != "" {
		args = append(args, "-n", namespace)
	}
	return append(args, "-f", "-")
}

func applyOnce(ctx context.Context, kctx, namespace, manifests string) (applyStdout, applyStderr string, err error) {
	args := kubectlApplyArgs(namespace)
	collector, _ := ctx.Value(chartPruneContextKey{}).(*chartPrune)
	if collector != nil {
		if err := collector.checkLock(ctx); err != nil {
			return "", "", err
		}
		args = append(args, "-o", "json")
	}
	cmd := kubectlCmd(ctx, kctx, args...)
	cmd.Stdin = strings.NewReader(manifests)
	var outBuf, errBuf bytes.Buffer
	if collector == nil {
		cmd.Stdout = io.MultiWriter(os.Stdout, &outBuf)
	} else {
		cmd.Stdout = &outBuf
	}
	cmd.Stderr = io.MultiWriter(os.Stderr, &errBuf)
	err = cmd.Run()
	if collector != nil && err == nil {
		var status string
		status, err = collector.collectApply(outBuf.Bytes())
		if err != nil {
			return "", errBuf.String(), err
		}
		fmt.Print(status)
		return status, errBuf.String(), nil
	}
	return outBuf.String(), errBuf.String(), err
}

// immutableTarget identifies the single resource an immutable-field apply
// failure was about, so the recovery can scope its delete to exactly that
// object (never a wipe).
type immutableTarget struct {
	Kind      string
	Name      string
	Namespace string // empty = cluster-scoped or unknown; delete omits -n
}

// withDefaultNamespace fills in an immutableTarget's Namespace from the
// apply's default namespace when the manifest did not declare one, so the
// recovery's `kubectl delete` / `kubectl get` are scoped exactly as the apply
// that failed was. A target that already names a namespace is unchanged, and
// an empty default leaves it empty (kubectl then omits `-n`, which is correct
// for a cluster-scoped object and ignored for one — measured: `kubectl delete
// clusterrole -n <ns>` warns "not scoped to the provided namespace" and
// deletes the right object).
func withDefaultNamespace(t immutableTarget, namespace string) immutableTarget {
	if t.Namespace == "" {
		t.Namespace = strings.TrimSpace(namespace)
	}
	return t
}

// immutableResource decides whether an apply failure is the recoverable
// immutable-field case and, if so, which resource to reset. It keys off
// the error text k8s emits for an immutable update, in EITHER apply mode —
//
//	client-side: The Job "control-plane-migrate" is invalid: spec.template: … field is immutable
//	server-side: Job.batch "control-plane-migrate" is invalid: spec.template: … field is immutable
//
// requiring BOTH the `is invalid:` framing and `field is immutable` so it
// never fires on an unrelated apply error. The kind+name come from that
// message (generic — Job is the motivating case, but any immutable kind
// matches); parseInvalidResource normalizes the server-side GVR form
// (`Job.batch` → `Job`) so the namespace is recovered by matching that
// kind+name back to its source document in the applied manifests. Returns
// ok=false when the stderr isn't an immutable-field error or the named
// resource can't be found in the bundle.
func immutableResource(stderr, manifests string) (immutableTarget, bool) {
	if !strings.Contains(stderr, "is invalid:") || !reportsImmutableUpdate(stderr) {
		return immutableTarget{}, false
	}
	kind, name, ok := parseInvalidResource(stderr)
	if !ok {
		return immutableTarget{}, false
	}
	ns := namespaceForResource(manifests, kind, name)
	return immutableTarget{Kind: kind, Name: name, Namespace: ns}, true
}

// forbiddenUpdateRe matches the OTHER way the apiserver says "this field
// cannot change": a type's own update validation using field.Forbidden rather
// than the generic ValidateImmutableField. StorageClass is the case that
// matters — its validation answers a `parameters` or `provisioner` change with
//
//	The StorageClass "workspace-ssd" is invalid: parameters: Forbidden: updates to parameters are forbidden.
//
// (verified on GKE v1.35.8), while k3d's apiserver phrased the same rejection
// as `field is immutable`. Matching only that phrasing let the k3d-pinned
// recovery pass its tests and then fail a real prod deploy.
var forbiddenUpdateRe = regexp.MustCompile(`Forbidden: updates to [^\s]+ (?:is|are) forbidden`)

// reportsImmutableUpdate reports whether an apply error body is the apiserver
// refusing to change a field in place, in either phrasing. It decides only
// "immutable or not"; whether the kind may be deleted to heal it is
// recreatableKinds' call, so a forbidden update on a kind that owns data still
// fails the deploy.
func reportsImmutableUpdate(body string) bool {
	return strings.Contains(body, "field is immutable") || forbiddenUpdateRe.MatchString(body)
}

// recreatableKinds is release.RecreatableKind: the allowlist lives in
// pkg/release so the deploy plan names exactly the objects this recovery may
// delete and re-apply (see that list's comment for what membership means).

// immutableResources is the batch-aware extractor: it returns EVERY distinct
// recoverable immutable-field conflict reported by a single apply, not just
// the first. forge applies the workload batch (the migrate Job + Deployments)
// in ONE `kubectl apply --server-side`, so a warm redeploy can fail with more
// than one immutable conflict at once — kubectl emits one
// `<Kind> "<name>" is invalid: … field is immutable` line per offending
// resource. Healing only the first (immutableResource) left the rest to
// re-surface on the re-apply and propagate a non-zero exit even though every
// conflict was independently recoverable; that was the deploy-exits-1-on-a-
// healthy-cluster bug. This scans all `is invalid:` segments, keeps only the
// ones whose error is `field is immutable`, parses each Kind/name, recovers
// the namespace from the manifest bundle, and de-dupes by Kind+Name+Namespace
// (stable order: first occurrence wins). Returns an empty slice when the
// stderr carries NO recoverable immutable conflict — the caller treats that as
// "not the recoverable case, surface the error unchanged."
func immutableResources(stderr, manifests string) []immutableTarget {
	const inv = " is invalid:"
	var out []immutableTarget
	seen := map[string]struct{}{}
	rest := stderr
	for {
		idx := strings.Index(rest, inv)
		if idx < 0 {
			break
		}
		// The segment up to and including this `is invalid:` head holds the
		// `<Kind> "<name>"` for THIS conflict; parseInvalidResource keys off the
		// LAST quoted token before the framing, so feeding it the head is exact.
		head := rest[:idx+len(inv)]
		// Advance past this match so the next iteration finds the following one.
		rest = rest[idx+len(inv):]
		// Scope the "field is immutable" check to THIS resource's error body —
		// the text between this `is invalid:` and the next `is invalid:` (or end
		// of stderr). Avoids a later resource's immutability bleeding onto an
		// earlier non-immutable one.
		body := rest
		if next := strings.Index(rest, inv); next >= 0 {
			body = rest[:next]
		}
		if !reportsImmutableUpdate(body) {
			continue
		}
		kind, name, ok := parseInvalidResource(head)
		if !ok || !release.RecreatableKind(kind) {
			continue
		}
		ns := namespaceForResource(manifests, kind, name)
		key := kind + "\x00" + name + "\x00" + ns
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, immutableTarget{Kind: kind, Name: name, Namespace: ns})
	}
	return out
}

// parseInvalidResource pulls the Kind and name out of a k8s
// `<Kind> "<name>" is invalid:` message. It scans for the `"…"` quoted
// name and takes the single token immediately before the opening quote as
// the Kind. Returns ok=false if the shape doesn't match.
//
// kubectl emits TWO shapes for the same failure depending on the apply
// mode, and the recovery must handle both:
//
//   - client-side apply:  The Job "control-plane-migrate" is invalid: …
//   - server-side apply:  Job.batch "control-plane-migrate" is invalid: …
//
// forge always applies with `--server-side` (see applyOnce), so the CLOUD
// warm-redeploy path always hits the SECOND shape, where the token before
// the name is the GVR-style `<resource>.<group>` ("Job.batch"), not the
// bare kind ("Job"). The `.group` suffix is stripped here so the returned
// kind is the bare `kind:` value — which is what namespaceForResource
// matches against the manifest documents to recover the namespace. Without
// this normalization the SSA error parsed kind="Job.batch", matched no
// `kind: Job` document, lost the namespace, and the recovery delete ran in
// the wrong (default) namespace as a no-op — so the re-apply hit the still
// immutable Job again and the deploy exited 1. (The e2e path never tripped
// it: a cold apply into a fresh namespace has no pre-existing Job to
// conflict with, so the recovery only ever fires on a warm cloud
// redeploy.) `kubectl delete` accepts the bare kind too, so deleting by
// the normalized kind is correct for both shapes.
func parseInvalidResource(stderr string) (kind, name string, ok bool) {
	const inv = " is invalid:"
	idx := strings.Index(stderr, inv)
	if idx < 0 {
		return "", "", false
	}
	head := stderr[:idx] // e.g. `The Job "control-plane-migrate"` or `Job.batch "control-plane-migrate"`
	closeIdx := strings.LastIndex(head, `"`)
	if closeIdx < 0 {
		return "", "", false
	}
	open := strings.LastIndex(head[:closeIdx], `"`)
	if open < 0 {
		return "", "", false
	}
	name = head[open+1 : closeIdx]
	// Kind is the last whitespace-delimited token before the opening quote.
	fields := strings.Fields(strings.TrimSpace(head[:open]))
	if len(fields) == 0 || name == "" {
		return "", "", false
	}
	kind = fields[len(fields)-1]
	// Strip the `.group` suffix of the server-side-apply GVR form
	// ("Job.batch" → "Job") so the kind matches the bare `kind:` value in
	// the manifest documents (namespaceForResource) on BOTH apply modes.
	if dot := strings.IndexByte(kind, '.'); dot > 0 {
		kind = kind[:dot]
	}
	return kind, name, true
}

// namespaceForResource finds the metadata.namespace of the manifest
// document whose kind+name match, so the recovery delete can target the
// right namespace. Returns "" when no doc matches or the doc carries no
// namespace (cluster-scoped, or namespace defaulted at apply time).
func namespaceForResource(manifests, kind, name string) string {
	for _, doc := range splitDocs(manifests) {
		var m struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name      string `yaml:"name"`
				Namespace string `yaml:"namespace"`
			} `yaml:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
			continue
		}
		if m.Kind == kind && m.Metadata.Name == name {
			return m.Metadata.Namespace
		}
	}
	return ""
}

// kubectlDeleteResource issues a scoped, idempotent
// `kubectl delete <kind> <name> [-n <ns>] --ignore-not-found=true
// --wait=true --cascade=foreground` for the one resource an immutable
// apply failed on. --ignore-not-found keeps it safe if the object vanished
// between apply and delete.
//
// --wait + --cascade=foreground close the recovery RACE: the immutable
// recovery deletes the offending resource and immediately re-applies it,
// so the delete MUST finalize before the re-apply runs. For a Job the
// dependents (its pods) matter — a default background delete returns as
// soon as the delete is ACCEPTED, while the Job object and its pods are
// still terminating, and the re-apply can then either re-hit the immutable
// (the Job hasn't actually gone) or collide with the GC of the old pods.
// Foreground cascade makes `kubectl delete` block until the Job AND its
// dependents are gone, giving the re-apply a real happens-before. This is
// what makes the warm CLOUD redeploy deterministic rather than racy.
func kubectlDeleteResource(ctx context.Context, kctx string, t immutableTarget) error {
	args := []string{
		"delete", strings.ToLower(t.Kind), t.Name,
		"--ignore-not-found=true",
		"--wait=true",
		"--cascade=foreground",
	}
	if t.Namespace != "" {
		args = append(args, "-n", t.Namespace)
	}
	cmd := kubectlCmd(ctx, kctx, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// kubectlPatchStrategy issues the scoped JSON merge patch that reconciles one
// Deployment's rollout strategy to what the manifest declares, removing a
// rollingUpdate block another field manager's defaulting left stranded. See
// strategy_recovery.go for why this cannot be expressed as an apply.
//
// A merge patch, not `--type=strategic` or an apply: `null` is a DELETION in a
// JSON merge patch, which is the one operation available here — the field is
// owned by kube-controller-manager, and SSA can only remove a field the
// applying manager owns.
//
// The namespace is defaulted the same way the apply's was, so the patch
// targets the object the apply actually failed on rather than `default`.
func kubectlPatchStrategy(ctx context.Context, kctx string, t strategyTarget, namespace string) error {
	patch, err := strategyMergePatch(t)
	if err != nil {
		return err
	}
	args := []string{"patch", strings.ToLower(t.Kind), t.Name, "--type=merge", "-p", patch}
	// Same defaulting the immutable recovery uses: the manifest's own
	// namespace when it declares one, otherwise the apply's default.
	if ns := withDefaultNamespace(immutableTarget{Namespace: t.Namespace}, namespace).Namespace; ns != "" {
		args = append(args, "-n", ns)
	}
	cmd := kubectlCmd(ctx, kctx, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// kubectlWaitResourceGone polls `kubectl get <kind> <name> [-n <ns>]`
// until it reports NotFound (the resource is truly gone), or the timeout
// elapses. This is the explicit happens-before the immutable recovery
// needs BEFORE it re-applies: `kubectl delete --wait --cascade=foreground`
// returns when the API has accepted+finalized the delete, but under cloud
// load the recovery still observed the re-apply racing a not-yet-gone Job
// (the staging-0 / prod-1 split). Polling get→NotFound closes that window
// deterministically rather than trusting delete's --wait alone.
//
// Returns nil the instant a get fails with `NotFound` (the success
// signal). A get that SUCCEEDS means the object still exists, so we keep
// polling. Any other get error (transient kubectl/API flake) is treated as
// "unknown, keep trying" until the deadline. On timeout it returns a
// non-nil error, which the caller logs but does not treat as fatal — the
// re-apply is the real arbiter.
func kubectlWaitResourceGone(ctx context.Context, kctx string, t immutableTarget) error {
	const (
		timeout  = 30 * time.Second
		interval = 500 * time.Millisecond
	)
	deadline := time.Now().Add(timeout)
	for {
		args := []string{"get", strings.ToLower(t.Kind), t.Name, "--ignore-not-found=true", "-o", "name"}
		if t.Namespace != "" {
			args = append(args, "-n", t.Namespace)
		}
		cmd := kubectlCmd(ctx, kctx, args...)
		out, err := cmd.Output()
		// --ignore-not-found makes a missing resource exit 0 with EMPTY
		// stdout. Empty output therefore means "gone" — the success signal.
		if err == nil && strings.TrimSpace(string(out)) == "" {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for %s %q to be deleted", timeout, t.Kind, t.Name)
		}
		time.Sleep(interval)
	}
}

// EnsureNamespace idempotently creates the target namespace so resources
// scoped to it (e.g. dotenv-projected Secrets) can be applied BEFORE the
// main manifest stream — which is where the Namespace object itself is
// rendered. Without this, the first thing the deploy applies (the
// secret_provider Secrets) lands before the Namespace exists and fails
// "namespaces \"…\" not found". The full manifest apply later re-applies
// the Namespace with its labels (server-side apply is idempotent), so this
// early create is a pure ordering fix, not a competing owner.
//
// Uses `kubectl create --dry-run=client -o yaml | kubectl apply` so a
// pre-existing namespace is a no-op rather than an AlreadyExists error.
func EnsureNamespace(ctx context.Context, kctx, namespace string) error {
	if strings.TrimSpace(kctx) == "" {
		return fmt.Errorf("refusing to create a namespace without an explicit kubectl context")
	}
	if strings.TrimSpace(namespace) == "" {
		return nil
	}
	manifest := "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: " + namespace + "\n"
	return KubectlApply(ctx, kctx, manifest)
}

// WaitRollout blocks until the named Deployment reaches a healthy
// rollout state, with a 60s timeout (down from 120s — dev iteration
// is the dominant path, and a failing rollout almost always means
// the image won't pull / the pod won't start, not that 120s of patience
// would have rescued it).
//
// On timeout, automatically dumps a short diagnostic burst so the
// developer doesn't have to context-switch to a separate kubectl
// shell to figure out WHY it's stuck. The dump covers:
//   - The non-Ready pod's `kubectl describe` Events tail (image-pull
//     errors, scheduling failures, readiness probe failures).
//   - Recent namespace events (`kubectl get events`) so cluster-level
//     issues (admission webhooks, missing ConfigMaps, etc.) show up.
//   - Pod log tail when the pod is at least pulled, captures
//     CrashLoopBackOff reasons like "NATS_URL is required".
//
// Diagnostics are best-effort — any kubectl invocation failure is
// swallowed so the wait error itself remains the primary signal.
func WaitRollout(ctx context.Context, kctx, name, namespace string) error {
	return WaitRolloutTimeout(ctx, kctx, name, namespace, DefaultRolloutTimeout)
}

// WaitRolloutTimeout is WaitRollout with an explicit per-resource budget,
// so a policy can give a cold cloud rollout the minutes it needs without
// making the local inner loop wait that long to learn the same thing.
func WaitRolloutTimeout(ctx context.Context, kctx, name, namespace string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = DefaultRolloutTimeout
	}
	cmd := kubectlCmd(ctx, kctx, "rollout", "status",
		"deployment/"+name,
		"-n", namespace,
		"--timeout="+timeout.String(),
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		diagnoseFailedRollout(ctx, kctx, name, namespace)
		return err
	}
	return nil
}

// rolloutError renders the collected failures as ONE error naming every
// resource, so a caller sees "these 3 of 12" rather than discovering them
// one re-run at a time.
func rolloutError(failures []string) error {
	if len(failures) == 0 {
		return nil
	}
	if len(failures) == 1 {
		return fmt.Errorf("rollout failed: %s did not become ready", failures[0])
	}
	return fmt.Errorf("rollout failed: %d resources did not become ready: %s",
		len(failures), strings.Join(failures, ", "))
}

// podSelectorForDeploy builds the label selector that matches the pods
// of a forge-deployed Deployment. forge stamps workloads with
// AppNameLabel (app.kubernetes.io/name), never a bare `app=` label, so
// the selector MUST reference the constant — a literal "app=" matched
// zero pods and silently produced empty rollout diagnostics.
func podSelectorForDeploy(deploy string) string {
	return AppNameLabel + "=" + deploy
}

// diagnoseFailedRollout prints the most useful kubectl diagnostics for
// a Deployment that didn't reach Ready in time. Indented under a clear
// banner so the existing "Warning: rollout for X" line precedes it.
//
// Order matters: pod status first (most likely culprit: ImagePullBackOff,
// CrashLoopBackOff), then events (admission / scheduling failures),
// then log tail (only when the container actually started — catches
// "missing env var" startup crashes).
func diagnoseFailedRollout(ctx context.Context, kctx, deploy, namespace string) {
	fmt.Printf("\n  ── Diagnostics for %s ──────────────────\n", deploy)

	// Pod status — phase, reason, message. The most useful single line.
	pods := kubectlCmd(ctx, kctx, "get", "pods",
		"-n", namespace,
		"-l", podSelectorForDeploy(deploy),
		"-o", "custom-columns=NAME:.metadata.name,STATUS:.status.phase,REASON:.status.containerStatuses[*].state.waiting.reason,MESSAGE:.status.containerStatuses[*].state.waiting.message",
		"--no-headers",
	)
	if out, err := pods.Output(); err == nil && len(out) > 0 {
		fmt.Println("  Pods:")
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			fmt.Println("    " + line)
		}
	}

	// Recent events for the deployment. Filtered to events that mention
	// the deploy name keeps the output bounded.
	events := kubectlCmd(ctx, kctx, "get", "events",
		"-n", namespace,
		"--sort-by=.lastTimestamp",
		"--field-selector=type!=Normal",
		"-o", "custom-columns=LAST:.lastTimestamp,TYPE:.type,REASON:.reason,OBJECT:.involvedObject.name,MESSAGE:.message",
		"--no-headers",
	)
	if out, err := events.Output(); err == nil && len(out) > 0 {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		// Keep the 5 most recent matching this deployment name.
		var kept []string
		for i := len(lines) - 1; i >= 0 && len(kept) < 5; i-- {
			if strings.Contains(lines[i], deploy) {
				kept = append([]string{lines[i]}, kept...)
			}
		}
		if len(kept) > 0 {
			fmt.Println("  Recent warning/error events:")
			for _, line := range kept {
				fmt.Println("    " + line)
			}
		}
	}

	// Log tail — best effort. When the pod never pulled (ImagePullBackOff)
	// kubectl returns an error which we swallow; when it crashed at
	// startup (CrashLoopBackOff) this surfaces the panic / "X is required"
	// line that explains why.
	logs := kubectlCmd(ctx, kctx, "logs",
		"deployment/"+deploy,
		"-n", namespace,
		"--tail=15",
		"--all-containers=true",
	)
	if out, err := logs.Output(); err == nil && len(strings.TrimSpace(string(out))) > 0 {
		fmt.Println("  Log tail (last 15 lines):")
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			fmt.Println("    " + line)
		}
	}

	fmt.Println("  ─────────────────────────────────────────────")
}

// WaitJobComplete blocks until the named Job in namespace reaches
// `condition=complete`. Timeout is 5m — Jobs in this lane are
// deploy-time migrations / backfills, which routinely run for minutes.
func WaitJobComplete(ctx context.Context, kctx, name, namespace string) error {
	return WaitJobCompleteTimeout(ctx, kctx, name, namespace, DefaultRolloutTimeout)
}

// WaitJobCompleteTimeout is WaitJobComplete under the policy's budget.
//
// `kubectl wait --for=condition=complete` does NOT return when the Job
// fails — a failed Job satisfies `condition=failed`, not `complete`, so
// this would otherwise sit out the whole timeout on a migration that
// crashed in its first second. Waiting on both conditions and reporting
// which one fired turns "5 minutes then a timeout" into an immediate,
// accurate failure.
func WaitJobCompleteTimeout(ctx context.Context, kctx, name, namespace string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = DefaultRolloutTimeout
	}
	type result struct {
		cond string
		err  error
	}
	done := make(chan result, 2)
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	for _, cond := range []string{"complete", "failed"} {
		go func(cond string) {
			cmd := kubectlCmd(waitCtx, kctx, "wait",
				"--for=condition="+cond,
				"job/"+name,
				"-n", namespace,
				"--timeout="+timeout.String(),
			)
			// Once the losing watcher is killed, stop waiting on its stderr
			// pipe after this long even if something it spawned still holds
			// it — the drain below must not turn into a hang.
			cmd.WaitDelay = 5 * time.Second
			// kubectl's own words are the only thing that tells an expired
			// budget ("timed out waiting for the condition") from any other
			// wait error, and it writes them to stderr. Discarded, a Job
			// still running at the deadline was graded as a FAILED Job with
			// the message "exit status 1" — wrong verdict, no diagnosis.
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()
			if err != nil {
				if msg := strings.TrimSpace(stderr.String()); msg != "" {
					err = fmt.Errorf("%w: %s", err, msg)
				}
			}
			done <- result{cond: cond, err: err}
		}(cond)
	}

	// The first watcher to return WITHOUT error is the verdict. The other is
	// then cancelled AND reaped before returning: cancel() only signals it,
	// and a kubectl still alive after this function returns is a child that
	// outlives its caller — racing the deploy's next step for the terminal,
	// and, under test, writing into a TempDir its cleanup is already removing.
	var verdict error
	decided := false
	for i := 0; i < 2; i++ {
		r := <-done
		if decided {
			continue // the loser, drained
		}
		switch {
		case r.err == nil && r.cond == "failed":
			verdict, decided = fmt.Errorf("job %s failed", name), true
		case r.err == nil:
			verdict, decided = nil, true
		default:
			verdict = r.err
			continue
		}
		cancel()
	}
	return verdict
}

// ListManagedDeployments returns the names of every forge-owned
// Deployment in the namespace (filtered by the
// `app.kubernetes.io/managed-by=forge` label). This is the
// authoritative list for rollout-watching — it covers shared-binary
// `<project>-<svc>` names, per-service `<svc>` names, operator and
// worker deployments, and anything packs add, without forge having to
// guess naming schemes per scaffold mode.
func ListManagedDeployments(ctx context.Context, kctx, namespace string) ([]string, error) {
	cmd := kubectlCmd(ctx, kctx, "get", "deployments",
		"-n", namespace,
		"-l", "app.kubernetes.io/managed-by=forge",
		"-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	names := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			names = append(names, l)
		}
	}
	return names, nil
}

// docDelimiter is the separator between documents in a `---`-separated
// multi-doc YAML stream. It is the single source of truth for both
// splitting (splitDocs) and re-joining manifest streams in this file.
const docDelimiter = "\n---\n"

// splitDocs splits a `---`-separated multi-doc YAML stream into its
// individual documents, trimming surrounding whitespace and dropping
// empty / whitespace-only docs. It is the single source of the
// delimiter for the manifest-scanning helpers below.
func splitDocs(manifests string) []string {
	raw := strings.Split(manifests, docDelimiter)
	docs := make([]string, 0, len(raw))
	for _, doc := range raw {
		if trimmed := strings.TrimSpace(doc); trimmed != "" {
			docs = append(docs, trimmed)
		}
	}
	return docs
}

// parsedDoc is the minimal view of a Kubernetes manifest the scanning
// helpers below need: the document's kind, its metadata.name, and its
// metadata.labels.
type parsedDoc struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name        string            `yaml:"name"`
		Namespace   string            `yaml:"namespace"`
		Labels      map[string]string `yaml:"labels"`
		Annotations map[string]string `yaml:"annotations"`
	} `yaml:"metadata"`
}

// parseDoc unmarshals a single YAML manifest document into a parsedDoc.
// A doc that doesn't parse returns ok=false so callers can apply their
// own conservative fallback (keep, or place in rest).
func parseDoc(doc string) (parsedDoc, bool) {
	var m parsedDoc
	if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
		return parsedDoc{}, false
	}
	return m, true
}

// SelectManifestsByGroup is the ONE uniform, mechanical, EXCLUSIVE
// `--target` filter: it keeps a `---`-separated multi-doc YAML stream's
// documents iff their KCL-declared service GROUP (`app.kubernetes.io/name`)
// is in targets, and DROPS every other document. The rule, doc by doc:
//
//   - KEEP iff `metadata.labels[app.kubernetes.io/name]` ∈ targets.
//   - DROP otherwise — including a doc whose group is NOT targeted AND a
//     doc with NO group label.
//
// EXCLUSIVE means EXACTLY that: nothing is kept implicitly. The group is
// always the service, and there is no synthetic group for shared infra:
// the env-shared manifests (Namespace, ConfigMap, RuntimeClass,
// NetworkPolicy) and bundle-level additional_manifests carry NO group, so a
// service `--target` DROPS them — they apply only on a bare deploy. A shared
// resource an app needs is kept under that app's `--target` ONLY if it was
// declared on the service's `manifests` (so it inherits the service group)
// in KCL. The decision of WHICH manifests a `--target` includes is thus
// entirely KCL-declared data; this function only filters on it. Callers pass
// targets only when non-empty — an empty/whole-env apply never calls this
// (Apply keeps everything when Targets is empty).
//
// A doc that doesn't parse as YAML carries no readable group and is DROPPED
// like any ungrouped doc: under exclusive targeting "I can't read its
// group" means "it isn't in the target set". Empty / whitespace-only docs
// are dropped.
func SelectManifestsByGroup(manifests string, targets []string) string {
	want := map[string]struct{}{}
	for _, t := range targets {
		want[t] = struct{}{}
	}
	var kept []string
	for _, doc := range splitDocs(manifests) {
		m, ok := parseDoc(doc)
		if !ok {
			continue
		}
		if _, in := want[ManifestGroup(m.Metadata.Labels)]; in {
			kept = append(kept, doc)
		}
	}
	return strings.Join(kept, docDelimiter)
}

// ScopeManifestsToGroup filters a `---`-separated env manifest stream down
// to the documents that belong on ONE deploy group's cluster. Routing is
// DECLARED-CLUSTER-ONLY: a manifest lands on the cluster of its OWNING
// service, identified by the service's `app.kubernetes.io/name` label.
// There is no "primary cluster" and no most-services heuristic — forge
// stamps that label on every workload AND on every per-service owned
// manifest (forge.Service.manifests, which is also how an image-less infra
// service pins env-level resources — Namespace, Gateways, CRDs — to a
// specific declared cluster). KCL still renders the whole env as one
// stream; this filter routes each doc to the cluster its owner declares.
//
// FIRST-CLASS cluster attribution takes priority over the app-label rule
// below: a manifest carrying the `forge.dev/cluster` label (stamped by
// forge's gateway/route builders when an ingress entity declares
// `cluster = "<name>"`) is routed by that label DIRECTLY — kept iff the
// label equals scope.Cluster, dropped otherwise — with no
// `app.kubernetes.io/name` indirection. This is the explicit replacement
// for the old trick of piggybacking an unrelated service's app label.
// When scope.Cluster is empty (the label-only routing path), the
// first-class match is skipped and the doc falls through to the app-label
// rule. A manifest WITHOUT the routing label always uses the app-label
// rule.
//
// The ownership rule, document by document (by its
// `app.kubernetes.io/name` label `a`):
//
//   - a ∈ scope.OwnApps   → KEEP. This group's own service or its owned
//     manifests (Deployment / Service / per-service RBAC / HPA / a CRD or
//     infra resource attached via forge.Service.manifests — all carry the
//     owning service's app label).
//   - a ∈ scope.OtherApps → DROP. Owned by a DIFFERENT cluster's group —
//     never apply it here (this is what stops cross-contamination: a
//     secondary cluster never receives the primary's services, CRDs, or
//     gateways, so it can't hard-fail on a CRD it doesn't have).
//   - a != "" but in NEITHER set → KEEP. An app-labelled doc whose owner
//     isn't in any group should not occur once every service is grouped;
//     keeping (rather than dropping or routing by guess) is the safe,
//     non-heuristic default.
//   - a == "" and kind == Namespace → KEEP. Every cluster needs its
//     namespace (a workload can't apply into a missing namespace), and the
//     namespace is genuinely env-wide, so it is replicated to every group.
//   - a == "" (any other unlabeled doc — an env-level resource the user
//     did NOT attribute to a cluster, e.g. a RuntimeClass, which forge.render
//     leaves unstamped because every cluster running the class needs it) →
//     KEEP. The deploy layer never PICKS a cluster for an unattributed
//     resource; it replicates the
//     genuinely-shared ones rather than guess a primary. To pin such a
//     resource to ONE cluster, declare it on an image-less infra service's
//     `manifests` so it carries that service's app label and routes via
//     OwnApps/OtherApps above.
//
// A doc that doesn't parse as YAML is conservatively KEPT (it can't be
// confirmed as another cluster's, and silently swallowing it is worse than
// letting kubectl reject genuinely-broken YAML).
//
// Empty / whitespace-only docs are dropped. The single-cluster path never
// reaches here (ApplyOpts.ClusterScope stays nil), so this is a no-op for
// the common case and multi-cluster envs are the only behaviour change.
func ScopeManifestsToGroup(manifests string, scope GroupScope) string {
	var kept []string
	for _, doc := range splitDocs(manifests) {
		m, parsed := parseDoc(doc)
		app := ""
		routeCluster := ""
		if parsed {
			app = ManifestGroup(m.Metadata.Labels)
			routeCluster = m.Metadata.Labels[ClusterRoutingLabel]
		}
		// First-class cluster attribution wins over the app-label rule: a
		// manifest stamped with `forge.dev/cluster` routes to exactly that
		// cluster. Only engaged when this group knows its own cluster name
		// (scope.Cluster); otherwise fall through to app-label routing.
		if routeCluster != "" && scope.Cluster != "" {
			if routeCluster == scope.Cluster {
				kept = append(kept, doc)
			}
			// routeCluster != scope.Cluster → drop (another cluster's).
			continue
		}
		switch {
		case app != "":
			if _, other := scope.OtherApps[app]; other {
				continue // another cluster's owner — drop
			}
			// Owned by this group, OR app-labelled but ungrouped: keep.
			// (OwnApps membership and the ungrouped case both keep; only an
			// explicit OtherApps match drops.)
			kept = append(kept, doc)
		case !parsed:
			// Unparseable: keep (can't be confirmed another cluster's).
			kept = append(kept, doc)
		default:
			// Unlabeled (Namespace or any other env-level resource the user
			// didn't attribute to a cluster): replicate to every group. The
			// deploy layer never guesses a primary for an unattributed doc.
			kept = append(kept, doc)
		}
	}
	return strings.Join(kept, docDelimiter)
}

// RenderedDeploymentNames extracts the `metadata.name` of every
// `kind: Deployment` document in a `---`-separated YAML stream. Used by
// Prune to compute the desired set against which the namespace's
// actual forge-managed Deployments are diffed.
//
// Malformed documents are skipped (callers get a best-effort list).
func RenderedDeploymentNames(manifests string) []string {
	return renderedNamesByKind(manifests, "Deployment")
}

// renderedNamesByKind extracts the `metadata.name` of every document
// whose `kind` matches kind in a `---`-separated YAML stream. Malformed
// documents are skipped (callers get a best-effort list). It backs both
// RenderedDeploymentNames and RenderedJobNames, which differ only by the
// kind they select.
func renderedNamesByKind(manifests, kind string) []string {
	var out []string
	for _, doc := range splitDocs(manifests) {
		m, ok := parseDoc(doc)
		if !ok {
			continue
		}
		if m.Kind == kind && m.Metadata.Name != "" {
			out = append(out, m.Metadata.Name)
		}
	}
	return out
}

// configFirstKinds are the manifest kinds applied in the first pass of
// Apply's two-pass apply. They are the resources a workload pod can
// reference at schedule time and that must therefore exist (and be
// readable) before the workload lands: the Namespace the workload is
// created in, and the ConfigMaps / Secrets its containers mount or
// project into env. Cluster-scoped config (RuntimeClass, CRDs) is left
// in the second pass — those are already ordered ahead of workloads
// within the rendered stream and don't suffer the same apiserver-cache
// readability race that a freshly-applied namespaced Secret does.
var configFirstKinds = map[string]struct{}{
	"Namespace": {},
	"ConfigMap": {},
	"Secret":    {},
}

// splitByTargetNamespace splits a stream into (onTarget, elsewhere) for an
// apply that will pass `-n target`:
//
//   - onTarget — docs with NO `metadata.namespace` (the ones that need the
//     flag; this is the whole point) and docs that already declare exactly
//     the target namespace (the flag is a no-op for them).
//   - elsewhere — docs that declare a DIFFERENT namespace. They must not
//     ride the flag: kubectl refuses the mismatch and fails the ENTIRE
//     stream, not just that doc. Applied without the flag, they keep the
//     namespace they declared.
//
// A chart that deliberately places something outside its own namespace is
// therefore honoured rather than relocated, and cannot break the deploy for
// everything applied alongside it.
//
// An empty target means no flag will be passed, so nothing needs separating
// and everything is onTarget. A doc that does not parse is also left
// onTarget: it is passed through to kubectl either way, and kubectl is the
// right thing to report on a malformed manifest.
func splitByTargetNamespace(manifests, target string) (onTarget, elsewhere string) {
	target = strings.TrimSpace(target)
	if target == "" {
		return manifests, ""
	}
	var on, other []string
	for _, doc := range splitDocs(manifests) {
		m, ok := parseDoc(doc)
		if ok && m.Metadata.Namespace != "" && m.Metadata.Namespace != target {
			other = append(other, doc)
			continue
		}
		on = append(on, doc)
	}
	return strings.Join(on, docDelimiter), strings.Join(other, docDelimiter)
}

// PartitionConfigManifests splits a `---`-separated multi-doc YAML
// stream into (config, rest): config holds the documents whose `kind`
// is one of configFirstKinds (Namespace, ConfigMap, Secret) in their
// original relative order, rest holds everything else (also in order).
// Empty / whitespace-only docs are dropped from both halves. A doc that
// doesn't parse as YAML is conservatively placed in rest — it can't be
// confirmed config, and rest is the pass that always runs.
//
// This is the ordering primitive behind Apply's two-pass apply: config
// is applied (and kubectl returns) before rest, so a workload in rest
// never schedules ahead of the ConfigMap/Secret it references.
func PartitionConfigManifests(manifests string) (config, rest string) {
	var cfg, other []string
	for _, doc := range splitDocs(manifests) {
		m, ok := parseDoc(doc)
		if !ok {
			// Can't be confirmed config — rest is the pass that always runs.
			other = append(other, doc)
			continue
		}
		if _, ok := configFirstKinds[m.Kind]; ok {
			cfg = append(cfg, doc)
		} else {
			other = append(other, doc)
		}
	}
	return strings.Join(cfg, docDelimiter), strings.Join(other, docDelimiter)
}

// RenderedJobNames extracts the `metadata.name` of every `kind: Job`
// document in a `---`-separated YAML stream. This is the authoritative
// source for the one-shot-Job wait set: forge waits on whatever Jobs
// the deploy actually applies, regardless of how they entered the
// bundle.
//
// Deriving from an entity list instead was fragile in both directions.
// It only saw Jobs that round-tripped through the typed `forge.CronJob`
// -> `output.cronjobs` contract, so it MISSED a `schedule==""` Job that
// never surfaced there (the real-launch gap where forge rolled the
// workloads without blocking on the migrate Job) and any raw `kind: Job`
// from `additional_manifests` — and it INVENTED names that were never
// applied once one-shot Jobs were named by spec hash (see
// oneShotWaitSet). The rendered manifests are what kubectl actually
// applies, so deriving from them closes both holes at once.
//
// Malformed documents are skipped (callers get a best-effort list).
func RenderedJobNames(manifests string) []string {
	return renderedNamesByKind(manifests, "Job")
}

// Prune deletes every forge-managed Deployment in namespace that is
// NOT in the rendered manifest stream. The managed-by guard comes
// from the kubectl label filter inside ListManagedDeployments — only
// resources carrying `app.kubernetes.io/managed-by=forge` are eligible
// for prune. This invariant protects user-applied Deployments living
// alongside forge-owned ones in the same namespace.
//
// An empty desired set (no Deployments at all in the render) is
// treated as a misuse case (almost certainly the user pointed at the
// wrong env dir) and prune is skipped rather than wiping every
// forge-managed Deployment in the namespace.
//
// Errors during the list or per-Deployment delete are returned to the
// caller (which logs them as warnings rather than failing the whole
// deploy — pruning is a maintenance step, not a correctness gate).
func Prune(ctx context.Context, kctx, manifests, namespace string) error {
	desired := map[string]struct{}{}
	for _, n := range RenderedDeploymentNames(manifests) {
		desired[n] = struct{}{}
	}
	if len(desired) == 0 {
		fmt.Println("Skipping prune (no Deployments in render).")
		return nil
	}
	current, err := ListManagedDeployments(ctx, kctx, namespace)
	if err != nil {
		return fmt.Errorf("list deployments: %w", err)
	}
	var orphans []string
	for _, dep := range current {
		if _, keep := desired[dep]; !keep {
			orphans = append(orphans, dep)
		}
	}
	if len(orphans) == 0 {
		return nil
	}
	fmt.Printf("Pruning %d orphan Deployment(s) in %s: %s\n",
		len(orphans), namespace, strings.Join(orphans, ", "))
	for _, name := range orphans {
		delCmd := kubectlCmd(ctx, kctx, "delete", "deployment", name,
			"-n", namespace, "--ignore-not-found=true")
		delCmd.Stdout = os.Stdout
		delCmd.Stderr = os.Stderr
		if err := delCmd.Run(); err != nil {
			fmt.Printf("  Warning: delete %s: %v\n", name, err)
		}
	}
	return nil
}

// SplitManifestDocs splits a `---`-separated multi-doc YAML manifest stream
// into its individual documents, trimming surrounding whitespace and dropping
// empty ones.
//
// Exported so callers OUTSIDE this package (the `forge env render` printer)
// walk the stream document by document through the same splitter the routing
// filters use, rather than re-deriving the delimiter. A second splitter that
// disagreed about where a document ends would attribute the wrong bytes to
// the wrong cluster — the precise failure the render printer exists to
// prevent.
func SplitManifestDocs(manifests string) []string {
	return splitDocs(manifests)
}

// ApplySecrets server-side-applies a Secret manifest stream under an explicit
// field manager, and prints nothing from it: the apply's own per-object lines
// would name Secrets, and a caller summarises by count instead.
//
// Conflicts are forced, as in [KubectlApply]: forge is the declared source of
// these values, and the Secrets it takes over are exactly the ones a previous
// writer (a hand-run `forge env secrets sync`, or an older reconciler applying
// a redaction marker) already owns fields of.
func ApplySecrets(ctx context.Context, kctx, namespace, fieldManager, manifests string) error {
	if strings.TrimSpace(kctx) == "" {
		return fmt.Errorf("refusing to apply Secrets without an explicit kubectl context")
	}
	if strings.TrimSpace(fieldManager) == "" {
		return fmt.Errorf("refusing to apply Secrets without a field manager")
	}
	args := []string{"apply", "--server-side", "--field-manager", fieldManager, "--force-conflicts", "-n", namespace, "-f", "-"}
	cmd := kubectlCmd(ctx, kctx, args...)
	cmd.Stdin = strings.NewReader(manifests)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if _, err := cmd.Output(); err != nil {
		return fmt.Errorf("kubectl apply (field manager %s): %w: %s", fieldManager, err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

// RedactSecretValues replaces every value in a `kind: Secret` document of a
// manifest stream with a placeholder. A dry run is read by humans and pasted
// into tickets and CI logs, so it shows a Secret's shape (its name and keys),
// never its content.
func RedactSecretValues(stream string) string {
	docs := SplitManifestDocs(stream)
	changed := false
	for i, doc := range docs {
		var body map[string]any
		if err := yaml.Unmarshal([]byte(doc), &body); err != nil || body["kind"] != "Secret" {
			continue
		}
		for _, field := range []string{"data", "stringData"} {
			values, ok := body[field].(map[string]any)
			if !ok {
				continue
			}
			for key := range values {
				values[key] = "<redacted>"
			}
		}
		out, err := yaml.Marshal(body)
		if err != nil {
			continue
		}
		docs[i] = string(out)
		changed = true
	}
	if !changed {
		return stream
	}
	return strings.Join(docs, "\n---\n")
}
