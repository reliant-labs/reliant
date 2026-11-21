package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Invalidate is the per-deploy CDN cache policy.
//
//   - entrypoints (DEFAULT) — invalidate only the documents that cannot be
//     content-addressed: the HTML entry points and the runtime config
//     document. Content-hashed assets never need invalidating, so this is
//     O(a few paths) per deploy regardless of site size.
//   - none — invalidate nothing. Correct for a short TTL or a promotion that
//     did not change the bytes.
//   - all — purge /*. Correct only for a build that does not hash asset
//     filenames. Invalidation is rate-limited per project, so this is a
//     deliberate choice with a real cost and never a default.
//
// +kubebuilder:validation:Enum=entrypoints;none;all
type Invalidate string

const (
	InvalidateEntrypoints Invalidate = "entrypoints"
	InvalidateNone        Invalidate = "none"
	InvalidateAll         Invalidate = "all"
)

// StaticSiteCDN is the CDN in front of the site's bucket, and what a deploy
// does to the edge cache afterwards. Neither side creates the CDN: URLMap names
// an EXISTING load-balancer URL map whose backend is the bucket.
type StaticSiteCDN struct {
	// +kubebuilder:validation:MinLength=1
	URLMap string `json:"urlMap"`

	// Invalidate is the per-deploy cache policy. Empty means entrypoints.
	// The zero value MUST NOT mean all: an unset field is exactly the case
	// where the quota-expensive choice is least likely to have been
	// considered.
	// +optional
	// +kubebuilder:default=entrypoints
	Invalidate Invalidate `json:"invalidate,omitempty"`

	// ExtraInvalidatePaths adds absolute site paths to the entrypoints set,
	// for documents the planner cannot infer (a bare /manifest.json, a
	// service worker). Ignored under none and all.
	// +optional
	ExtraInvalidatePaths []string `json:"extraInvalidatePaths,omitempty"`
}

// EffectiveInvalidate resolves an unset policy to entrypoints.
func (c StaticSiteCDN) EffectiveInvalidate() Invalidate {
	if c.Invalidate == "" {
		return InvalidateEntrypoints
	}
	return c.Invalidate
}

