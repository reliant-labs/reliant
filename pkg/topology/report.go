package topology

import (
	"github.com/reliant-labs/forge/pkg/release"
)

// The destination vocabulary: where an environment runs, as one word. Every
// deployable part votes by its own runtime binding; one kind is that kind,
// several are "mixed". Hosted is one vote among them, not an env mode.
const (
	DestinationHosted  = "hosted"
	DestinationCluster = "cluster"
	DestinationCompose = "compose"
	DestinationHost    = "host"
	DestinationStatic  = "static"
	// DestinationExternal: every workload runs on the author's own deploy
	// tooling (forge.OnExternal).
	DestinationExternal = "external"
	DestinationMixed    = "mixed"
)

// The placement vocabulary: where one part runs.
const (
	PlacementHosted    = "hosted"     // the control plane's platform (OnHosted, a hosted database)
	PlacementCluster   = "cluster"    // a Kubernetes cluster the author operates (OnCluster, a cluster database)
	PlacementBucket    = "bucket"     // the author's object storage (OnBucket)
	PlacementFirebase  = "firebase"   // Firebase Hosting (OnFirebase)
	PlacementCompose   = "compose"    // docker compose on a developer machine
	PlacementHost      = "host"       // a host process / dev server on a developer machine
	PlacementBuildOnly = "build-only" // built, never run
	PlacementExternal  = "external"   // the author's own deploy tooling (OnExternal)
)

// The actuator vocabulary: WHO applies a part.
const (
	ActuatorPlatform = "platform" // the control plane converges it
	ActuatorHub      = "hub"      // the control plane's hub converges its delivery unit onto a connected cluster
	ActuatorForge    = "forge"    // `forge env deploy` applies it from the deploying machine
	ActuatorMachine  = "machine"  // `forge env up` runs it on a developer machine
	ActuatorNone     = "none"     // nothing runs it
	ActuatorExternal = "external" // the author's tooling applies it, through forge's hooks or on its own (OnExternal)
)

// The report source vocabulary ([Report.Source]).
const (
	// SourceCheckout: gathered from a forge checkout by the CLI. The CLI
	// leaves Source empty, which means the same thing, so its document is
	// unchanged from before the field existed.
	SourceCheckout = "checkout"
	// SourceControlPlane: gathered by a control plane from its own records.
	SourceControlPlane = "control_plane"
)

// ObservedByPlatform is [Image.ObservedBy] for a state the control plane
// observed itself (a hosted deployment's observed digest), rather than one
// `--verify` read from a cluster with the caller's credentials.
const ObservedByPlatform = "platform"

// Image is one image's row in one environment.
type Image struct {
	// Image is the bare image name, as it keys the binding's resolved map
	// and the release's artifacts ("control-plane", "reliant").
	Image string `json:"image"`
	// Digest is the digest the binding FROZE at promote time. This is the
	// declaration, not an observation.
	Digest string `json:"digest"`
	// State is the reconciliation verdict, "not_verified" unless something
	// compared it. See ImageState: not_verified is unknown, never OK.
	State ImageState `json:"state"`
	// Running is the digest (or tag reference) actually observed.
	Running string `json:"running,omitempty"`
	// Detail is the human-readable reason for a non-match verdict.
	Detail string `json:"detail,omitempty"`
	// ObservedBy names who observed State when it was not the caller's own
	// --verify: ObservedByPlatform for the control plane. Empty otherwise.
	ObservedBy string `json:"observed_by,omitempty"`
}

// Lag is how far behind the newest cut release an environment is.
//
// TWO UNITS, BECAUSE THEY ANSWER DIFFERENT QUESTIONS AND DISAGREE. "15
// releases behind" measures how much CHANGE has accumulated; "10 weeks
// behind" measures how STALE the environment is. An env can be one release
// behind and six months stale, or fifteen releases behind and a day stale.
type Lag struct {
	// LatestRelease is the newest release known to the gatherer, which is
	// what the lag is measured against. A checkout knows only the releases
	// in its ledger; a control plane knows every release recorded on it.
	LatestRelease string `json:"latest_release"`
	// Current is true when the env is bound to the newest release. When
	// true both lag figures are zero.
	Current bool `json:"current"`
	// ReleasesBehind counts how many releases were cut AFTER the one this
	// env runs.
	ReleasesBehind int `json:"releases_behind"`
	// BehindSeconds is the wall-clock gap between the newest release's
	// cut time and this env's release's. Zero when either is unknown,
	// which Behind then reports as empty rather than as "0s".
	BehindSeconds int64 `json:"behind_seconds,omitempty"`
	// Behind is BehindSeconds rendered for humans ("71d13h").
	Behind string `json:"behind,omitempty"`
}

