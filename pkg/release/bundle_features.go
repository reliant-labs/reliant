package release

import (
	"bytes"
	"encoding/json"
	"maps"
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
	// BundleFeatureReleaseHealthBaseline is ShapeReleaseHealth.RequireBaseline:
	// how many services a release needs a before window to compare against
	// (forge.ReleaseHealth.require_baseline). A control plane that predates it
	// is sent the policy without it, and judges by the default (any).
	BundleFeatureReleaseHealthBaseline = "shape-release-health-baseline"
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
	// BundleFeatureDeclaredEnvs is BundleDoc.DeclaredEnvs: the project's
	// declared-environment catalog (bundle_catalog.go). A control plane that
	// predates it lists only the envs it has records of.
	BundleFeatureDeclaredEnvs = "doc-declared-envs"
	// BundleFeatureShapePlacement is ShapeWorkload.Unit and Shape.HostInfra:
	// the two placement facts a reader needs to place every part of an env
	// from its shape alone — which delivery unit a cluster workload is in
	// (so whether the hub or forge applies it), and whether host infra votes
	// "host". Both strict-decoded paths carry the shape, so both are limited
	// by it ([BundleReader.LimitShape]).
	BundleFeatureShapePlacement = "shape-placement"
	// BundleFeatureIdentitySignIn is the identity v2 sign-in declaration on
	// ShapeIdentity: Owners, OwnerRole, Signup, Methods, PasskeyRPID, Social,
	// Email and DevMail. A control plane that predates it converges the
	// identity as v1 did: the login policy is left alone, nobody is seeded,
	// and no login_policy is delivered, so the broker serves its legacy
	// policy. That is the fail-closed reading: nothing is enabled that nobody
	// converged.
	BundleFeatureIdentitySignIn = "shape-identity-signin"
	// BundleFeatureExternalDatabases is Shape.ExternalDatabases: the
	// databases the env uses but forge does not run, as routes (Service,
	// database, role, password Secret name). A control plane that predates it
	// lists only the env's managed databases, so a shape carrying them is
	// written only to one that names the feature. A shape that declares none
	// carries no key, so it needs nothing of the reader.
	BundleFeatureExternalDatabases = "shape-external-databases"
	// BundleFeatureExternalDatabaseTLS is authenticated external route TLS.
	// Older readers receive no such route, never a route with trust stripped
	// or authentication downgraded. Legacy routes remain unchanged.
	BundleFeatureExternalDatabaseTLS = "shape-external-database-tls"
	// BundleFeatureStorage is Shape.Buckets and ShapeDataAccess.Storage: the
	// env's buckets as the storage browser lists them, and the policy it
	// browses them under. A control plane that predates it browses nothing,
	// so a shape carrying either is written only to one that names the
	// feature; one that does not reads the storage policy as db_query's,
	// which is the fallback the field defines. A shape that declares no
	// bucket and no separate policy carries no key, so it needs nothing of
	// the reader.
	BundleFeatureStorage = "shape-storage"
	// BundleFeatureFunnelPerson is a ShapeFunnel counted by "visitor" or
	// "person" (forge.Funnel.by, the built-in product analytics). A reader
	// that predates it refuses those values, so it is sent only the funnels
	// counted by "session" or "user".
	BundleFeatureFunnelPerson = "shape-funnel-person"
	// BundleFeatureReleaseProbes is Shape.ReleaseProbes, and the release-probe
	// finding codes (HealthProbeFailed, HealthProbeLatency) in
	// ShapeReleaseHealth.FailOn. A control plane that predates it probes
	// nothing for the env, and its validation refuses a finding code it does
	// not know, so it is sent neither.
	BundleFeatureReleaseProbes = "shape-release-probes"
)

