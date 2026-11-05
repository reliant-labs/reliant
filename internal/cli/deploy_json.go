package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/internal/cluster"
)

// The deploy REPORT — the whole invocation as one machine-readable document.
//
// WHY THIS FILE EXISTS. A forge UI now has read-only screens (topology,
// secrets, env status, audit) and a guarded promote, and the rule those were
// built on is that THE UI NEVER COMPUTES ANYTHING THE CLI CANNOT REPORT.
// Deploy was the last command with no machine-readable output at all: 51
// fmt.Printf calls and nothing else, so the only way to learn what a deploy
// did was to read its prose. A consumer that scrapes prose is a consumer that
// silently starts lying the first time a message is reworded.
//
// ONE DEPLOY BODY, TWO RENDERERS, AND THAT IS THE LOAD-BEARING CONSTRAINT.
// This report is INSTRUMENTATION threaded through the existing deploy path,
// not a second computation of the same facts. Every method here is nil-safe,
// so text mode passes a nil *deployReport and runs byte-identically, while
// --json runs the SAME code and reads the accumulator afterwards. A preview
// that disagrees with the deploy would be worse than no preview: it converts
// "I did not look" into "I looked and was told the wrong thing". The exit code
// is the deploy's own returned error either way, so the two modes cannot
// diverge about success.
//
// WHAT A CONSUMER NEEDS, AND WHY EACH PIECE IS MODELLED EXPLICITLY.
//
//   - MODE, unambiguously. "Did bytes move?" must never be inferred. explain
//     and dry_run touch nothing; apply does. A consumer that has to
//     deduce this from the absence of a field will get it wrong on the day it
//     matters.
//   - The GUARD, as data. The deploy applies to the context the env's KCL
//     DECLARES, never the ambient current-context, and the only failure is a
//     declared context absent from the kubeconfig. That verdict is the single
//     most consequential thing to show a user before they confirm.
//   - The TARGET. Which cluster and namespace, so a UI can put the words
//     "production" in front of someone before they press the button.
//   - PINNING, per image. Live verify already caught prod's internal-console
//     running by mutable tag, so "this deploy ships a tag, not a digest" is a
//     real safety fact and gets a field rather than a footnote.
//   - ROLLOUT, three ways. ready / failed / (timed_out | not_waited). A
//     timeout is NOT a success and NOT a failure — it is the absence of an
//     answer, and a document that collapses it into either one is the "green
//     deploy over a broken environment" failure this project already paid for
//     once.
//
// ZERO VALUES ARE THE SAFE READING, EVERYWHERE. Every enum below has
// "unknown" as its zero value and an UnmarshalJSON that REFUSES a string it
// does not recognise. The tempting zero values — allow, ready, digest,
// applied — are each the one answer that makes a real problem invisible, and a
// lenient decoder in an older binary reading a newer forge's output would
// produce exactly that. promote_plan.go's decoder is the model; this follows
// it deliberately rather than by coincidence.

// ─── Mode. What this invocation actually DID ──────────────────────────────────

// deployJSONMode is the operation performed, and the answer to the only
// question a consumer must never have to infer: did bytes move?
//
// Zero value is unknown. "apply" as a zero value would tell a reader a deploy
// happened when the struct was simply never populated.
type deployJSONMode int

const (
	// deployModeUnknown: not recorded. Never produced deliberately.
	deployModeUnknown deployJSONMode = iota
	// deployModeExplain: --explain. Printed the guard verdict and exited.
	// Nothing was rendered, nothing was applied.
	deployModeExplain
	// deployModeDryRun: --dry-run. The env was rendered and the guard ran,
	// and the pipeline returned BEFORE any kubectl apply.
	deployModeDryRun
	// deployModeApply: a real deploy. Manifests reached the cluster.
	deployModeApply
)

func (m deployJSONMode) String() string {
	switch m {
	case deployModeExplain:
		return "explain"
	case deployModeDryRun:
		return "dry_run"
	case deployModeApply:
		return "apply"
	default:
		return "unknown"
	}
}

// MarshalJSON emits the lowercase form, derived from String() so the text
// report and the JSON value cannot disagree about what happened.
func (m deployJSONMode) MarshalJSON() ([]byte, error) {
	return []byte(`"` + m.String() + `"`), nil
}

// UnmarshalJSON rejects an unrecognised mode. Defaulting would let a newer
// forge's mode decode as one of the read-only ones, telling a consumer nothing
// was applied when something was.
func (m *deployJSONMode) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return fmt.Errorf("deploy mode must be a string: %w", err)
	}
	switch strings.ToLower(name) {
	case "unknown":
		*m = deployModeUnknown
	case "explain":
		*m = deployModeExplain
	case "dry_run":
		*m = deployModeDryRun
	case "apply":
		*m = deployModeApply
	default:
		return fmt.Errorf("unknown deploy mode %q (expected explain, dry_run or apply) — "+
			"refusing to decode it as a default, which would report a real apply as a preview", name)
	}
	return nil
}

// Writes reports whether this mode MUTATES the target. It exists so a consumer
// asks the question once, here, instead of each caller re-deriving it from the
// mode set and getting it wrong when a mode is added.
func (m deployJSONMode) Writes() bool {
	return m == deployModeApply
}

// ─── The declared-context guard ───────────────────────────────────────────────

// deployJSONGuardVerdict is whether forge will deploy at all.
//
// Zero value is unknown, NOT allow. An unpopulated guard must never read as
// permission — this is the check that makes a wrong-cluster deploy impossible,
// and a consumer treating "nobody computed it" as "go ahead" defeats it
// entirely.
type deployJSONGuardVerdict int

const (
	// deployGuardVerdictUnknown: the guard was not evaluated.
	deployGuardVerdictUnknown deployJSONGuardVerdict = iota
	// deployGuardVerdictAllow: the deploy may proceed against the declared
	// context (or the env declares no cluster, so there is nothing to guard).
	deployGuardVerdictAllow
	// deployGuardVerdictRefuse: forge will not deploy. Reason says why and
	// Fix says what would change it.
	deployGuardVerdictRefuse
)

func (v deployJSONGuardVerdict) String() string {
	switch v {
	case deployGuardVerdictAllow:
		return "allow"
	case deployGuardVerdictRefuse:
		return "refuse"
	default:
		return "unknown"
	}
}

func (v deployJSONGuardVerdict) MarshalJSON() ([]byte, error) {
	return []byte(`"` + v.String() + `"`), nil
}

func (v *deployJSONGuardVerdict) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return fmt.Errorf("deploy guard verdict must be a string: %w", err)
	}
	switch strings.ToLower(name) {
	case "unknown":
		*v = deployGuardVerdictUnknown
	case "allow":
		*v = deployGuardVerdictAllow
	case "refuse":
		*v = deployGuardVerdictRefuse
	default:
		return fmt.Errorf("unknown deploy guard verdict %q (expected allow or refuse) — "+
			"refusing to decode it as a default, which would read a refusal as permission to deploy", name)
	}
	return nil
}

