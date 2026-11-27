package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
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
	// BundleFeatureReleaseHealthRegressedMinEvents is
	// ShapeReleaseHealth.RegressedIssueMinEvents: the after-window events a
	// regressed issue needs before it counts
	// (forge.ReleaseHealth.regressed_issue_min_events). A control plane that
	// predates it is sent the policy without it, and counts every regression.
	BundleFeatureReleaseHealthRegressedMinEvents = "shape-release-health-regressed-min-events"
	// BundleFeatureReleaseHealthErrorRate is ShapeReleaseHealth.ErrorRateMinErrors
	// and ShapeReleaseHealth.BaselineWindowSeconds: the errors error_rate
	// needs, and the longer baseline a sparse endpoint is compared against
	// (forge.ReleaseHealth.error_rate_min_errors, baseline_window). A control
	// plane that predates it is sent the policy without them, and judges by
	// its own defaults.
	BundleFeatureReleaseHealthErrorRate = "shape-release-health-error-rate"
	// BundleFeatureReleaseHealthAlertBaseline is
	// ShapeReleaseHealth.AlertBaselineMinFirings: how often an alert must have
	// fired before the promotion to be read as the env's noise rather than
	// the release's (forge.ReleaseHealth.alert_baseline_min_firings). A
	// control plane that predates it is sent the policy without it, and
	// judges by its own rule.
	BundleFeatureReleaseHealthAlertBaseline = "shape-release-health-alert-baseline"
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
	// BundleFeatureIdentityCodeLifetime is ShapeIdentity.CodeLifetime: how
	// long a password-set code stays redeemable (forge#2265). A control
	// plane that predates it keeps the provider's one-hour default, so a
	// shape declaring a lifetime is sent to it without one rather than
	// refused.
	BundleFeatureIdentityCodeLifetime = "shape-identity-code-lifetime"
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
	// BundleFeaturePreview is Shape.Preview: the env is a preview instance
	// with an expiry (forge.Preview). A control plane that predates it does
	// not delete the env when it expires — `forge env gc` still does — so a
	// shape carrying it is written only to one that names the feature.
	BundleFeaturePreview = "shape-preview"
	// BundleFeaturePromotionChecks is ShapeDeliveryPolicy.RequiredGates and
	// ShapeDeliveryPolicy.ApprovalHook: the gates a promotion must carry as
	// passed, and the team's approval hook asked before one is written. A
	// control plane that predates it enforces neither, so a shape carrying
	// them is written only to one that names the feature, which is the control
	// plane that enforces them — and forge refuses to declare an ENFORCING
	// one to a control plane that ENFORCES them
	// ([CapabilityEnforcesPromotionChecks], [BundleReader.UnenforcedChecks]).
	BundleFeaturePromotionChecks = "shape-promotion-checks"
	// BundleFeatureReleaseProbes is Shape.ReleaseProbes, and the release-probe
	// finding codes (HealthProbeFailed, HealthProbeLatency) in
	// ShapeReleaseHealth.FailOn. A control plane that predates it probes
	// nothing for the env, and its validation refuses a finding code it does
	// not know, so it is sent neither. It is also sent no browser finding
	// code in FailOn: those predate this feature by hours but have none of
	// their own, and such a reader judges no browser telemetry.
	BundleFeatureReleaseProbes = "shape-release-probes"
	// BundleFeatureReleaseHealthChangedOps is the changed-op finding codes
	// (HealthChangedOpErrors, HealthChangedOpLatency) in
	// ShapeReleaseHealth.FailOn. A value feature, like
	// BundleFeatureFunnelPerson: it adds no key, but a control plane that
	// predates it refuses a fail_on naming a code it does not know, and with
	// it the whole shape. So it is sent the policy without them, and judges
	// them as the default does: warn.
	BundleFeatureReleaseHealthChangedOps = "shape-release-health-changed-ops"
	// BundleFeatureReleaseHealthSiteAudit is the site-audit finding codes
	// (HealthSEORegressed, HealthA11yRegressed) in ShapeReleaseHealth.FailOn,
	// and their thresholds, ShapeReleaseHealth.SEOMaxScoreDrop and
	// A11yMaxScoreDrop (forge.ReleaseHealth.seo_max_score_drop,
	// a11y_max_score_drop). A control plane that predates it refuses a
	// fail_on naming a code it does not know, and strict-decodes the
	// thresholds, so it is sent the policy without them; it judges no site
	// audit.
	BundleFeatureReleaseHealthSiteAudit = "shape-release-health-site-audit"
	// BundleFeatureChanges is Shape.Changes: the Forge Changes the release
	// declares (changes.yaml). A control plane that predates it serves no
	// change for the env, so the app evaluates every key as unknown and
	// keeps its code defaults: a shape carrying them is written only to one
	// that names the feature. A project that declares none carries no key,
	// so it needs nothing of the reader.
	BundleFeatureChanges = "shape-changes"
	// BundleFeatureTelemetry is Shape.Telemetry: the declared telemetry
	// targets (forge.Telemetry) of a hosted env, which the platform collects
	// because forge runs no collector there. Like every feature here it means
	// only that the reader DECODES them: a control plane advertises
	// [BundleDocFeatures] verbatim on every forge pin bump. Collecting them is
	// [CapabilityCollectsTelemetry], and forge sends the targets only to a
	// reader that names both ([BundleReader.CollectsTelemetry]); to any other
	// it leaves them out and warns, naming the targets
	// ([BundleReader.TelemetryWarning]). A shape that declares no hosted
	// target carries no key, so it needs nothing of the reader.
	BundleFeatureTelemetry = "shape-telemetry"
	// BundleFeatureIdentities is Shape.Identities: the env's managed
	// identities when it declares several (KCL Bundle.identities). Decoding
	// the key is not converging it: a control plane advertises this list on
	// every forge pin bump, and one whose reconciler reads only
	// Shape.Identity would record such a shape and converge none of its
	// identities. So a shape carrying it is also refused to a control plane
	// that does not advertise [CapabilityConvergesSeveralIdentities]
	// ([BundleReader.CheckIdentities]) — never sent to it with the key
	// stripped. A shape with one identity carries Shape.Identity alone, as it
	// always did, and needs nothing of the reader.
	BundleFeatureIdentities = "shape-identities"
	// BundleFeatureVulnScopedExemptions is ShapeVulnGate.ScopedExemptions:
	// the env's vulnerability exemptions that cover their advisory only in
	// the packages, artifacts or paths they name (forge.VulnExemption's
	// packages, artifacts and locations). Decoding is not evaluation: producers
	// also require CapabilityEvaluatesScopedVulnExemptions before sending a
	// scoped policy to a control plane (CheckScopedVulnExemptions). Scoped
	// exemptions are never broadened into entries of Exemptions.
	BundleFeatureVulnScopedExemptions = "shape-vuln-scoped-exemptions"

	// BundleFeatureStatusPage is Shape.StatusPage: the env's declared status
	// page (forge.StatusPage). A control plane that predates it builds no
	// page for the env, so a shape carrying it is written only to one that
	// names the feature. The page names uptime checks, so it is also left
	// out for a reader that does not decode those. A project that declares
	// none carries no key, so it needs nothing of the reader.
	BundleFeatureStatusPage = "shape-status-page"

	// BundleFeatureReleaseURLs is Shape.HostedFrontends: what an env declares
	// about each hosted static frontend beyond its StaticSite, today
	// forge.Frontend.release_urls (the opt-out from per-release URLs). A
	// control plane that predates it is sent the shape without the key; one
	// that serves release URLs without decoding it is refused the opt-out
	// instead ([BundleReader.CheckReleaseURLs]), so the opt-out is never
	// dropped where it would matter. A shape whose frontends all keep the
	// default carries no key, so it needs nothing of the reader.
	BundleFeatureReleaseURLs = "shape-release-urls"
)

