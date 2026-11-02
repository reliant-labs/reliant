package release

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// The deploy PLAN (owner decision O-13).
//
// With a promotion converger on, writing a promotion IS the deploy: nothing
// stands between the write and the rollout. So the review has to happen
// BEFORE the record, and the approval has to be bound to exactly what was
// reviewed. A Plan is that review: the bundle about to be deployed, compared
// against what is live, classified so the dangerous changes cannot hide among
// the ordinary ones, and digested so an approval names one plan and no other.
//
// # One function, both sides
//
// [BuildPlan] is the ONLY place a plan is computed. A hosted backend
// recomputes it under the environment's row lock when the approval arrives
// and refuses unless the digests match; a file ledger does the same under its
// lock. Because both call this function, a digest mismatch means the world
// moved between plan and approve — never that two implementations disagreed
// about the rules. The table tests over the stop-class rules are therefore
// the acceptance tests for every backend.
//
// # What the digest covers
//
// The digest is sha256 over the canonical JSON of the plan's MEANING: the
// environment and bundle, the live basis it was computed against, whether the
// config was identical, and each finding's code, class, section and subject.
// It excludes the digest itself, ComputedAt (two computations of one plan
// happen at two times), and each finding's Detail — prose, which may be worded
// differently by two forge versions without the plan meaning anything
// different. Everything Detail says is derived from fields the digest covers
// (the bundle pins its images; the basis names what is live).

// FindingClass is how much a finding gates a deploy. Closed.
type FindingClass string

const (
	// ClassInfo: a change the operator should see. Approval covers it.
	ClassInfo FindingClass = "info"
	// ClassWarn: something to read before approving — a missing secret,
	// drift the deploy will overwrite, a section that could not be
	// computed. Approval covers it.
	ClassWarn FindingClass = "warn"
	// ClassStop: a change a general approval must NOT cover — a stateful
	// deletion, a load balancer's identity changing. It needs its own
	// acknowledgement BY CODE, even with --yes, because --yes is the flag
	// that ends up hard-coded in CI and would otherwise pre-approve every
	// future destructive change.
	ClassStop FindingClass = "stop"
)

// FindingClasses is the closed set, least to most severe.
var FindingClasses = []FindingClass{ClassInfo, ClassWarn, ClassStop}

// Valid reports whether c is one of [FindingClasses].
func (c FindingClass) Valid() bool {
	for _, k := range FindingClasses {
		if c == k {
			return true
		}
	}
	return false
}

// UnmarshalJSON refuses an unknown class.
func (c *FindingClass) UnmarshalJSON(data []byte) error {
	names := make([]string, len(FindingClasses))
	for i, k := range FindingClasses {
		names[i] = string(k)
	}
	return decodeClosed(data, "plan finding class", func(s string) bool { return FindingClass(s).Valid() }, names, (*string)(c))
}

// Finding codes. Closed: a consumer switches on them, and a stop-class code
// is what an operator names to acknowledge it.
const (
	FindingObjectAdded      = "object_added"
	FindingObjectChanged    = "object_changed"
	FindingObjectRemoved    = "object_removed"
	FindingImageChanged     = "image_changed"
	FindingConfigChanged    = "config_changed"
	FindingSecretNeeded     = "secret_needed"
	FindingStatefulDeletion = "stateful_deletion"
	FindingLBIdentityChange = "lb_identity_change"
	// FindingTreeMoved: an object changes delivery tree between releases.
	// Under a pruning reconciler that is a delete in the tree it leaves and a
	// create in the one it joins, with the object's data in between.
	FindingTreeMoved = "tree_moved"
	FindingDrift     = "drift"
	// FindingUnknown: a section could not be computed (no recorded live
	// config, drift not observable, secret presence not readable). It is a
	// WARN, never silence: a blank section must not read as "nothing
	// changes" (F-20).
	FindingUnknown = "unknown"
)

// FindingCodes is the closed set of codes.
var FindingCodes = []string{
	FindingObjectAdded, FindingObjectChanged, FindingObjectRemoved,
	FindingImageChanged, FindingConfigChanged, FindingSecretNeeded,
	FindingStatefulDeletion, FindingLBIdentityChange, FindingTreeMoved, FindingDrift, FindingUnknown,
}

