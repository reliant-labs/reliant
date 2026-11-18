package fluxdelivery

import (
	"encoding/json"
	"fmt"

	"github.com/reliant-labs/forge/pkg/release"
)

// Schema is the version of the input shape. The template's first action is
// to refuse (sprig `fail`) any other value, so an input written by a newer
// writer stalls the ResourceSet instead of misrendering. Any change to the
// input's shape bumps it — and with it the template.
const Schema = "forge.dev/flux-delivery/v1"

// FenceAnnotation is stamped on the OCIRepository with [BindingInput.Fence]:
// the annotation the hub's converger already writes there (control-plane's
// deployconverge.GenerationAnnotation), which its observer and fence reader
// read back. Kept under the same key so adopting a live object is a no-op.
const FenceAnnotation = "deploy.reliant.dev/generation"

// BindingInput is ONE ResourceSet input: everything one ledger binding — one
// promotion-row stream — decides. Today a binding is one environment
// (Binding == "").
//
// Every field is computed by the ledger's writer; the template derives
// nothing but the safety stamps (see the package doc). Field names are fixed
// by the JSON tags, and [BindingInput.Map] is the only way to produce the map
// a ResourceSet carries.
type BindingInput struct {
	// Schema is [Schema].
	Schema string `json:"schema"`
	// Org is the owning organization. RFC-1123 label.
	Org string `json:"org"`
	// Env is the environment id. RFC-1123 label. (Not `id`: flux-operator
	// reserves inputs.id and inputs.provider.)
	Env string `json:"env"`
	// Binding is the ledger binding within the environment. "" — the whole
	// environment — is the only value [Validate] accepts until the ledger has
	// per-binding promotion; the key exists so nothing re-shapes later.
	Binding string `json:"binding"`
	// Namespace is where the ResourceSet and everything it renders live
	// (`flux-<org>` on the hub, `flux-system` self-hosted).
	Namespace string `json:"namespace"`
	// Fence is the writer's lease fence at the last write that moved
	// something, as decimal digits (inputs are JSON). Stamped on the
	// OCIRepository as [FenceAnnotation].
	Fence string `json:"fence"`
	// Promotion is the ledger row this input projects.
	Promotion string `json:"promotion"`
	// Retire is "", [RetireOrphan] or [RetireTeardown], and only ever from a
	// ledger-recorded intent. Binding-wide; an applier may also be retired
	// alone ([Applier.Retire]).
	Retire string `json:"retire"`
	// Labels are stamped on EVERY rendered object. Computed by the writer
	// (the hub: managed-by, org, environment; a binding label only for a
	// non-empty binding); the template adds only the shard label.
	Labels map[string]string `json:"labels"`
	// Source is the binding's ONE OCIRepository.
	Source Source `json:"source"`
	// Appliers are one Kustomization each: a layout-v2 tree, or a flat
	// (layout v1) cluster.
	Appliers []Applier `json:"appliers"`
}

// Source is the binding's OCIRepository.
type Source struct {
	// Name is [SourceName](org, env, binding).
	Name string `json:"name"`
	// URL is the bundle repository, `oci://` scheme included, without a tag:
	// the digest pins it.
	URL string `json:"url"`
	// Digest is the bundle's OCI MANIFEST digest ("sha256:" + 64 hex), the
	// value Flux reports as the revision.
	Digest string `json:"digest"`
	// Interval is the reconcile interval of the source and of every applier,
	// as a canonical Go duration ("5m0s"): the form the API server stores, so
	// a render equals the live object byte for byte.
	Interval string `json:"interval"`
	// Insecure allows the pull over plain HTTP. The digest still pins the
	// content; this weakens transport, not integrity.
	Insecure bool `json:"insecure"`
	// Provider is Flux's pull auth provider ("" for Flux's generic default).
	Provider string `json:"provider"`
	// PullSecret names the registry pull Secret in Namespace, written by the
	// writer (or the self-hosted bootstrap), never by the template. "" for
	// an anonymous pull.
	PullSecret string `json:"pullSecret"`
	// Verify is the signature verification the source enforces.
	Verify Verify `json:"verify"`
}

// The verification modes ([Verify.Mode]): the effective delivery policy's
// `flux_verify`. Only enforce renders `spec.verify`; audit is the writer's
// business (it records the publisher's verdict) and renders nothing.
const (
	VerifyOff     = "off"
	VerifyAudit   = "audit"
	VerifyEnforce = "enforce"
)

// Verify is the source's signature verification.
type Verify struct {
	// Mode is [VerifyOff], [VerifyAudit] or [VerifyEnforce].
	Mode string `json:"mode"`
	// Provider is Flux's verification provider, "cosign" or "notation".
	// Required in enforce mode.
	Provider string `json:"provider"`
	// SecretRef names the Secret of trusted public keys in Namespace, written
	// by the writer before an enforcing input. Key-based verification;
	// exclusive with OIDC.
	SecretRef string `json:"secretRef"`
	// OIDC are the keyless identities a signature may carry; exclusive with
	// SecretRef.
	OIDC []OIDCIdentity `json:"oidc"`
}