// deployJSONGuardReason is WHY the guard reached its verdict — carried
// separately from the verdict so a consumer can branch on the cause (offer
// "run gcloud get-credentials" for one, "fix the KCL" for another) without
// parsing Fix's prose.
//
// Zero value is unknown for the same reason the verdict's is: an unevaluated
// guard must not present as a checked-and-fine one.
type deployJSONGuardReason int

const (
	// deployGuardReasonUnknown: not evaluated.
	deployGuardReasonUnknown deployJSONGuardReason = iota
	// deployGuardReasonContextDeclared: the env declares a cluster and that
	// kubectl context exists. The ordinary allow.
	deployGuardReasonContextDeclared
	// deployGuardReasonNoClusterDeclared: the env's KCL declares no
	// forge.K8sCluster.cluster, so there is no binding to enforce. An allow —
	// a host-only or compose env runs no kubectl writes.
	deployGuardReasonNoClusterDeclared
	// deployGuardReasonKubectlUnavailable: kubectl's contexts could not be
	// listed at all (not installed, no kubeconfig). A refusal that says
	// nothing about the env — the guard could not run.
	deployGuardReasonKubectlUnavailable
	// deployGuardReasonDeclaredContextMissing: the env declares a cluster
	// whose kubectl context is NOT in the kubeconfig. The one true failure of
	// the declarative model, and the only thing that refuses a deploy here.
	deployGuardReasonDeclaredContextMissing
	// deployGuardReasonControlPlaneDeclared: the env is HOSTED (its Bundle
	// declares control_plane). There is no kubectl context; the declared
	// destination is the control plane's endpoint, carried in
	// DeclaredContext so a confirmation keyed on "where do bytes land" keeps
	// working. An allow.
	deployGuardReasonControlPlaneDeclared
)

func (r deployJSONGuardReason) String() string {
	switch r {
	case deployGuardReasonContextDeclared:
		return "context_declared"
	case deployGuardReasonNoClusterDeclared:
		return "no_cluster_declared"
	case deployGuardReasonKubectlUnavailable:
		return "kubectl_unavailable"
	case deployGuardReasonDeclaredContextMissing:
		return "declared_context_missing"
	case deployGuardReasonControlPlaneDeclared:
		return "control_plane_declared"
	default:
		return "unknown"
	}
}

func (r deployJSONGuardReason) MarshalJSON() ([]byte, error) {
	return []byte(`"` + r.String() + `"`), nil
}

func (r *deployJSONGuardReason) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return fmt.Errorf("deploy guard reason must be a string: %w", err)
	}
	switch strings.ToLower(name) {
	case "unknown":
		*r = deployGuardReasonUnknown
	case "context_declared":
		*r = deployGuardReasonContextDeclared
	case "no_cluster_declared":
		*r = deployGuardReasonNoClusterDeclared
	case "kubectl_unavailable":
		*r = deployGuardReasonKubectlUnavailable
	case "declared_context_missing":
		*r = deployGuardReasonDeclaredContextMissing
	case "control_plane_declared":
		*r = deployGuardReasonControlPlaneDeclared
	default:
		return fmt.Errorf("unknown deploy guard reason %q — "+
			"refusing to decode it as a default, which would misattribute why a deploy was refused", name)
	}
	return nil
}

// deployJSONGuard is the declared-context decision, whole.
//
// DeclaredContext is the ONLY context the deploy ever applies to
// (forge.K8sCluster.cluster IS the kubectl context name). CurrentContext is
// reported because an operator invariably wants to see it, and is otherwise
// PURELY INFORMATIONAL — forge never reads it and never falls back to it. It
// is in this document so a UI can show "you are pointed at X, this deploys to
// Y" without implying the deploy consults X.
type deployJSONGuard struct {
	// DeclaredContext is the kubectl context the env's KCL declares. Empty
	// means no cluster is declared (see deployGuardReasonNoClusterDeclared).
	DeclaredContext string `json:"declared_context"`
	// CurrentContext is the ambient kubectl current-context. NEVER used by
	// the deploy. Empty when kubectl is not configured.
	CurrentContext string `json:"current_context"`
	// Verdict is whether the deploy may proceed.
	Verdict deployJSONGuardVerdict `json:"verdict"`
	// Reason is the cause behind Verdict.
	Reason deployJSONGuardReason `json:"reason"`
	// Fix, on a refusal, is what would make the deploy possible. Empty on an
	// allow.
	Fix string `json:"fix,omitempty"`
	// AvailableContexts is the kubeconfig's context list, populated when it
	// is the evidence for the verdict (a declared context that is missing
	// FROM this list). A UI renders it as "did you mean".
	AvailableContexts []string `json:"available_contexts,omitempty"`
}

// ─── Where the deploy lands ───────────────────────────────────────────────────

// deployJSONTarget is WHICH cluster and namespace this invocation addresses —
// the fact a confirmation dialog is built around. Both fields can be empty for
// an env that declares no cluster (host-only / compose / frontend-only), which
// is a real and legitimate shape rather than an error.
type deployJSONTarget struct {
	// KubeContext is the declared context, identical to
	// deployJSONGuard.DeclaredContext. Duplicated here because "where does
	// this land" and "may I deploy" are different questions a consumer asks
	// at different moments, and making the target section incomplete would
	// force every caller to cross-reference the guard.
	KubeContext string `json:"kube_context"`
	// Namespace is the resolved target namespace: --namespace, else the
	// env's declared forge.K8sCluster.namespace, else <project>-<env>.
	Namespace string `json:"namespace"`

	// AllKubeContexts is EVERY declared cluster context this invocation
	// addresses, sorted.
	//
	// A multi-cluster env is not hypothetical — control-plane's own dev env
	// declares two (k3d-control-plane and k3d-cp-daemon), and each group
	// applies to its own declared context. KubeContext names the env-wide
	// one the single-cluster consumers use, which for such an env is one of
	// several; a UI that asked "which cluster am I about to touch" and got
	// only that would show the user a cluster list missing a cluster it is
	// about to write to. That is the precise failure a confirmation dialog
	// exists to prevent, so the full set is reported rather than left to be
	// inferred.
	//
	// Has one entry for the ordinary single-cluster env, and is empty for a
	// host-only / compose / frontend-only env that declares none.
	AllKubeContexts []string `json:"all_kube_contexts,omitempty"`

	// Destination is where this env's workloads land: "hosted" for an env
	// whose Bundle declares control_plane, "cluster" otherwise. Additive; a
	// hosted deploy leaves KubeContext / Namespace / AllKubeContexts EMPTY,
	// because forge applies nothing to a cluster for it.
	Destination string `json:"destination,omitempty"`
	// Endpoint is the control plane's normalized base URL. Hosted only.
	Endpoint string `json:"endpoint,omitempty"`
	// EnvironmentID is the control plane's id for this env. Hosted only;
	// empty when the env has never been ensured (a dry run of a first
	// deploy). Never fabricated.
	EnvironmentID string `json:"environment_id,omitempty"`
}

