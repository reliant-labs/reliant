package deploytarget

// The HOSTED provider: ship forge's deploy tiers to a control plane.
//
// A workload bound to the Hosted runtime (forge.OnHosted), a hosted
// ManagedDatabase and an OnHosted frontend are not applied to any cluster
// forge can see. They ride the env's BUNDLE as forge.dev/v1alpha1 records — a
// Workload, ManagedDatabase or StaticSite per hosted thing, under the hosted
// cluster tree (release.BundleHostedCluster) — and the control plane's hub
// Flux applies that bundle when the promotion lands. This provider never
// shells out to kubectl, ensures no cluster, and writes NO per-deployment row
// or config artifact: there is no EnsureDeployment and no
// PublishDeploymentConfig. Hosting is PER WORKLOAD: the same env may apply
// other workloads to a cluster in the same deploy.
//
// THE ORDER IS THE CONTRACT, and it is fixed so that nothing is written until
// everything is known to be admissible:
//
//  1. pin every workload's image to the digest the env's bound release froze
//  2. Workload.Validate(ProfileRestricted) + CheckShapeBand EVERY workload,
//     and render the hosted set as the platform will — a refusal here costs
//     zero RPCs
//  3. EnsureEnvironment (by name)
//  4. record the bundle (the RecordBundle hook: build, push, record)
//  5. a bounded readiness wait on the environment's rollout; timing out is
//     reported as timed out, never as success
//
// forge does not import control-plane. The request and response shapes below
// are the proto3-JSON subset forge reads, declared here, and carried by an
// injected Caller (cloud.Client in production).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/deploy"
	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
	"github.com/reliant-labs/forge/pkg/release"
)

// HostedProviderID is the registry id of the hosted provider.
const HostedProviderID = "hosted"

// HostedCaller is the one method the provider needs from a control-plane
// client. Declared here, at its consumer; cloud.Client satisfies it.
type HostedCaller interface {
	Call(ctx context.Context, procedure string, req, out any) error
}

// HostedTier is the tier a hosted workload is published as.
type HostedTier string

// The three hosted tiers, one per forge.dev kind the control plane admits.
const (
	HostedTierWorkload HostedTier = "workload"
	HostedTierDatabase HostedTier = "database"
	HostedTierStatic   HostedTier = "static"
)

// HostedWorkload is one declaration bound for the control plane. Exactly one
// of Workload / Database / Static is set, matching Tier.
type HostedWorkload struct {
	Tier HostedTier
	// Artifact is the release-ledger artifact name this workload's digest is
	// bound under. Empty means HostedArtifactName(Workload.Image) for a
	// workload and the workload's own name for a static site (the frontend
	// name `forge build` records its site release under).
	Artifact string
	// Workload is the spec published as a forge.dev Workload CR (named by
	// the ResolvedService name). Its Image is the declared image — a
	// registry-less artifact name this project builds, or a pinned
	// third-party reference — and the plan pins it to the release digest.
	Workload *v1alpha1.WorkloadSpec
	Database *v1alpha1.ManagedDatabaseSpec
	// Static is the deployed half of a StaticSite: everything but the
	// digest, which planHosted pins from the bound release as liveDigest.
	Static *v1alpha1.StaticSiteSpec
}

// HostedTarget is the env-level half of a hosted group.
type HostedTarget struct {
	// Endpoint is the control plane's normalized base URL. Display only —
	// the Caller already addresses it.
	Endpoint string
	// Project is the forge project name the environment is addressed under
	// on the control plane (identity is (org, project, env name)).
	Project string
	// Release is the version the env is promoted to. Empty means unbound,
	// which a deploy refuses: a hosted deploy ships only promoted digests.
	Release string
	// PromotionID is the promotion whose frozen pins this deploy is
	// publishing — the one forge read Digests from. Recorded server-side
	// as Deployment.applied_promotion_id, which is what lets the
	// converger tell "a promotion is waiting to be applied" from "this
	// row has drifted": the two need opposite answers under the OBSERVE
	// policy, and comparing digests cannot distinguish them because both
	// read as "the row does not match the pin".
	//
	// Empty when the env is unbound, or when a control plane that
	// predates the ledger's promotion ids returned none. An omitted
	// promotion leaves the stored one untouched server-side, which is the
	// correct reading of a hand-run deploy: that is drift, not a
	// promotion.
	PromotionID string
	// Digests is the bound release's artifact → digest map (the ledger's
	// Resolved set).
	Digests map[string]string
	// Registries is the bound release's artifact → registry map: where each
	// image was pushed (the release artifact's URI). It locates the bytes of
	// a workload whose image THIS project builds, which is declared
	// registry-less (`image = "api"`): forge composed the address from the
	// env's declared push base, and recorded it here by
	// `forge env build <env> --push`.
	Registries map[string]string
	// PushBase is the registry subtree this env's hosted artifacts live
	// under, composed by forge from the env's declaration
	// (`<registry_host>/<org>/<project>`) and carried here so the
	// pre-publish check judges a pinned image against the same base the
	// build pushed to.
	//
	// It is the CLIENT's composition, deliberately: the server does not
	// advertise a base, it ENFORCES one — at publish (ociregistry.Admit) and
	// at the registry realm, which refuses a token outside its own org's
	// subtree. So a mis-declared org is caught by the thing that knows,
	// rather than by forge believing an answer it was handed.
	PushBase string
	// Shape and DeclaredBy are the env's rendered DECLARATION, recorded by
	// the EnsureEnvironment this deploy already performs rather than by a
	// write of their own.
	//
	// That placement is the contract, not a convenience. The order in this
	// file's header exists so nothing is written until everything is known
	// to be admissible — every workload validated, the hosted set rendered
	// as the platform will — and a deploy that forge is about to refuse
	// (unbound, off shape band, image outside the push base) must leave the
	// control plane untouched. A declaration sent ahead of the plan would
	// be the one write that escaped that rule.
	//
	// Nil means "no render to record", which leaves the stored shape
	// untouched server-side.
	Shape      *release.Shape
	DeclaredBy *release.Provenance
}

// ClusterBinding is one rendered cluster and the connected cluster it maps to.
// Mirrors controlplane.v1.ClusterBinding.
type ClusterBinding struct {
	// Cluster is the rendered cluster / kube-context name, as it appears in
	// the bundle's ClusterPaths. forge's key.
	Cluster string
	// ClusterID is the connected cluster's id, from ConnectCluster.
	ClusterID string
}

// ─── Wire (controlplane.v1, proto3 JSON) ─────────────────────────────────────

const (
	procEnsureEnvironment = "controlplane.v1.DeployService/EnsureEnvironment"
	procGetStatus         = "controlplane.v1.DeployService/GetStatus"
	procListEnvironments  = "controlplane.v1.DeployService/ListEnvironments"
)

