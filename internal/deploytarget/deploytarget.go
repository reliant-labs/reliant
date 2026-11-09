// Package deploytarget owns the deploy dispatch — the surface that maps a
// rendered workload's RUNTIME (and each frontend's and database's deploy
// target) to a concrete pipeline that ships it somewhere.
//
// Placement is fully owned by KCL: every fw.Workload is bound to one runtime
// (forge.OnHost | OnCompose | OnCluster | OnHosted | BuildOnly), per workload,
// so one env can run some workloads on a cluster, some on the control plane
// and some as host processes. The CLI walks the rendered workloads, groups
// them by target (workloads that share a cluster/namespace or a compose file
// flow through one pipeline invocation), and dispatches to the right
// Provider.
//
// Providers:
//
//   - K8sClusterProvider — wraps internal/cluster.Apply (the render-KCL →
//     expand Workload records through pkg/deploy.RenderWorkloads →
//     kubectl-apply → wait-rollouts pipeline).
//   - HostedProvider     — publishes a Workload CR per hosted workload (plus
//     StaticSite / ManagedDatabase) to the control plane.
//   - ComposeProvider    — docker compose pull/up -d.
//   - HostInfraProvider  — a third-party server (postgres) run as a HOST
//     PROCESS, no container runtime. This is the DEFAULT shape for dev
//     infrastructure; Compose is the opt-in for projects that want the
//     container. See internal/hostinfra.
//   - FirebaseProvider / StaticSiteProvider — frontends.
//
// Host-bound and build-only workloads aren't providers — `forge env up` /
// `forge env up` own the host story, and BuildOnly is consumed by `forge
// build`. The dispatcher skips both rather than routing them through a
// Provider.
//
// HostInfra IS a provider even though it also runs on the host, because it
// answers a different question: a host workload launches code this project
// BUILDS, HostInfra supervises a dependency forge FETCHES. They fail
// differently (a compile error vs. a missing binary vs. a held port), and a
// single "runs on the host" target would have to guess which it was
// looking at.
//
//forge:lint-disable-next-line forge-exclude-contract-outbound-io: each registered Provider is its own outbound adapter (kubectl, compose, oras for StaticSite, the hosted API); the registry dispatches, and a per-provider package split is CONTRACTS follow-up F2
//forge:exclude-contract: a deploy-provider strategy registry — every target registers itself with (*Registry).Register, and each Provider is its own adapter
package deploytarget

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/deploy"
)

// Provider is the dispatch surface for one deploy target type. Each
// concrete provider owns the pipeline for its target (k8s, hosted,
// compose, etc.); the dispatcher in forge env deploy hands it a
// ServiceGroup and the provider does the rest.
//
// There is deliberately no Rollback verb. Recovery is ROLL FORWARD: ship a
// new release (or promote a newer build) through Deploy. A "go back to the
// previous revision" verb claims to undo a release it cannot undo — the
// release has already written data and run migrations the older code never
// saw — so forge does not offer one.
type Provider interface {
	// Name returns the provider's stable identifier — used in log
	// output and error messages so users can tell which provider
	// produced a given line.
	Name() string

	// Deploy ships every service in the group. The provider owns the
	// in-pipeline ordering (e.g. K8sClusterProvider does one
	// kubectl-apply for all services at once because they share a
	// cluster/namespace).
	Deploy(ctx context.Context, group ServiceGroup) error

	// Observe reports what this provider currently sees deployed, so a
	// caller can compare it against what was declared.
	//
	// A READ with no side effects: it may run against a target forge has
	// never deployed to, and it must never mutate one. Deploy describes
	// a transition; this is the only verb that answers
	// "what is actually running right now", which is what a reconciler
	// is built around.
	//
	// A provider that cannot read its target back returns
	// ErrObservationUnsupported (via the unsupported helper) — never a
	// green Observed. See observe.go for why unknown is the zero value
	// on every field that could otherwise default to "fine".
	Observe(ctx context.Context, group ServiceGroup) (Observed, error)
}