// GatesSummary is the one-line count of a promotion's evidence, both halves:
// what was claimed at promote time and what was recorded afterwards.
type GatesSummary struct {
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
	Errored int `json:"errored"`
}

// SummarizeGates counts gates by their stage status across every group. Nil
// when there are none, so a promotion with no evidence carries no summary.
func SummarizeGates(groups ...[]release.Gate) *GatesSummary {
	var s GatesSummary
	n := 0
	for _, gates := range groups {
		for _, g := range gates {
			n++
			switch release.StageStatusOfGate(g) {
			case release.StagePassed:
				s.Passed++
			case release.StageFailed:
				s.Failed++
			case release.StageSkipped:
				s.Skipped++
			default:
				s.Errored++
			}
		}
	}
	if n == 0 {
		return nil
	}
	return &s
}

// Placement is one deployable part of an env and where it runs.
type Placement struct {
	// Name is the workload, frontend or database name.
	Name string `json:"name"`
	// Kind is "workload", "frontend" or "database".
	Kind string `json:"kind"`
	// Placement is one of the Placement* constants.
	Placement string `json:"placement"`
	// Cluster is the rendered cluster a cluster part lands on, "" otherwise.
	Cluster string `json:"cluster,omitempty"`
	// Actuator is one of the Actuator* constants, or "" when the gatherer
	// cannot know it (never guessed).
	Actuator string `json:"actuator"`
}

// Workload is one hosted deployment's status, as the control plane reports
// it.
type Workload struct {
	Name string `json:"name"`
	// Tier is workload | database | static.
	Tier string `json:"tier,omitempty"`
	// URL is the platform-allocated public URL, once one exists.
	URL string `json:"url,omitempty"`
	// Hostname is URL's host, for a badge that has no room for a scheme.
	Hostname string `json:"hostname,omitempty"`
	// ServingPath is where Hostname's DNS sends its traffic, as the control
	// plane resolves it: edge | ingress | elsewhere, or empty when it did
	// not say.
	ServingPath string `json:"serving_path,omitempty"`
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
	Domains []CustomDomain `json:"domains,omitempty"`
}

// CustomDomain is one declared custom hostname's observed state.
type CustomDomain struct {
	Domain string `json:"domain"`
	// State is the lower-case vocabulary: pending_dns | verifying |
	// issuing | live | failed | conflict | unknown.
	State string `json:"state"`
	// RequiredRecords are the DNS records still to be set.
	RequiredRecords []DNSRecord `json:"required_records,omitempty"`
	LastError       string      `json:"last_error,omitempty"`
	// LiveSince is RFC3339 for when the domain started serving, empty
	// until it does.
	LiveSince string `json:"live_since,omitempty"`
	// ServingPath is where the domain's DNS sends its traffic: edge |
	// ingress | elsewhere, or empty when the control plane did not say.
	ServingPath string `json:"serving_path,omitempty"`
}

// DNSRecord is one record the author must publish at their registrar before
// a domain can go live.
type DNSRecord struct {
	// Type is A | CNAME | TXT.
	Type string `json:"type,omitempty"`
	// Name is the record name, as the registrar wants it.
	Name string `json:"name,omitempty"`
	// Value is the record's value — an address, a target, or a token.
	Value string `json:"value,omitempty"`
}

// BundleGit is the source provenance of an env's current config bundle — as
// distinct from its release's. A clean release can ship with config rendered
// from a dirty tree; this is where that shows.
type BundleGit struct {
	Commit string `json:"commit,omitempty"`
	Dirty  bool   `json:"dirty,omitempty"`
}

