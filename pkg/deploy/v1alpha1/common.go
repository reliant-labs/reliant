package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Hosted identity labels.
//
// Identity is METADATA, never spec (see the package doc): the control plane
// stamps these on every CR it publishes, and its operators read them. They are
// declared here, next to the types, only so the two sides of the wire spell
// them identically. Render never reads or writes them — a renderer that
// consulted hosted identity would no longer be the same function for both
// destinations.
const (
	LabelOrgID         = "forge.dev/org-id"
	LabelEnvironmentID = "forge.dev/environment-id"
	LabelDeploymentID  = "forge.dev/deployment-id"
)

// EnvVar is one container environment variable.
//
// # SEVEN CHANNELS, AT MOST ONE SET
//
// MERGE DECISION. forge's tier used its generic EnvVar (value / secret_ref /
// config_map_ref / field_ref). control-plane's CRD used value / secretRef /
// a platform-secret name / databaseRef. The merged type has five channels
// every profile may use, and two that only the Full profile may:
//
//   - Value — an inline literal. Both sides had it.
//
//   - SecretRef — one key of a Secret in the workload's own namespace. Both
//     sides had it. Self-hosted, forge's secrets provider renders that Secret.
//     Hosted, the operator additionally refuses names outside the customer's
//     prefix, because the hosted namespace also holds platform-owned secrets.
//     That check is a HOSTED decoration and stays in control-plane.
//
//   - ManagedSecret — a value stored in the environment's secret STORE, named
//     by a bare logical name. This is control-plane's platform-secret channel, kept on
//     merit and renamed. It is the highest-level way to say "my API key". The
//     author names the secret, never a Kubernetes object, so there is no
//     spelling of the field that reaches another customer's value: scope comes
//     from where the workload runs, never from the string. The concept is
//     not hosted-only. Self-hosted resolves the name through forge's secrets
//     provider for the env, and hosted resolves it through the managed store
//     (OpenBao). Either way the value is materialized into
//     ManagedSecretsSecretName before the pod starts. It is renamed because
//     the old name was the hosted platform's vocabulary, which means
//     nothing to a self-hosted user, while every environment has a secret store.
//
//     ManagedSecret.Optional means the materializer SKIPS the name when the
//     store has no value for it, and the pod starts without the variable.
//     That is the projection of an OPTIONAL sensitive config field (a tier's
//     off-switch: "unset means the feature is not registered"), which would
//     otherwise be a hard deploy requirement in every environment that left
//     the feature off. A required name the store lacks fails the
//     materialization, and nothing starts; that is the default because an
//     app reading an unset credential as "" often falls back to an
//     unauthenticated path. The renderer marks the secretKeyRef optional as
//     well, so the pod also starts when the key is absent from the Secret.
//
//   - DatabaseRef — the credential of a ManagedDatabase in the same
//     namespace. control-plane's design, kept on merit. It removes the most
//     common hand-wiring task in a three-tier app, and it ran live (a
//     backend reached a real CNPG Postgres through it). It NAMES A DATABASE,
//     NEVER A SECRET, so the set of things it can resolve to is closed by
//     construction.
//
//   - WorkloadURL — the public URL of a sibling workload (a backend's
//     CORS_ORIGINS naming its StaticSite). Added with StaticSite runtime
//     config, which uses the same reference kind. Like DatabaseRef it NAMES
//     A WORKLOAD, never a hostname, so it cannot point outside the env.
//
//   - ConfigMapRef — one key of a ConfigMap in the workload's namespace.
//     FULL PROFILE ONLY.
//
//   - FieldRef — a Downward-API field path (metadata.name, status.podIP).
//     FULL PROFILE ONLY.
//
// Why the last two are Full-only: a Workload is one type for every runtime
// (ADR 0002), so a cluster the author operates must be able to say
// everything forge.K8sCluster could. A shared hosted namespace is different.
// ConfigMapRef reads an arbitrary namespace object the hosted user did not write,
// and FieldRef exposes pod and node placement the platform owns. So
// Validate(ProfileRestricted) refuses both, by name, through FieldProfiles.
// That is an explicit allowlist with a default-deny test behind it, not a
// denylist that has to chase new fields: an unclassified field fails the
// build.
//
// SecretRef is Full-only by the same table. A raw Secret name in a shared
// namespace can address a platform-owned Secret, and ManagedSecret is the
// hosted spelling of "my credential".
//
// Two channels set is REFUSED by Validate rather than resolved by
// precedence. A spec that says two contradictory things has no correct
// reading, and guessing one hides the mistake. Kubernetes also rejects an
// env entry carrying both value and valueFrom, but only server-side, so a
// client dry-run passes and the deploy dies against the real cluster.
// Nothing set is legal and means an empty-valued variable.
type EnvVar struct {
	// Name is the variable name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[A-Za-z_][A-Za-z0-9_]*$`
	Name string `json:"name"`

	// Value is an inline literal.
	// +optional
	// +kubebuilder:validation:MaxLength=32768
	Value string `json:"value,omitempty"`

	// SecretRef projects one key of a Secret in the workload's namespace.
	// +optional
	SecretRef *SecretKeyRef `json:"secretRef,omitempty"`

	// ManagedSecret names a value in the environment's secret store, and
	// whether its absence is tolerated.
	// +optional
	ManagedSecret *ManagedSecretRef `json:"managedSecret,omitempty"`

	// DatabaseRef projects a ManagedDatabase's credential.
	// +optional
	DatabaseRef *DatabaseRef `json:"databaseRef,omitempty"`

	// WorkloadURL is the public URL of another workload in the SAME
	// environment — the canonical case is a backend's CORS_ORIGINS naming
	// the StaticSite that calls it. It names a workload, never a hostname,
	// so it follows the workload when its URL changes.
	//
	// It is a REFERENCE that must be resolved before a pod can carry it:
	// the control plane resolves it on a hosted env (and re-resolves when
	// the target's URL moves), and pkg/deploy.Render refuses a spec that
	// still holds one rather than render an empty variable.
	// +optional
	WorkloadURL *WorkloadURLRef `json:"workloadURL,omitempty"`

	// ConfigMapRef projects one key of a ConfigMap in the workload's
	// namespace. Full profile only.
	// +optional
	ConfigMapRef *ConfigMapKeyRef `json:"configMapRef,omitempty"`

	// FieldRef projects a pod field through the Downward API
	// (valueFrom.fieldRef.fieldPath). Full profile only.
	// +optional
	FieldRef *FieldRef `json:"fieldRef,omitempty"`
}