// ServiceGroup is a set of services that share a deploy target —
// same provider type, same cluster/host/compose-file. The dispatcher
// groups by the (Provider, target-identifier) tuple so each group can
// flow through one provider invocation.
//
// The common env-wide fields (Cluster/Namespace/Registry/Domain) live
// on the group because K8sCluster refs make them identical across
// every service in the group. The provider reads them off the group
// rather than re-deriving them from each ResolvedService.
type ServiceGroup struct {
	// Env is the environment name (matches the deploy/kcl/<env>/
	// directory name and the `forge env deploy <env>` CLI arg).
	Env string

	// ProviderID identifies the provider type — "k8s-cluster",
	// "hosted", "compose". Used by the dispatcher to look the
	// provider up and by log output to tag lines per-group.
	ProviderID string

	// Services is the per-service list. Each entry's Deploy field is
	// the dispatched view of the rendered KCL — see ResolvedService.
	Services []ResolvedService

	// Frontends carries the frontends the "firebase" provider ships.
	// It's the frontend analogue of Services — empty for the
	// service-shaped providers (k8s-cluster / compose / host-infra), which
	// read Services instead. The Firebase provider reads Frontends +
	// DryRun off the group so it dispatches through the registry like
	// every other provider.
	Frontends []FirebaseFrontend

	// StaticSites carries the frontends the "static-site" provider
	// ships. A separate field rather than a shared one because the two
	// frontend providers consume genuinely different specs (a hosting
	// site id vs. a bucket + CDN + retention policy), and a group only
	// ever routes to ONE provider — so a union type here would buy a
	// type assertion in both providers and nothing else.
	StaticSites []StaticSiteFrontend

	// Hosted is the env-level half of a "hosted" group: the control plane's
	// endpoint and the bound release's digests. Nil for every other
	// provider. Services carry the per-workload tier specs.
	Hosted *HostedTarget

	// HostedPlan is the plan a hosted group is admitted under: the
	// standard (billed) plan, judged by the shape band, or the anonymous
	// claimable plan, judged by its caps (deploy.CheckHostedResources). A
	// field rather than a property of the credential, so a preview with no
	// credential (`forge env shape --claimable`, `--claimable --plan-only`)
	// is judged exactly as the deploy will be.
	HostedPlan deploy.HostedPlan
	// ClaimableLimits are the claimable plan's caps HostedPlanClaimable is
	// judged by: the control plane's published answer, or forge's
	// compiled-in copy when it could not be asked. Zero (an unset field)
	// means the compiled-in copy.
	ClaimableLimits deploy.ClaimableLimits

	// ImageTag is the tag forge built (or is about to build) for
	// these services. Passed through to the provider so it can stamp
	// the image references correctly.
	ImageTag string

	// Common K8sCluster fields. Pulled from the first service in the
	// group (KCL refs guarantee they're identical across the group).
	// Empty for non-cluster providers.
	Cluster   string
	Namespace string
	Registry  string
	Domain    string

	// DryRun, when true, instructs the provider to print the exact
	// commands it would exec instead of running them, and skip any
	// state-file writes. Providers honor this independently of the
	// cluster.ApplyOpts.DryRun knob (K8sCluster's provider plumbs it
	// through ApplyOpts; Compose checks this field
	// directly because they don't go through cluster.Apply).
	DryRun bool
}

// ClaimableCaps is the caps a claimable group is judged by: ClaimableLimits,
// or the compiled-in copy when none were resolved.
func (g ServiceGroup) ClaimableCaps() deploy.ClaimableLimits {
	if g.ClaimableLimits.Valid() {
		return g.ClaimableLimits
	}
	return deploy.ClaimableLimitsFallback()
}

// ResolvedService is one service in a group, with its deploy block
// already dispatched by type. Exactly one of K8sCluster/Hosted/
// Compose/HostInfra is non-nil; the dispatcher discards services with
// HostDeploy/BuildOnly (those aren't in any deploy-target group).
type ResolvedService struct {
	Name string

	// Exactly one of the following is non-nil. Discriminated by the
	// owning ServiceGroup.ProviderID, but kept as separate pointers
	// so each provider's Deploy method can type-assert against its
	// own concrete shape without a runtime switch.
	K8sCluster *K8sClusterSpec
	Compose    *ComposeSpec
	HostInfra  *HostInfraSpec
	Hosted     *HostedWorkload

	// Secrets carries resolved secret values to inject into the runtime
	// env (compose). Populated by the deploy dispatch from a
	// dotenv secret_provider's All() map; nil for external/none providers
	// (those resolve secrets out-of-band) and always nil for K8sCluster
	// services (those get rendered Secret objects + secretKeyRef, not
	// inlined env values). Merged UNDER the env_file overlay so an
	// explicit env_file entry wins on key conflict.
	Secrets map[string]string
}

