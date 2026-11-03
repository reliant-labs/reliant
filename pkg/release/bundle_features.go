package release

import (
	"bytes"
	"encoding/json"
	"slices"
)

// Optional parts of the bundle document ([BundleDoc]) that a reader may not
// decode yet.
//
// WHY THIS EXISTS. A backend that records a bundle decodes its config blob
// with [DecodeBundleDoc], which refuses an unknown field — deliberately: a
// backend must not record a summary of a document it only half understood.
// The flip side is that every field added to the document breaks every
// control plane that predates it. forge#850 added three (the commit subject
// and author in the provenance, and the per-object summaries), and a forge
// carrying it could no longer record a release on a control plane built
// before it: `bundle document: json: unknown field "commit_subject"`, with no
// way to ship the control plane that understands the field, because shipping
// is recording.
//
// So an addition to the document is a FEATURE: written only to a reader that
// says it decodes it. A control plane advertises the features its own
// DecodeBundleDoc accepts from DeployService/GetCapabilities
// ([BundleDocFeatures]); forge asks before it builds a hosted bundle and
// leaves out every part the server did not name ([BundleReader.Limit]). A
// server that predates the capability, or the RPC, names none, and gets the
// document its strict decoder knew.
//
// The snapshot fields (Snapshot, SnapshotDigest) are not features: a snapshot
// bundle is a different schema, and composing one already requires the
// server's snapshot capability and fails closed without it.
//
// Adding an optional field to BundleDoc means adding a feature here.
// TestEveryBundleDocAdditionIsAFeature (bundle_features_test.go) fails until
// it is mapped.
const (
	// BundleFeaturePlacementPrerequisites gates required mixed dependency evidence.
	// Unlike browse-only additions, Limit must NEVER discard this safety contract.
	//
	// v2 (graph version 2) is v1 with the shared passive rule
	// (PlacementPassiveKind): every kind but the runtime kinds, ConfigMap and
	// Secret is applied inventory, where a v1 reader refused any kind outside
	// a closed list. v1 was never advertised by a control
	// plane and no v1 graph was ever recorded, so it is retired rather than
	// kept: a reader that names only v1 is refused before composition, never
	// handed a graph it would reject or under-check.
	BundleFeaturePlacementPrerequisites = "placement-prerequisites-v2"
	// BundleFeatureCommitIdentity is Provenance.CommitSubject and
	// Provenance.CommitAuthor (forge#850).
	BundleFeatureCommitIdentity = "provenance-commit-identity"
	// BundleFeatureObjectSummaries is BundleDoc.Objects, the per-object
	// summaries (forge#850).
	BundleFeatureObjectSummaries = "object-summaries"
	// BundleFeatureFrontendTelemetry is Shape.FrontendTelemetry and
	// ShapeObservabilityPolicy.Retention: what a frontend declares about its
	// browser telemetry, and how long the platform keeps it. The shape is
	// strict-decoded on two paths, the bundle document and the environment
	// declaration, so both are limited by it ([BundleReader.LimitShape]).
	BundleFeatureFrontendTelemetry = "shape-frontend-telemetry"
	// BundleFeatureStoreSecrets is Shape.StoreSecrets, Shape.GeneratedSecrets,
	// Shape.OpenBao and Shape.ExternalSecretsOperators: the store names each
	// namespace reads, the values to mint, and the components the env
	// declares. A control plane that predates them cannot derive the access a
	// store-backed Secret needs, so a shape carrying them is written only to
	// one that names the feature. It also owns
	// ShapeControlPlaneSecret.Delivery: a control plane that cannot read the
	// store bindings cannot write the store, so it is left delivering by push.
	BundleFeatureStoreSecrets = "shape-store-secrets"
	// BundleFeatureDataAccess is Shape.DataAccess: whether the env's database
	// may be queried (`forge db query`) openly, audited, or not at all. A
	// control plane that predates it cannot enforce the declaration, so a shape
	// carrying it is written only to one that names the feature.
	BundleFeatureDataAccess = "shape-data-access"
	// BundleFeatureReleaseHealth is Shape.ReleaseHealth: the thresholds the
	// env's promotions are judged by (forge.ReleaseHealth). A control plane
	// that predates it judges with the defaults.
	BundleFeatureReleaseHealth = "shape-release-health"
	// BundleFeatureUptimeChecks is Shape.UptimeChecks: the outside-in checks
	// the control plane's prober runs. A control plane that predates it
	// probes nothing for the env.
	BundleFeatureUptimeChecks = "shape-uptime-checks"
	// BundleFeatureDatabaseDashboards is Shape.Databases: whether live
	// dashboard panels may query each managed database, and the row cap,
	// timeout and concurrency they run under. A control plane that predates it
	// cannot enforce the limits, so a shape carrying them is written only to
	// one that names the feature; one that does not reads every database as
	// dashboards-disabled, which is the fail-closed reading.
	BundleFeatureDatabaseDashboards = "shape-database-dashboards"
	// BundleFeatureServiceCatalog is the manifests layer's
	// [BundleServiceCatalogPath] artifact, of any size up to the layer's
	// bound. It is a layer feature, not a document one: a control plane whose
	// [ValidateManifestsLayer] predates it refuses the whole layer ("only the
	// dedicated frozen frontend declaration asset is permitted under forge/"),
	// so forge uploads the catalog only to one that names it.
	BundleFeatureServiceCatalog = "layer-service-catalog"
	// BundleFeatureHealthGate is ShapeDeliveryPolicy.HealthGate: the envs a
	// release must be proven good in before this env takes it
	// (forge.HealthGate). A control plane that predates it gates nothing, so
	// a shape carrying it is written only to one that names the feature,
	// which is the control plane that enforces it.
	BundleFeatureHealthGate = "shape-health-gate"
)