// ─── Image pinning ────────────────────────────────────────────────────────────

// deployJSONPinning is how ONE image reference is bound to bytes.
//
// Zero value is unknown, NOT digest. Reporting an unexamined image as
// digest-pinned would claim a safety property forge never verified — and
// digest pinning is precisely the property that stops a re-tagged or
// node-cached layer from shipping.
type deployJSONPinning int

const (
	// deployPinningUnknown: the reference was not classified.
	deployPinningUnknown deployJSONPinning = iota
	// deployPinningDigest: pinned as <image>@sha256:... — immutable. The
	// bytes that were verified are the bytes that run.
	deployPinningDigest
	// deployPinningTag: referenced as <image>:<tag> — MUTABLE. Whatever the
	// tag points at when the kubelet pulls is what runs, which may not be
	// what was built or scanned.
	deployPinningTag
)

func (p deployJSONPinning) String() string {
	switch p {
	case deployPinningDigest:
		return "digest"
	case deployPinningTag:
		return "tag"
	default:
		return "unknown"
	}
}

func (p deployJSONPinning) MarshalJSON() ([]byte, error) {
	return []byte(`"` + p.String() + `"`), nil
}

func (p *deployJSONPinning) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return fmt.Errorf("image pinning must be a string: %w", err)
	}
	switch strings.ToLower(name) {
	case "unknown":
		*p = deployPinningUnknown
	case "digest":
		*p = deployPinningDigest
	case "tag":
		*p = deployPinningTag
	default:
		return fmt.Errorf("unknown image pinning %q (expected digest or tag) — "+
			"refusing to decode it as a default, which would claim a mutable tag was digest-pinned", name)
	}
	return nil
}

// deployJSONImage is one container image reference exactly as the manifests
// carry it, with how it is bound.
//
// The reference is read off the RENDERED stream rather than inferred from
// --no-digest and the build state: the rendered text is what the kubelet will
// pull, so classifying it directly cannot drift from what actually ships.
type deployJSONImage struct {
	// Reference is the full image reference in the manifest, e.g.
	// "us-central1-docker.pkg.dev/p/r/admin-server@sha256:ab…" or
	// "ghcr.io/org/api:v1.2.3".
	Reference string `json:"reference"`
	// Repository is Reference with the tag or digest stripped — the image's
	// identity, so a consumer can group the two forms of the same image.
	Repository string `json:"repository"`
	// Pinning is whether Reference is immutable.
	Pinning deployJSONPinning `json:"pinning"`
}

// deployJSONImages is the pinning picture for the whole invocation.
type deployJSONImages struct {
	// Images is every distinct image reference in the rendered stream,
	// sorted. Empty when nothing was rendered (explain) or the env renders
	// no containers.
	Images []deployJSONImage `json:"images"`
	// DigestCount and TagCount are the tallies, so a consumer can decide
	// whether to warn without walking the list. TagCount > 0 is the "this
	// deploy ships mutable references" signal.
	DigestCount int `json:"digest_count"`
	TagCount    int `json:"tag_count"`
	// NoDigestRequested records that --no-digest was passed, i.e. the
	// operator ASKED for mutable references. It distinguishes "forge had no
	// digest to pin" from "forge was told not to pin", which are the same
	// outcome and very different mistakes.
	NoDigestRequested bool `json:"no_digest_requested"`
}

// ─── Preflight ────────────────────────────────────────────────────────────────

// deployJSONPreflightStatus is whether the deployability preflight ran, and
// when it did not, why. It is a first-class value because the preflight is
// skippable and is naturally off for local clusters: "no findings" and "never
// looked" are completely different claims to put in front of a user, and a
// consumer that cannot tell them apart will present an unchecked deploy as a
// clean one.
//
// Zero value is unknown — never "ran".
type deployJSONPreflightStatus int

const (
	// deployPreflightUnknown: not recorded.
	deployPreflightUnknown deployJSONPreflightStatus = iota
	// deployPreflightRan: the preflight executed. Findings are complete.
	deployPreflightRan
	// deployPreflightSkippedFlag: --skip-preflight. The operator bypassed it.
	deployPreflightSkippedFlag
	// deployPreflightSkippedNoCluster: the env has no K8sCluster services, so
	// there is no live target to check anything against.
	deployPreflightSkippedNoCluster
)

func (s deployJSONPreflightStatus) String() string {
	switch s {
	case deployPreflightRan:
		return "ran"
	case deployPreflightSkippedFlag:
		return "skipped_flag"
	case deployPreflightSkippedNoCluster:
		return "skipped_no_cluster"
	default:
		return "unknown"
	}
}

func (s deployJSONPreflightStatus) MarshalJSON() ([]byte, error) {
	return []byte(`"` + s.String() + `"`), nil
}

func (s *deployJSONPreflightStatus) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return fmt.Errorf("preflight status must be a string: %w", err)
	}
	switch strings.ToLower(name) {
	case "unknown":
		*s = deployPreflightUnknown
	case "ran":
		*s = deployPreflightRan
	case "skipped_flag":
		*s = deployPreflightSkippedFlag
	case "skipped_no_cluster":
		*s = deployPreflightSkippedNoCluster
	default:
		return fmt.Errorf("unknown preflight status %q — "+
			"refusing to decode it as a default, which would present an unchecked deploy as a checked one", name)
	}
	return nil
}

// deployJSONFinding is ONE structured preflight finding — a missing Secret
// key, an absent image, an arch mismatch — rather than a line of the formatted
// report.
//
// Check is a plain string, deliberately, where everything else here is a
// strict enum. The set of checks GROWS (the preflight has gained CRD,
// arch, byte-match and back-propagation gates over time), so a strict decoder
// would reject a newer forge's output over a finding a consumer could simply
// display. The safety-critical bit is not WHICH check fired but whether it
// STOPS the deploy, and that is carried explicitly in Blocking rather than
// derived from the name — so an unrecognised check still reports its
// consequence correctly.
type deployJSONFinding struct {
	// Check names the gate that produced this finding, e.g.
	// "missing_secret_key", "missing_image", "arch_mismatch".
	Check string `json:"check"`
	// Subject is what the finding is ABOUT: "<namespace>/<secret>" for a
	// Secret or ConfigMap, the image reference for an image check.
	Subject string `json:"subject"`
	// Keys, for a key-scoped check, are the specific missing keys.
	Keys []string `json:"keys,omitempty"`
	// Detail is the human explanation, when the check carries one.
	Detail string `json:"detail,omitempty"`
	// Blocking is whether this finding REFUSES the deploy. Advisory
	// warnings (an inconclusive registry lookup, an unreadable image arch)
	// are reported with Blocking false — they are things forge could not
	// vouch for, not things it found wrong.
	//
	// false is the safe zero here in the sense that matters: an
	// unpopulated finding never claims the authority to stop a deploy that
	// forge in fact allowed. Whether the deploy proceeded is stated
	// authoritatively by the document's OK and Mode, never inferred from
	// this field.
	Blocking bool `json:"blocking"`
}