// BundleDocFeatures is every optional bundle feature this version's
// [DecodeBundleDoc] and [ValidateManifestsLayer] accept. A control plane
// advertises exactly this list, so what it claims to decode and what its
// decoder accepts are one fact.
func BundleDocFeatures() []string {
	return []string{BundleFeatureCommitIdentity, BundleFeatureObjectSummaries, BundleFeatureFrontendTelemetry, BundleFeatureStoreSecrets, BundleFeatureDataAccess, BundleFeatureReleaseHealth, BundleFeatureUptimeChecks, BundleFeatureDatabaseDashboards, BundleFeatureServiceCatalog, BundleFeaturePlacementPrerequisites, BundleFeatureHealthGate, BundleFeatureDeclaredEnvs, BundleFeatureShapePlacement, BundleFeatureIdentitySignIn, BundleFeatureExternalDatabases, BundleFeatureExternalDatabaseTLS, BundleFeatureStorage, BundleFeatureFunnelPerson, BundleFeatureReleaseProbes, BundleFeatureReleaseHealthBaseline}
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
	if !r.Supports(BundleFeatureDeclaredEnvs) {
		d.DeclaredEnvs = nil
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
	} else if !r.Supports(BundleFeatureReleaseHealthBaseline) && s.ReleaseHealth != nil && s.ReleaseHealth.RequireBaseline != "" {
		h := *s.ReleaseHealth
		h.RequireBaseline = ""
		if h.IsDefault() {
			s.ReleaseHealth = nil // all that differed from the default was the baseline
		} else {
			s.ReleaseHealth = &h
		}
	}
	if !r.Supports(BundleFeatureDataAccess) {
		s.DataAccess = nil
	}
	if !r.Supports(BundleFeatureUptimeChecks) {
		s.UptimeChecks = nil
	}
	if !r.Supports(BundleFeatureReleaseProbes) {
		// A probed env that keeps the default policy records none, and the
		// default now fails on probe_failed. A reader of release health is
		// sent that policy stated, without the probe codes, so what it judges
		// by is the producer's policy rather than its own default. An env
		// without probes is sent as before.
		if s.ReleaseHealth == nil && len(s.ReleaseProbes) > 0 && r.Supports(BundleFeatureReleaseHealth) {
			def := DefaultReleaseHealth()
			s.ReleaseHealth = &def
		}
		s.ReleaseProbes = nil
		s.ReleaseHealth = withoutProbeFindings(s.ReleaseHealth)
	}
	if !r.Supports(BundleFeatureDatabaseDashboards) {
		s.Databases = nil
	}
	if !r.Supports(BundleFeatureExternalDatabases) {
		s.ExternalDatabases = nil
	} else if !r.Supports(BundleFeatureExternalDatabaseTLS) {
		// Copy before filtering: limiting must not mutate the full declaration.
		s.ExternalDatabases = slices.DeleteFunc(slices.Clone(s.ExternalDatabases), func(x ShapeExternalDatabase) bool {
			return x.SSLMode == ExternalDatabaseSSLVerifyFull || x.CASecret != "" || x.CAConfigMap != "" || x.CAKey != "" || x.TLSServerName != ""
		})
	}
	if !r.Supports(BundleFeatureShapePlacement) {
		s.HostInfra = false
		if slices.ContainsFunc(s.Workloads, func(w ShapeWorkload) bool { return w.Unit != "" }) {
			ws := slices.Clone(s.Workloads)
			for i := range ws {
				ws[i].Unit = ""
			}
			s.Workloads = ws
		}
	}
	if !r.Supports(BundleFeatureIdentitySignIn) && s.Identity != nil {
		id := *s.Identity
		id.Owners, id.OwnerRole, id.Signup, id.Methods = nil, "", nil, nil
		id.PasskeyRPID, id.Social, id.Email, id.DevMail = "", nil, nil, ""
		// The login_policy output is a sign-in addition too: a reader predating
		// it rejects a consumer that maps or omits it as an unknown output.
		id.Consumers = withoutLoginPolicyOutput(id.Consumers)
		s.Identity = &id
	}
	if !r.Supports(BundleFeatureHealthGate) && s.DeliveryPolicy != nil && s.DeliveryPolicy.HealthGate != nil {
		pol := *s.DeliveryPolicy
		pol.HealthGate = nil
		s.DeliveryPolicy = &pol
	}
	if !r.Supports(BundleFeatureStorage) {
		s.Buckets = nil
		if s.DataAccess != nil && s.DataAccess.Storage != "" {
			da := *s.DataAccess
			da.Storage = ""
			s.DataAccess = &da
		}
	}
	if r.Supports(BundleFeatureFrontendTelemetry) {
		if !r.Supports(BundleFeatureFunnelPerson) {
			s.FrontendTelemetry = withoutProductFunnels(s.FrontendTelemetry)
		}
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

// identityOutputLoginPolicy is the identity output the sign-in feature added.
const identityOutputLoginPolicy = "login_policy"

// withoutLoginPolicyOutput returns cs with every login_policy mapping and
// omission removed. A copy when anything is removed: cs is not modified.
func withoutLoginPolicyOutput(cs []ShapeIdentityConsumer) []ShapeIdentityConsumer {
	names := func(c ShapeIdentityConsumer) bool {
		_, mapped := c.Fields[identityOutputLoginPolicy]
		return mapped || slices.Contains(c.Omit, identityOutputLoginPolicy)
	}
	if !slices.ContainsFunc(cs, names) {
		return cs
	}
	out := slices.Clone(cs)
	for i, c := range out {
		if !names(c) {
			continue
		}
		if _, mapped := c.Fields[identityOutputLoginPolicy]; mapped {
			fields := maps.Clone(c.Fields)
			delete(fields, identityOutputLoginPolicy)
			if len(fields) == 0 {
				fields = nil
			}
			out[i].Fields = fields
		}
		if slices.Contains(c.Omit, identityOutputLoginPolicy) {
			omit := slices.DeleteFunc(slices.Clone(c.Omit), func(o string) bool { return o == identityOutputLoginPolicy })
			if len(omit) == 0 {
				omit = nil
			}
			out[i].Omit = omit
		}
	}
	return out
}

// withoutProductFunnels returns ts without the funnels counted by "visitor"
// or "person", dropping an entry left declaring nothing. A copy when anything
// is removed: ts is not modified.
func withoutProductFunnels(ts []ShapeFrontendTelemetry) []ShapeFrontendTelemetry {
	product := func(f ShapeFunnel) bool { return f.By == FunnelByVisitor || f.By == FunnelByPerson }
	if !slices.ContainsFunc(ts, func(t ShapeFrontendTelemetry) bool { return slices.ContainsFunc(t.Funnels, product) }) {
		return ts
	}
	var out []ShapeFrontendTelemetry
	for _, t := range ts {
		t.Funnels = slices.DeleteFunc(slices.Clone(t.Funnels), product)
		if len(t.Funnels) == 0 {
			t.Funnels = nil
		}
		if !t.IsDefault() {
			out = append(out, t)
		}
	}
	return out
}