type wireEnvironment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Project is the forge project the environment belongs to (forge.yaml
	// `name`). Identity is (org, project, name).
	Project   string `json:"project,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	// NO ImagePushBase. The push base is forge's to COMPOSE from the env's
	// declaration and credential — `<registry_host>/<org>/<project>`, see
	// internal/hostedimage.PushBase — not the server's to advertise. Reading
	// it off the wire made the address unknowable offline and put a cached
	// second copy on disk; the server's role is to ENFORCE the subtree at
	// publish and at the registry realm, which it still does.

	// ConvergesPromotions reports whether a promotion to this environment
	// will be APPLIED server-side (DeployEnvironment.converges_promotions,
	// tag 17): the control plane runs a converger and the env's reconcile
	// policy is not PINNED.
	//
	// It exists so a client can refuse FAST instead of waiting out a
	// timeout. A `forge env deploy <env> vX` against an environment that
	// converges nothing would poll for fifteen minutes and then report a
	// failure whose cause is "nobody was ever going to apply this" —
	// which looks exactly like a broken release. Reading this turns that
	// into an immediate, accurate "this env does not converge promotions;
	// add --deploy".
	//
	// DERIVED server-side, not stored: it is a fact about the control
	// plane's own configuration and the env's policy, so a stored copy
	// could disagree with either.
	ConvergesPromotions bool `json:"convergesPromotions,omitempty"`
}

type wireObserved struct {
	State       string     `json:"state,omitempty"`
	Replicas    int        `json:"replicas,omitempty"`
	ImageDigest string     `json:"imageDigest,omitempty"`
	LastError   string     `json:"lastError,omitempty"`
	URL         string     `json:"url,omitempty"`
	StableSince *time.Time `json:"stableSince,omitempty"`
	// Domains is the per-domain convergence state for every custom
	// hostname this deployment declares. Absent from a control plane that
	// does not serve custom domains — which is also the one that does not
	// advertise the capability, so forge refused the declaration before
	// ever publishing it.
	Domains []wireCustomDomainStatus `json:"domains,omitempty"`
}

type wireDeployment struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Tier     string        `json:"tier,omitempty"`
	Observed *wireObserved `json:"observed,omitempty"`
	RunState string        `json:"runState,omitempty"`

	// Artifact is the release artifact KEY whose digest this deployment
	// runs (Deployment.artifact, tag 12) — the same key forge computes for
	// its own plan, which is why the client sends it rather than the
	// server inferring it: the two cannot then pair a workload with a
	// digest differently.
	//
	// EMPTY MEANS NOT RELEASE-BOUND: a ManagedDatabase, or a third-party
	// image pinned directly in the spec. The server's converger SKIPS an
	// empty one rather than guessing a pairing from the deployment's name.
	// Names coinciding with artifact keys is a convention, not a
	// guarantee, and a converger that guessed would silently repoint the
	// wrong workload the first time they differed.
	Artifact string `json:"artifact,omitempty"`

	// AppliedPromotionID is the promotion whose pins this row's spec
	// currently carries (tag 13).
	//
	// It is the discriminator between "a promotion is waiting to be
	// applied" and "this row has drifted", which need opposite answers
	// under the OBSERVE policy: a promotion is a declared change and
	// ships, while drift is corrected only under CONVERGE. Comparing
	// digests cannot tell them apart — both read as "the row does not
	// match the pin" — which is why this is a promotion id and not a
	// digest comparison.
	AppliedPromotionID string `json:"appliedPromotionId,omitempty"`
}

type wireDeploymentStatus struct {
	Deployment    wireDeployment `json:"deployment"`
	Verdict       string         `json:"verdict,omitempty"`
	VerdictReason string         `json:"verdictReason,omitempty"`
	DesiredDigest string         `json:"desiredDigest,omitempty"`
	Drifted       bool           `json:"drifted,omitempty"`
	ObservedAt    *time.Time     `json:"observedAt,omitempty"`
}

type wireStatusResponse struct {
	Deployments        []wireDeploymentStatus `json:"deployments"`
	EnvironmentVerdict string                 `json:"environmentVerdict,omitempty"`
	ReconcilePolicy    string                 `json:"reconcilePolicy,omitempty"`
}

// The verdict and observed-state value names this file branches on.
const (
	wireVerdictPrefix     = "DEPLOY_VERDICT_"
	wireVerdictConverged  = "DEPLOY_VERDICT_CONVERGED"
	wireObservedPrefix    = "DEPLOY_OBSERVED_STATE_"
	wireObservedReady     = "DEPLOY_OBSERVED_STATE_READY"
	wireObservedPending   = "DEPLOY_OBSERVED_STATE_PENDING"
	wireObservedProgress  = "DEPLOY_OBSERVED_STATE_PROGRESSING"
	wireObservedDegraded  = "DEPLOY_OBSERVED_STATE_DEGRADED"
	wireObservedSuspended = "DEPLOY_OBSERVED_STATE_SUSPENDED"
	wireObservedDeleted   = "DEPLOY_OBSERVED_STATE_DELETED"
)

// ─── Environment lookup (shared with the CLI's hosted ledger and secrets) ────

// ErrHostedEnvironmentNotFound is LookupHostedEnvironment's "no environment of
// that name" answer.
var ErrHostedEnvironmentNotFound = errors.New("the control plane has no environment")

// HostedEnvKind is the controlplane.v1.DeployEnvironmentKind value name an
// environment is ensured with. It is IMMUTABLE server-side: ensuring an
// existing environment with a different kind is refused (FailedPrecondition),
// which is what keeps a persistent env's secrets from ever becoming pullable.
type HostedEnvKind string

const (
	// HostedEnvPersistent is an env whose workloads the platform runs (it
	// declares at least one hosted tier).
	HostedEnvPersistent HostedEnvKind = "DEPLOY_ENVIRONMENT_KIND_PERSISTENT"
	// HostedEnvLocal is an env whose workloads run on a developer machine
	// (`forge env up`); the control plane is only its secret store, and its
	// secrets are pullable.
	HostedEnvLocal HostedEnvKind = "DEPLOY_ENVIRONMENT_KIND_LOCAL"
	// HostedEnvSelfManaged is an env whose ledger the control plane keeps
	// while forge applies it to a cluster the author operates: nothing is
	// hosted, something runs on a cluster. The platform places nothing, and
	// its secrets are write-only — a cluster env's secrets must never be
	// pullable the way a LOCAL env's are.
	HostedEnvSelfManaged HostedEnvKind = "DEPLOY_ENVIRONMENT_KIND_SELF_MANAGED"
)

// HostedEnvRef addresses one control-plane environment: (project, name) in
// the caller's org, plus the kind an ensure creates it with, plus — for the
// paths that have rendered the env — what its KCL declares.
type HostedEnvRef struct {
	// Project is the forge project name (forge.yaml `name`).
	Project string
	Name    string
	Kind    HostedEnvKind

	// Shape is the env's rendered shape, recorded as the DECLARATION:
	// what runs, where, which secrets it needs, one hash per object. It is
	// what lets a reader answer "what kind, which secrets, which provider"
	// with no checkout and no daemon, which is the whole point of
	// recording it.
	//
	// NIL MEANS "I DID NOT RENDER", NOT "THE ENV DECLARES NOTHING". An
	// absent shape leaves the stored one untouched server-side, so the
	// ensures that run without a render — `forge secret set`, a promote —
	// cannot erase a declaration that a build recorded. Only a path that
	// actually rendered the env may set it.
	Shape *release.Shape
	// DeclaredBy is the provenance of the render Shape came from: which
	// commit, which checkout, which forge. It is a CLAIM, recorded as
	// one — identity is the caller's token, never this (O-11).
	DeclaredBy *release.Provenance

	// ClusterBindings maps each cluster the bundle renders for to the
	// CONNECTED cluster it deploys to — the control plane's record of where
	// this environment lands.
	//
	// A REPEATED FIELD BECAUSE ONE ENV CAN HAVE SEVERAL TARGETS. A forge
	// bundle renders a subtree per cluster (release.BundleClusterPath), so
	// an env that spans two clusters has two bindings, which a single
	// cluster id could not express.
	//
	// Nil means "no bindings to record", which leaves the stored ones
	// untouched server-side — the same rule Shape follows, and for the same
	// reason: an ensure that did not render (a secret set, a promote) must
	// not erase what a build recorded.
	ClusterBindings []ClusterBinding

	// HubConverged is WHO APPLIES the env's bound clusters, as decided from
	// the same render as Shape: true ONLY when the control plane's hub
	// converges EVERY delivery unit and forge applies none of them, false when
	// forge applies any part of the env from the author's machine. It is the
	// env-level summary of HubConvergedUnits, kept because a control plane that
	// predates per-unit convergence reads only this: sending true for an env
	// that has moved some units would make an old control plane converge the
	// units forge still applies, so the summary is deliberately the
	// conservative one. A bound cluster says WHERE the env lands, not who puts
	// it there.
	//
	// A SHAPE FIELD, sent only beside a Shape, because it is a projection of
	// that render. Nil means "this ensure did not render" and leaves the
	// stored value untouched, like Shape and ClusterBindings. An env never
	// declared is treated as forge-applied server-side, so an older forge
	// can never make the hub a second applier over a cluster it applies.
	HubConverged *bool
	// HubConvergedUnits are the delivery units the control plane's hub
	// converges, by name: the per-unit declaration an env uses to move onto
	// Flux one unit at a time (Bundle.hub_converges). Every other unit of the
	// env is applied by forge. Nil means this ensure did not render, like
	// HubConverged; a non-nil empty slice says "the hub converges none".
	//
	// A control plane that predates the field ignores it (connect's JSON codec
	// discards unknown fields) and obeys HubConverged, which is false until
	// every unit has moved: so an old control plane never converges a
	// partially moved env.
	HubConvergedUnits *[]string
}

// DeclaresShape reports whether this ref carries a declaration to record.
func (r HostedEnvRef) DeclaresShape() bool { return r.Shape != nil }

// LookupHostedEnvironment resolves an environment (project, NAME) to the
// control plane's id, by listing the caller's environments and matching the
// name EXACTLY. `search` only narrows server-side: it is a free-text match,
// and "prod" must not resolve to "prod-eu". Two exact matches is a contract
// violation and is refused rather than guessed at — a guess would write into
// the wrong env.
func LookupHostedEnvironment(ctx context.Context, c HostedCaller, project, envName string) (string, error) {
	env, err := lookupHostedEnvironment(ctx, c, project, envName)
	return env.ID, err
}

func lookupHostedEnvironment(ctx context.Context, c HostedCaller, project, envName string) (wireEnvironment, error) {
	var resp struct {
		Environments []wireEnvironment `json:"environments"`
	}
	req := map[string]any{"search": envName}
	if project != "" {
		req["project"] = project
	}
	if err := c.Call(ctx, procListEnvironments, req, &resp); err != nil {
		return wireEnvironment{}, fmt.Errorf("resolve hosted environment %q: %w", envName, err)
	}
	var matches []wireEnvironment
	for _, e := range resp.Environments {
		// The project filter is applied server-side; this is the client's
		// second check. A row that states a DIFFERENT project is never
		// ours. (A row stating none predates the project dimension.)
		if e.Name == envName && (e.Project == "" || project == "" || e.Project == project) {
			matches = append(matches, e)
		}
	}
	switch len(matches) {
	case 0:
		return wireEnvironment{}, fmt.Errorf("%w named %q\nfix: `forge env deploy %s` creates it on first deploy",
			ErrHostedEnvironmentNotFound, envName, envName)
	case 1:
		if strings.TrimSpace(matches[0].ID) == "" {
			return wireEnvironment{}, fmt.Errorf("the control plane returned environment %q with no id", envName)
		}
		return matches[0], nil
	default:
		return wireEnvironment{}, fmt.Errorf("the control plane returned %d environments named %q; refusing to guess", len(matches), envName)
	}
}

// EnsureHostedEnvironment makes the environment ref names exist in the
// caller's org and returns its id and whether this call created it. It is the
// one write every hosted path that needs an environment goes through, so a
// hosted env is DECLARED in KCL and never provisioned by a manual step —
// whichever of promote / deploy / secret set runs first creates it.
func EnsureHostedEnvironment(ctx context.Context, c HostedCaller, ref HostedEnvRef) (string, bool, error) {
	env, created, err := ensureHostedEnvironment(ctx, c, ref)
	return env.ID, created, err
}

func ensureHostedEnvironment(ctx context.Context, c HostedCaller, ref HostedEnvRef) (wireEnvironment, bool, error) {
	if ref.Kind == "" {
		// Never defaulted: the kind is immutable server-side, so a guess
		// here would be a permanent decision nobody made.
		return wireEnvironment{}, false, fmt.Errorf("ensure environment %q: no environment kind (local or persistent) was derived", ref.Name)
	}
	var ensured struct {
		Environment wireEnvironment `json:"environment"`
		Created     bool            `json:"created"`
	}
	spec := map[string]any{"name": ref.Name, "kind": string(ref.Kind)}
	if ref.Project != "" {
		spec["project"] = ref.Project
	}
	if ref.Shape != nil {
		// DeployEnvironmentSpec.shape is a google.protobuf.Struct, whose
		// proto3-JSON form is the object itself. So the shape rides as
		// its canonical JSON decoded back into a map: encoding it once
		// through Shape.Encode is what guarantees the server stores the
		// same bytes `forge env shape` printed, rather than whatever
		// Go's marshaller happened to emit for the struct.
		declared := *ref.Shape
		if declared.HasFrontendTelemetry() {
			// The control plane strict-decodes this shape, so a part it
			// predates would be refused whole. Asked only when there is
			// something to withhold: every other declaration costs no call.
			reader, rerr := HostedBundleReader(ctx, c)
			if rerr != nil {
				return wireEnvironment{}, false, fmt.Errorf("ensure environment %q: %w", ref.Name, rerr)
			}
			declared = reader.LimitShape(declared)
		}
		shape, err := shapeWireFields(declared)
		if err != nil {
			return wireEnvironment{}, false, fmt.Errorf("ensure environment %q: %w", ref.Name, err)
		}
		spec["shape"] = shape
		if ref.DeclaredBy != nil {
			spec["declaredBy"] = ProvenanceWireFields(*ref.DeclaredBy)
		}
		// Sent explicitly when false: "forge applies this" is a decision
		// the control plane must record, not the absence of one.
		if ref.HubConverged != nil {
			spec["hubConverged"] = *ref.HubConverged
		}
		if ref.HubConvergedUnits != nil {
			units := append([]string{}, (*ref.HubConvergedUnits)...)
			spec["hubConvergedUnits"] = units
		}
	}
	if len(ref.ClusterBindings) > 0 {
		bindings := make([]map[string]any, 0, len(ref.ClusterBindings))
		for _, b := range ref.ClusterBindings {
			bindings = append(bindings, map[string]any{"cluster": b.Cluster, "clusterId": b.ClusterID})
		}
		spec["clusterBindings"] = bindings
	}
	if err := c.Call(ctx, procEnsureEnvironment, map[string]any{"spec": spec}, &ensured); err != nil {
		return wireEnvironment{}, false, fmt.Errorf("ensure environment %q: %w", ref.Name, err)
	}
	if ensured.Environment.ID == "" {
		return wireEnvironment{}, false, fmt.Errorf("ensure environment %q: the control plane returned no environment id", ref.Name)
	}
	return ensured.Environment, ensured.Created, nil
}

// deployRebuildFix is the ONE remedy every "this release cannot ship" refusal
// below names: re-run the deploy, which builds, pushes, cuts and ships in one
// command, or name a release that already exists.
//
// It replaced five hand-written variants of `forge env build <env> --release
// <version> --no-build`, and the reason is that that advice was WRONG, not
// merely verbose. --no-build cuts a release over digests an earlier push left
// in .forge/state, so every one of these refusals — no promoted release, an
// artifact the release does not pin, an image nobody pushed — told the user to
// produce a release with no images in it. The second cut then failed its own
// completeness gate, or succeeded and deployed the same hole. O-15 makes the
// honest remedy a single verb.
func deployRebuildFix(env string) string {
	return fmt.Sprintf("forge env deploy %s (builds, pushes, cuts and deploys), "+
		"or forge env deploy %s <existing-version> to ship a release that is already cut", env, env)
}

// hostedPushBase is the group's declared push base, or "" when the group
// carries no hosted target.
func hostedPushBase(group ServiceGroup) string {
	if group.Hosted == nil {
		return ""
	}
	return group.Hosted.PushBase
}

// checkImagePushBase refuses every workload in the plan whose pinned image is
// not under the org's image push base — the subtree the platform's
// publish-time boundary admits. It runs BEFORE any EnsureDeployment or
// publish, so an image the platform will refuse costs no deployment write and
// no ledger entry. Every offending workload is reported together.
//
// The base is forge's own composition from the env's declaration, so this
// check runs with no dependence on what the server said. It is a PRE-FLIGHT
// for the real boundary rather than the boundary itself: ociregistry.Admit
// and the registry realm are what actually enforce the subtree, and they are
// the things that enforce the organization.
//
// An EMPTY base means the credential's org was not learned, so no base composes
// and there is no address any image could be under; that is refused too,
// unless the plan carries no workload.
//
// This is deliberately stricter than the server's own Contains, which lets an
// image OUTSIDE the platform registry through to the tier's registry
// allowlist: the hosted path ships only images forge can prove the platform
// will run, and the allowlist is not exposed.
func checkImagePushBase(envName, base string, plan []hostedPlanItem) error {
	base = strings.TrimSuffix(strings.TrimSpace(base), "/")
	var errs []error
	for _, item := range plan {
		if item.Tier != HostedTierWorkload {
			continue
		}
		spec, ok := item.Spec.(v1alpha1.WorkloadSpec)
		if !ok {
			continue
		}
		if base == "" {
			return fmt.Errorf("hosted env %q resolved no image push base, so the platform would refuse every workload image "+
				"(first: %s for %s).\n"+
				"  forge composes the base from the org your credential acts for; it could not learn it.\n"+
				"  fix: authenticate (%s, or the env's token_env) and re-run",
				envName, spec.Image, item.Name, cloud.SignInHint())
		}
		repo := HostedImageRepository(spec.Image)
		if !strings.HasPrefix(repo, base+"/") {
			errs = append(errs, fmt.Errorf("%s: image %s is not under this org's image push base %s, and the control plane "+
				"refuses to publish it.\n"+
				"  fix: drop the registry host from the workload's image (forge resolves a bare hosted image to %s/%s), "+
				"then %s",
				item.Name, spec.Image, base, base, HostedArtifactName(spec.Image), deployRebuildFix(envName)))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("hosted env %q: refusing to publish anything — %d image(s) are outside the registry the platform admits:\n  %w",
			envName, len(errs), errors.Join(errs...))
	}
	return nil
}

// ─── Artifact naming and digest pinning ──────────────────────────────────────

// HostedImageRepository is an image reference with its digest or tag removed.
func HostedImageRepository(image string) string {
	if i := strings.LastIndex(image, "@"); i >= 0 {
		image = image[:i]
	}
	if slash, colon := strings.LastIndex(image, "/"), strings.LastIndex(image, ":"); colon > slash {
		image = image[:colon]
	}
	return image
}

// HostedArtifactName is the release-ledger artifact name a hosted workload's
// image is recorded and bound under: the repository's LAST path segment
// ("ghcr.io/acme/api:v1" → "api"). That is the same key forge's own builds use
// (a registry-less image name, with the registry held separately as the
// artifact's URI), so a workload whose image forge builds and one whose image
// CI pushes resolve through one rule.
func HostedArtifactName(image string) string {
	repo := HostedImageRepository(image)
	return repo[strings.LastIndex(repo, "/")+1:]
}

// hostedWorkloadRepository is the repository a workload's bound digest is pinned
// under.
//
//   - A spec image that names its registry (ghcr.io/acme/api:v1) is an image
//     built elsewhere: its own repository, as declared.
//   - A registry-less spec image (`api`) is one THIS project builds. Its
//     repository is where the release recorded pushing it: the artifact's
//     registry + "/" + the artifact name — the same coordinates
//     `forge env build --push` wrote the digest under.
//
// A registry-less image whose release recorded no registry was built without
// --push, so there are no addressable bytes to pin: refused, naming the fix.
func hostedWorkloadRepository(image, artifact string, group ServiceGroup) (string, error) {
	repo := HostedImageRepository(image)
	if hostedImageNamesRegistry(repo) {
		return repo, nil
	}
	// AN ARTIFACT KEY THAT NAMES A REGISTRY IS THE REPOSITORY. A bare hosted
	// image is keyed by the address it was pushed to (hostedArtifactKey:
	// `<push base>/<name>`), and releaseRegistries maps that key to itself,
	// so composing `registry + "/" + artifact` would write the repository
	// twice — `<base>/hounders/<base>/hounders@…`, which the control plane
	// refuses as ImageRejected. The static-site branch already reads its key
	// this way; this is the same rule. Only a legacy registry-less key still
	// composes below.
	if hostedImageNamesRegistry(artifact) {
		return artifact, nil
	}
	var registry string
	if group.Hosted != nil {
		registry = strings.TrimSuffix(group.Hosted.Registries[artifact], "/")
	}
	if registry == "" {
		return "", fmt.Errorf("image %q names no registry, and release %s recorded none for artifact %q — it was built without a push.\n"+
			"  fix: %s",
			image, group.Hosted.Release, artifact, deployRebuildFix(group.Env))
	}
	return registry + "/" + artifact, nil
}

// hostedImageNamesRegistry reports whether an image repository's first path
// component is a registry host — the same rule v1alpha1.ValidateImage applies.
func hostedImageNamesRegistry(repo string) bool {
	host, _, found := strings.Cut(repo, "/")
	return found && (strings.ContainsAny(host, ".:") || host == "localhost")
}

// HostedImageDigest is the digest an image reference is pinned by, or "".
func HostedImageDigest(image string) string {
	if i := strings.LastIndex(image, "@"); i >= 0 {
		return image[i+1:]
	}
	return ""
}

// ─── Planning (pure: no RPCs) ────────────────────────────────────────────────

// hostedPlanItem is one workload ready to publish: its name, tier and the
// exact spec document the control plane will store.
type hostedPlanItem struct {
	Name          string
	Tier          HostedTier
	Spec          any
	DesiredDigest string
	// Artifact is the release artifact key this workload's digest is bound
	// under, as hostedArtifactOf computed it — EMPTY for a managed
	// database, which is never release-bound.
	//
	// It is carried on the plan rather than recomputed at publish time so
	// the key forge SENDS is provably the key it looked the digest up
	// under. Two computations of "the artifact" could disagree, and the
	// symptom would be a server pairing a workload with a digest
	// differently from the client — exactly what sending the key from the
	// client exists to prevent.
	Artifact string
}

// planHosted pins, validates and shape-checks every workload in a hosted
// group, returning the specs to publish. It makes no calls, so a refusal here
// provably wrote nothing. EVERY workload is checked and every problem is
// reported together: a deploy refused one error at a time is a deploy that
// takes N attempts to learn N things.
func planHosted(group ServiceGroup) ([]hostedPlanItem, error) {
	return planHostedWith(group, groupDigests(group))
}

func groupDigests(group ServiceGroup) map[string]string {
	if group.Hosted == nil {
		return nil
	}
	return group.Hosted.Digests
}

func planHostedWith(group ServiceGroup, digests map[string]string) ([]hostedPlanItem, error) {
	if group.Hosted == nil {
		return nil, errors.New("hosted deploy: the group carries no hosted target")
	}
	if group.Hosted.Release == "" && hostedGroupHasPinnedArtifact(group) {
		return nil, fmt.Errorf("hosted env %q has no promoted release, so there is no digest to deploy.\n"+
			"A hosted deploy ships only the digests a promotion froze — never a tag, never a local build.\n"+
			"  fix: %s",
			group.Env, deployRebuildFix(group.Env))
	}
	var (
		out  []hostedPlanItem
		errs []error
		seen = map[string]string{}
	)
	for _, svc := range group.Services {
		w := svc.Hosted
		if w == nil {
			errs = append(errs, fmt.Errorf("%s: not a hosted tier workload", svc.Name))
			continue
		}
		switch w.Tier {
		case HostedTierWorkload:
			if w.Workload == nil {
				errs = append(errs, fmt.Errorf("%s: hosted workload has no spec", svc.Name))
				continue
			}
			spec := *w.Workload
			artifact := hostedArtifactOf(svc.Name, w)
			if other, dup := seen[artifact]; dup && HostedImageRepository(spec.Image) != other {
				errs = append(errs, fmt.Errorf("%s: image %q and %q share the release artifact name %q, so one binding cannot pin both; rename one repository",
					svc.Name, spec.Image, other, artifact))
				continue
			}
			seen[artifact] = HostedImageRepository(spec.Image)
			digest, ok := digests[artifact]
			if !ok || digest == "" {
				errs = append(errs, fmt.Errorf("%s: release %s pins no artifact %q (the workload's image %s).\n"+
					"  fix: %s",
					svc.Name, group.Hosted.Release, artifact, spec.Image, deployRebuildFix(group.Env)))
				continue
			}
			repo, rerr := hostedWorkloadRepository(spec.Image, artifact, group)
			if rerr != nil {
				errs = append(errs, fmt.Errorf("%s: %w", svc.Name, rerr))
				continue
			}
			spec.Image = repo + "@" + digest
			// The admission entrypoint the control plane itself runs, under
			// the profile of the destination: the author sees the refusal
			// here, before anything is written.
			cr := v1alpha1.Workload{Spec: spec}
			cr.Name = svc.Name
			if err := cr.Validate(v1alpha1.ProfileRestricted); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", svc.Name, err))
				continue
			}
			if err := deploy.CheckShapeBand(spec.Resources); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", svc.Name, err))
				continue
			}
			out = append(out, hostedPlanItem{Name: svc.Name, Tier: w.Tier, Spec: spec, DesiredDigest: digest, Artifact: artifact})
		case HostedTierStatic:
			if w.Static == nil {
				errs = append(errs, fmt.Errorf("%s: static site workload has no spec", svc.Name))
				continue
			}
			spec := *w.Static
			artifact := hostedArtifactOf(svc.Name, w)
			// The artifact key IS the repository the site was pushed to,
			// so it must be an ADDRESS. A key naming no registry host
			// cannot be pulled by anyone, and publishing it would hand the
			// platform a digest it has no way to locate — the failure this
			// whole field exists to prevent, arriving one layer later.
			// buildHostedGroup always supplies one (the render requires a
			// registry-bearing `image` on forge.OnHosted), so this is
			// reachable only from a hand-built group.
			if !hostedImageNamesRegistry(artifact) {
				errs = append(errs, fmt.Errorf("%s: the release artifact key %q names no registry host, so the site release has no "+
					"pullable address.\n"+
					"  fix: declare the frontend's full image reference (image = \"ghcr.io/<owner>/%s\"); forge appends the "+
					"platform's static.v1 layout and records the result as the release's repository",
					svc.Name, artifact, svc.Name))
				continue
			}
			digest, ok := digests[artifact]
			if !ok || digest == "" {
				errs = append(errs, fmt.Errorf("%s: release %s pins no static site artifact %q.\n"+
					"  fix: %s",
					svc.Name, group.Hosted.Release, artifact, deployRebuildFix(group.Env)))
				continue
			}
			// A release is a REFERENCE plus a digest, and both are
			// published. The artifact key IS the repository the site was
			// pushed to (HostedStaticRepository of the frontend's declared
			// image), so the spec carries the exact address the platform
			// must pull — never a path it recomposes from a registry base
			// and an org id, which is a second derivation free to disagree
			// with where the bytes actually went.
			spec.ReleaseRepository = artifact
			spec.LiveDigest = digest
			if err := spec.Validate(); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", svc.Name, err))
				continue
			}
			out = append(out, hostedPlanItem{Name: svc.Name, Tier: w.Tier, Spec: spec, DesiredDigest: digest, Artifact: artifact})
		case HostedTierDatabase:
			if w.Database == nil {
				errs = append(errs, fmt.Errorf("%s: database workload has no spec", svc.Name))
				continue
			}
			spec := w.Database.WithDefaults()
			if err := spec.Validate(); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", svc.Name, err))
				continue
			}
			out = append(out, hostedPlanItem{Name: svc.Name, Tier: w.Tier, Spec: spec})
		default:
			errs = append(errs, fmt.Errorf("%s: unknown hosted tier %q", svc.Name, w.Tier))
		}
	}
	errs = append(errs, hostedReferenceErrors(group)...)
	if len(errs) == 0 {
		if err := renderHostedSet(out); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("hosted env %q: refusing to publish anything — %d workload(s) are not admissible:\n  %w",
			group.Env, len(errs), errors.Join(errs...))
	}
	return out, nil
}