// Plan sections, in display order (§8.6).
const (
	SectionObjects  = "objects"
	SectionStateful = "stateful_deletions"
	SectionLB       = "lb_identity"
	SectionTrees    = "tree_moves"
	SectionImages   = "images"
	SectionConfig   = "config"
	SectionSecrets  = "secrets"
	SectionDrift    = "drift"
)

// PlanSections is the closed set, in display order.
var PlanSections = []string{SectionStateful, SectionLB, SectionTrees, SectionObjects, SectionImages, SectionConfig, SectionSecrets, SectionDrift}

// Plan is one reviewed deploy. Field names match controlplane.v1.DeployPlan.
type Plan struct {
	Digest string `json:"digest"`
	// BaseDigest is the digest before chart findings were folded in by
	// [Plan.WithCharts]; empty when none were. Never serialized: it exists for
	// the write to a control plane that recomputes the plan without charts.
	BaseDigest     string        `json:"-"`
	EnvironmentID  string        `json:"environment_id"`
	BundleID       string        `json:"bundle_id"`
	ReleaseVersion string        `json:"release_version,omitempty"`
	LiveBasis      PlanBasis     `json:"live_basis"`
	Findings       []PlanFinding `json:"findings"`
	// ConfigIdentical: the bundle's config digest equals the live one, so
	// the deploy changes images at most. A no-op deploy says so instead of
	// manufacturing a diff to approve.
	ConfigIdentical bool      `json:"config_identical"`
	ComputedAt      time.Time `json:"computed_at"`
}

// PlanBasis is what a plan was computed AGAINST. Any change to it — another
// promotion, another bundle applied, drift appearing — changes the digest, so
// an approval of a plan computed against an older Live is refused as stale.
type PlanBasis struct {
	CurrentPromotionID  string `json:"current_promotion_id,omitempty"`
	AppliedBundleID     string `json:"applied_bundle_id,omitempty"`
	AppliedConfigDigest string `json:"applied_config_digest,omitempty"`
	DriftObserved       bool   `json:"drift_observed"`
}

// PlanFinding is one thing the plan says.
type PlanFinding struct {
	Code    string       `json:"code"`
	Class   FindingClass `json:"class"`
	Section string       `json:"section"`
	// Subject is "<cluster>/<kind>/<ns>/<name>" for an object, else the
	// workload, artifact or secret name, or the section for an unknown.
	Subject string `json:"subject"`
	// Detail is human-readable and NEVER a secret value. Not digested.
	Detail string `json:"detail,omitempty"`
	// Fingerprint pins the CONTENT a finding is about when no bundle does:
	// the hash of a chart object's desired document. It is digested, so an
	// approval of a plan names the exact chart change and not just the
	// object's name. Empty for every bundle-backed finding, which keeps their
	// digests what they always were.
	Fingerprint string `json:"fingerprint,omitempty"`
}

// PlanInput is everything [BuildPlan] reads. It is assembled by the caller
// from records it already holds — the candidate bundle and the live state —
// so BuildPlan itself does no I/O.
type PlanInput struct {
	EnvironmentID  string
	BundleID       string
	ReleaseVersion string

	Candidate             Shape
	CandidateConfigDigest string

	// Live is the applied bundle's shape, else the env's declared shape;
	// nil when nothing is recorded (every object then reads as added, and
	// the plan carries an unknown finding).
	Live *Shape
	// Basis is the live state the plan is bound to. AppliedConfigDigest is
	// also what ConfigIdentical compares against.
	Basis PlanBasis

	// Drift is the live-cluster observation: nil when drift is not
	// observable for this env (an unknown finding), else the objects found
	// diverged from the applied bundle (possibly none).
	Drift *DriftObservation
	// SecretPresence maps a secret name to whether its value is set, for
	// the secrets whose presence the caller COULD read: a managed store, or
	// the target cluster's Secrets when the caller is the one applying to
	// it. A name the map does not contain is UNVERIFIABLE — a store or a
	// cluster that could not be read — never "missing": one env commonly
	// mixes providers, and reading every unread secret as missing would make
	// the warning meaningless. nil means nothing could be read.
	//
	// It is consulted for every declared secret: a newly declared one is a
	// finding either way (info when present), and a previously declared one
	// is a finding only when explicitly false — declared at the last apply,
	// missing now.
	SecretPresence map[string]bool

	// Charts is what the env's forge.HelmChart platform deps would change in
	// their clusters. A chart is applied on its own path and rides no bundle,
	// so the shapes above cannot see it; the caller observes it and BuildPlan
	// folds it into the same sections, under the same digest.
	Charts ChartPlan
}