// ConfigMapKeyRef names one key of a ConfigMap in the workload's own
// namespace.
type ConfigMapKeyRef struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Key string `json:"key"`
}

// FieldRef selects a pod field for the Downward API. Only the path is
// modelled. apiVersion is always v1 for the fields a container env can read,
// so a knob for it would be a field that is either redundant or wrong.
type FieldRef struct {
	// FieldPath is the pod field, e.g. metadata.name or status.podIP.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	FieldPath string `json:"fieldPath"`
}

// WorkloadURLRef names a workload, in the same environment, whose public URL
// is the value. Resolution is scoped by construction: the name carries no
// org, env or namespace component, so a reference can only ever reach a
// sibling workload.
//
// What the URL IS depends on the target's kind: a StaticSite's URL is its
// site origin plus basePath, and a Workload's is the origin of the hostname
// its exposed port is served at. A reference to a workload with no public URL
// (nothing exposed, a database) is unresolvable and is refused, never
// guessed.
type WorkloadURLRef struct {
	// Name is the target workload's name (its metadata.name).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`
}

// RuntimeConfigValue is one entry of a StaticSite's runtime config: EXACTLY
// ONE of a literal value or a workload URL reference.
//
// Value is a pointer because an explicitly empty string is a legal value
// (the conventional "feature off" sentinel) and must not read as "unset".
// Every value is a STRING on the wire. A bundle that wants a number or a
// boolean coerces it (forge's generated config module does), so the document
// has one type rule no matter which side resolved it.
//
// +kubebuilder:validation:ExactlyOneOf=value;workloadURL
type RuntimeConfigValue struct {
	// Value is an inline literal.
	// +optional
	// +kubebuilder:validation:MaxLength=32768
	Value *string `json:"value,omitempty"`

	// WorkloadURL resolves to another workload's public URL.
	// +optional
	WorkloadURL *WorkloadURLRef `json:"workloadURL,omitempty"`
}

// ManagedSecretRef names one value in the environment's secret store.
type ManagedSecretRef struct {
	// Name is the secret's logical name. A BARE LOGICAL NAME: env-var shaped,
	// with no separator to traverse, carrying no customer or environment
	// component, so no spelling of it reaches another customer's value.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[A-Za-z_][A-Za-z0-9_]*$`
	Name string `json:"name"`

	// Optional tolerates the store having no value: the materializer skips
	// the name, and the pod starts without the variable. Default false: a
	// missing value fails the materialization and nothing starts.
	// +optional
	Optional bool `json:"optional,omitempty"`
}

// ManagedSecretsSecretName is the Secret every ManagedSecret env var reads
// from, keyed by the secret's logical name. Whoever resolves the
// environment's secret store — forge's secrets provider when self-hosted, the
// control plane's materializer when hosted — writes this Secret into the
// workload's namespace before the workload starts.
//
// ONE name, fixed here. Render emits the secretKeyRef and the materializer
// writes the Secret, and both must agree on the name without being told it.
const ManagedSecretsSecretName = "forge-managed-secrets"