// StaticSiteSpec is a site served from an object-storage bucket, optionally
// behind a CDN.
//
// # WHAT A DEPLOY IS
//
// Both sides already implemented the same model, and it is kept. An
// assembled tree is archived IMMUTABLY under releases/<digest>/, and the
// live/ prefix is synced from one release. Deploy and promotion are
// therefore the same mechanical move: re-point live/ at an archive that
// already exists. Neither rebuilds, so a promotion cannot produce different
// bytes than the release that was tested.
//
// MERGE DECISIONS:
//
//   - The DEPLOYED spec carries control-plane's fields (the digests to serve
//     and to protect, retention, CDN). forge's BUILD inputs (public_dir,
//     bundle, cache_control) are not here, because they describe how to
//     PRODUCE a release, not what to serve. They stay on forge's KCL
//     StaticSite build block, which runs before an artifact exists and is
//     deliberately not a CR field. A CR carrying public_dir would name a
//     directory on a machine the operator never sees.
//   - The digests are SPEC, not status (control-plane's design, on merit).
//     LiveDigest is the pointer whose movement IS the deploy.
//     PreviousDigest and RetainedDigests are what retention must never
//     delete. The retention guarantee has to arrive from the release ledger
//     and not be inferred from the bucket, and a value the executor
//     computed for itself could be lost to a status wipe, after which
//     retention would no longer know what to protect.
//   - Nothing here encodes HOW bytes move. forge's `gcloud storage cp`
//     executor is to be replaced by a single storage-SDK planner. These
//     types describe only the desired pointer and the retention set, so
//     they fit either executor.
type StaticSiteSpec struct {
	// Bucket is the bucket the site's prefixes live in: gs://name or a bare
	// name. Self-hosted, the user's own bucket. Hosted, the platform assigns
	// a shared bucket and a per-customer prefix and ignores this value.
	// +optional
	// +kubebuilder:validation:MaxLength=222
	Bucket string `json:"bucket,omitempty"`

	// BasePath mounts the site under a sub-path ("/admin"). Empty is the
	// root.
	// +optional
	// +kubebuilder:validation:Pattern=`^/.*$`
	BasePath string `json:"basePath,omitempty"`

	// AssetDir is the build's CONTENT-HASHED asset directory, relative to
	// the site's mount (BasePath): "assets" for a Vite build, "_next/static"
	// for a Next.js export. Every file under it is named for its bytes.
	//
	// It is a SERVING fact, not a build input, because two serving
	// guarantees hang off it and neither can be inferred from a bucket:
	//
	//   - CACHE POLICY. Files under it may be cached forever
	//     (`public, max-age=31536000, immutable`); everything else — the
	//     entry document, config.js, version.json — must revalidate
	//     (`no-cache`), so a deploy is visible on the next load.
	//   - STALE TABS KEEP WORKING. A page loaded before a deploy lazy-loads
	//     chunks the new release no longer contains. The server keeps the
	//     previous releases' files under this directory serveable, so that
	//     import succeeds instead of failing on a 404 (or, worse, on an HTML
	//     fallback parsed as JavaScript).
	//
	// Empty means the build declares none: no immutable caching and no
	// carried-forward assets.
	// +optional
	// +kubebuilder:validation:MaxLength=255
	AssetDir string `json:"assetDir,omitempty"`

	// ReleaseRepository is the OCI repository LiveDigest was pushed to —
	// the whole reference, registry host included, with no tag or digest
	// on it. Together with LiveDigest it is the complete address of the
	// release: `<releaseRepository>@<liveDigest>`.
	//
	// IT IS RECORDED, NOT RECONSTRUCTED, and that is the whole point of
	// the field. A site's release goes to the frontend's OWN declared
	// image reference plus the platform's layout segment
	// (deploytarget.HostedStaticRepository), which is a reference only the
	// builder knows: it may be an org's registry subtree, or a registry
	// the platform does not host at all. Anything that recomposed a path
	// from a registry base, an org id and a site name would be writing
	// down a second, independent derivation of where the bytes are — and
	// when the two disagree, the puller 404s on an artifact that exists.
	// That is exactly the defect this field closes, and it is the same
	// rule a workload's image already follows: the reference carries its
	// registry, and the release ledger keys artifacts by full reference.
	//
	// WHERE a caller may push is therefore a platform policy enforced as
	// ADMISSION on this recorded value — a reference inside the platform's
	// own registry must be inside that org's subtree — never by deriving
	// the value and calling the derivation a boundary.
	//
	// Empty before the first deploy, like LiveDigest. A spec carrying a
	// digest but no repository is not pullable, and whoever executes it
	// must refuse rather than guess a path.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	ReleaseRepository string `json:"releaseRepository,omitempty"`

	// LiveDigest is the release the live prefix must serve. A digest, never
	// a tag: a tag is a mutable pointer, and re-pointing one would void
	// "the bytes that passed staging are the bytes in prod". Empty before
	// the first deploy. Addressed within ReleaseRepository.
	// +optional
	// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
	LiveDigest string `json:"liveDigest,omitempty"`

	// PreviousDigest is the release that was live before LiveDigest.
	// Retention never deletes it: pages loaded from it moments ago are still
	// fetching its hashed assets.
	// +optional
	// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
	PreviousDigest string `json:"previousDigest,omitempty"`

	// RetainedDigests are releases that MUST survive retention (every digest
	// an environment is promoted to, plus any pin), projected from the
	// release ledger. A retention planner that cannot resolve its protected
	// set deletes NOTHING. It never reads "I know of nothing to protect" as
	// "nothing needs protecting".
	// +optional
	RetainedDigests []string `json:"retainedDigests,omitempty"`

	// KeepReleases is how many archived releases to retain, newest first.
	// Unset means DefaultKeepReleases. An explicit 0 retains everything.
	// Otherwise it must be at least MinKeepReleases, because keeping fewer
	// than the live release plus its predecessor would delete assets pages
	// loaded moments ago still fetch.
	//
	// A POINTER because there are three states. control-plane's plain
	// omitempty int32 made "unset" and "retain everything" the same value,
	// so a CR that omitted the field silently disabled retention. forge's
	// KCL defaulted to 10. Both are honoured now: nil is the default, and
	// 0 is a deliberate choice.
	// +optional
	// +kubebuilder:validation:Minimum=0
	KeepReleases *int32 `json:"keepReleases,omitempty"`

	// Entrypoints are the site-absolute paths that cannot be
	// content-addressed and are invalidated on every deploy. Empty uses the
	// planner's default set.
	// +optional
	Entrypoints []string `json:"entrypoints,omitempty"`

	// CDN is the CDN in front of the bucket. Omitted means a bucket served
	// directly, with no invalidation.
	// +optional
	CDN *StaticSiteCDN `json:"cdn,omitempty"`

	// Domains are custom hostnames, in addition to the default one.
	// +optional
	// +kubebuilder:validation:MaxItems=8
	Domains []string `json:"domains,omitempty"`

	// RuntimeConfig is the site's runtime configuration document: the
	// object the browser receives as window.__FORGE_CONFIG__, keyed by the
	// name the bundle reads (API_URL, OIDC_ISSUER, ...).
	//
	// It is SPEC, not a file inside the release, and that is the point.
	// A release artifact that carried its own config.js would be frozen to
	// the environment it was built for, and promoting it would ship
	// staging's backend URL to production. Keeping the document here makes
	// the artifact environment-agnostic: whoever executes the spec writes
	// `live/<basePath>/config.js` AFTER syncing live/ from the release, on
	// every sync (a sync prunes files the release does not contain), as
	//
	//	window.__FORGE_CONFIG__ = {"KEY": "resolved value", ...};
	//
	// (pkg/deploy.RuntimeConfigJS renders exactly that document.) An entry
	// is either a literal or a reference to another workload's URL, which
	// is resolved where the URL is known: by the control plane for a
	// hosted env, at render time by forge otherwise. See RuntimeConfigValue.
	// +optional
	// +kubebuilder:validation:MaxProperties=128
	RuntimeConfig map[string]RuntimeConfigValue `json:"runtimeConfig,omitempty"`
}