// hostedArtifactOf is the release artifact a pinned workload's digest is
// bound under: the declared Artifact, else HostedArtifactName(image) for a
// workload and the workload's own name for a static site (the frontend name
// `forge build` records its site release under). One rule, read by both the
// plan and PreflightHosted, so the preflight can never pin a key the deploy
// would not look up.
//
// A MANAGED DATABASE IS NEVER RELEASE-BOUND, and that is the one case worth
// stating here. A promotion moves a Workload's digest or a StaticSite's
// release and nothing else, so a database has no artifact — the empty string
// is its answer, not a missing one.
//
// This used to fall through to `return name` for any non-Workload tier,
// which handed a database the deployment's own NAME. Nothing caught it
// because the plan only asked about the two release-bound tiers, so the
// wrong answer was latent rather than wrong-on-screen. It stops being latent
// the moment the artifact is actually SENT (F1): the control plane refuses
// an artifact on a database row twice over — InvalidArgument in
// EnsureDeployment, and the schema's
// ck_cp_deployments_artifact_release_bound_tier behind it — so the fallback
// would turn every env with a hosted database into a failed deploy.
//
// The refusal is the right behaviour and this is the right fix: a name that
// coincides with an artifact key is a convention, not a guarantee, and a
// converger handed that pairing would silently repoint the wrong workload
// the first time the two differed.
func hostedArtifactOf(name string, w *HostedWorkload) string {
	// Checked BEFORE the declared Artifact, deliberately. A declaration
	// that names an artifact for a database is a declaration the platform
	// will refuse, and honouring it here would only move the failure to
	// the far side of the wire where the message is worse.
	if w.Tier == HostedTierDatabase {
		return ""
	}
	if w.Artifact != "" {
		return w.Artifact
	}
	if w.Tier == HostedTierWorkload && w.Workload != nil {
		return HostedArtifactName(w.Workload.Image)
	}
	return name
}