// OIDCIdentity is one keyless signer identity, as Flux's matchOIDCIdentity.
type OIDCIdentity struct {
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
}

// Applier is one Kustomization.
type Applier struct {
	// Name is [TreeKustomizationName] for a tree, [KustomizationName] for a
	// flat cluster.
	Name string `json:"name"`
	// Source is the OCIRepository the applier reads, and must equal
	// Source.Name: one input, one source.
	Source string `json:"source"`
	// Cluster, Unit and Phase are the applier's coordinates in the bundle:
	// the rendered cluster name as the bundle's paths index it (a kube
	// context, not necessarily a DNS label), the delivery unit and the phase
	// of a tree. Unit and Phase are "" for a flat cluster. [Validate] derives
	// Name and Path from them; the template reads neither.
	Cluster string        `json:"cluster"`
	Unit    string        `json:"unit"`
	Phase   release.Phase `json:"phase"`
	// Path is release.BundleTreePath (tree) or release.BundleClusterPath
	// (flat) of the coordinates — CHECKED, because a Kustomization at a path
	// the artifact does not hold reconciles as "no resources" and reports
	// Ready.
	Path string `json:"path"`
	// Retire retires this applier alone: "" or [RetireOrphan]. The first of
	// the two writes that drop an applier.
	Retire string `json:"retire"`
	// Labels are stamped on this Kustomization beside BindingInput.Labels
	// (the hub: cluster, delivery unit, phase — [LabelValue] of each).
	Labels map[string]string `json:"labels"`
	// KubeConfigSecret names the destination kubeconfig Secret in Namespace
	// (key [KubeConfigKey]). Exactly one of KubeConfigSecret and InCluster.
	KubeConfigSecret string `json:"kubeConfigSecret"`
	// InCluster applies to the namespace's own cluster with the controller's
	// in-cluster identity, impersonating ServiceAccountName. Explicit rather
	// than inferred from an empty KubeConfigSecret: an absent kubeConfig is
	// a destination breach when it happens by omission.
	InCluster bool `json:"inCluster"`
	// ServiceAccountName is the identity an in-cluster apply impersonates.
	// Required with InCluster, refused beside a kubeconfig (the connected
	// shard applies as the kubeconfig's identity and nothing else). Rendered
	// even when empty, as the hub's objects already carry it.
	ServiceAccountName string `json:"serviceAccountName"`
	// TargetNamespace forces every applied object into one namespace (a
	// hosted destination); "" leaves the bundle's own namespaces.
	TargetNamespace string `json:"targetNamespace"`
	// DependsOn are applier names IN THIS INPUT that must be Ready first.
	DependsOn []string `json:"dependsOn"`
	// ExternalDependsOn are Kustomization names OUTSIDE this binding, in the
	// same namespace. Not resolved here; empty today.
	ExternalDependsOn []string `json:"externalDependsOn"`
	// Health is what the applier is judged on (instead of Wait).
	Health []HealthCheck `json:"health"`
	// HealthExprs override kstatus for kinds whose Ready alone does not prove
	// the current spec reconciled.
	HealthExprs []HealthCheckExpr `json:"healthExprs"`
	// Wait judges every applied object; a tree waits on Health instead.
	Wait bool `json:"wait"`
	// Timeout and RetryInterval are canonical Go durations ("1m30s");
	// RetryInterval "" leaves Flux's default.
	Timeout       string `json:"timeout"`
	RetryInterval string `json:"retryInterval"`
	// Suspended carries a live suspend into the input (adoption, or a tree
	// added suspended).
	Suspended bool `json:"suspended"`
	// CommonLabels are stamped on every object the applier applies (a hosted
	// destination's identity).
	CommonLabels map[string]string `json:"commonLabels"`
	// Patches are kustomize patches on the applied objects (a hosted
	// destination's deployment ids), in a stable order.
	Patches []Patch `json:"patches"`
}

// HealthCheck is one entry of Kustomization.spec.healthChecks.
type HealthCheck struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace,omitempty"`
}

// HealthCheckExpr is one entry of Kustomization.spec.healthCheckExprs.
type HealthCheckExpr struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	// InProgress and Failed are evaluated before Current, in that order
	// (Flux RFC 0009). omitempty keeps an expression that sets only Current
	// rendering exactly as before.
	InProgress string `json:"inProgress,omitempty"`
	Failed     string `json:"failed,omitempty"`
	Current    string `json:"current"`
}

// Patch is one entry of Kustomization.spec.patches.
type Patch struct {
	Patch  string      `json:"patch"`
	Target PatchTarget `json:"target"`
}