// CapabilityServesReleaseURLs is advertised beside the bundle features by a
// control plane that SERVES per-release URLs for hosted static frontends: each
// release it still retains at a URL of its own on the preview domain. Like
// [CapabilityEnforcesPromotionChecks] it is deliberately not in
// [BundleDocFeatures]: decoding Shape.HostedFrontends is a matter of the forge
// a control plane pins, serving the URLs is not. A control plane adds it by
// hand, in the change that serves them. Forge reads it only to refuse an
// opt-out such a control plane could not read ([BundleReader.CheckReleaseURLs]).
const CapabilityServesReleaseURLs = "serves-release-urls"

// CapabilityConvergesSeveralIdentities is advertised beside the bundle
// features by a control plane that converges EVERY entry of
// Shape.Identities (one instance per name, each with its own slug from
// [IdentitySlugFor], key-store namespace, status and deliveries) and whose
// IdentityService addresses an instance by the request's `identity` name.
// Like [CapabilityEnforcesPromotionChecks] it is deliberately not in
// [BundleDocFeatures]: that list means "decodes" and is advertised verbatim
// on every pin bump, which would claim a convergence the reconciler does not
// perform. A control plane adds this by hand, in the change that makes it
// converge several identities. Until it does, forge refuses to declare or
// record a shape with several identities there ([BundleReader.CheckIdentities]),
// so an older control plane fails the deploy instead of converging nothing.
const CapabilityConvergesSeveralIdentities = "converges-several-identities"