// DriftObservation is what an observer saw of the live cluster.
type DriftObservation struct {
	Drifted []ObjectKey
}

// StatefulKind reports whether a kind's removal destroys data or everything
// under it — [statefulKinds] as a predicate.
//
// Exported because the PLAN and the BUNDLE WRITER need the same answer, for
// two halves of one guarantee. The plan calls a removal of one of these a
// stop-class finding that must be acknowledged; the bundle writer stamps
// `kustomize.toolkit.fluxcd.io/prune: disabled` on it so a reconciler cannot
// delete it at all. Two spellings of this list would mean an object the plan
// treats as irreplaceable is one the reconciler happily prunes, and the
// divergence would only ever be discovered by losing the data.
func StatefulKind(kind string) bool { return statefulKinds[kind] }

// statefulKinds are object kinds whose removal destroys data or everything
// under them. Removing one is a stop-class finding. ShapeObject.Stateful marks
// anything else that holds data (a declared database's objects).
var statefulKinds = map[string]bool{
	"PersistentVolumeClaim":    true,
	"PersistentVolume":         true,
	"StatefulSet":              true,
	"Secret":                   true,
	"Namespace":                true,
	"CustomResourceDefinition": true,
}

// recreatableKinds is the allowlist of kinds an apply's immutable-field
// recovery may delete and re-apply: deleting one loses no state, because it owns
// no data and nothing is garbage-collected through it. A kind that holds data
// (PersistentVolumeClaim, PersistentVolume, StatefulSet) or cascades to
// everything inside it (Namespace, CustomResourceDefinition) is deliberately
// absent — an immutable conflict there fails the deploy loudly. The one
// immutable StatefulSet change that converges, a larger volume, does not
// recreate through this list: pkg/stsresize grows the claims and
// orphan-deletes the object, so its pods keep running.
//
// ONE list, read by both the recovery (internal/cluster) and the plan, so the
// plan can never promise less than the apply is allowed to do.
var recreatableKinds = map[string]bool{
	"Job": true, "Deployment": true, "DaemonSet": true, "Service": true,
	"StorageClass": true, "RuntimeClass": true, "PriorityClass": true,
	"ClusterRoleBinding": true, "RoleBinding": true,
}

// RecreatableKind reports whether kind may be deleted and re-applied by the
// immutable-field recovery.
func RecreatableKind(kind string) bool { return recreatableKinds[kind] }

// recreateNote is appended to a finding for a recreatable object whose live
// state the plan cannot rule out as different. Detail is not digested, so this
// changes what a reader is told, never what an approval names.
const recreateNote = "may be DELETED AND RECREATED if an immutable field differs (stateless kind; bound data is not touched)"