// MinKeepReleases is the retention floor: the live release plus its
// predecessor.
const MinKeepReleases = 2

// DefaultKeepReleases is forge's default retention depth.
const DefaultKeepReleases = 10

// StaticSiteStatus is the observed state of a StaticSite.
type StaticSiteStatus struct {
	TierStatus `json:",inline"`

	// BucketPrefix is the site's root prefix within its bucket, RECORDED
	// rather than re-derived, so that a later change to the derivation rule
	// cannot silently re-point a prefix that already holds live objects.
	// +optional
	BucketPrefix string `json:"bucketPrefix,omitempty"`

	// LiveDigest is what the live prefix was CONFIRMED to serve. It lags
	// spec.liveDigest until a sync succeeds, and that gap is the drift
	// signal. PreviousDigest is its confirmed predecessor.
	// +optional
	LiveDigest string `json:"liveDigest,omitempty"`
	// +optional
	PreviousDigest string `json:"previousDigest,omitempty"`

	// RecentDigests are the releases the live prefix CONFIRMED serving
	// before LiveDigest, newest first (PreviousDigest leads). The executor
	// keeps their files under spec.assetDir serveable — a page loaded from
	// one of them still lazy-loads its chunks after a deploy — so retention
	// must never delete them. Ordered history that a bucket listing cannot
	// give: archive prefixes carry no time.
	// +optional
	// +kubebuilder:validation:MaxItems=32
	RecentDigests []string `json:"recentDigests,omitempty"`

	// ReleaseCount is how many archived releases the last retention pass
	// observed.
	// +optional
	// +kubebuilder:validation:Minimum=0
	ReleaseCount int32 `json:"releaseCount,omitempty"`

	// LastSyncedAt is nil until the live prefix has synced once.
	// +optional
	LastSyncedAt *metav1.Time `json:"lastSyncedAt,omitempty"`
}

// StaticSite is a static site served from object storage.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ss,categories=forge
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Hostname",type=string,JSONPath=`.status.hostname`
// +kubebuilder:printcolumn:name="Live",type=string,JSONPath=`.status.liveDigest`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type StaticSite struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   StaticSiteSpec   `json:"spec"`
	Status StaticSiteStatus `json:"status,omitempty"`
}

// StaticSiteList contains a list of StaticSite.
//
// +kubebuilder:object:root=true
type StaticSiteList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []StaticSite `json:"items"`
}

// EffectiveKeepReleases applies the default and the predecessor floor. The floor
// is applied here, on the type, so that no retention planner is one forgotten
// max() away from deleting the predecessor. 0 means "retain everything" and
// is not floored. Validate refuses 1 outright; the floor here is the
// defense-in-depth for a spec that skipped it.
func (s StaticSiteSpec) EffectiveKeepReleases() int32 {
	switch {
	case s.KeepReleases == nil:
		return DefaultKeepReleases
	case *s.KeepReleases == 0:
		return 0
	case *s.KeepReleases < MinKeepReleases:
		return MinKeepReleases
	default:
		return *s.KeepReleases
	}
}
