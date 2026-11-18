package release

// The SECRET STAGE classifier: where an object sits in the order a store-backed
// secret needs. It is finer than [Phase] and is meaningful to an applier that
// sequences one cluster's stream.
//
//	CRDs -> ESO -> OpenBao -> stores -> ExternalSecrets -> workloads
//
// ESO and its CRDs arrive before the stream (the chart path applies them and
// waits for Established). What this classifies is the rest, in apply order:
//
//   - [SecretStageBootstrap]: everything the store component renders that is
//     neither a workload nor a Job: its ServiceAccounts, RBAC, Services,
//     NetworkPolicy, certificate and its public-endpoint route objects, plus its
//     OWN SecretStore and ExternalSecrets (the seal key and database password it
//     needs before it can start). The ExternalSecrets are awaited before the
//     store rolls; the rest is inert until the store runs;
//   - [SecretStageStorePre]: the component's pre-rollout Jobs (OpenBao's
//     `widen`, which grants a name a release adds before anything reads it);
//   - [SecretStageStore]: the component's workloads and post-rollout Jobs
//     (OpenBao's StatefulSet and its `converge` Job). A SecretStore cannot log
//     in until the store is up AND converged, so these are awaited before the
//     next stage;
//   - [SecretStageSecrets]: every other SecretStore and ExternalSecret,
//     applied once the store answers and awaited until synced, so a consumer
//     never starts, or rolls, ahead of the value it reads.
//
// Everything else is [SecretStageNone] and keeps the [Phase] rules.
//
// WHY THIS IS NOT A FOURTH [Phase]. A bundle's trees are named by phase
// (manifests/<cluster>/<unit>/<phase>) and a Flux Kustomization is built from
// each, so a new phase changes the layout the control plane reads and the
// digest of every bundle that carries such an object. The trees keep their
// three phases: awaited ExternalSecrets hold their pre tree's health until
// ESO has synced the current generation, so downstream rollout trees wait.
// Only the direct applier (internal/cluster) sequences WITHIN a phase by stage.
// Flux applies a whole tree before checking health: a Job or store bootstrap
// that must wait before it is applied needs an upstream delivery unit, not a
// same-tree health check. The classifier remains here beside [ClassifyPhase];
// it is not a second rollout framework.

import "strings"

// SecretStage places an object in the secret-store ordering.
type SecretStage string

const (
	// SecretStageNone is every object this ordering does not touch.
	SecretStageNone SecretStage = ""
	// SecretStageBootstrap is a store component object that is neither a
	// workload nor a Job: its support objects and its own SecretStore and
	// ExternalSecrets.
	SecretStageBootstrap SecretStage = "bootstrap"
	// SecretStageStorePre is a store component Job that must complete before
	// the component's workloads roll.
	SecretStageStorePre SecretStage = "store-pre"
	// SecretStageStore is the store component's workloads and post-rollout
	// Jobs.
	SecretStageStore SecretStage = "store"
	// SecretStageSecrets is any other SecretStore or ExternalSecret.
	SecretStageSecrets SecretStage = "secrets"
)

// SecretStages is the closed set, in apply order.
var SecretStages = []SecretStage{SecretStageBootstrap, SecretStageStorePre, SecretStageStore, SecretStageSecrets}

// LabelComponent names the platform component an object belongs to.
const LabelComponent = "forge.dev/component"

// LabelSecretStage places an object in the secret ordering by declaration. KCL
// stamps it on the login identity, SecretStores and ExternalSecrets of a
// store-backed source ([SecretStageSecrets] is the only value in use).
const LabelSecretStage = "forge.dev/secret-stage"

// ComponentOpenBao is the [LabelComponent] value of every object the OpenBao
// component renders.
const ComponentOpenBao = "openbao"

// ESOGroup is the API group of ExternalSecret and SecretStore.
const ESOGroup = "external-secrets.io"

// Annotations forge stamps for the ordering.
const (
	// AnnotationSecretPins is set on a consumer's pod template: the store
	// versions its ExternalSecrets are pinned to, one "<secret>/<NAME>=<version>"
	// per pin, sorted and comma-joined. A pin bump changes it, which changes
	// the pod template, which rolls the consumer; and because the applier
	// holds workloads until the ExternalSecret has synced the new version,
	// the roll happens only after the Secret holds it.
	AnnotationSecretPins = "forge.dev/secret-pins"

	// AnnotationSecretSync on an ExternalSecret says whether a deploy waits for
	// it. Absent means await it: the Secret is something a workload reads, so
	// no workload is applied until it has synced.
	AnnotationSecretSync = "forge.dev/secret-sync"
	// SecretSyncDeferred means the value is written AFTER the deploy (the
	// control plane renews a token in place), so a first deploy would wait for
	// a value that cannot exist yet.
	SecretSyncDeferred = "deferred"
	// SecretSyncShadow means the ExternalSecret syncs to a shadow Secret no
	// workload reads (a migration's comparison copy), so it gates nothing.
	SecretSyncShadow = "shadow"
)

// SecretSyncAwaited reports whether a deploy waits for an ExternalSecret with
// these annotations.
func SecretSyncAwaited(annotations map[string]string) bool {
	switch annotations[AnnotationSecretSync] {
	case SecretSyncDeferred, SecretSyncShadow:
		return false
	}
	return true
}

// IsESOKind reports whether apiVersion/kind is an External Secrets Operator
// SecretStore or ExternalSecret.
func IsESOKind(apiVersion, kind string) bool {
	group, _, _ := strings.Cut(apiVersion, "/")
	return group == ESOGroup && (kind == "SecretStore" || kind == "ExternalSecret")
}

// ClassifySecretStage is the secret stage of one object. An unknown
// [AnnotationDeployPhase] on a store component Job is the same error
// [ClassifyPhase] returns.
//
// An object carrying [LabelSecretStage] is in that stage by declaration: the
// env's SecretStores log in as a service account through a token Role, none of
// which is an ESO kind, and all of which must be in the cluster before the
// store is judged. A hand-written SecretStore or ExternalSecret carries no
// label and is placed by its kind.
func ClassifySecretStage(apiVersion, kind, name string, labels, annotations map[string]string) (SecretStage, error) {
	if labels[LabelSecretStage] == string(SecretStageSecrets) {
		return SecretStageSecrets, nil
	}
	if labels[LabelComponent] != ComponentOpenBao {
		if IsESOKind(apiVersion, kind) {
			return SecretStageSecrets, nil
		}
		return SecretStageNone, nil
	}
	phase, err := ClassifyPhase(kind, name, annotations)
	if err != nil {
		return SecretStageNone, err
	}
	switch {
	case kind == "Job" && phase == PhasePre:
		return SecretStageStorePre, nil
	case kind == "Job", RolloutWorkloadKind(kind):
		return SecretStageStore, nil
	}
	return SecretStageBootstrap, nil
}