// deployJSONPreflight is the preflight section: whether it ran, and what it
// found.
type deployJSONPreflight struct {
	Status deployJSONPreflightStatus `json:"status"`
	// Findings are the structured results, sorted for stable output. Empty
	// with Status ran means a clean preflight; empty with any skipped status
	// means nothing was checked.
	Findings []deployJSONFinding `json:"findings"`
	// Blocking is the count of findings that refuse the deploy, so a
	// consumer need not walk the list to decide whether to show a stop.
	Blocking int `json:"blocking"`
}

// ─── Resources ────────────────────────────────────────────────────────────────

// deployJSONResource is one resource identity from the manifest stream — what
// a consumer diffs between two deploys.
//
// Identity, not body: a UI wants "these 34 objects, and 2 of them are new",
// and shipping the YAML would make the document enormous while answering a
// question nobody asked of a JSON report. The human --dry-run manifest dump is
// unchanged for anyone who does want the bytes.
type deployJSONResource struct {
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
}

// ─── Rollout ──────────────────────────────────────────────────────────────────

// deployJSONRolloutMode mirrors cluster.RolloutMode. It is in the document
// because it changes what an absent answer MEANS: under skip every resource is
// legitimately unknown, and a consumer must not render that as a problem —
// whereas the same unknown under wait is a resource that never reported in.
//
// Zero value is unknown rather than wait. Naming a mode forge did not run
// would misrepresent how hard it actually looked.
type deployJSONRolloutMode int

const (
	deployRolloutModeUnknown deployJSONRolloutMode = iota
	// deployRolloutModeWait: waited for every resource, and a resource that
	// never became ready FAILS the deploy. The default.
	deployRolloutModeWait
	// deployRolloutModeWarn: waited and reported, but did not fail.
	deployRolloutModeWarn
	// deployRolloutModeSkip: applied and returned, waiting for nothing.
	deployRolloutModeSkip
)

func (m deployJSONRolloutMode) String() string {
	switch m {
	case deployRolloutModeWait:
		return "wait"
	case deployRolloutModeWarn:
		return "warn"
	case deployRolloutModeSkip:
		return "skip"
	default:
		return "unknown"
	}
}

func (m deployJSONRolloutMode) MarshalJSON() ([]byte, error) {
	return []byte(`"` + m.String() + `"`), nil
}

func (m *deployJSONRolloutMode) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return fmt.Errorf("rollout mode must be a string: %w", err)
	}
	switch strings.ToLower(name) {
	case "unknown":
		*m = deployRolloutModeUnknown
	case "wait":
		*m = deployRolloutModeWait
	case "warn":
		*m = deployRolloutModeWarn
	case "skip":
		*m = deployRolloutModeSkip
	default:
		return fmt.Errorf("unknown rollout mode %q (expected wait, warn or skip) — "+
			"refusing to decode it as a default, which would claim forge waited when it did not", name)
	}
	return nil
}

// deployJSONRolloutModeFrom maps the cluster policy's mode onto the reported
// one. The policy is normalized first, so an unset mode reports the wait it
// actually performed rather than unknown.
func deployJSONRolloutModeFrom(p cluster.RolloutPolicy) deployJSONRolloutMode {
	switch p.Normalize().Mode {
	case cluster.RolloutWait:
		return deployRolloutModeWait
	case cluster.RolloutWarn:
		return deployRolloutModeWarn
	case cluster.RolloutSkip:
		return deployRolloutModeSkip
	default:
		return deployRolloutModeUnknown
	}
}

// deployJSONRolloutState is ONE resource's readiness outcome, and the three-way
// distinction this project insists on everywhere.
//
// READY and FAILED are answers. TIMED_OUT and NOT_WAITED are the ABSENCE of an
// answer, and they are not the same as either: a Deployment that did not become
// ready inside its budget may be mid-pull on a cold node or may be
// crash-looping, and forge does not know which. Collapsing that into "failed"
// cries wolf; collapsing it into "ready" is the green-deploy-over-a-broken-
// environment failure that made RolloutPolicy exist in the first place.
//
// Zero value is unknown, which is why READY is not the first constant.
type deployJSONRolloutState int

const (
	// deployRolloutStateUnknown: no outcome was recorded for this resource.
	deployRolloutStateUnknown deployJSONRolloutState = iota
	// deployRolloutStateReady: the Deployment reached its ready condition,
	// or the one-shot Job completed. A positive answer.
	deployRolloutStateReady
	// deployRolloutStateFailed: the resource reported a genuine failure — a
	// Job that satisfied condition=failed, a rollout that errored for a
	// reason other than expiring its budget.
	deployRolloutStateFailed
	// deployRolloutStateTimedOut: the readiness budget expired with no
	// verdict. NOT a success and NOT a failure: unknown, with the specific
	// cause "we stopped waiting".
	deployRolloutStateTimedOut
	// deployRolloutStateNotWaited: forge never asked. Under rollout mode
	// skip this is every resource's honest state — the manifests were
	// applied and nothing was observed converging.
	deployRolloutStateNotWaited
)

func (s deployJSONRolloutState) String() string {
	switch s {
	case deployRolloutStateReady:
		return "ready"
	case deployRolloutStateFailed:
		return "failed"
	case deployRolloutStateTimedOut:
		return "timed_out"
	case deployRolloutStateNotWaited:
		return "not_waited"
	default:
		return "unknown"
	}
}

func (s deployJSONRolloutState) MarshalJSON() ([]byte, error) {
	return []byte(`"` + s.String() + `"`), nil
}

// UnmarshalJSON rejects an unrecognised state. This is the decoder the whole
// zero-value discipline is aimed at: a state that fell back to "ready" would
// report a stuck rollout as a healthy one, which is exactly the failure the
// three-way distinction exists to prevent.
func (s *deployJSONRolloutState) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return fmt.Errorf("rollout state must be a string: %w", err)
	}
	switch strings.ToLower(name) {
	case "unknown":
		*s = deployRolloutStateUnknown
	case "ready":
		*s = deployRolloutStateReady
	case "failed":
		*s = deployRolloutStateFailed
	case "timed_out":
		*s = deployRolloutStateTimedOut
	case "not_waited":
		*s = deployRolloutStateNotWaited
	default:
		return fmt.Errorf("unknown rollout state %q (expected ready, failed, timed_out or not_waited) — "+
			"refusing to decode it as a default, which would report a stuck rollout as ready", name)
	}
	return nil
}

