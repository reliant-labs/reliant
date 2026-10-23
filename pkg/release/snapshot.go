package release

// SnapshotSchema versions the complete desired-environment contract. Historical
// bundles without a snapshot remain readable, but are not composition bases.
const SnapshotSchema = "forge.dev/snapshot/v1"

// ArtifactID addresses artifact bytes, not a build unit or a runtime consumer.
type ArtifactID string

// SnapshotArtifact is one immutable output. Content is an OCI sha256 digest, a
// resolved Git object ID, or a native integrity hash for other artifact kinds.
// Location is the repository/immutable download location, never an OCI tag.
// Platforms records verified OCI manifest platforms, not requested build flags.
type SnapshotArtifact struct {
	Kind      Kind           `json:"kind"`
	Location  string         `json:"location"`
	Content   string         `json:"content"`
	Platforms []string       `json:"platforms,omitempty"`
	Origin    ArtifactOrigin `json:"origin"`
}

// ArtifactOrigin survives carry-forward verbatim. Receipt identifies the actual
// effective build inputs, not merely the command's working directory.
// ExternalArtifact records registry verification rather than inventing build
// provenance for third-party bytes. DescriptorDigest must match OCI Content;
// the resolver verifies descriptor/platform bytes before issuing this record.
type ExternalArtifact struct {
	Verifier         string `json:"verifier"`
	DescriptorDigest string `json:"descriptor_digest"`
}

type ArtifactOrigin struct {
	External *ExternalArtifact `json:"external,omitempty"`
	Release  string            `json:"release"`
	Source   *Source           `json:"source,omitempty"`
	Receipt  *BuildReceipt     `json:"receipt,omitempty"`
}

// ConsumerKey is environment-relative; project/environment are on Snapshot.
// Workload is a declared stable name, including non-Kubernetes consumers.
type ConsumerKey struct {
	Cluster  string `json:"cluster"`
	Workload string `json:"workload"`
}

// Consumer declares all artifact slots and conservative peer requirements.
// Slot spellings are container:NAME, init:NAME, frontend, build-only, or
// input:NAME. Names, never container list positions, identify a slot.
type Consumer struct {
	// False is unknown compatibility, not independent release permission.
	DependenciesDeclared bool                 `json:"dependencies_declared"`
	Key                  ConsumerKey          `json:"key"`
	Slots                []string             `json:"slots"`
	DependsOn            []ConsumerDependency `json:"depends_on,omitempty"`
}

type ConsumerDependency struct {
	Consumer ConsumerKey `json:"consumer"`
	Contract string      `json:"contract"`
}

type ConsumerBinding struct {
	Consumer ConsumerKey `json:"consumer"`
	Slot     string      `json:"slot"`
	Artifact ArtifactID  `json:"artifact"`
}