// BuildPlan computes the plan. Pure and deterministic: the same input yields
// the same findings in the same order and the same digest. ComputedAt is the
// only clock-dependent field and is left for the caller to set.
func BuildPlan(in PlanInput) (Plan, error) {
	if err := in.Candidate.Validate(); err != nil {
		return Plan{}, fmt.Errorf("plan: candidate %w", err)
	}
	if in.Live != nil {
		if err := in.Live.Validate(); err != nil {
			return Plan{}, fmt.Errorf("plan: live %w", err)
		}
		if in.Live.Kind != in.Candidate.Kind {
			return Plan{}, fmt.Errorf("%w: plan: the environment is %s, but the bundle renders it as %s — an environment's kind is immutable", ErrInvalid, in.Live.Kind, in.Candidate.Kind)
		}
	}
	diff := DiffShapes(in.Live, in.Candidate)
	p := Plan{
		EnvironmentID:  in.EnvironmentID,
		BundleID:       in.BundleID,
		ReleaseVersion: in.ReleaseVersion,
		LiveBasis:      in.Basis,
		ConfigIdentical: in.CandidateConfigDigest != "" &&
			in.CandidateConfigDigest == in.Basis.AppliedConfigDigest,
	}
	add := func(code string, class FindingClass, section, subject, detail string) {
		p.Findings = append(p.Findings, PlanFinding{Code: code, Class: class, Section: section, Subject: subject, Detail: detail})
	}

	if diff.LiveUnknown {
		add(FindingUnknown, ClassWarn, SectionObjects, SectionObjects,
			"no recorded config to compare against: every object appears added, and deletions cannot be detected")
	}
	for _, o := range diff.Added {
		// With no recorded live config an "added" object may already exist
		// with different immutable fields: say so for the kinds the apply is
		// allowed to recreate, because the plan cannot show that diff.
		detail := ""
		if diff.LiveUnknown && RecreatableKind(o.Kind) {
			detail = recreateNote
		}
		add(FindingObjectAdded, ClassInfo, SectionObjects, o.Key().String(), detail)
	}
	for _, o := range diff.Removed {
		switch {
		case o.Stateful || statefulKinds[o.Kind]:
			add(FindingStatefulDeletion, ClassStop, SectionStateful, o.Key().String(),
				fmt.Sprintf("%s %q is removed; the data it holds is not recoverable", o.Kind, o.Name))
		case hasExternalIdentity(o):
			add(FindingLBIdentityChange, ClassStop, SectionLB, o.Key().String(),
				fmt.Sprintf("%s %q is removed and with it its external address (%s)", o.Kind, o.Name, identitySummary(o.Identity)))
		default:
			add(FindingObjectRemoved, ClassWarn, SectionObjects, o.Key().String(), "")
		}
	}
	// A move between two trees is a delete and a create nothing orders: stop.
	// A move into or out of the flat layout is the one-time layout migration,
	// which happens to every object of a cluster at once and to every env on
	// the release that first records trees. It is reported once per cluster,
	// as a warn: a direct-apply env is unaffected by it, and a Flux env
	// adopts the new trees (suspended, prune off) before its first promotion.
	layout := map[string]int{}
	var layoutDetail = map[string]string{}
	for _, m := range diff.Moved {
		if m.From == "" || m.To == "" {
			layout[m.Key.Cluster]++
			layoutDetail[m.Key.Cluster] = treeMoveDetail(m)
			continue
		}
		add(FindingTreeMoved, ClassStop, SectionTrees, m.Key.String(), treeMoveDetail(m))
	}
	for _, cluster := range sortedStrings(mapKeys(layout)) {
		add(FindingTreeMoved, ClassWarn, SectionTrees, cluster,
			fmt.Sprintf("%d object(s) change layout between the flat cluster tree and per-unit trees; e.g. %s", layout[cluster], layoutDetail[cluster]))
	}
	for _, c := range diff.Changed {
		subject := c.Candidate.Key().String()
		if why, changed := identityChange(c.Live, c.Candidate); changed {
			add(FindingLBIdentityChange, ClassStop, SectionLB, subject, why)
		}
		for _, img := range c.Images {
			add(FindingImageChanged, ClassInfo, SectionImages, subject+"#"+img.Artifact,
				fmt.Sprintf("%s: %s → %s", img.Artifact, shortDigest(img.From), shortDigest(img.To)))
		}
		changeNote := ""
		if RecreatableKind(c.Candidate.Kind) {
			changeNote = recreateNote
		}
		switch {
		case c.Live.ConfigHash == "" || c.Candidate.ConfigHash == "":
			if len(c.Images) == 0 {
				add(FindingObjectChanged, ClassInfo, SectionObjects, subject, changeNote)
			}
		case c.ConfigChanged:
			add(FindingConfigChanged, ClassInfo, SectionConfig, subject, changeNote)
		}
	}

	in.Charts.fold(&p)

	// Secrets. Presence is consulted for EVERY declared secret, not only the
	// new ones: a secret that was declared at the last apply and has since
	// been deleted is the deploy that crash-loops on its next restart, and it
	// is invisible to a diff of declarations. What stays silent is the steady
	// state — declared before, still present (or unverifiable, because
	// "could not look" must not manufacture a finding every deploy).
	newlyDeclared := map[string]bool{}
	for _, s := range diff.SecretsAdded {
		newlyDeclared[s.Name] = true
	}
	for _, s := range in.Candidate.Canonical().Secrets {
		set, known := in.SecretPresence[s.Name]
		switch {
		case newlyDeclared[s.Name] && !known:
			add(FindingSecretNeeded, ClassWarn, SectionSecrets, s.Name, "newly declared; presence not verifiable for provider "+s.Provider)
		case newlyDeclared[s.Name] && set:
			add(FindingSecretNeeded, ClassInfo, SectionSecrets, s.Name, "newly declared; present")
		case newlyDeclared[s.Name]:
			add(FindingSecretNeeded, ClassWarn, SectionSecrets, s.Name, "newly declared; MISSING — set it before the workload starts")
		case known && !set:
			add(FindingSecretNeeded, ClassWarn, SectionSecrets, s.Name, "declared at the last apply and now MISSING — set it before the workload restarts")
		}
	}

	if in.Drift == nil {
		add(FindingUnknown, ClassWarn, SectionDrift, SectionDrift, "live drift is not observable for this environment")
	} else {
		for _, k := range sortedKeys(in.Drift.Drifted) {
			add(FindingDrift, ClassWarn, SectionDrift, k.String(),
				"changed outside forge since the last apply; this deploy overwrites it")
		}
	}
	p.LiveBasis.DriftObserved = in.Drift != nil && len(in.Drift.Drifted) > 0

	sortFindings(p.Findings)
	if p.Findings == nil {
		p.Findings = []PlanFinding{}
	}
	d, err := p.computeDigest()
	if err != nil {
		return Plan{}, err
	}
	p.Digest = d
	return p, nil
}