// Catalog is the provenance of the declared-environment list a control plane
// read the report's untracked envs from: the newest released bundle that
// carried one. Absent when none did ("no topology published yet").
type Catalog struct {
	Release    string `json:"release,omitempty"`
	Commit     string `json:"commit,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Dirty      bool   `json:"dirty,omitempty"`
	RecordedAt string `json:"recorded_at,omitempty"`
	BundleID   string `json:"bundle_id,omitempty"`
}

// Env is one environment's full row: what it is bound to, where that came
// from, where it runs, and how far behind it is.
type Env struct {
	// Env is the environment name.
	Env string `json:"env"`
	// Lifecycle is the env's declared Bundle.lifecycle ("local",
	// "ephemeral", or "" when unset or unknown).
	Lifecycle string `json:"lifecycle"`
	// Declared is true when the gatherer's source declares the env
	// (deploy/kcl/<env>/main.k). An env can be bound without being
	// declared there — a release promoted on a branch that has the env —
	// and that is a real, reportable state rather than an error.
	Declared bool `json:"declared"`
	// Ledger is where THIS env's promotions are recorded: a control
	// plane's endpoint URL or a file ledger's location. Opaque — for
	// display only.
	Ledger string `json:"ledger,omitempty"`
	// Hosted is true when Ledger is a control plane. It drives the CLI's
	// text renderer only and is NOT part of the JSON contract: consumers
	// read `destination == "hosted"`.
	Hosted bool `json:"-"`
	// Kind is the current entry's kind. Always "promote": a legacy
	// "rollback" entry reads as the promote of its release.
	Kind release.PromotionKind `json:"kind,omitempty"`
	// Bound is false for an env that has never been promoted. Not a
	// failure: it has declared nothing, so there is nothing to be wrong
	// about.
	Bound bool `json:"bound"`
	// Release is the version label the binding names. Empty when unbound.
	Release string `json:"release,omitempty"`
	// PromotedAt is RFC3339 for when the env was PROMOTED — not when it
	// was deployed. Promotion writes a pointer; deployment moves bytes.
	PromotedAt string `json:"promoted_at,omitempty"`
	// ReleaseKnown is true when the gatherer holds the release record for
	// Release. FALSE IS NOT AN ERROR: a release cut elsewhere leaves a
	// real binding whose provenance and lag are unavailable, so Git,
	// ReleaseCreatedAt and Lag are omitted rather than guessed.
	ReleaseKnown bool `json:"release_known"`
	// Git is the source provenance of the bound release. Read `dirty`: a
	// release cut from a tree with uncommitted changes ships bytes that
	// correspond to no reviewable commit.
	Git *release.Git `json:"git,omitempty"`
	// ReleaseCreatedAt is RFC3339 for when the bound release was CUT.
	ReleaseCreatedAt string `json:"release_created_at,omitempty"`
	// KubeContext and Namespace are where this env runs on a cluster the
	// author operates, resolved declaratively. Empty for a hosted, host or
	// compose env.
	KubeContext string `json:"kube_context,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	// Lag is how far behind the newest release this env is. Nil when it
	// cannot be computed: unbound, or a release the gatherer lacks.
	Lag *Lag `json:"lag,omitempty"`
	// Images is one row per image the binding declares, sorted by name.
	// Always non-nil so consumers see [] rather than null.
	Images []Image `json:"images"`
	// Note explains a state that would otherwise look like missing data.
	Note string `json:"note,omitempty"`

	// Destination is one of the Destination* constants; see the
	// vocabulary above. ControlPlaneKind is "local" | "persistent" for an
	// env that declares control_plane. Endpoint is that control plane's
	// URL. EnvironmentID, Verdict and Workloads are hosted-only, and
	// EnvironmentID is empty (never fabricated) for a hosted env that has
	// not been deployed.
	Destination      string     `json:"destination,omitempty"`
	ControlPlaneKind string     `json:"control_plane_kind,omitempty"`
	Endpoint         string     `json:"endpoint,omitempty"`
	EnvironmentID    string     `json:"environment_id,omitempty"`
	Verdict          string     `json:"verdict,omitempty"`
	Workloads        []Workload `json:"workloads,omitempty"`
	// Placements is every part of the env and where it runs.
	Placements []Placement `json:"placements,omitempty"`

	// PromotionID is the current binding's ledger entry — the value
	// `promote --expect-current` and `--from-promotion` take.
	PromotionID string `json:"promotion_id,omitempty"`
	// FromEnv is the environment the current binding was promoted from.
	FromEnv string `json:"from_env,omitempty"`
	// GatesSummary counts the current binding's evidence, both halves.
	// Absent when it carries none.
	GatesSummary *GatesSummary `json:"gates_summary,omitempty"`
	// RolloutPhase is the current promotion's rollout phase as the control
	// plane computes it: "succeeded", "progressing", "degraded", "unknown"
	// … Envs whose ledger is a control plane only.
	RolloutPhase string `json:"rollout_phase,omitempty"`
	// NotApplied is true when the ledger records that the current
	// binding's apply FAILED: Release is what the env is bound to, NOT what
	// it runs. ApplyError is what the apply said.
	NotApplied bool   `json:"not_applied,omitempty"`
	ApplyError string `json:"apply_error,omitempty"`

	// Tracked reports whether a control plane holds a record of this env.
	// Nil in a checkout's report (the CLI does not know); false for an env
	// a control plane knows only from the declared-env catalog — declared
	// in code, outside the control plane.
	Tracked *bool `json:"tracked,omitempty"`
	// BundleDigest and BundleGit are the env's current config bundle and
	// its own provenance, distinct from the release's.
	BundleDigest string     `json:"bundle_digest,omitempty"`
	BundleGit    *BundleGit `json:"bundle_git,omitempty"`
}