// K8sClusterSpec is the per-service portion of a K8sCluster deploy
// target. Env-wide fields (cluster/namespace/registry/domain) live on
// the ServiceGroup, not here.
//
// Ingress used to be a per-service field; it now lives at the
// Gateway/HTTPRoute level (see kcl/schema.k, internal/cli/kcl_render.go
// KCLEntities.Gateways), with routes referencing services by name.
type K8sClusterSpec struct {
	Replicas int
	Platform string
	Ports    []int

	// OwnedClaims names the PersistentVolumeClaims forge ITSELF emits
	// for this workload, so an observation can be accountable for them.
	//
	// The distinction is ownership, not usage. A forge.Volume of type
	// "pvc" REFERENCES a claim by name and leaves its existence to
	// whoever provisioned it — forge has no standing to report on a
	// claim it did not create. A workload's `storageGiB` is the one
	// case where forge emits the PVC (pkg/deploy.RenderWorkloads, named by PVCName), and a thing forge creates is a thing forge must be
	// able to read back.
	//
	// It matters because an unbound claim is invisible in the half of
	// the state this observation otherwise reads. A Deployment whose PVC
	// never binds reports "0/1 ready" — true, and it names the symptom
	// while the cause (no default StorageClass on this cluster, or a
	// storage_gib change the class refused) sits one object away.
	//
	// Empty for an ordinary cluster service, which is why this is a
	// nil-able slice rather than a bool plus a naming convention: the
	// observer asks the group what forge owns instead of re-deriving
	// "<name>-data" and hoping the render still agrees.
	OwnedClaims []string
}

// ComposeSpec is the per-service docker-compose deploy spec. Mirrors
// the kcl/schema.k Compose schema.
type ComposeSpec struct {
	ComposeFile string
	Service     string
	EnvFile     string

	// Env is the KCL-declared map forge puts in the `docker compose`
	// PROCESS environment — which is what the compose file's own `${VAR}`
	// references interpolate from. EnvFile is a different channel: it only
	// forwards values into CONTAINERS, and passing --env-file also
	// REPLACES compose's default `.env`.
	//
	// It is what lets a value be declared once in an env's KCL and reach
	// the container (the dev IdP's port is the motivating case — see
	// Compose.env in kcl/schema.k). These entries WIN over the inherited
	// shell environment, deliberately: a shell override would move the
	// container while every KCL reference to the same value stayed put.
	Env map[string]string

	// Wait selects readiness-gated deploys: `docker compose up -d
	// --wait` blocks until the service's declared healthcheck passes
	// instead of returning the moment the container is created.
	//
	// It is a pointer because the useful default is TRUE and a bool's
	// zero value is false: a nil Wait means "not specified", which
	// composeWait resolves to true. That keeps a ComposeSpec built by
	// any path — KCL, a test literal, a future caller — readiness-gated
	// unless someone opts OUT on purpose.
	Wait *bool

	// WaitTimeoutSeconds is a CEILING on the wait, not the mechanism:
	// readiness is decided by the healthcheck, and this only bounds how
	// long forge tolerates a container that never gets there, so a
	// wedged service fails loudly instead of hanging a dev loop or a CI
	// job forever. Zero means "no explicit ceiling" and lets the
	// healthcheck's own retries x interval bound it.
	WaitTimeoutSeconds int

	// ProjectDirectory is the directory compose runs the stack FROM: it
	// resolves ComposeFile and EnvFile, it is what every relative bind
	// mount in the file resolves against, and it is the
	// `com.docker.compose.project.working_dir` compose stamps on the
	// containers. Empty means the process working directory — a stack the
	// current checkout owns.
	//
	// The dispatcher sets it to the repo's PRIMARY checkout for a
	// `shared = True` stack (OnCompose.shared), so every worktree drives
	// the SAME containers with the SAME resolved config and an `up` from
	// any of them is a no-op once the stack is running.
	ProjectDirectory string

	// Shared records that the stack is machine infrastructure every
	// worktree uses (OnCompose.shared). It changes how a FOREIGN owner is
	// treated — see checkComposeOwnership.
	Shared bool
}

// HostInfraSpec is the per-service host-infra deploy spec. Mirrors the
// kcl/schema.k HostInfra schema: a third-party server forge runs as a host
// process rather than a container. See internal/hostinfra.
type HostInfraSpec struct {
	Engine   string
	Port     int
	Database string
	User     string
	Password string
	DataDir  string
	Version  string

	// engine = "zitadel" only — the dev IdP's backing database and its
	// declarative bootstrap. See the HostInfra schema in kcl/schema.k.
	IDPDatabase     string
	IDPDatabasePort int
	IDPMasterKey    string
	IDPStepsFile    string
	IDPPATPath      string

	// engine = "nats" only.
	NATSMonitorPort int
	// engine = "temporal" only.
	TemporalNamespaces []string
	TemporalUIPort     int
}