// hostedReferenceErrors refuses the cross-workload references the control
// plane can never satisfy. Each spec validates alone, so these are invisible
// to Validate, and the platform does not refuse them either: it publishes
// the spec and the workload waits forever.
//
//   - a databaseRef naming no ManagedDatabase in this env reads a
//     "<name>-app" Secret nothing will ever publish, so the pod never
//     starts (CreateContainerConfigError, no application log);
//   - a workloadURL naming a workload the env does not publish, or a
//     Workload that exposes no port, has no URL the control plane can
//     allocate — its resolver refuses such a target permanently, and the
//     referring workload waits on it forever.
func hostedReferenceErrors(group ServiceGroup) []error {
	databases := map[string]bool{}
	urls := map[string]string{} // workload → "" (has a URL) or why it has none
	for _, svc := range group.Services {
		w := svc.Hosted
		if w == nil {
			continue
		}
		switch {
		case w.Tier == HostedTierDatabase:
			databases[svc.Name] = true
		case w.Tier == HostedTierStatic:
			urls[svc.Name] = ""
		case w.Tier == HostedTierWorkload && w.Workload != nil:
			if w.Workload.ExposedPort() == nil {
				urls[svc.Name] = "it exposes no port, and only a workload with an exposed port has a URL"
			} else {
				urls[svc.Name] = ""
			}
		}
	}
	urlRef := func(from, field, target string) error {
		why, known := urls[target]
		switch {
		case !known:
			return fmt.Errorf("%s: %s references workload %q, which this env does not publish to the control plane", from, field, target)
		case why != "":
			return fmt.Errorf("%s: %s references workload %q, but %s — the control plane can never resolve it", from, field, target, why)
		}
		return nil
	}

	var errs []error
	for _, svc := range group.Services {
		w := svc.Hosted
		if w == nil {
			continue
		}
		if w.Workload != nil {
			for _, e := range w.Workload.Env {
				if e.DatabaseRef != nil && !databases[e.DatabaseRef.Name] {
					errs = append(errs, fmt.Errorf("%s: env var %s reads databaseRef %q, but this env declares no ManagedDatabase of that name — "+
						"its credential Secret will never exist and the pod will never start", svc.Name, e.Name, e.DatabaseRef.Name))
				}
				if e.WorkloadURL != nil {
					if err := urlRef(svc.Name, "env var "+e.Name, e.WorkloadURL.Name); err != nil {
						errs = append(errs, err)
					}
				}
			}
		}
		if w.Static != nil {
			keys := make([]string, 0, len(w.Static.RuntimeConfig))
			for k := range w.Static.RuntimeConfig {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if ref := w.Static.RuntimeConfig[k].WorkloadURL; ref != nil {
					if err := urlRef(svc.Name, "runtimeConfig."+k, ref.Name); err != nil {
						errs = append(errs, err)
					}
				}
			}
		}
	}
	return errs
}