// StopCodes is the set of stop-class finding codes in the plan, sorted.
func (p Plan) StopCodes() []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range p.Findings {
		if f.Class == ClassStop && !seen[f.Code] {
			seen[f.Code] = true
			out = append(out, f.Code)
		}
	}
	sort.Strings(out)
	return out
}

// Unacknowledged returns the stop-class codes the acknowledgements do not
// name. Empty means the plan may be approved. Acknowledgement is by CODE, so
// one acknowledgement covers every finding of that code in THIS plan — and,
// because the digest binds the approval to this plan, nothing beyond it.
func (p Plan) Unacknowledged(acknowledged []string) []string {
	ack := map[string]bool{}
	for _, a := range acknowledged {
		ack[strings.TrimSpace(a)] = true
	}
	var out []string
	for _, c := range p.StopCodes() {
		if !ack[c] {
			out = append(out, c)
		}
	}
	return out
}

// VerifyDigest recomputes the digest and reports whether it matches Digest.
func (p Plan) VerifyDigest() (bool, error) {
	d, err := p.computeDigest()
	if err != nil {
		return false, err
	}
	return d == p.Digest, nil
}

// planDigestBody is exactly what the digest covers. A separate type rather
// than a filtered Plan, so adding a field to Plan cannot silently change (or
// silently fail to change) the digest — it has to be added here on purpose.
type planDigestBody struct {
	EnvironmentID   string              `json:"environment_id"`
	BundleID        string              `json:"bundle_id"`
	ReleaseVersion  string              `json:"release_version"`
	LiveBasis       PlanBasis           `json:"live_basis"`
	ConfigIdentical bool                `json:"config_identical"`
	Findings        []planDigestFinding `json:"findings"`
}