// Ready reports whether this state is a POSITIVE answer. Everything else —
// failed, timed out, not waited, unrecorded — is not, and callers get that
// judgment from here rather than writing `!= failed` and accidentally passing
// two kinds of unknown.
func (s deployJSONRolloutState) Ready() bool { return s == deployRolloutStateReady }

// deployJSONRolloutStateFrom maps cluster's classification onto the reported
// state. The mapping is total: a cluster state this binary does not recognise
// becomes unknown rather than ready.
func deployJSONRolloutStateFrom(s cluster.RolloutState) deployJSONRolloutState {
	switch s {
	case cluster.RolloutStateReady:
		return deployRolloutStateReady
	case cluster.RolloutStateFailed:
		return deployRolloutStateFailed
	case cluster.RolloutStateTimedOut:
		return deployRolloutStateTimedOut
	case cluster.RolloutStateNotWaited:
		return deployRolloutStateNotWaited
	default:
		return deployRolloutStateUnknown
	}
}

// deployJSONRolloutResult is one awaited resource and what became of it.
type deployJSONRolloutResult struct {
	// Kind is the workload kind forge waited on: "Deployment" or "Job".
	Kind string `json:"kind"`
	Name string `json:"name"`
	// State is the three-way outcome.
	State deployJSONRolloutState `json:"state"`
	// Detail carries the underlying error text for a failed or timed-out
	// resource. Empty when ready.
	Detail string `json:"detail,omitempty"`
}

// deployJSONRollout is the rollout section.
type deployJSONRollout struct {
	// Mode is what forge was asked to do after the manifests landed. Read
	// this before reading Results: under skip, every not_waited is expected.
	Mode deployJSONRolloutMode `json:"mode"`
	// TimeoutSeconds is the PER-RESOURCE readiness budget, not the budget
	// for the set. Named in seconds so a consumer need not parse a duration.
	TimeoutSeconds int `json:"timeout_seconds"`
	// Results is one entry per resource forge waited on (or, under skip,
	// per workload it declined to wait on), sorted by kind then name.
	Results []deployJSONRolloutResult `json:"results"`
	// Ready, Failed, TimedOut and NotWaited are the tallies. They are
	// separate fields rather than a "healthy" boolean because the whole
	// point is that a consumer can distinguish three outcomes, and one
	// boolean can only distinguish two.
	Ready     int `json:"ready"`
	Failed    int `json:"failed"`
	TimedOut  int `json:"timed_out"`
	NotWaited int `json:"not_waited"`
}

// ─── The document ─────────────────────────────────────────────────────────────

// deployJSONReport is the whole invocation as one JSON document.
//
// ADDITIVE-EXTENSION CONTRACT: fields are only ever ADDED, never renamed and
// never repurposed. A consumer written against this shape keeps working; a
// field that changed meaning under a stable name would break it silently,
// which is the one failure mode a machine-readable contract must not have.
type deployJSONReport struct {
	// Env is the environment name, as passed.
	Env string `json:"env"`
	// Mode is what was performed. Read this FIRST: it is the authoritative
	// answer to whether anything was written.
	Mode deployJSONMode `json:"mode"`
	// ApplyPath names which machinery ships this env (deployApplyPath):
	// client apply, mixed, hosted, hub converge or flux. Set by --explain.
	ApplyPath string `json:"apply_path,omitempty"`

	// Guard is the declared-context decision. A consumer deciding whether a
	// deploy is POSSIBLE reads guard.verdict — not ok, which reports whether
	// this invocation itself succeeded (an --explain that refuses is a
	// successful explain, and exits 0, exactly as text mode does).
	Guard deployJSONGuard `json:"guard"`
	// Target is where the deploy lands.
	Target deployJSONTarget `json:"target"`

	// ImageTag is the resolved env-wide mutable tag, and TagSource says
	// where it came from (--tag, the build state, a bound release, or git
	// describe) — the provenance line the text banner prints.
	ImageTag  string `json:"image_tag,omitempty"`
	TagSource string `json:"tag_source,omitempty"`
	// Release is the release this env is promoted to, when it is bound to
	// one; its digests are what get pinned. Empty when the env has no
	// binding.
	Release string `json:"release,omitempty"`

	// ReleaseDecision is what a no-version deploy would do about the
	// release — reuse one or cut a new one — set by --dry-run from the
	// same function the real run decides with.
	ReleaseDecision *deployJSONReleaseDecision `json:"release_decision,omitempty"`

	Preflight deployJSONPreflight `json:"preflight"`
	Images    deployJSONImages    `json:"images"`

	// Resources are the identities the stream carries. Under dry_run these
	// are what WOULD be applied; under apply, what was sent. Mode is what
	// distinguishes the two — there is deliberately no second per-resource
	// "applied" flag that could disagree with it.
	Resources []deployJSONResource `json:"resources"`

	Rollout deployJSONRollout `json:"rollout"`

	// Targets and Namespace scoping flags, so a consumer can tell a
	// whole-env reconcile from a single-app deploy. A --target deploy that
	// looks like a full one in the report would make a diff view lie.
	Targets       []string `json:"targets,omitempty"`
	SkipFrontend  bool     `json:"skip_frontend,omitempty"`
	FrontendsOnly bool     `json:"frontends_only,omitempty"`
	Prune         bool     `json:"prune,omitempty"`

	// DurationMS is the wall-clock time the deploy body took. Omitted for
	// explain, which does no work worth timing.
	DurationMS int64 `json:"duration_ms,omitempty"`

	// OK is true exactly when text mode would exit 0, and ExitCode is
	// exactly the code text mode would produce. They are computed from the
	// deploy's own returned error, so the two modes cannot disagree.
	OK       bool `json:"ok"`
	ExitCode int  `json:"exit_code"`
	// Error is the failure message when OK is false.
	Error string `json:"error,omitempty"`
	// The error's structure — code, reason, scopes (json_error_facts.go).
	jsonErrorFacts
}

// deployJSONReleaseDecision is the release decision, whole.
type deployJSONReleaseDecision struct {
	// Action is "reuse" or "cut".
	Action string `json:"action"`
	// Version is the release reused, or the one that would be cut.
	Version string `json:"version"`
	// Reason is why nothing was reused. Empty for a reuse.
	Reason string `json:"reason,omitempty"`
	// Dirty is true when the checkout has uncommitted changes of its own,
	// DirtyPaths lists them, and NestedCheckouts lists the nested git
	// checkouts that were seen and ignored.
	Dirty           bool     `json:"dirty"`
	DirtyPaths      []string `json:"dirty_paths"`
	NestedCheckouts []string `json:"nested_checkouts"`
	// RequiresConfirmation is true when a real run asks before building
	// (a dirty cut on a shared env).
	RequiresConfirmation bool `json:"requires_confirmation"`
}

// ─── The accumulator ──────────────────────────────────────────────────────────