// renderHostedSet renders the admitted hosted Workloads as ONE set, under
// ProfileRestricted, exactly as the control plane's operator will. Each spec
// validated alone; this is what catches the cross-workload refusals the
// platform would otherwise discover after the publish succeeded — a `before`
// naming a workload the set does not contain, a job cycle, a duplicate name.
//
// workloadURL references are resolved to a placeholder first, as the
// operator resolves them before it renders: hostedReferenceErrors already
// proved every reference resolvable, and the value the platform allocates is
// not knowable here.
func renderHostedSet(plan []hostedPlanItem) error {
	var set []v1alpha1.Workload
	for _, item := range plan {
		spec, ok := item.Spec.(v1alpha1.WorkloadSpec)
		if !ok {
			continue
		}
		env, err := deploy.ResolveEnvWorkloadURLs(spec.Env, func(name string) (string, error) {
			return "https://" + name + ".hosted.invalid", nil
		})
		if err != nil {
			return fmt.Errorf("%s: %w", item.Name, err)
		}
		spec.Env = env
		w := v1alpha1.Workload{Spec: spec}
		w.Name = item.Name
		set = append(set, w)
	}
	if len(set) == 0 {
		return nil
	}
	if _, err := deploy.RenderWorkloads(set, v1alpha1.ProfileRestricted, deploy.Context{Namespace: HostedPreflightNamespace}); err != nil {
		return fmt.Errorf("the platform could not render the hosted workload set: %w", err)
	}
	return nil
}

// HostedPreflightNamespace stands in for the namespace the control plane
// allocates. It only has to be well-formed: what renders into it is read,
// never applied.
const HostedPreflightNamespace = "hosted-platform"

// HostedPreflightItem is one workload PreflightHosted admitted: its name,
// tier and the spec the control plane would store — pinned to a PLACEHOLDER
// digest, because no release is involved. Exactly one spec is set.
type HostedPreflightItem struct {
	Name     string
	Tier     HostedTier
	Workload *v1alpha1.WorkloadSpec
	Database *v1alpha1.ManagedDatabaseSpec
	Static   *v1alpha1.StaticSiteSpec
}

// preflightDigest and preflightRegistry stand in for what a promotion
// supplies. They are well-formed, so every rule that does not depend on WHICH
// bytes ship still runs, and obviously not real, so a placeholder can never be
// mistaken for a pin.
const (
	preflightDigest   = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	preflightRegistry = "preflight.forge.invalid"
)

// PreflightHosted answers "would the control plane admit this env's
// workloads?" WITHOUT a release, a credential or a control plane — the
// question CI asks of a checkout (`forge ci validate-kcl`, `forge doctor`)
// before anything is built, pushed or promoted.
//
// It is planHostedWith, not a re-statement of it: every artifact the plan
// would pin is bound to a placeholder digest (and a registry-less image to a
// placeholder registry), and the plan then runs every rule it runs at deploy
// time — spec validation, the shape band, the artifact collision rule and the
// cross-workload references. What it cannot check is what only a live deploy
// knows: that the promoted release pins these artifacts, and that the org's
// image push base admits the image.
func PreflightHosted(group ServiceGroup) ([]HostedPreflightItem, error) {
	plan, err := preflightHostedPlan(group)
	if err != nil {
		return nil, err
	}
	out := make([]HostedPreflightItem, 0, len(plan))
	for _, item := range plan {
		p := HostedPreflightItem{Name: item.Name, Tier: item.Tier}
		switch spec := item.Spec.(type) {
		case v1alpha1.WorkloadSpec:
			p.Workload = &spec
		case v1alpha1.ManagedDatabaseSpec:
			p.Database = &spec
		case v1alpha1.StaticSiteSpec:
			p.Static = &spec
		}
		out = append(out, p)
	}
	return out, nil
}

// preflightHostedPlan supplies obviously fake release facts for display and
// author-time validation only. Copy the group so no placeholder can leak into
// a later live deploy, which still requires the actual, addressed artifact.
func preflightHostedPlan(group ServiceGroup) ([]hostedPlanItem, error) {
	group.Services = append([]ResolvedService(nil), group.Services...)
	for i := range group.Services {
		svc := &group.Services[i]
		if svc.Hosted == nil || svc.Hosted.Tier != HostedTierStatic {
			continue
		}
		artifact := hostedArtifactOf(svc.Name, svc.Hosted)
		if !hostedImageNamesRegistry(artifact) {
			w := *svc.Hosted
			w.Artifact = preflightRegistry + "/" + artifact
			svc.Hosted = &w
		}
	}
	digests := map[string]string{}
	registries := map[string]string{}
	for _, svc := range group.Services {
		w := svc.Hosted
		if w == nil || (w.Tier != HostedTierWorkload && w.Tier != HostedTierStatic) {
			continue
		}
		artifact := hostedArtifactOf(svc.Name, w)
		digests[artifact] = preflightDigest
		registries[artifact] = preflightRegistry
	}
	g := group
	g.Hosted = &HostedTarget{Release: "(preflight: no release)", Digests: digests, Registries: registries}
	return planHostedWith(g, digests)
}

// hostedGroupHasPinnedArtifact reports whether any workload ships bytes a
// release must pin — a workload image or a static site. A database-only env
// needs no release.
func hostedGroupHasPinnedArtifact(group ServiceGroup) bool {
	for _, s := range group.Services {
		if s.Hosted != nil && (s.Hosted.Tier == HostedTierWorkload || s.Hosted.Tier == HostedTierStatic) {
			return true
		}
	}
	return false
}

// ─── The provider ────────────────────────────────────────────────────────────