// ErrProviderNotImplemented is the sentinel future providers (Lambda,
// EdgeWorker, etc.) wrap when their dispatch lands as a stub. Keep
// using errors.Is to distinguish "feature deferred" from "real
// failure" — the active K8sCluster / Compose / Hosted providers do
// NOT return this; they implement the full pipeline.
var ErrProviderNotImplemented = errors.New("forge: deploy provider not yet implemented in this release")

// Registry holds the set of Providers registered with the dispatcher.
// In forge today there's one canonical registry built by NewRegistry;
// tests can construct their own to swap in fakes.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry returns a Registry pre-populated with the canonical
// forge providers (k8s-cluster + compose + host-infra + hosted + frontends).
// Callers that need to inject test doubles should construct an empty
// Registry and Register the doubles directly.
//
// The Firebase provider is registered with its zero value here; the
// deploy dispatcher re-registers a ProjectDir-configured one (the same
// pattern K8sClusterProvider uses for its ApplyOptsBuilder) before
// dispatching the frontend group.
func NewRegistry() *Registry {
	r := &Registry{providers: map[string]Provider{}}
	r.Register(K8sClusterProvider{})
	r.Register(ComposeProvider{})
	r.Register(HostInfraProvider{})
	r.Register(FirebaseProvider{})
	r.Register(StaticSiteProvider{})
	r.Register(HostedProvider{})
	return r
}

// Register adds (or replaces) a provider under its declared Name().
// Safe on a zero-value Registry (lazy-inits the map) so tests can do
// `r := &Registry{}; r.Register(fake)` without going through
// NewRegistry — matches the package doc-comment promise.
func (r *Registry) Register(p Provider) {
	if r.providers == nil {
		r.providers = map[string]Provider{}
	}
	r.providers[p.Name()] = p
}

// Lookup returns the provider for an id, or nil if none registered.
// Callers should treat a nil return as "no provider for this target
// type" and emit a friendly error pointing at the migration skill.
func (r *Registry) Lookup(id string) Provider {
	return r.providers[id]
}