// CapabilityEnforcesPromotionChecks is advertised beside the bundle features
// by a control plane whose promote transaction JUDGES the required gates and
// the approval hook. It is deliberately not in [BundleDocFeatures]: that list
// means "decodes", and a control plane advertises it verbatim on every forge
// pin bump, so a pin bump alone would claim enforcement its promote does not
// perform, and an enforcing check would admit every promotion. A control
// plane adds this by hand, in the change that makes its promote judge both.
const CapabilityEnforcesPromotionChecks = "enforces-promotion-checks"

// CapabilityServesDashboardFilterParams is advertised beside the bundle
// features by a control plane that serves the dashboards forge ships only to
// a server that says so: the built-in card dashboards (pkg/obsbuiltin.Cards:
// Traffic and Uptime) and any dashboard whose sql panels read filter params
// ({f_<name>:String}, pkg/dashspec.FilterParam). Serving one needs the forge
// whose obsquery binds a filter param (to the empty string when the request
// names none); an older control plane cannot run such a panel.
//
// Like [CapabilityEnforcesPromotionChecks] it is deliberately not in
// [BundleDocFeatures]: a control plane advertises that list on every forge pin
// bump, and the bundles recorded while it does must also be served by the
// release it would roll back to. So a control plane adds this by hand, once
// its rollback target binds filter params too. Until a control plane names
// it, forge withholds those dashboards from the bundles it records there, and
// a bundle built for no control plane (the machine ledger) carries none.
const CapabilityServesDashboardFilterParams = "serves-dashboard-filter-params"

// CapabilityCollectsTelemetry is advertised beside the bundle features by a
// control plane that COLLECTS a hosted env's declared telemetry
// (Shape.Telemetry): its platform agent scrapes, receives and tails the
// targets the shape names. Decoding them is [BundleFeatureTelemetry], which
// a control plane advertises verbatim on every forge pin bump, so a pin bump
// alone would otherwise claim collection, and forge would send the targets
// without a warning while nothing collected them.
//
// Like [CapabilityEnforcesPromotionChecks] it is deliberately not in
// [BundleDocFeatures]. A control plane adds this by hand, in the change that
// makes it collect hosted telemetry. Until a control plane names both, forge
// withholds the targets from what it records there and warns the person
// deploying ([BundleReader.TelemetryWarning]).
const CapabilityCollectsTelemetry = "collects-telemetry"

// CapabilityServesNamedSiteAPIs is advertised beside the bundle features by
// a control plane that SERVES a hosted static site's named same-origin APIs
// (StaticSiteSpec.APIs): its operator records each name's host in the site's
// serving document and writes `apis` into config.js, and its origin routes
// <any host of the site>/_api/@<name>/* to that host, prefix stripped, with
// the client's X-Forwarded-* replaced by its own.
//
// Decoding the field is a matter of the forge a control plane pins, which it
// would gain on any pin bump; serving it is not. So, like
// [CapabilityEnforcesPromotionChecks], it is deliberately not in
// [BundleDocFeatures]: a control plane adds it by hand, in the change that
// serves the routes. Until it does, forge refuses to record a site that
// declares named APIs there ([BundleReader.CheckNamedSiteAPIs]), so a deploy
// fails with the capability named instead of shipping a site whose
// /_api/@<name> calls 404.
const CapabilityServesNamedSiteAPIs = "serves-named-site-apis"