// deployReport accumulates the document as the deploy runs.
//
// EVERY METHOD IS NIL-SAFE, and that is the design. Text mode threads a nil
// *deployReport through the identical code path, so there is no "if json" fork
// in the deploy body and therefore no way for the reported facts to describe a
// different sequence of events than the one that occurred. The mutex guards
// against a callback arriving from cluster's concurrent wait paths.
type deployReport struct {
	mu  sync.Mutex
	doc deployJSONReport
}

// newDeployReport returns an accumulator for envName, or nil when the caller
// is in text mode. Returning a typed nil on purpose: callers pass it straight
// through and every method tolerates it.
func newDeployReport(envName string, enabled bool) *deployReport {
	if !enabled {
		return nil
	}
	return &deployReport{doc: deployJSONReport{
		Env:       envName,
		Resources: []deployJSONResource{},
		Preflight: deployJSONPreflight{Findings: []deployJSONFinding{}},
		Images:    deployJSONImages{Images: []deployJSONImage{}},
		Rollout:   deployJSONRollout{Results: []deployJSONRolloutResult{}},
	}}
}

// Enabled reports whether a report is being accumulated — the one question
// the deploy body legitimately asks, to decide whether to divert the human
// stream off stdout.
func (r *deployReport) Enabled() bool { return r != nil }

// setMode records what this invocation is doing.
func (r *deployReport) setApplyPath(path string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.doc.ApplyPath = path
}

func (r *deployReport) setReleaseDecision(c deployReleaseChoice, requiresConfirmation bool) {
	if r == nil {
		return
	}
	d := &deployJSONReleaseDecision{
		Action: "cut", Version: c.Version, Reason: c.Reason, Dirty: c.Dirty,
		DirtyPaths: append([]string{}, c.DirtyPaths...), NestedCheckouts: append([]string{}, c.Nested...),
		RequiresConfirmation: requiresConfirmation,
	}
	if c.Reuse != nil {
		d.Action, d.Version = "reuse", c.Reuse.Version
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.doc.ReleaseDecision = d
}

func (r *deployReport) setMode(m deployJSONMode) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.doc.Mode = m
}

// setGuard records the declared-context decision.
func (r *deployReport) setGuard(g deployJSONGuard) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.doc.Guard = g
	// The guard already resolved the declared context; seeding the target
	// from it here means a refusal still reports WHICH cluster it refused to
	// touch, which is the first thing anyone asks.
	if r.doc.Target.KubeContext == "" {
		r.doc.Target.KubeContext = g.DeclaredContext
	}
	// And it joins the context SET, not just the single field. Recording it
	// only in KubeContext would lose it the moment the env-wide context
	// resolves to a different cluster — which is exactly what happens in a
	// multi-cluster env, where the guard's context and the env-wide one are
	// two of several real targets.
	r.addKubeContextLocked(g.DeclaredContext)
}

// addKubeContextLocked adds one declared cluster context to the target set,
// keeping it de-duplicated and sorted. Caller holds the mutex.
func (r *deployReport) addKubeContextLocked(kubeContext string) {
	kubeContext = strings.TrimSpace(kubeContext)
	if kubeContext == "" {
		return
	}
	for _, existing := range r.doc.Target.AllKubeContexts {
		if existing == kubeContext {
			return
		}
	}
	r.doc.Target.AllKubeContexts = append(r.doc.Target.AllKubeContexts, kubeContext)
	sort.Strings(r.doc.Target.AllKubeContexts)
}

// setTarget records where the deploy lands. allContexts is every declared
// cluster context the invocation addresses — one for the ordinary env, more for
// a multi-cluster one, none for a host-only env.
func (r *deployReport) setTarget(kubeContext, namespace string, allContexts ...string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if kubeContext != "" {
		r.doc.Target.KubeContext = kubeContext
	}
	r.doc.Target.Namespace = namespace

	// UNION with whatever is already recorded, never a replacement: a later
	// call must not drop a cluster an earlier one established, or a
	// multi-group dispatch would under-report its own blast radius. The
	// env-wide context is folded in too — it is a cluster this deploy writes
	// to, and omitting it because it arrived by a different route would
	// understate the reach.
	for _, candidate := range append([]string{r.doc.Target.KubeContext}, allContexts...) {
		r.addKubeContextLocked(candidate)
	}
}

// setTags records the resolved tag, its provenance, and the bound release.
func (r *deployReport) setTags(imageTag, tagSource, release string, noDigest bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.doc.ImageTag = imageTag
	r.doc.TagSource = tagSource
	r.doc.Release = release
	r.doc.Images.NoDigestRequested = noDigest
}

// setScope records the flags that narrow what this deploy touches.
func (r *deployReport) setScope(targets []string, skipFrontend, frontendsOnly, prune bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.doc.Targets = targets
	r.doc.SkipFrontend = skipFrontend
	r.doc.FrontendsOnly = frontendsOnly
	r.doc.Prune = prune
}

// setPreflightStatus records whether the preflight ran, or why it did not.
func (r *deployReport) setPreflightStatus(s deployJSONPreflightStatus) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.doc.Preflight.Status = s
}

// setPreflightResult flattens cluster's structured result into findings.
// Called with the result whether or not the preflight blocked, so a clean run
// is distinguishable from one that was never performed.
func (r *deployReport) setPreflightResult(res cluster.PreflightResult) {
	if r == nil {
		return
	}
	findings := deployFindingsFromPreflight(res)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.doc.Preflight.Status = deployPreflightRan
	r.doc.Preflight.Findings = findings
	blocking := 0
	for _, f := range findings {
		if f.Blocking {
			blocking++
		}
	}
	r.doc.Preflight.Blocking = blocking
}

// setStream records the resource identities and image references of the
// manifest stream an apply is about to send. Fires for --dry-run too — that
// is the whole point, since a preview is what a UI calls first.
//
// Called once per deploy group in a multi-cluster env, so entries accumulate
// and are de-duplicated: the same Namespace document legitimately appears in
// two groups' streams, and reporting it twice would make a diff view show
// phantom objects.
func (r *deployReport) setStream(gvks []cluster.ManifestGVK, images []string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[deployJSONResource]struct{}{}
	for _, existing := range r.doc.Resources {
		seen[existing] = struct{}{}
	}
	for _, g := range gvks {
		res := deployJSONResource{APIVersion: g.APIVersion, Kind: g.Kind, Name: g.Name}
		if _, dup := seen[res]; dup {
			continue
		}
		seen[res] = struct{}{}
		r.doc.Resources = append(r.doc.Resources, res)
	}
	sortDeployResources(r.doc.Resources)

	haveImage := map[string]struct{}{}
	for _, existing := range r.doc.Images.Images {
		haveImage[existing.Reference] = struct{}{}
	}
	for _, ref := range images {
		if ref == "" {
			continue
		}
		if _, dup := haveImage[ref]; dup {
			continue
		}
		haveImage[ref] = struct{}{}
		r.doc.Images.Images = append(r.doc.Images.Images, classifyDeployImage(ref))
	}
	sort.Slice(r.doc.Images.Images, func(i, j int) bool {
		return r.doc.Images.Images[i].Reference < r.doc.Images.Images[j].Reference
	})
	r.doc.Images.DigestCount, r.doc.Images.TagCount = 0, 0
	for _, img := range r.doc.Images.Images {
		switch img.Pinning {
		case deployPinningDigest:
			r.doc.Images.DigestCount++
		case deployPinningTag:
			r.doc.Images.TagCount++
		}
	}
}