// IDs returns every registered provider id, sorted.
//
// It exists so a test can ENUMERATE the registry rather than restate it.
// A hand-written list of providers in a test is a second declaration of
// the same fact, and the failure mode is silence: a provider added to
// NewRegistry and not to the list is simply never checked, which is the
// exact shape — a clean board for something nobody measured — that the
// observation contract exists to prevent. See TestEveryRegisteredProvider
// ObservesOrDeclares in observe_audit_test.go.
func (r *Registry) IDs() []string {
	out := make([]string, 0, len(r.providers))
	for id := range r.providers {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// GroupServices walks a rendered service list and returns the deploy
// groups it should be split into. Services with deploy types `host`
// and `build-only` are NOT included — those are owned by `forge env up`
// and `forge build`.
//
// Cluster grouping rule: services sharing a (Cluster, Namespace,
// Registry) tuple end up in one group. This handles the typical
// pattern (single K8sCluster ref attached to many services) AND
// per-service overrides via `_prod_k8s | { replicas = 5 }` (which
// preserves cluster/namespace/registry so the override service joins
// the same group).
//
// Compose grouping rule: services sharing a ComposeFile end up in
// one group.
//
// The returned groups are sorted by ProviderID then by the
// target-identifier so test output is deterministic.
func GroupServices(env string, services []RawService) ([]ServiceGroup, error) {
	// Key per group: providerID + target-identifier.
	groups := map[string]*ServiceGroup{}
	keyOrder := []string{}

	for _, s := range services {
		switch {
		case s.K8sCluster != nil:
			key := fmt.Sprintf("k8s-cluster|%s|%s|%s",
				s.K8sCluster.Cluster, s.K8sCluster.Namespace, s.K8sCluster.Registry)
			grp, ok := groups[key]
			if !ok {
				grp = &ServiceGroup{
					Env:        env,
					ProviderID: "k8s-cluster",
					Cluster:    s.K8sCluster.Cluster,
					Namespace:  s.K8sCluster.Namespace,
					Registry:   s.K8sCluster.Registry,
					Domain:     s.K8sCluster.Domain,
				}
				groups[key] = grp
				keyOrder = append(keyOrder, key)
			}
			grp.Services = append(grp.Services, ResolvedService{
				Name:       s.Name,
				K8sCluster: s.K8sCluster.Spec,
			})

		case s.Compose != nil:
			// The project directory is part of the key: the same file run
			// from the primary checkout (a shared stack) and from this one
			// is two different compose projects' worth of mount sources.
			key := fmt.Sprintf("compose|%s|%s", s.Compose.ProjectDirectory, s.Compose.ComposeFile)
			grp, ok := groups[key]
			if !ok {
				grp = &ServiceGroup{
					Env:        env,
					ProviderID: "compose",
				}
				groups[key] = grp
				keyOrder = append(keyOrder, key)
			}
			grp.Services = append(grp.Services, ResolvedService{
				Name:    s.Name,
				Compose: s.Compose,
				Secrets: s.Secrets,
			})

		case s.HostInfra != nil:
			// ONE group for every host-infra instance in the env. Unlike
			// compose (grouped by file) or k8s (grouped by cluster) there is
			// no shared target to key on — each instance is its own server,
			// on its own port, with its own data directory. Grouping them
			// together is what lets the provider attempt all of them and
			// report the failures together.
			const key = "host-infra"
			grp, ok := groups[key]
			if !ok {
				grp = &ServiceGroup{
					Env:        env,
					ProviderID: "host-infra",
				}
				groups[key] = grp
				keyOrder = append(keyOrder, key)
			}
			grp.Services = append(grp.Services, ResolvedService{
				Name:      s.Name,
				HostInfra: s.HostInfra,
			})

		default:
			// Host / BuildOnly / nil — skipped by the deploy dispatch.
		}
	}

	out := make([]ServiceGroup, 0, len(keyOrder))
	// Deterministic order: sort by key for stable output.
	sort.Strings(keyOrder)
	for _, k := range keyOrder {
		out = append(out, *groups[k])
	}
	return out, nil
}

// RawService is the input shape for GroupServices — one entry per
// rendered Service, with the deploy union already dispatched to the
// matching variant. Exactly one of K8sCluster / Compose /
// HostInfra is non-nil for services the dispatcher should ship; all nil
// means "skip" (host / build-only / no deploy declared).
type RawService struct {
	Name string

	// K8sCluster carries both the env-wide fields (used for grouping)
	// and the per-service spec (carried through to the provider).
	K8sCluster *RawK8sCluster

	Compose   *ComposeSpec
	HostInfra *HostInfraSpec

	// Secrets carries resolved secret values to inline into the runtime
	// env for Compose services. Carried verbatim onto the
	// ResolvedService. nil for K8sCluster services (those get rendered
	// Secret objects, not inlined values) and for external/none
	// providers.
	Secrets map[string]string
}

// RawK8sCluster combines the env-wide K8sCluster fields (which key
// the group) with the per-service spec (which the provider consumes).
// Kept separate from the group-level fields so GroupServices can read
// them without unpacking K8sClusterSpec twice.
type RawK8sCluster struct {
	Cluster   string
	Namespace string
	Registry  string
	Domain    string
	Spec      *K8sClusterSpec
}

// FormatGroupSummary returns a one-line description of a group for
// CLI output. Shape:
//
//	[<provider>] <target>: <svc-1>, <svc-2>, ...
func FormatGroupSummary(g ServiceGroup) string {
	names := make([]string, 0, len(g.Services)+len(g.Frontends)+len(g.StaticSites))
	for _, s := range g.Services {
		names = append(names, s.Name)
	}
	for _, f := range g.Frontends {
		names = append(names, f.Name)
	}
	for _, f := range g.StaticSites {
		names = append(names, f.Name)
	}
	target := groupTarget(g)
	return fmt.Sprintf("[%s] %s: %s", g.ProviderID, target, strings.Join(names, ", "))
}

func groupTarget(g ServiceGroup) string {
	switch g.ProviderID {
	case "k8s-cluster":
		return fmt.Sprintf("cluster=%s ns=%s", g.Cluster, g.Namespace)
	case "compose":
		if len(g.Services) > 0 && g.Services[0].Compose != nil {
			if c := g.Services[0].Compose; c.Shared {
				return "file=" + c.ComposeFile + " shared, from " + c.ProjectDirectory
			}
			return "file=" + g.Services[0].Compose.ComposeFile
		}
		return "file=?"
	case "host-infra":
		return "host processes"
	case "firebase":
		if len(g.Frontends) > 0 {
			return "site=" + g.Frontends[0].Spec.resolvedTarget()
		}
		return "site=?"
	case HostedProviderID:
		if g.Hosted != nil {
			return "control-plane=" + g.Hosted.Endpoint
		}
		return "control-plane=?"
	case "static-site":
		if len(g.StaticSites) > 0 {
			return "bucket=" + g.StaticSites[0].Spec.normalizedBucket()
		}
		return "bucket=?"
	default:
		return ""
	}
}