// CapabilityAutoscalesWorkloads is advertised beside the bundle features by a
// control plane that APPLIES a hosted workload's autoscale
// (WorkloadSpec.Autoscale): its operator renders the HorizontalPodAutoscaler,
// hands spec.replicas over to it, and admits the workload at its maximum.
//
// Decoding the field is a matter of the forge a control plane pins, which it
// would gain on any pin bump, and a control plane that predates it ignores
// the unknown field, so the workload would run at one replica with nothing
// saying so. Like [CapabilityEnforcesPromotionChecks] it is deliberately not
// in [BundleDocFeatures]: a control plane adds it by hand, in the change that
// applies HPAs. Until it does, forge refuses to deploy an autoscaled hosted
// workload there ([BundleReader.CheckAutoscaledWorkloads]).
const CapabilityAutoscalesWorkloads = "autoscales-workloads"

// BundleDocFeatures is every optional bundle feature this version's
// [DecodeBundleDoc] and [ValidateManifestsLayer] accept. A control plane
// advertises exactly this list, so what it claims to decode and what its
// decoder accepts are one fact.
func BundleDocFeatures() []string {
	return []string{BundleFeatureCommitIdentity, BundleFeatureObjectSummaries, BundleFeatureFrontendTelemetry, BundleFeatureStoreSecrets, BundleFeatureDataAccess, BundleFeatureReleaseHealth, BundleFeatureUptimeChecks, BundleFeatureDatabaseDashboards, BundleFeatureServiceCatalog, BundleFeaturePlacementPrerequisites, BundleFeatureHealthGate, BundleFeatureDeclaredEnvs, BundleFeatureShapePlacement, BundleFeatureIdentitySignIn, BundleFeatureExternalDatabases, BundleFeatureExternalDatabaseTLS, BundleFeatureStorage, BundleFeatureFunnelPerson, BundleFeatureReleaseProbes, BundleFeatureReleaseHealthBaseline, BundleFeaturePromotionChecks, BundleFeaturePreview, BundleFeatureReleaseHealthRegressedMinEvents, BundleFeatureReleaseHealthErrorRate, BundleFeatureReleaseHealthAlertBaseline, BundleFeatureReleaseHealthChangedOps, BundleFeatureIdentityCodeLifetime, BundleFeatureTelemetry, BundleFeatureChanges, BundleFeatureIdentities, BundleFeatureStatusPage, BundleFeatureVulnScopedExemptions, BundleFeatureReleaseURLs, BundleFeatureReleaseHealthSiteAudit}
}