// ResourceKey identifies an object in one destination, independent of API version.
type ResourceKey struct {
	Cluster   string `json:"cluster"`
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// SnapshotResource freezes the original serialized object bytes. []byte uses
// base64 in JSON; it intentionally does NOT canonicalize YAML/JSON or restamp
// pod templates. Secret values must never enter this inventory.
type SnapshotResource struct {
	// Order preserves the recorded render's apply order within its delivery tree.
	Order     int           `json:"order"`
	Key       ResourceKey   `json:"key"`
	Spec      []byte        `json:"spec"`
	Digest    string        `json:"digest"`
	Tree      string        `json:"tree"`
	Phase     string        `json:"phase"`
	Owners    []ConsumerKey `json:"owners"`
	DependsOn []ResourceKey `json:"depends_on,omitempty"`
}

// SnapshotBase is captured before building. All three fields are required;
// promotion CAS must compare PromotionID under the environment lock.
type SnapshotBase struct {
	PromotionID    string `json:"promotion_id"`
	SnapshotDigest string `json:"snapshot_digest"`
	BundleDigest   string `json:"bundle_digest"`
}

// CompatibilityEvidence permits one peer to remain carried. The four subject
// digests are ConsumerDigest outputs, binding evidence to exact old/new bytes.
// ReportDigest is an immutable validator report reference, not a boolean claim.
type CompatibilityEvidence struct {
	Consumer          ConsumerKey `json:"consumer"`
	Peer              ConsumerKey `json:"peer"`
	Contract          string      `json:"contract"`
	BaseConsumer      string      `json:"base_consumer"`
	CandidateConsumer string      `json:"candidate_consumer"`
	BasePeer          string      `json:"base_peer"`
	CandidatePeer     string      `json:"candidate_peer"`
	PolicyDigest      string      `json:"policy_digest"`
	Validator         string      `json:"validator"`
	ReportDigest      string      `json:"report_digest"`
}

type CompositionDecision struct {
	Consumer ConsumerKey `json:"consumer"`
	Action   string      `json:"action"` // build, reuse, carry
	Reason   string      `json:"reason"`
}

// Snapshot is full desired state, even when Selected is a strict subset.
// Source describes composition; each artifact retains its original provenance.
// Prerequisites are immutable non-bundled input references (including Secret
// provider versions). Mutable/unverifiable prerequisites prohibit partial change.
// SnapshotAsset preserves non-applied bundle assets (dashboards/metrics schema).
type SnapshotAsset struct {
	Path   string `json:"path"`
	Data   []byte `json:"data"`
	Digest string `json:"digest"`
}

// ArtifactBuildEvent records a selected rebuild attestation separately when
// identical content retains the original catalog provenance for carried users.
type ArtifactBuildEvent struct {
	Consumer ConsumerKey    `json:"consumer"`
	Artifact ArtifactID     `json:"artifact"`
	Origin   ArtifactOrigin `json:"origin"`
}

type Snapshot struct {
	BootstrapFromPromotionID string               `json:"bootstrap_from_promotion_id,omitempty"`
	BuildEvents              []ArtifactBuildEvent `json:"build_events,omitempty"`
	// Declaration is the canonical declaration-only Shape (Objects empty).
	// Complete bundle indexes must derive from this recorded metadata, not the
	// current checkout. Partial compositions retain the base declaration.
	Declaration       *Shape                          `json:"declaration,omitempty"`
	StatefulWorkloads []string                        `json:"stateful_workloads,omitempty"`
	DeliveryUnits     []DeliveryUnit                  `json:"delivery_units,omitempty"`
	Assets            []SnapshotAsset                 `json:"assets,omitempty"`
	SecretRefs        []BundleSecretRef               `json:"secret_refs,omitempty"`
	Schema            string                          `json:"schema"`
	Project           string                          `json:"project"`
	Environment       string                          `json:"environment"`
	Base              *SnapshotBase                   `json:"base,omitempty"`
	Source            Source                          `json:"source"`
	PolicyDigest      string                          `json:"policy_digest"`
	Selected          []ConsumerKey                   `json:"selected"`
	Closure           []ConsumerKey                   `json:"closure"`
	Artifacts         map[ArtifactID]SnapshotArtifact `json:"artifacts"`
	Consumers         []Consumer                      `json:"consumers"`
	Bindings          []ConsumerBinding               `json:"bindings"`
	Resources         []SnapshotResource              `json:"resources"`
	Prerequisites     map[string]string               `json:"prerequisites,omitempty"`
	Evidence          []CompatibilityEvidence         `json:"evidence,omitempty"`
	Decisions         []CompositionDecision           `json:"decisions"`
}

// SnapshotSeal is the immutable (release, environment) association. Snapshot
// identity is computed before release labels/OCI packaging, avoiding hash cycles.
type SnapshotSeal struct {
	Release        string `json:"release"`
	Project        string `json:"project"`
	Environment    string `json:"environment"`
	SnapshotDigest string `json:"snapshot_digest"`
	BundleDigest   string `json:"bundle_digest"`
}