type planDigestFinding struct {
	Code    string       `json:"code"`
	Class   FindingClass `json:"class"`
	Section string       `json:"section"`
	Subject string       `json:"subject"`
	// Omitted when empty so a plan without chart findings digests exactly as
	// it did before the field existed.
	Fingerprint string `json:"fingerprint,omitempty"`
}

func (p Plan) computeDigest() (string, error) {
	findings := append([]PlanFinding(nil), p.Findings...)
	sortFindings(findings)
	body := planDigestBody{
		EnvironmentID:   p.EnvironmentID,
		BundleID:        p.BundleID,
		ReleaseVersion:  p.ReleaseVersion,
		LiveBasis:       p.LiveBasis,
		ConfigIdentical: p.ConfigIdentical,
		Findings:        make([]planDigestFinding, len(findings)),
	}
	for i, f := range findings {
		body.Findings[i] = planDigestFinding{Code: f.Code, Class: f.Class, Section: f.Section, Subject: f.Subject, Fingerprint: f.Fingerprint}
	}
	b, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("plan digest: %w", err)
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func sortFindings(fs []PlanFinding) {
	rank := map[string]int{}
	for i, s := range PlanSections {
		rank[s] = i
	}
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if rank[a.Section] != rank[b.Section] {
			return rank[a.Section] < rank[b.Section]
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return a.Subject < b.Subject
	})
}

func sortedKeys(keys []ObjectKey) []ObjectKey {
	out := append([]ObjectKey(nil), keys...)
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// hasExternalIdentity reports whether removing the object releases an
// external address: a LoadBalancer Service, or anything holding a static or
// load-balancer IP.
func hasExternalIdentity(o ShapeObject) bool {
	return o.Identity[IdentityType] == "LoadBalancer" ||
		o.Identity[IdentityLoadBalancerIP] != "" || o.Identity[IdentityStaticIP] != ""
}

// identityChange reports whether an object that survives the deploy changes
// its external identity, and why. Becoming a LoadBalancer from nothing gains
// an address and takes none away, so it is not a change of identity; every
// other move of type, class or address to or from a load balancer is.
func identityChange(live, cand ShapeObject) (string, bool) {
	lt, ct := live.Identity[IdentityType], cand.Identity[IdentityType]
	var why []string
	if lt != ct && lt == "LoadBalancer" {
		why = append(why, fmt.Sprintf("type %s → %s releases its load balancer", lt, orNone(ct)))
	}
	for _, k := range []string{IdentityLoadBalancerIP, IdentityLoadBalancerClass, IdentityStaticIP} {
		if l, c := live.Identity[k], cand.Identity[k]; l != "" && l != c {
			why = append(why, fmt.Sprintf("%s %s → %s", k, l, orNone(c)))
		}
	}
	if len(why) == 0 {
		return "", false
	}
	return strings.Join(why, "; "), true
}

func identitySummary(id map[string]string) string {
	keys := make([]string, 0, len(id))
	for k := range id {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + id[k]
	}
	return strings.Join(parts, ", ")
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func shortDigest(d string) string {
	if d == "" {
		return "(none)"
	}
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		d = d[:12]
	}
	return d
}

// treeMoveDetail explains one move. A pruning Kustomization deletes what its
// path no longer carries and the one the object joins creates it, so the move
// is a delete and a create, in an order nothing guarantees.
func treeMoveDetail(m ObjectMove) string {
	from, to := m.From, m.To
	switch {
	case from == "":
		return fmt.Sprintf("%s %q moves from the flat cluster tree into %s: the layout is migrating to per-unit trees; every object of the cluster moves at once, so acknowledge it once and adopt the new trees first (suspend, prune off)", m.Key.Kind, m.Key.Name, to)
	case to == "":
		return fmt.Sprintf("%s %q moves from %s back into the flat cluster tree", m.Key.Kind, m.Key.Name, from)
	}
	return fmt.Sprintf("%s %q moves from %s to %s: the tree it leaves prunes it and the tree it joins creates it. Adopt it into the new tree first (suspend, prune off), or accept the delete and re-create", m.Key.Kind, m.Key.Name, from, to)
}

func mapKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