// HostedProvider ships a hosted group through a control plane.
//
// The zero value is registered in NewRegistry so the observation audit covers
// it; the deploy dispatcher re-registers a configured one (Client set), the
// same pattern K8sClusterProvider uses for its ApplyOptsBuilder.
type HostedProvider struct {
	Client HostedCaller
	// Rollout bounds and shapes the readiness wait: mode skip waits for
	// nothing, warn reports without failing, wait (the default) fails on a
	// workload that never becomes ready. Timeout is the WHOLE wait.
	Rollout cluster.RolloutPolicy
	// PollInterval is the GetStatus cadence. Zero means 3s.
	PollInterval time.Duration
	// OnRollout, when set, receives one outcome per published workload.
	OnRollout func(cluster.RolloutObservation)
	// OnEnvironment, when set, receives the control plane's environment id
	// as soon as it is known, so a report can carry it even if a later step
	// fails.
	OnEnvironment func(id string)
	// RecordBundle builds, pushes and records the env's bundle once the
	// environment exists. The provider cannot do it itself: building needs
	// the project render, which only the CLI can drive.
	// It returns the recorded bundle's digest, which the readiness wait
	// uses to confirm the platform applied THIS bundle before judging
	// workloads ("" skips that confirmation).
	RecordBundle func(ctx context.Context, environmentID string) (bundleDigest string, err error)
}

// Name is the provider's registry id.
func (HostedProvider) Name() string { return HostedProviderID }

func (p HostedProvider) client() (HostedCaller, error) {
	if p.Client == nil {
		return nil, errors.New("hosted provider has no control-plane client (the deploy dispatcher must register a configured one)")
	}
	return p.Client, nil
}

// Deploy runs the fixed order in the file header.
func (p HostedProvider) Deploy(ctx context.Context, group ServiceGroup) error {
	plan, err := planHosted(group)
	if err != nil {
		return err
	}
	if group.DryRun {
		printHostedPlan(group, plan)
		return nil
	}
	c, err := p.client()
	if err != nil {
		return err
	}
	ref := HostedEnvRef{Project: groupProject(group), Name: group.Env, Kind: HostedEnvPersistent}
	if group.Hosted != nil {
		ref.Shape, ref.DeclaredBy = group.Hosted.Shape, group.Hosted.DeclaredBy
	}
	env, created, err := ensureHostedEnvironment(ctx, c, ref)
	if err != nil {
		return err
	}
	envID := env.ID
	if p.OnEnvironment != nil {
		p.OnEnvironment(envID)
	}
	verb := "exists"
	if created {
		verb = "created"
	}
	fmt.Printf("  environment %s %s (id %s)\n", group.Env, verb, envID)
	if err := checkImagePushBase(group.Env, hostedPushBase(group), plan); err != nil {
		return err
	}
	// Before the first EnsureDeployment: on hosted a domain is a bound
	// control-plane resource, so one carried in spec must not reach a
	// published document, where it is indistinguishable from a binding
	// that simply has not converged yet.
	if err := checkCustomDomains(group.Env, plan); err != nil {
		return err
	}
	// THE BUNDLE, NOT A PER-DEPLOYMENT PUBLISH. The env's hosted tiers ride
	// the same recorded bundle every other env ships; the control plane's hub
	// Flux applies it when the promotion lands. forge writes no deployment row
	// and no config artifact.
	bundleDigest := ""
	if p.RecordBundle != nil {
		d, err := p.RecordBundle(ctx, envID)
		if err != nil {
			return err
		}
		bundleDigest = d
	}
	promotionID := ""
	if group.Hosted != nil {
		promotionID = group.Hosted.PromotionID
	}
	return p.wait(ctx, c, group.Env, envID, promotionID, bundleDigest, plan)
}

// wait polls until every published workload is confirmed running the bytes
// this deploy published, or the budget expires. Timing out is reported as
// TIMED OUT, never as success.
//
// WHICH READ DECIDES "DONE" depends on whether this deploy published a
// promotion's pins.
//
// With a promotion (the normal case for a released env), the verdict comes
// from **GetRollout**, the control plane's one server-computed rollout phase,
// scoped to THAT promotion's frozen pins. That is what makes the answer
// trustworthy: a promote of v7 landing mid-wait cannot make a wait on v6
// succeed on bytes it was never asked about, a mid-rollout DIVERGED is not
// read as drift, and an observation claiming the new digest while the new
// ReplicaSet crash-loops is caught by the updated/desired replica pair. The
// CLI's `forge env status --wait` reads the same RPC, so deploy and wait share ONE
// definition of done rather than two that drift (§3.2).
//
// Without one — an unbound env, or a control plane that does not serve
// GetRollout — it falls back to the GetStatus poll: per workload, verdict
// CONVERGED or observed READY on the desired digest.
//
// THE DEPLOY THRESHOLD IS "PUBLISHED AND SERVING", NOT "STABLE". Both paths
// complete on a workload that is serving the right bytes without sitting out
// the server's stability window (STABILIZING counts; see
// rolloutPhaseServing). A deploy that waited for the window would be two
// minutes slower every time, to answer a question `forge env status --wait` is the
// verb for.
func (p HostedProvider) wait(ctx context.Context, c HostedCaller, envName, envID, promotionID, bundleDigest string, plan []hostedPlanItem) error {
	policy := p.Rollout.Normalize()
	if policy.Mode == cluster.RolloutSkip {
		for _, item := range plan {
			p.observe(item.Name, cluster.RolloutStateNotWaited, nil)
		}
		fmt.Println("  readiness: not waited (--rollout skip)")
		return nil
	}
	interval := p.PollInterval
	if interval <= 0 {
		interval = 3 * time.Second
	}
	deadline := time.Now().Add(policy.Timeout)
	var last map[string]string
	// rollout is sticky-off: once this wait has given up on the rollout
	// read, every later poll goes straight to GetStatus. Re-asking each
	// time would spend a call per poll re-learning the same answer.
	rollout := promotionID != ""
	rolloutFailures := 0
	// heldChecked: whether this wait has asked if the promotion is QUEUED.
	// A queued promotion is never applied, so the question is asked once,
	// the first time the platform reports the bundle NOT applied — never on
	// the ordinary path, where the bundle lands and the loop's own rollout
	// read (which also recognises HELD) takes over.
	heldChecked := !rollout
	// applied gates every readiness read: until the platform reports the
	// recorded bundle applied, workload status describes the PREVIOUS
	// revision and must not be judged (or reported) as this deploy's.
	applied := bundleDigest == ""
	var progress applyProgress
	// The apply wait has its OWN budget, and readiness gets a fresh one once
	// the bundle lands. The platform re-observes an env every
	// EnvironmentInterval (2m, control-plane reconcile/worker.go); a promote
	// nudges it within one 15s sweep, but if that wake is missed the apply
	// alone can take ~2m. Charging that to the readiness budget would spend
	// the (default 5m) timeout before any workload was looked at.
	applyDeadline := time.Now().Add(policy.Timeout)
	for {
		if !applied {
			state, perr := pollBundleApplied(ctx, c, envID, promotionID, bundleDigest)
			// NOT APPLIED: is that because the control plane is HOLDING it?
			// A queued promotion (accepted, recorded, waiting on billing) is
			// never applied until a person acts, so waiting would spend the
			// whole budget and report a timeout for a release nothing is
			// wrong with. Asked once; the caller renders the hold.
			// (`forge env deploy` usually knows already — the Promote
			// response says so — and publishes with skip; this is the check
			// for every path that does not.)
			held := heldWhileUnapplied(ctx, c, &heldChecked, state, perr, envName, envID, promotionID)
			switch {
			case held != nil:
				return p.reportHeld(plan, held)
			case perr != nil && !state.known:
				fmt.Printf("  readiness: could not read the platform's applied revision (%v); judging workloads directly\n", perr)
				applied = true
			case state.applied:
				applied = true
				deadline = time.Now().Add(policy.Timeout)
			default:
				progress.announce(state, bundleDigest)
				if time.Now().After(applyDeadline) || ctx.Err() != nil {
					return p.applyNotLanded(envName, bundleDigest, state, plan, policy.Mode)
				}
				select {
				case <-ctx.Done():
				case <-time.After(interval):
				}
				continue
			}
		}
		pending, reasons, published, err := p.pollOnce(ctx, c, envID, plan)
		if rollout {
			rolloutPending, rolloutReasons, rerr := p.pollRolloutOnce(ctx, c, envID, promotionID, plan)
			var held *HeldError
			switch {
			case errors.As(rerr, &held):
				// QUEUED (phase HELD): accepted and recorded, waiting on a
				// person. Terminal for this wait — no amount of polling
				// moves it — and not a timeout.
				held.Env = envName
				return p.reportHeld(plan, held)
			case errors.Is(rerr, errRolloutUnavailable):
				// Said once, because the fallback is a WEAKER
				// completion check and an operator reading a
				// green deploy should know which question was
				// actually answered.
				fmt.Printf("  readiness: %s cannot report this promotion's rollout; falling back to the status poll\n", envName)
				rollout = false
			case errors.Is(rerr, errRolloutSuperseded):
				// TERMINAL, and deliberately not a timeout: the
				// bytes being waited on are no longer what the
				// env wants, so no amount of further waiting
				// can make this deploy's question answerable.
				return p.rolloutSuperseded(envName, rerr, plan, policy)
			case rerr != nil:
				// Any other failure is retried a FEW times and
				// then abandoned in favour of the status poll.
				//
				// Retried, because a control plane restarting
				// mid-rollout should not weaken the completion
				// check for the rest of the deploy. Abandoned
				// rather than retried forever, because the
				// alternative is worse in both directions: a
				// rollout read that keeps failing would
				// otherwise consume the entire rollout budget
				// and time the deploy out with "last status
				// read failed", even though GetStatus was
				// answering perfectly well the whole time. A
				// weaker completion check, announced, beats a
				// deploy that fails for a reason that has
				// nothing to do with the deploy.
				rolloutFailures++
				if rolloutFailures >= rolloutReadAttempts {
					fmt.Printf("  readiness: could not read this promotion's rollout (%v); falling back to the status poll\n", rerr)
					rollout = false
					break
				}
				// Only err == nil below updates `last`, so a
				// retried tick keeps the previous reasons.
				pending, reasons, err = nil, nil, rerr
			default:
				// The rollout decides readiness; the status read
				// above contributes only its URLs and domains.
				pending, reasons, err = rolloutPending, rolloutReasons, nil
			}
		}
		if err == nil && len(pending) == 0 {
			for _, item := range plan {
				p.observe(item.Name, cluster.RolloutStateReady, nil)
			}
			fmt.Printf("  readiness: all %d workload(s) ready\n", len(plan))
			printHostedURLs(plan, published)
			printHostedDomains(plan, published)
			return nil
		}
		if err == nil {
			last = reasons
			// Printed on the way past, not only at the end: a domain
			// waiting on a DNS record the author has not set yet is
			// exactly what a deploy that looks stuck is waiting for,
			// and the record is actionable right now.
			printHostedDomains(plan, published)
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return p.readinessTimedOut(envName, last, err, plan, policy)
		}
		select {
		case <-ctx.Done():
		case <-time.After(interval):
		}
	}
}