// streamObserver returns the cluster.ApplyOpts.OnStream callback that records
// the manifest stream, or nil in text mode.
//
// Returning nil rather than a no-op closure is what keeps text mode's apply
// byte-identical: cluster checks the hook for nil and skips the work of
// assembling the combined stream entirely.
func (r *deployReport) streamObserver() func(string) {
	if r == nil {
		return nil
	}
	return func(manifests string) {
		r.setStream(cluster.CollectManifestGVKs(manifests), imagesFromManifests(manifests))
	}
}

// rolloutObserver returns the cluster.ApplyOpts.OnRollout callback, or nil in
// text mode.
func (r *deployReport) rolloutObserver() func(cluster.RolloutObservation) {
	if r == nil {
		return nil
	}
	return func(obs cluster.RolloutObservation) {
		detail := ""
		if obs.Err != nil {
			detail = obs.Err.Error()
		}
		r.addRollout(obs.Kind, obs.Name, obs.State, detail)
	}
}

// imagesFromManifests reads the distinct image references out of a rendered
// stream, sorted.
//
// The REFERENCES are what matter, not the build state's intent: what the kubelet
// pulls is the text in the manifest, so classifying that text is the only way
// the pinning report cannot drift from what actually ships.
func imagesFromManifests(manifests string) []string {
	refs := cluster.CollectManifestRefs(manifests, cluster.RefPlacement{})
	out := make([]string, 0, len(refs.Images))
	for ref := range refs.Images {
		out = append(out, ref)
	}
	sort.Strings(out)
	return out
}

// setRolloutPolicy records what forge was asked to do after the apply.
func (r *deployReport) setRolloutPolicy(p cluster.RolloutPolicy) {
	if r == nil {
		return
	}
	normalized := p.Normalize()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.doc.Rollout.Mode = deployJSONRolloutModeFrom(p)
	r.doc.Rollout.TimeoutSeconds = int(normalized.Timeout / time.Second)
}

// addRollout records one resource's outcome, re-tallying as it goes so the
// counts can never drift from the list.
func (r *deployReport) addRollout(kind, name string, state cluster.RolloutState, detail string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.doc.Rollout.Results = append(r.doc.Rollout.Results, deployJSONRolloutResult{
		Kind:   kind,
		Name:   name,
		State:  deployJSONRolloutStateFrom(state),
		Detail: detail,
	})
	sort.SliceStable(r.doc.Rollout.Results, func(i, j int) bool {
		a, b := r.doc.Rollout.Results[i], r.doc.Rollout.Results[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Name < b.Name
	})
	r.retallyRolloutLocked()
}

// retallyRolloutLocked recomputes the four counts from Results. Derived rather
// than incremented, so a tally cannot disagree with the list it summarizes.
func (r *deployReport) retallyRolloutLocked() {
	r.doc.Rollout.Ready, r.doc.Rollout.Failed = 0, 0
	r.doc.Rollout.TimedOut, r.doc.Rollout.NotWaited = 0, 0
	for _, res := range r.doc.Rollout.Results {
		switch res.State {
		case deployRolloutStateReady:
			r.doc.Rollout.Ready++
		case deployRolloutStateFailed:
			r.doc.Rollout.Failed++
		case deployRolloutStateTimedOut:
			r.doc.Rollout.TimedOut++
		case deployRolloutStateNotWaited:
			r.doc.Rollout.NotWaited++
		}
	}
}

// markWorkloadsNotWaited records every workload in the rendered stream as
// not_waited — the honest state under rollout mode skip, where forge applied
// the manifests and observed nothing converging.
//
// Derived from the stream rather than reported by cluster because under skip
// cluster never asks the cluster anything, so there is no list of awaited
// resources for it to report. Resources that already carry an outcome are left
// alone, so this can never downgrade a real verdict.
func (r *deployReport) markWorkloadsNotWaited() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.doc.Rollout.Mode != deployRolloutModeSkip {
		return
	}
	have := map[string]struct{}{}
	for _, res := range r.doc.Rollout.Results {
		have[res.Kind+"/"+res.Name] = struct{}{}
	}
	for _, res := range r.doc.Resources {
		if !isDeployWaitableKind(res.Kind) {
			continue
		}
		if _, dup := have[res.Kind+"/"+res.Name]; dup {
			continue
		}
		r.doc.Rollout.Results = append(r.doc.Rollout.Results, deployJSONRolloutResult{
			Kind:   res.Kind,
			Name:   res.Name,
			State:  deployRolloutStateNotWaited,
			Detail: "rollout mode is skip — forge applied the manifests and did not wait, so this resource's readiness is unknown",
		})
	}
	sort.SliceStable(r.doc.Rollout.Results, func(i, j int) bool {
		a, b := r.doc.Rollout.Results[i], r.doc.Rollout.Results[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Name < b.Name
	})
	r.retallyRolloutLocked()
}

// finish stamps the outcome. err is the deploy's OWN returned error, so ok and
// the exit code are the same verdict text mode produces rather than a second
// opinion about it.
func (r *deployReport) finish(err error, elapsed time.Duration) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.doc.OK = err == nil
	r.doc.ExitCode = exitCodeForError(err)
	if err != nil {
		r.doc.Error = err.Error()
	}
	r.doc.jsonErrorFacts = errorFactsOf(err)
	if elapsed > 0 {
		r.doc.DurationMS = elapsed.Milliseconds()
	}
}