// SecretKeyRef names one key of a Secret in the workload's own namespace.
type SecretKeyRef struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Key string `json:"key"`
	// Optional lets the pod start when the Secret or the key does not exist;
	// the variable is then simply unset. It is what the config projection
	// emits for an OPTIONAL sensitive field, whose absence the app already
	// handles. A required reference to a missing key holds the pod in
	// CreateContainerConfigError, with no application log, which is the
	// right outcome only when the value really is required.
	// +optional
	Optional bool `json:"optional,omitempty"`
}

// DatabaseCredentialKey selects one part of a ManagedDatabase credential.
//
// "uri" IS THE ONE A CONSUMER SHOULD USE, and it is the default. Reassembling
// a URI from its parts inside a container's env
// (postgres://$(USERNAME):$(PASSWORD)@...) does not work: Kubernetes $(VAR)
// substitution resolves only names declared EARLIER in the same env list, so
// the pod receives a literal "$(PASSWORD)" and the database reports an
// authentication failure that names the wrong cause.
//
// The values are exactly the keys CloudNativePG publishes in a Cluster's
// "<cluster>-app" Secret.
//
// +kubebuilder:validation:Enum=uri;host;port;dbname;username;password
type DatabaseCredentialKey string

const (
	DatabaseKeyURI      DatabaseCredentialKey = "uri"
	DatabaseKeyHost     DatabaseCredentialKey = "host"
	DatabaseKeyPort     DatabaseCredentialKey = "port"
	DatabaseKeyDBName   DatabaseCredentialKey = "dbname"
	DatabaseKeyUsername DatabaseCredentialKey = "username"
	DatabaseKeyPassword DatabaseCredentialKey = "password"
)