// rolloutSuperseded ends a wait whose promotion was replaced by a newer one:
// every workload is observed NOT WAITED, and the error is returned, or only
// printed under --rollout warn.
func (p HostedProvider) rolloutSuperseded(envName string, cause error, plan []hostedPlanItem, policy cluster.RolloutPolicy) error {
	for _, item := range plan {
		p.observe(item.Name, cluster.RolloutStateNotWaited, cause)
	}
	err := fmt.Errorf("hosted env %q: %w", envName, cause)
	if policy.Mode == cluster.RolloutWarn {
		fmt.Printf("  Warning: %v\n", err)
		return nil
	}
	return err
}

// readinessTimedOut ends a wait whose readiness budget ran out. last is the
// most recent successful read's not-ready reasons (nil if no read succeeded,
// in which case nothing is known ready); readErr is the latest read's error,
// if it failed. Each workload is observed TIMED OUT or ready accordingly.
// Under --rollout warn the error is printed and nil returned.
func (p HostedProvider) readinessTimedOut(envName string, last map[string]string, readErr error, plan []hostedPlanItem, policy cluster.RolloutPolicy) error {
	var lines []string
	for _, item := range plan {
		if r, notReady := last[item.Name]; notReady || last == nil {
			p.observe(item.Name, cluster.RolloutStateTimedOut, errors.New(r))
			lines = append(lines, fmt.Sprintf("%s: %s", item.Name, emptyOr(r, "no status reported")))
		} else {
			p.observe(item.Name, cluster.RolloutStateReady, nil)
		}
	}
	sort.Strings(lines)
	werr := fmt.Errorf("hosted env %q: TIMED OUT after %s waiting for readiness (published, not confirmed running):\n  %s",
		envName, policy.Timeout, strings.Join(lines, "\n  "))
	if readErr != nil {
		werr = fmt.Errorf("%w\n  last status read failed: %v", werr, readErr)
	}
	if policy.Mode == cluster.RolloutWarn {
		fmt.Printf("  Warning: %v\n", werr)
		return nil
	}
	return werr
}

// applyProgress is what wait has already printed about the platform's apply,
// so each change is announced once rather than on every poll.
type applyProgress struct{ current, failure string }

// announce prints what changed since the last poll: a reconciler failure of
// this promotion, and the revision the platform currently has applied.
func (a *applyProgress) announce(state bundleApplyState, bundleDigest string) {
	if state.failure != "" && a.failure != state.failure {
		a.failure = state.failure
		fmt.Printf("  platform reports the apply of %s failed: %s (still waiting; the reconciler retries)\n", shortDigest(bundleDigest), state.failure)
	}
	if cur := shortDigest(state.current); a.current != cur {
		a.current = cur
		fmt.Printf("  waiting for the platform to apply %s (currently %s)\n", shortDigest(bundleDigest), emptyOr(cur, "none"))
	}
}

// applyNotLanded ends a wait whose apply budget ran out before the platform
// applied the recorded bundle. Every workload is observed TIMED OUT, and the
// error names the missing — or failed — apply rather than any workload's
// state, which still describes the previous revision. Under --rollout warn
// the error is printed and nil returned.
func (p HostedProvider) applyNotLanded(envName, bundleDigest string, state bundleApplyState, plan []hostedPlanItem, mode cluster.RolloutMode) error {
	cause := errBundleNotApplied
	msg := fmt.Sprintf("the platform has not applied this release yet (waiting for %s, currently %s); workloads were not judged",
		shortDigest(bundleDigest), emptyOr(shortDigest(state.current), "none"))
	if state.failure != "" {
		cause = fmt.Errorf("the platform failed to apply this release: %s", state.failure)
		msg = fmt.Sprintf("the platform failed to apply this release (%s, currently %s): %s",
			shortDigest(bundleDigest), emptyOr(shortDigest(state.current), "none"), state.failure)
	}
	for _, item := range plan {
		p.observe(item.Name, cluster.RolloutStateTimedOut, cause)
	}
	werr := fmt.Errorf("hosted env %q: TIMED OUT waiting for the platform to apply: %s", envName, msg)
	if mode == cluster.RolloutWarn {
		fmt.Printf("  Warning: %v\n", werr)
		return nil
	}
	return werr
}

// errRolloutSuperseded ends the wait immediately: a newer promotion replaced
// the one this deploy published, so the bytes being waited on are no longer
// what the environment wants. Sitting out the timeout would report "this
// deploy failed" for a deploy that was simply overtaken, and the operator's
// next step is to look at the newer promotion rather than at this one.
var errRolloutSuperseded = errors.New("a newer promotion replaced the one this deploy published")

// pollRolloutOnce reads the promotion's rollout once and returns the plan
// items not yet confirmed serving its pins, with a reason for each.
func (p HostedProvider) pollRolloutOnce(ctx context.Context, c HostedCaller, envID, promotionID string, plan []hostedPlanItem) ([]string, map[string]string, error) {
	rollout, err := readHostedRollout(ctx, c, envID, promotionID)
	if err != nil {
		return nil, nil, err
	}
	if rollout.Phase == wireRolloutPhaseSuperseded {
		return nil, nil, fmt.Errorf("%w: %s", errRolloutSuperseded, emptyOr(rollout.Reason, "promotion "+promotionID+" was superseded"))
	}
	if rollout.Phase == WireRolloutPhaseHeld {
		return nil, nil, &HeldError{PromotionID: promotionID, Release: rollout.Promotion.ReleaseVersion, Holds: rollout.Holds}
	}
	pending, reasons := rolloutPendingOf(plan, rollout)
	return pending, reasons, nil
}

// heldWhileUnapplied asks — once per wait, tracked in *checked, and only when
// the platform positively reports the bundle NOT applied — whether the
// promotion is QUEUED. Nil when it is not, when it was already asked, when
// the apply state is unknown or applied, or when the rollout read fails (an
// unreadable rollout is not evidence of a hold; the apply wait carries on and
// its own deadline decides).
func heldWhileUnapplied(
	ctx context.Context, c HostedCaller, checked *bool, state bundleApplyState, pollErr error,
	envName, envID, promotionID string,
) *HeldError {
	if *checked || state.applied || (pollErr != nil && !state.known) {
		return nil
	}
	*checked = true
	r, err := readHostedRollout(ctx, c, envID, promotionID)
	if err != nil || r.Phase != WireRolloutPhaseHeld {
		return nil
	}
	return &HeldError{Env: envName, PromotionID: promotionID, Release: r.Promotion.ReleaseVersion, Holds: r.Holds}
}

// reportHeld ends a wait on a queued promotion: every workload is recorded as
// not waited on, with the hold as the reason, and the hold is the result.
func (p HostedProvider) reportHeld(plan []hostedPlanItem, held *HeldError) error {
	for _, item := range plan {
		p.observe(item.Name, cluster.RolloutStateNotWaited, held)
	}
	return held
}

// hostedPublished is where one workload answers: its platform URL and its
// custom domains, as of one status read.
type hostedPublished struct {
	URL     string
	Domains []HostedCustomDomain
}

// pollOnce reads status once and returns the names still not ready, with a
// reason for each, and where each workload in the status answers.
func (p HostedProvider) pollOnce(ctx context.Context, c HostedCaller, envID string, plan []hostedPlanItem) ([]string, map[string]string, map[string]hostedPublished, error) {
	var resp wireStatusResponse
	if err := c.Call(ctx, procGetStatus, map[string]any{"environmentId": envID}, &resp); err != nil {
		return nil, nil, nil, err
	}
	envConverged := resp.EnvironmentVerdict == wireVerdictConverged
	byName := map[string]wireDeploymentStatus{}
	for _, d := range resp.Deployments {
		byName[d.Deployment.Name] = d
	}
	var pending []string
	reasons := map[string]string{}
	published := map[string]hostedPublished{}
	for _, item := range plan {
		st, ok := byName[item.Name]
		if !ok {
			pending = append(pending, item.Name)
			reasons[item.Name] = "not in the environment's status yet"
			continue
		}
		pub := hostedPublished{Domains: customDomainsOf(st.Deployment.Observed)}
		if obs := st.Deployment.Observed; obs != nil {
			pub.URL = obs.URL
		}
		published[item.Name] = pub
		if envConverged || hostedDeploymentReady(st, item.DesiredDigest) {
			continue
		}
		pending = append(pending, item.Name)
		reasons[item.Name] = describeHostedStatus(st)
	}
	return pending, reasons, published, nil
}