// PatchTarget selects the object a [Patch] applies to.
type PatchTarget struct {
	Group   string `json:"group,omitempty"`
	Version string `json:"version,omitempty"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
}

// Map is the input as a ResourceSet carries it: `spec.inputs[i]`, or a
// ResourceSetInputProvider's `defaultValues`.
//
// Every key is present, with empty maps and lists rather than null: the
// template runs with missingkey=error, so an absent key is a stalled render,
// and a writer comparing the live input with the next one must not see a
// difference between null and empty.
func (in BindingInput) Map() (map[string]any, error) {
	in = in.normalized()
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("fluxdelivery: encoding the input: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("fluxdelivery: decoding the input: %w", err)
	}
	return out, nil
}

// normalized replaces every nil map and slice with an empty one.
func (in BindingInput) normalized() BindingInput {
	in.Labels = emptyIfNil(in.Labels)
	if in.Source.Verify.OIDC == nil {
		in.Source.Verify.OIDC = []OIDCIdentity{}
	}
	appliers := make([]Applier, len(in.Appliers))
	for i, a := range in.Appliers {
		a.Labels = emptyIfNil(a.Labels)
		a.CommonLabels = emptyIfNil(a.CommonLabels)
		if a.DependsOn == nil {
			a.DependsOn = []string{}
		}
		if a.ExternalDependsOn == nil {
			a.ExternalDependsOn = []string{}
		}
		if a.Health == nil {
			a.Health = []HealthCheck{}
		}
		if a.HealthExprs == nil {
			a.HealthExprs = []HealthCheckExpr{}
		}
		if a.Patches == nil {
			a.Patches = []Patch{}
		}
		appliers[i] = a
	}
	in.Appliers = appliers
	return in
}

func emptyIfNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// HealthAPIVersion is the apiVersion of a kind an applier may be judged on,
// and whether the kind is known. The set the hub's trees use; a kind outside
// it has no apiVersion to name, and an unknown health check never reports.
func HealthAPIVersion(kind string) (string, bool) {
	switch kind {
	case "Deployment", "StatefulSet", "DaemonSet":
		return "apps/v1", true
	case "Job":
		return "batch/v1", true
	case "ExternalSecret":
		return "external-secrets.io/v1", true
	case "Workload", "ManagedDatabase", "StaticSite":
		return forgeTierAPIVersion, true
	}
	return "", false
}

// forgeTierAPIVersion is the forge.dev tier kinds' group/version
// (pkg/deploy/v1alpha1), spelled here so this package imports no CRD types.
const forgeTierAPIVersion = "forge.dev/v1alpha1"

// A STOPPED TIER IS SETTLED, NOT STILL ROLLING OUT.
//
// A forge.dev tier whose compute was stopped on purpose (its owner suspended
// it, or a billing lapse did) carries Suspended=True and Ready=False. kstatus
// reads a custom resource's Ready=False as InProgress, so an applier waiting
// on a suspended tier timed out its health check and its tree read failed
// (control-plane prod's egresscanary env, 2026-11-01: FluxTreeNotReady for an
// environment exactly as its owner left it).
//
// These judge the tier kinds as kstatus would, plus one arm: Suspended=True
// at the current generation is Current. Reconciling=True is InProgress,
// Stalled=True is Failed (a controller's fail-fast), and Ready=True is Current.
// Current also requires status.observedGeneration to equal metadata.generation
// itself: Flux holds a stale observedGeneration InProgress before any
// expression runs, but the expression does not lean on that, so a Ready=True
// left over from a previous spec never reads settled for a newer one.
const (
	ForgeTierInProgress = `has(status.conditions) &&
status.conditions.exists(c, c.type == 'Reconciling' && c.status == 'True')`

	ForgeTierFailed = `has(status.conditions) &&
status.conditions.exists(c, c.type == 'Stalled' && c.status == 'True')`

	ForgeTierCurrent = `has(status.conditions) &&
has(status.observedGeneration) && status.observedGeneration == metadata.generation && (
status.conditions.exists(c, c.type == 'Ready' && c.status == 'True') ||
status.conditions.exists(c, c.type == 'Suspended' && c.status == 'True' &&
  (!has(c.observedGeneration) || c.observedGeneration == metadata.generation)))`
)

// ForgeTierHealthExprs is one expression per forge.dev tier kind, for an
// applier that waits on every object it applies (Wait).
func ForgeTierHealthExprs() []HealthCheckExpr {
	kinds := []string{"Workload", "ManagedDatabase", "StaticSite"}
	out := make([]HealthCheckExpr, 0, len(kinds))
	for _, kind := range kinds {
		out = append(out, HealthCheckExpr{
			APIVersion: forgeTierAPIVersion, Kind: kind,
			InProgress: ForgeTierInProgress, Failed: ForgeTierFailed, Current: ForgeTierCurrent,
		})
	}
	return out
}

// ExternalSecretCurrent is the health expression for an ExternalSecret
// (moved from control-plane's fluxtenant). ESO leaves Ready=True from the
// preceding sync while reconciling a pin bump; its syncedResourceVersion is
// "<generation>-<metadata hash>" and advances only after the target Secret is
// written. The delimiter is part of the match (generation 1 must not accept
// generation 10). A missing status field is not current, and an absent
// variable is a CEL error, which Flux also treats as not current.
const ExternalSecretCurrent = `has(metadata.generation) &&
metadata.generation > 0 &&
has(status.conditions) &&
has(status.syncedResourceVersion) &&
status.conditions.exists(c, has(c.type) && has(c.status) && c.type == 'Ready' && c.status == 'True') &&
status.syncedResourceVersion.startsWith(string(metadata.generation) + '-')`