// BundleReader is what the backend a bundle is recorded in decodes: the
// optional document features it named. A nil *BundleReader is this forge's
// own decoder — every feature — which is what a machine ledger reads with.
type BundleReader struct {
	Features []string
	// Telemetry is the caps the control plane holds a hosted env's declared
	// telemetry to (GetCapabilities.telemetry_limits), nil when it advertises
	// none: then the defaults apply ([BundleReader.TelemetryCaps]).
	Telemetry *TelemetryLimits
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

// UnenforcedChecks names the promotion checks s declares in enforce mode that
// this reader does not enforce: one that does not advertise
// [CapabilityEnforcesPromotionChecks] (decoding the fields is not judging
// them) would admit every promotion they were declared to refuse. The
// environment declaration refuses to send such a shape rather than let an
// enforcing check silently become nothing.
func (r *BundleReader) UnenforcedChecks(s Shape) []string {
	if r.Supports(CapabilityEnforcesPromotionChecks) || s.DeliveryPolicy == nil {
		return nil
	}
	var out []string
	if g := s.DeliveryPolicy.RequiredGates; g != nil && g.Mode == DeliveryEnforce && len(g.Names) > 0 {
		out = append(out, "required_gates")
	}
	if h := s.DeliveryPolicy.ApprovalHook; h != nil && h.Mode == DeliveryEnforce {
		out = append(out, "approval_hook")
	}
	return out
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
	} else if s.ReleaseHealth != nil {
		h, limited := *s.ReleaseHealth, false
		if !r.Supports(BundleFeatureReleaseHealthBaseline) && h.RequireBaseline != "" {
			h.RequireBaseline, limited = "", true
		}
		if !r.Supports(BundleFeatureReleaseHealthRegressedMinEvents) && h.RegressedIssueMinEvents != 0 {
			h.RegressedIssueMinEvents, limited = 0, true
		}
		if !r.Supports(BundleFeatureReleaseHealthErrorRate) && (h.ErrorRateMinErrors != 0 || h.BaselineWindowSeconds != 0) {
			h.ErrorRateMinErrors, h.BaselineWindowSeconds, limited = 0, 0, true
		}
		if !r.Supports(BundleFeatureReleaseHealthAlertBaseline) && h.AlertBaselineMinFirings != nil {
			h.AlertBaselineMinFirings, limited = nil, true
		}
		if changedOp := func(c string) bool { return slices.Contains(ChangedOpFindings, c) }; !r.Supports(BundleFeatureReleaseHealthChangedOps) && slices.ContainsFunc(h.FailOn, changedOp) {
			h.FailOn, limited = slices.DeleteFunc(slices.Clone(h.FailOn), changedOp), true
		}
		if !r.Supports(BundleFeatureReleaseHealthSiteAudit) {
			if siteAudit := func(c string) bool { return slices.Contains(SiteAuditFindings, c) }; slices.ContainsFunc(h.FailOn, siteAudit) {
				h.FailOn, limited = slices.DeleteFunc(slices.Clone(h.FailOn), siteAudit), true
			}
			if h.SEOMaxScoreDrop != nil || h.A11yMaxScoreDrop != nil {
				h.SEOMaxScoreDrop, h.A11yMaxScoreDrop, limited = nil, nil, true
			}
		}
		switch {
		case !limited:
		case h.IsDefault():
			s.ReleaseHealth = nil // all that differed from the default was what the reader cannot decode
		default:
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
		// sent that policy stated, without the probe (or browser) codes, so
		// what it judges by is the producer's policy rather than its own
		// default. An env without probes is sent as before.
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
	if !r.CollectsTelemetry() {
		s.Telemetry = nil
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
	if s.Identity != nil {
		id := r.limitIdentity(*s.Identity)
		s.Identity = &id
	}
	if !r.Supports(BundleFeatureIdentities) {
		// Never reached for a shape that carries them: CheckIdentities refuses
		// the reader first. Stripped for the strict decoder all the same.
		s.Identities = nil
	} else if len(s.Identities) > 0 {
		ids := slices.Clone(s.Identities)
		for i := range ids {
			ids[i] = r.limitIdentity(ids[i])
		}
		s.Identities = ids
	}
	if !r.Supports(BundleFeatureHealthGate) && s.DeliveryPolicy != nil && s.DeliveryPolicy.HealthGate != nil {
		pol := *s.DeliveryPolicy
		pol.HealthGate = nil
		s.DeliveryPolicy = &pol
	}
	if !r.Supports(BundleFeatureVulnScopedExemptions) && s.DeliveryPolicy != nil && len(s.DeliveryPolicy.VulnGate.ScopedExemptions) > 0 {
		pol := *s.DeliveryPolicy
		pol.VulnGate.ScopedExemptions = nil
		s.DeliveryPolicy = &pol
		if pol.IsDefault() {
			s.DeliveryPolicy = nil // all that differed from the default was what the reader cannot decode
		}
	}
	if !r.Supports(BundleFeaturePreview) {
		s.Preview = nil
	}
	if !r.Supports(BundleFeatureChanges) {
		s.Changes = nil
	}
	if !r.Supports(BundleFeatureStatusPage) || !r.Supports(BundleFeatureUptimeChecks) {
		s.StatusPage = nil
	}
	if !r.Supports(BundleFeatureReleaseURLs) {
		s.HostedFrontends = nil
	}
	if !r.Supports(BundleFeaturePromotionChecks) && s.DeliveryPolicy != nil &&
		(s.DeliveryPolicy.RequiredGates != nil || s.DeliveryPolicy.ApprovalHook != nil) {
		pol := *s.DeliveryPolicy
		pol.RequiredGates, pol.ApprovalHook = nil, nil
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

// limitIdentity is one identity without the parts the reader does not decode.
// A copy: id's lists are not modified.
func (r *BundleReader) limitIdentity(id ShapeIdentity) ShapeIdentity {
	if !r.Supports(BundleFeatureIdentitySignIn) {
		id.Owners, id.OwnerRole, id.Signup, id.Methods = nil, "", nil, nil
		id.PasskeyRPID, id.Social, id.Email, id.DevMail = "", nil, nil, ""
		// The login_policy output is a sign-in addition too: a reader predating
		// it rejects a consumer that maps or omits it as an unknown output.
		id.Consumers = withoutLoginPolicyOutput(id.Consumers)
	}
	if !r.Supports(BundleFeatureIdentityCodeLifetime) && id.CodeLifetime != "" {
		id.CodeLifetime = ""
	}
	return id
}

// CapabilityEvaluatesScopedVulnExemptions is advertised explicitly by a control
// plane only once promotion and vulnerability reads evaluate exemptions against
// occurrence data. It is deliberately NOT in BundleDocFeatures: decoding the
// scoped field alone does not mean the server honours it.
const CapabilityEvaluatesScopedVulnExemptions = "evaluates-scoped-vuln-exemptions"

// CheckScopedVulnExemptions refuses a scoped policy unless the reader both
// decodes and evaluates it. Call before declaration or recording, before LimitShape
// can strip unsupported fields. The nil reader is this forge's local evaluator.
func (r *BundleReader) CheckScopedVulnExemptions(s Shape) error {
	if s.DeliveryPolicy == nil || len(s.DeliveryPolicy.VulnGate.ScopedExemptions) == 0 {
		return nil
	}
	if r.Supports(BundleFeatureVulnScopedExemptions) && r.Supports(CapabilityEvaluatesScopedVulnExemptions) {
		return nil
	}
	return fmt.Errorf("this env declares scoped vulnerability exemptions, but this control plane does not advertise %q and %q; upgrade the control plane to occurrence-aware scoped evaluation before using this policy", BundleFeatureVulnScopedExemptions, CapabilityEvaluatesScopedVulnExemptions)
}

// CheckIdentities refuses a shape with several identities to a reader that
// does not converge them: one that does not advertise
// [CapabilityConvergesSeveralIdentities] would record the shape and converge
// none of its identities (or, predating [BundleFeatureIdentities], be sent
// none at all), so every workload reading them would wait on Secrets nobody
// writes. A shape with one identity, and the nil reader (this forge's own
// machine ledger), always pass. Callers run it before anything is written.
func (r *BundleReader) CheckIdentities(s Shape) error {
	if len(s.Identities) == 0 || r.Supports(CapabilityConvergesSeveralIdentities) {
		return nil
	}
	names := make([]string, 0, len(s.Identities))
	for _, i := range s.Identities {
		names = append(names, i.Name)
	}
	return fmt.Errorf("this env declares %d managed identities (%s), and this control plane converges only one per env "+
		"(it does not advertise %q). Upgrade the control plane, or declare one identity in this env until it is",
		len(names), strings.Join(names, ", "), CapabilityConvergesSeveralIdentities)
}

// CheckNamedSiteAPIs refuses static sites that declare named same-origin
// APIs (sites names them) to a reader that does not serve them: one that
// does not advertise [CapabilityServesNamedSiteAPIs] would publish the site
// and answer every /_api/@<name> call with a 404. No such site, and the nil
// reader (this forge's own machine ledger), always pass. Callers run it
// before anything is written.
func (r *BundleReader) CheckNamedSiteAPIs(sites []string) error {
	if len(sites) == 0 || r.Supports(CapabilityServesNamedSiteAPIs) {
		return nil
	}
	return fmt.Errorf("static site %s declares named same-origin APIs (Frontend.apis, served at /_api/@<name>), and this control plane "+
		"does not serve them (it does not advertise %q), so every call to one would 404. Upgrade the control plane, or call those "+
		"backends by URL (a literal in runtime_config) until it is",
		strings.Join(sites, ", "), CapabilityServesNamedSiteAPIs)
}

// CheckAutoscaledWorkloads refuses hosted workloads that declare autoscale
// (workloads names them) to a reader that does not apply it: one that does
// not advertise [CapabilityAutoscalesWorkloads] would run each at one replica
// and never scale it. One error per workload, joined. No such workload, and
// the nil reader (this forge's own machine ledger), always pass. Callers run
// it before anything is written.
func (r *BundleReader) CheckAutoscaledWorkloads(workloads []string) error {
	if len(workloads) == 0 || r.Supports(CapabilityAutoscalesWorkloads) {
		return nil
	}
	errs := make([]error, 0, len(workloads))
	for _, name := range workloads {
		errs = append(errs, fmt.Errorf("workload %q declares autoscale, but this control plane does not apply HorizontalPodAutoscalers yet "+
			"(it does not advertise %q); set replicas instead until it is upgraded", name, CapabilityAutoscalesWorkloads))
	}
	return errors.Join(errs...)
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