// Tally counts image cells by state across every environment, so a consumer
// can badge the whole screen without walking the matrix itself.
type Tally struct {
	NotVerified int `json:"not_verified"`
	Match       int `json:"match"`
	Drift       int `json:"drift"`
	Missing     int `json:"missing"`
	Untagged    int `json:"untagged"`
	Unreachable int `json:"unreachable"`
}

// Report is the whole screen: `forge env status --json` with no env named.
type Report struct {
	// Project is the forge project name.
	Project string `json:"project,omitempty"`
	// Ledger names where bindings were read from — a path for a file
	// ledger, a URL for a control plane. An opaque label for DISPLAY.
	Ledger string `json:"ledger"`
	// GeneratedAt is RFC3339 for when this snapshot was taken.
	GeneratedAt string `json:"generated_at"`
	// LatestRelease is the newest release known — the baseline every
	// env's lag is measured against. Empty when none was cut.
	LatestRelease string `json:"latest_release,omitempty"`
	// LatestReleaseCreatedAt is RFC3339 for when that release was cut.
	LatestReleaseCreatedAt string `json:"latest_release_created_at,omitempty"`
	// Releases is every known release, NEWEST FIRST. Always non-nil.
	Releases []string `json:"releases"`
	// Images is the union of every image name across every environment,
	// sorted: THE MATRIX'S COLUMN AXIS. An image present in prod and
	// absent from staging shows up as a gap only if the column exists.
	// Always non-nil.
	Images []string `json:"images"`
	// Environments is one row per environment, sorted by name.
	Environments []Env `json:"environments"`
	// Verified reports whether a verification pass ran. When false EVERY
	// image state the caller did not observe is "not_verified".
	Verified bool `json:"verified"`
	// Tally counts image cells by state across all environments.
	Tally Tally `json:"tally"`
	// OK is false exactly when ExitCode is non-zero. Unverified, it is
	// always true: reading a ledger cannot prove anything wrong.
	OK bool `json:"ok"`
	// Detail carries the one-line reason for a non-OK result.
	Detail string `json:"detail,omitempty"`

	// Source is SourceControlPlane for a control plane's report. Empty
	// (meaning SourceCheckout) for the CLI's.
	Source string `json:"source,omitempty"`
	// Catalog is the provenance of the declared-env list a control plane
	// used. Absent when none was available.
	Catalog *Catalog `json:"catalog,omitempty"`
}