// BundleDocFeatures is every optional bundle feature this version's
// [DecodeBundleDoc] and [ValidateManifestsLayer] accept. A control plane
// advertises exactly this list, so what it claims to decode and what its
// decoder accepts are one fact.
func BundleDocFeatures() []string {
	return []string{BundleFeatureCommitIdentity, BundleFeatureObjectSummaries, BundleFeatureFrontendTelemetry, BundleFeatureStoreSecrets, BundleFeatureDataAccess, BundleFeatureReleaseHealth, BundleFeatureUptimeChecks, BundleFeatureDatabaseDashboards, BundleFeatureServiceCatalog, BundleFeaturePlacementPrerequisites, BundleFeatureHealthGate}
}

// BundleReader is what the backend a bundle is recorded in decodes: the
// optional document features it named. A nil *BundleReader is this forge's
// own decoder — every feature — which is what a machine ledger reads with.
type BundleReader struct {
	Features []string
}

// Supports reports whether the reader decodes feature. A nil reader decodes
// every feature this forge writes.
func (r *BundleReader) Supports(feature string) bool {
	return r == nil || slices.Contains(r.Features, feature)
}

// Limit removes optional browse-only parts the reader does not decode.
// PlacementPrerequisites is a safety contract and is NEVER removed: producers
// must refuse unsupported readers before effects rather than drop dependencies.
// Omitted browse-only fields use omitempty; empty unknown fields still fail.
func (r *BundleReader) Limit(d BundleDoc) BundleDoc {
	if !r.Supports(BundleFeatureCommitIdentity) {
		d.Provenance.CommitSubject = ""
		d.Provenance.CommitAuthor = ""
	}
	if !r.Supports(BundleFeatureObjectSummaries) {
		d.Objects = nil
	}
	d.Shape = r.LimitShape(d.Shape)
	return d
}

// HasOptionalParts reports whether s carries any part a reader that decodes
// NO optional feature would withhold — so whether sending s to a control
// plane needs its feature list first.
//
// It is DERIVED from LimitShape rather than listing fields, and that is the
// point: the environment declaration once asked only when the shape had
// frontend telemetry, so when forge #1285 added uptime checks to the shape a
// control plane predating them strict-decoded the declaration, refused it
// whole ("spec.shape is not a release.Shape"), and every release cut against
// it failed before building. A feature added to LimitShape is covered here
// with no second edit.
func (s Shape) HasOptionalParts() bool {
	limited, err := json.Marshal((&BundleReader{Features: []string{}}).LimitShape(s).Canonical())
	if err != nil {
		return true
	}
	full, err := json.Marshal(s.Canonical())
	if err != nil {
		return true
	}
	return !bytes.Equal(limited, full)
}

// LimitShape returns s without every optional part the reader does not decode.
// Limit applies it to a bundle document's shape; the environment declaration,
// which a control plane strict-decodes into the same Shape, applies it to the
// shape it sends. A copy: s is not modified.
func (r *BundleReader) LimitShape(s Shape) Shape {
	if !r.Supports(BundleFeatureStoreSecrets) {
		s.StoreSecrets, s.GeneratedSecrets, s.OpenBao, s.ExternalSecretsOperators = nil, nil, nil, nil
		if slices.ContainsFunc(s.ControlPlaneSecrets, func(c ShapeControlPlaneSecret) bool { return c.Delivery != "" }) {
			cps := slices.Clone(s.ControlPlaneSecrets)
			for i := range cps {
				cps[i].Delivery = ""
			}
			s.ControlPlaneSecrets = cps
		}
	}
	if !r.Supports(BundleFeatureReleaseHealth) {
		s.ReleaseHealth = nil
	}
	if !r.Supports(BundleFeatureDataAccess) {
		s.DataAccess = nil
	}
	if !r.Supports(BundleFeatureUptimeChecks) {
		s.UptimeChecks = nil
	}
	if !r.Supports(BundleFeatureDatabaseDashboards) {
		s.Databases = nil
	}
	if !r.Supports(BundleFeatureHealthGate) && s.DeliveryPolicy != nil && s.DeliveryPolicy.HealthGate != nil {
		pol := *s.DeliveryPolicy
		pol.HealthGate = nil
		s.DeliveryPolicy = &pol
	}
	if r.Supports(BundleFeatureFrontendTelemetry) {
		return s
	}
	s.FrontendTelemetry = nil
	if s.ObservabilityPolicy != nil && s.ObservabilityPolicy.Retention != nil {
		pol := *s.ObservabilityPolicy
		pol.Retention = nil
		s.ObservabilityPolicy = &pol
	}
	return s
}