// DatabaseRef names a ManagedDatabase, in the workload's own namespace, whose
// credential to project.
type DatabaseRef struct {
	// Name is the ManagedDatabase's name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=40
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`
	// Key is the credential part. Empty means uri.
	// +optional
	Key DatabaseCredentialKey `json:"key,omitempty"`
}

// EffectiveKey resolves an unset Key to uri.
func (r DatabaseRef) EffectiveKey() DatabaseCredentialKey {
	if r.Key == "" {
		return DatabaseKeyURI
	}
	return r.Key
}

// Resources is a workload's compute request and limit, in NEUTRAL units: CPU
// in millicores, memory in BYTES.
//
// MERGE DECISION: the units and the shape were already shared (forge's KCL
// and control-plane's CRD agreed on four independent ints, millicores and
// bytes), so both sides win. Bytes, not MiB. MiB appeared only in
// control-plane's deployrender, as a third unit system inside one product.
// Bytes are Kubernetes' own base unit and the only memory unit with no
// dialect (Mi is binary, M is decimal). Kubernetes quantity strings ("2Gi")
// are a rendering concern and never appear in a spec. They are a metering
// input, and re-parsing a dialect is where a billing number quietly picks
// up a rounding rule nobody chose.
//
// The 4 GiB-per-vCPU shape band is NOT a rule of this type. It is a BILLING
// rule and applies to hosted environments only (owner decision). It lives in
// deploy.CheckShapeBand, which the hosted path calls, and it is not imposed
// on every self-hosted user through the schema. Validate enforces only what
// is true everywhere: requests are positive and limits are at least the
// request.
//
// The defaults are forge's 250m / 1 GiB entry shape for an unset REQUEST,
// and the (defaulted) request for an unset LIMIT. The shape is conformant
// with the band, so an app that omits resources deploys to either
// destination unchanged, and "set a request, leave the limit" deploys with
// limits equal to the requests.
//
// The LIMITS deliberately carry no +kubebuilder:default marker. "Equal to
// the request" is not a static value. A static 250m default would be stamped
// by the API server under a 500m request, and Validate would then refuse the
// CR the operator was handed. The generated KCL has no default for the
// limits either, for the same reason. So the absence travels through the CR
// and the KCL record, and WithDefaults resolves it wherever the spec is
// consumed.
type Resources struct {
	// +optional
	// +kubebuilder:default=250
	// +kubebuilder:validation:Minimum=1
	CPURequestMillicores int64 `json:"cpuRequestMillicores,omitempty"`
	// Unset means equal to the CPU request.
	// +optional
	// +kubebuilder:validation:Minimum=1
	CPULimitMillicores int64 `json:"cpuLimitMillicores,omitempty"`
	// +optional
	// +kubebuilder:default=1073741824
	// +kubebuilder:validation:Minimum=1
	MemoryRequestBytes int64 `json:"memoryRequestBytes,omitempty"`
	// Unset means equal to the memory request.
	// +optional
	// +kubebuilder:validation:Minimum=1
	MemoryLimitBytes int64 `json:"memoryLimitBytes,omitempty"`
}

// Default requests — forge's entry shape. Mirrored by the kubebuilder
// default markers on the REQUEST fields above, and
// TestDefaultMarkersMatchGoDefaults keeps the two equal (and keeps the limits
// marker-free).
const (
	DefaultCPUMillicores int64 = 250
	DefaultMemoryBytes   int64 = 1 << 30
)

// WithDefaults fills every unset value. Requests first, to the entry shape.
// Then each unset limit is set to its RESOLVED request, so a request-only
// spec gets limit == request rather than a static limit below it. A limit
// that IS set is left alone, and Validate refuses it if it is below its
// request.
func (r Resources) WithDefaults() Resources {
	if r.CPURequestMillicores == 0 {
		r.CPURequestMillicores = DefaultCPUMillicores
	}
	if r.MemoryRequestBytes == 0 {
		r.MemoryRequestBytes = DefaultMemoryBytes
	}
	if r.CPULimitMillicores == 0 {
		r.CPULimitMillicores = r.CPURequestMillicores
	}
	if r.MemoryLimitBytes == 0 {
		r.MemoryLimitBytes = r.MemoryRequestBytes
	}
	return r
}

// Phase is a tier's observed lifecycle phase.
//
// ONE VOCABULARY FOR ALL THREE TIERS. control-plane had three:
// SimpleBackend used Pending/Ready/Degraded/Failed, StaticSite used
// Pending/Syncing/Ready/Failed, and ManagedDatabase used
// Pending/Provisioning/Ready/Locked/Failed. Syncing and Provisioning are
// the same fact, "work is in flight and nothing is wrong yet", so they
// fold into Progressing. That is also the word the deployments ledger's
// observed_state column already uses. One vocabulary is what lets
// deploy.ObservedStateOf map a phase without first knowing which kind
// reported it.
//
//   - Pending     — declared, not yet acted on. Nothing serves.
//   - Progressing — applied, converging: a rollout, a sync, a Cluster
//     provisioning.
//   - Ready       — serving what the spec declares.
//   - Degraded    — exists but is not serving: a crash loop, a failing
//     probe. The platform has done its part and the fault is inside the
//     workload.
//   - Failed      — the platform REFUSED or could not converge: a rejected
//     image, a cross-customer reference. Nothing was applied.
//   - Locked      — ManagedDatabase only: a retained database whose CR was
//     deleted. Every byte still exists, but nothing names it any more.
//
// +kubebuilder:validation:Enum=Pending;Progressing;Ready;Degraded;Failed;Locked
type Phase string

const (
	PhasePending     Phase = "Pending"
	PhaseProgressing Phase = "Progressing"
	PhaseReady       Phase = "Ready"
	PhaseDegraded    Phase = "Degraded"
	PhaseFailed      Phase = "Failed"
	PhaseLocked      Phase = "Locked"
)

// TierStatus is the observed state shared by the kinds that serve traffic
// (Workload, StaticSite).
//
// It was called WorkloadStatus until Workload became a kind of its own. The
// shared part is now TierStatus, so that WorkloadStatus can be the Workload
// kind's full status, following the <Kind>Status convention every other
// kind in this package uses.
//
// MERGE DECISION: control-plane's shape wins outright, since forge had no
// status at all. A self-hosted user could not ask "what is running in prod
// and is it up" without kubectl. Live, control-plane's status answered
// exactly the questions a user asks ("ImageRejected: … not from an allowed
// registry", "no release promoted to this site yet"). ONE shape: the
// operator writes it to the CR on hosted, and `forge env status` computes
// it on read when self-hosted.
//
// Every field is OBSERVED, never desired. A value appears only after the
// thing it names is already true.
type TierStatus struct {
	// +optional
	Phase Phase `json:"phase,omitempty"`

	// ObservedGeneration is the metadata.generation this status describes. A
	// status whose ObservedGeneration lags the object's generation describes
	// a PREVIOUS spec, and a reader must not treat it as current.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Hostname is the hostname serving this workload, and URL is its https://
	// form. On hosted this is the ALLOCATED platform hostname, recorded only
	// after the allocator committed it. Custom domains from spec.domains are
	// served in addition to it.
	// +optional
	Hostname string `json:"hostname,omitempty"`
	// +optional
	URL string `json:"url,omitempty"`

	// Message is the human-readable reason for the current phase.
	// +optional
	Message string `json:"message,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