// printHostedURLs is the deploy summary's answer to "where is it?": the
// platform URL of every workload that has one, in plan order. Printed once,
// when the deploy is ready — a URL printed mid-rollout may still serve the
// previous release.
func printHostedURLs(plan []hostedPlanItem, published map[string]hostedPublished) {
	ws := make([]HostedWorkloadStatus, 0, len(plan))
	for _, item := range plan {
		ws = append(ws, HostedWorkloadStatus{Name: item.Name, URL: published[item.Name].URL})
	}
	fmt.Print(FormatHostedURLs("  ", ws))
}

// FormatHostedURLs renders one "url <name>: <url>" line per workload that has
// a platform URL, in the order given; "" when none has one. It is the one
// spelling `forge env deploy` and `forge env status` share, so the line an
// author copies a URL from reads the same in both.
func FormatHostedURLs(indent string, workloads []HostedWorkloadStatus) string {
	var b strings.Builder
	for _, w := range workloads {
		if w.URL == "" {
			continue
		}
		fmt.Fprintf(&b, "%surl %s: %s\n", indent, w.Name, w.URL)
	}
	return b.String()
}

// printHostedDomains is the deploy summary's custom-domain block: per
// workload, each declared hostname's state and the DNS records still to be
// set. Silent for a deploy that declares none, and printed in plan order so
// it reads alongside the readiness line above it.
//
// A domain that is not yet live is NOT a deploy failure — the author has to
// go and edit DNS at their registrar, which forge cannot do and must not
// block on. Printing the exact record is what turns "not live" into a next
// step.
func printHostedDomains(plan []hostedPlanItem, published map[string]hostedPublished) {
	var printed bool
	for _, item := range plan {
		d := published[item.Name].Domains
		if len(d) == 0 {
			continue
		}
		if !printed {
			fmt.Println("  custom domains:")
			printed = true
		}
		fmt.Printf("    %s:\n", item.Name)
		fmt.Print(FormatCustomDomains("      ", d))
	}
}

func hostedDeploymentReady(st wireDeploymentStatus, desiredDigest string) bool {
	if st.Verdict == wireVerdictConverged {
		return true
	}
	obs := st.Deployment.Observed
	if obs == nil || obs.State != wireObservedReady {
		return false
	}
	want := desiredDigest
	if st.DesiredDigest != "" {
		want = st.DesiredDigest
	}
	return want == "" || obs.ImageDigest == want
}

func describeHostedStatus(st wireDeploymentStatus) string {
	parts := []string{"verdict " + VerdictName(st.Verdict)}
	if obs := st.Deployment.Observed; obs != nil {
		parts = append(parts, "observed "+observedName(obs.State))
		if obs.LastError != "" {
			parts = append(parts, "error: "+obs.LastError)
		}
	}
	if st.VerdictReason != "" {
		parts = append(parts, st.VerdictReason)
	}
	return strings.Join(parts, ", ")
}

func (p HostedProvider) observe(name string, state cluster.RolloutState, err error) {
	if p.OnRollout != nil {
		p.OnRollout(cluster.RolloutObservation{Kind: "HostedDeployment", Name: name, State: state, Err: err})
	}
}

func printHostedPlan(group ServiceGroup, plan []hostedPlanItem) {
	endpoint := ""
	if group.Hosted != nil {
		endpoint = group.Hosted.Endpoint
	}
	fmt.Printf("  [dry-run] would publish %d workload(s) to %s (release %s):\n", len(plan), endpoint, group.Hosted.Release)
	for _, item := range plan {
		raw, _ := json.Marshal(item.Spec)
		fmt.Printf("    %s (%s): %s\n", item.Name, item.Tier, raw)
	}
	// --dry-run must not be the one path that hides a refusal a real
	// deploy produces, so it names the same spec-carried domains and says
	// they are refused.
	if claims := hostedDomainClaims(plan); len(claims) > 0 {
		fmt.Println("  [dry-run] custom domains carried in spec (a real deploy REFUSES these; use `forge domain add` + `forge domain bind`):")
		for _, c := range claims {
			fmt.Printf("    %s (%s): %s\n", c.Workload, c.Tier, strings.Join(c.Domains, ", "))
		}
	}
}

// ─── Status (shared by Observe and the CLI's topology/status JSON) ───────────

// HostedWorkloadStatus is one deployment as the console contract reports it.
type HostedWorkloadStatus struct {
	Name string `json:"name"`
	// Tier is workload | database | static.
	Tier string `json:"tier,omitempty"`
	// URL is the platform-allocated public URL, once one exists.
	URL string `json:"url,omitempty"`
	// Hostname is URL's host, for a badge that has no room for a scheme.
	Hostname string `json:"hostname,omitempty"`
	// Verdict is the control plane's lower-case deploystate verdict:
	// unknown | converging | converged | diverged | degraded.
	Verdict       string `json:"verdict"`
	VerdictReason string `json:"verdict_reason,omitempty"`
	// ObservedState is the observation vocabulary: pending | progressing |
	// ready | degraded | suspended | deleted | unknown.
	ObservedState  string `json:"observed_state"`
	ObservedDigest string `json:"observed_digest,omitempty"`
	DesiredDigest  string `json:"desired_digest,omitempty"`
	Drifted        bool   `json:"drifted,omitempty"`
	LastError      string `json:"last_error,omitempty"`
	// Domains is the per-domain state of every custom hostname this
	// deployment declares — what it is doing, and what DNS the author
	// still owes. Empty for a deployment that declares none.
	Domains []HostedCustomDomain `json:"domains,omitempty"`
}

// HostedEnvStatus is one environment's hosted status.
type HostedEnvStatus struct {
	EnvironmentID string
	// Verdict is the environment-level verdict, lower-case.
	Verdict         string
	ReconcilePolicy string
	Workloads       []HostedWorkloadStatus
}

// ReadHostedStatus reads an environment's status by (project, NAME). An
// environment the control plane has never heard of returns
// ErrHostedEnvironmentNotFound.
func ReadHostedStatus(ctx context.Context, c HostedCaller, project, envName string) (HostedEnvStatus, error) {
	envID, err := LookupHostedEnvironment(ctx, c, project, envName)
	if err != nil {
		return HostedEnvStatus{}, err
	}
	var resp wireStatusResponse
	if err := c.Call(ctx, procGetStatus, map[string]any{"environmentId": envID}, &resp); err != nil {
		return HostedEnvStatus{EnvironmentID: envID}, err
	}
	out := HostedEnvStatus{
		EnvironmentID:   envID,
		Verdict:         VerdictName(resp.EnvironmentVerdict),
		ReconcilePolicy: strings.ToLower(strings.TrimPrefix(resp.ReconcilePolicy, "DEPLOY_RECONCILE_POLICY_")),
	}
	for _, d := range resp.Deployments {
		ws := HostedWorkloadStatus{
			Name:          d.Deployment.Name,
			Tier:          hostedTierName(d.Deployment.Tier),
			Verdict:       VerdictName(d.Verdict),
			VerdictReason: d.VerdictReason,
			DesiredDigest: d.DesiredDigest,
			Drifted:       d.Drifted,
			ObservedState: "unknown",
		}
		if obs := d.Deployment.Observed; obs != nil {
			ws.ObservedState = observedName(obs.State)
			ws.ObservedDigest = obs.ImageDigest
			ws.LastError = obs.LastError
			ws.URL = obs.URL
			if u, perr := url.Parse(obs.URL); perr == nil {
				ws.Hostname = u.Hostname()
			}
			ws.Domains = customDomainsOf(obs)
		}
		out.Workloads = append(out.Workloads, ws)
	}
	sort.Slice(out.Workloads, func(i, j int) bool { return out.Workloads[i].Name < out.Workloads[j].Name })
	return out, nil
}

// hostedTierName is the forge vocabulary for a wire DeployTier:
// DEPLOY_TIER_BACKEND carries a Workload (see wireTier), so it reads back as
// "workload".
func hostedTierName(wire string) string {
	t := strings.ToLower(strings.TrimPrefix(wire, "DEPLOY_TIER_"))
	if t == "backend" {
		return string(HostedTierWorkload)
	}
	return t
}

// VerdictName renders a DeployVerdict value name in the lower-case vocabulary
// forge's deploystate uses. Unset and unrecognised values are "unknown" —
// never defaulted toward health.
func VerdictName(wire string) string {
	switch v := strings.ToLower(strings.TrimPrefix(wire, wireVerdictPrefix)); v {
	case "converging", "converged", "diverged", "degraded":
		return v
	default:
		return "unknown"
	}
}

func observedName(wire string) string {
	switch v := strings.ToLower(strings.TrimPrefix(wire, wireObservedPrefix)); v {
	case "pending", "progressing", "ready", "degraded", "suspended", "deleted":
		return v
	default:
		return "unknown"
	}
}

// groupProject is the project a hosted group's environment is addressed
// under, or "" when the group carries no hosted target.
func groupProject(group ServiceGroup) string {
	if group.Hosted == nil {
		return ""
	}
	return group.Hosted.Project
}

func emptyOr(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}