// document returns a copy of the accumulated report.
func (r *deployReport) document() deployJSONReport {
	if r == nil {
		return deployJSONReport{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.doc
}

// emit writes the document to stdout, indented, per the house convention every
// other --json command in this package follows.
func (r *deployReport) emit() error {
	if r == nil {
		return nil
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r.document()); err != nil {
		return fmt.Errorf("write deploy report: %w", err)
	}
	return nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// classifyDeployImage reads a rendered image reference and says how it is
// bound. The digest form is authoritative: "@sha256:" in the reference means
// the kubelet pulls content-addressed bytes, and its absence means it resolves
// a mutable tag at pull time, whatever forge intended.
func classifyDeployImage(ref string) deployJSONImage {
	out := deployJSONImage{Reference: ref, Repository: ref, Pinning: deployPinningTag}
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		out.Repository = ref[:at]
		out.Pinning = deployPinningDigest
	}
	// A ":" is only a tag separator when it comes after the last "/" —
	// otherwise it is a registry port (localhost:5000/img), and treating that
	// as a tag would report the wrong repository.
	//
	// Stripped for a digest-pinned reference too, because forge renders a
	// forge-built image that pins its own tag as `repo:tag@sha256:…` — the
	// tag documents which tag the bytes were pushed under, and the digest is
	// what the kubelet pulls. Leaving the tag on Repository would give the
	// same image two identities depending on how it was declared, which
	// defeats the grouping this field exists for.
	if colon := strings.LastIndex(out.Repository, ":"); colon > strings.LastIndex(out.Repository, "/") {
		out.Repository = out.Repository[:colon]
	}
	return out
}

// isDeployWaitableKind reports whether a resource kind is one forge's rollout
// wait has a readiness verdict for. Only these are worth marking not_waited
// under skip: a ConfigMap has no readiness to be unknown about.
func isDeployWaitableKind(kind string) bool {
	switch kind {
	case "Deployment", "StatefulSet", "DaemonSet", "Job":
		return true
	default:
		return false
	}
}

// sortDeployResources orders resources by kind then name, so two runs of the
// same render produce byte-identical documents and a consumer can diff them.
func sortDeployResources(res []deployJSONResource) {
	sort.SliceStable(res, func(i, j int) bool {
		if res[i].Kind != res[j].Kind {
			return res[i].Kind < res[j].Kind
		}
		if res[i].Name != res[j].Name {
			return res[i].Name < res[j].Name
		}
		return res[i].APIVersion < res[j].APIVersion
	})
}

// deployFindingsFromPreflight flattens cluster's PreflightResult into sorted,
// structured findings.
//
// The blocking/advisory split mirrors PreflightResult.OK() exactly: the fields
// OK() consults are the ones that refuse a deploy, and the two warning fields
// it ignores are reported as advisory. Deriving it from the same source is what
// keeps "blocking" from drifting away from what actually blocks.
func deployFindingsFromPreflight(res cluster.PreflightResult) []deployJSONFinding {
	out := []deployJSONFinding{}

	keyed := func(check string, groups map[string][]string, detail string) {
		for subject, keys := range groups {
			out = append(out, deployJSONFinding{
				Check: check, Subject: subject, Keys: append([]string{}, keys...),
				Detail: detail, Blocking: true,
			})
		}
	}
	keyed("missing_secret_key", res.MissingSecretKeys,
		"the manifests reference these Secret keys and the live target does not provide them")
	keyed("missing_configmap_key", res.MissingConfigMapKeys,
		"the manifests reference these ConfigMap keys and the live target does not provide them")
	keyed("missing_required_secret_key", res.MissingRequiredSecretKeys,
		"a declared forge.ExternalSecret prerequisite is absent on the live target")

	listed := func(check string, subjects []string, blocking bool, detail string) {
		for _, subject := range subjects {
			out = append(out, deployJSONFinding{
				Check: check, Subject: subject, Detail: detail, Blocking: blocking,
			})
		}
	}
	listed("missing_image", res.MissingImages, true,
		"the image was confirmed absent from its registry")
	listed("unverifiable_image", res.UnverifiableImages, true,
		"the registry lookup was auth-denied, so the image could not be confirmed present")
	listed("arch_mismatch", res.ArchMismatchImages, true,
		"the image's architecture does not include the target cluster's declared node arch")
	listed("missing_crd", res.MissingCRDs, true,
		"the target cluster's API server does not serve a kind the bundle renders")
	listed("volume_resize_refused", res.VolumeResizeRefusals, true,
		"a StatefulSet's declared volume differs from the live one in a way no resize can converge (a shrink, a changed storage class, or a class that cannot expand)")
	listed("byte_match_mismatch", res.ByteMatchMismatches, true,
		"a declared value_group's live Secret values are not identical across the group")
	listed("store_crd_missing", res.Store.MissingCRDs, true,
		"a store-backed Secret needs ExternalSecret / SecretStore, which the cluster does not serve and this deploy does not install")
	listed("store_crd_version", res.Store.IncompatibleCRDs, true,
		"the cluster's External Secrets Operator does not serve the API version the render emits")
	listed("store_operator_owner", res.Store.OwnerConflicts, true,
		"another env owns the cluster's External Secrets Operator, and one cluster has one")
	listed("store_not_ready", res.Store.StoreNotReady, true,
		"the OpenBao this env runs has not finished converging, so no SecretStore can log in")
	listed("store_value_missing", res.Store.MissingStoreNames, true,
		"the store holds no value for a name an ExternalSecret reads, so its Secret would never fill")
	listed("store_unverified", res.Store.Warnings, false,
		"a store could not be asked whether it holds the names read, so they were not verified")
	listed("store_adoption", res.Store.Adoptions, false,
		"this deploy replaces a live OpenBao forge did not install; clients still on Kubernetes or AppRole auth lose access until they flip to JWT, so ship the flips in this release")
	// Advisory, and NOT blocking: a transport failure says nothing about the
	// image. Reported so a consumer knows the gate could not vouch for it.
	listed("image_warning", res.ImageWarnings, false,
		"the image check was inconclusive (transport failure) — neither present nor absent was established")
	listed("volume_resize_warning", res.VolumeResizeWarnings, false,
		"the live volumes of a StatefulSet could not be read, so its declared sizes were not checked")
	listed("arch_warning", res.ArchWarnings, false,
		"the image's architecture could not be read, so no mismatch could be asserted")
	listed("optional_key_warning", res.OptionalKeyWarnings, false,
		"a Secret or ConfigMap an `optional: true` reference reads exists but lacks the key; the pod starts with the variable unset")

	for _, mount := range res.UndeclaredSecretMounts {
		detail := "a workload mounts this Secret and nothing in the rendered bundle provides it"
		if len(mount.Workloads) > 0 {
			detail += " (mounted by " + strings.Join(mount.Workloads, ", ") + ")"
		}
		out = append(out, deployJSONFinding{
			Check:    "undeclared_secret_mount",
			Subject:  mount.Secret,
			Detail:   detail,
			Blocking: true,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Check != out[j].Check {
			return out[i].Check < out[j].Check
		}
		return out[i].Subject < out[j].Subject
	})
	return out
}

// setHostedTarget records a hosted env's destination. The kube-context fields
// stay empty: nothing is applied to any cluster forge addresses.
func (r *deployReport) setHostedTarget(endpoint, environmentID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.doc.Target.Destination = "hosted"
	r.doc.Target.Endpoint = endpoint
	if environmentID != "" {
		r.doc.Target.EnvironmentID = environmentID
	}
}

// clearKubeContexts drops any kube context recorded so far. A hosted deploy
// addresses no cluster, and a context left over from the guard seeding would
// make a confirmation dialog name a cluster the deploy never touches.
func (r *deployReport) clearKubeContexts() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.doc.Target.KubeContext = ""
	r.doc.Target.AllKubeContexts = nil
}
