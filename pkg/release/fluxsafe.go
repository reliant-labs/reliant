package release

// FLUX-SAFETY of a rendered object set.
//
// forge's direct apply is a one-shot: it applies, waits, and exits. A
// reconciler is not. Flux re-applies the same bytes on every interval, forever,
// and prunes what the bundle no longer carries, so some objects that are
// harmless under `kubectl apply` are wrong under it.
//
// Two kinds of rule live here, split by who can fix the object:
//
//   - FIXES the bundle build applies itself, because the fix does not change what
//     the object means ([SetExplicitRollout], [ForceRecreateKind]);
//   - FINDINGS ([CheckFluxSafety]) for objects only the author can fix, because the
//     fix changes behaviour: a Job's lifecycle, or what may be deleted.
//
// WHY EACH RULE EXISTS (the incident, not the principle):
//
//   - a Job with ttlSecondsAfterFinished: the TTL controller deletes the
//     finished Job, the next reconcile sees it missing and creates it again, so
//     a migration re-runs every TTL period. control-plane's temporal-schema,
//     temporal-namespace and openbao-bootstrap all carried a 24h TTL;
//   - a Job whose name carries no hash of its spec: its spec is immutable, so
//     after any change every reconcile fails instead of creating a new Job under
//     a new name, and the old completed one is never pruned;
//   - a Deployment whose strategy or progressDeadlineSeconds is implicit: the API
//     server defaults them, and materializes rollingUpdate under another field
//     manager, so the Recreate transition forge repairs by hand under direct
//     apply (strategy_recovery.go) wedges every reconcile instead;
//   - an immutable field on a stateless config object (a StorageClass's
//     parameters): direct apply repairs it by delete-and-recreate, and Flux does
//     the same only for an object that says so with its per-object force
//     annotation. A kind that holds data never gets one.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Flux's per-object recreate policy: when Enabled, kustomize-controller deletes
// and re-creates an object whose update fails on an immutable field. The value
// is Flux's, not forge's.
const (
	AnnotationFluxForce = "kustomize.toolkit.fluxcd.io/force"
	FluxForceEnabled    = "enabled"
)

// Defaults a Deployment states explicitly. They are Kubernetes' own, so stating
// them changes nothing at runtime: the render just says what the cluster would
// have said, instead of leaving a reconciler to see fields another manager owns.
const (
	DefaultProgressDeadlineSeconds int32 = 600
	DefaultRolloutPercent                = "25%"
)

// forceKinds are the stateless config kinds whose immutable fields forge's
// direct apply repairs by delete-and-recreate and that lose nothing when they
// are recreated: [RecreatableKind] minus the kinds where recreation has a cost
// the author must choose.
//
//   - Service: recreating releases a LoadBalancer's address (the plan's
//     lb_identity_change stop exists for exactly that);
//   - Deployment, DaemonSet: a selector change is a rollout from zero;
//   - Job: a hash-named Job never has an immutable change, it is replaced.
//
// An author who wants force on one of those annotates it themselves.
var forceKinds = map[string]bool{
	"StorageClass": true, "RuntimeClass": true, "PriorityClass": true,
	"ClusterRoleBinding": true, "RoleBinding": true,
}

// ForceRecreateKind reports whether the bundle build stamps kind with the
// per-object force annotation.
func ForceRecreateKind(kind string) bool { return forceKinds[kind] }

// SetExplicitRollout writes a Deployment's strategy and progressDeadlineSeconds
// into a decoded document where they are absent, with Kubernetes' defaults. It
// never overrides what the author wrote, and reports whether it changed
// anything. A document that is not a Deployment is left alone.
func SetExplicitRollout(doc map[string]any) bool {
	if doc["kind"] != "Deployment" {
		return false
	}
	spec, ok := doc["spec"].(map[string]any)
	if !ok {
		spec = map[string]any{}
		doc["spec"] = spec
	}
	changed := false
	strategy, _ := spec["strategy"].(map[string]any)
	if strategy == nil {
		strategy = map[string]any{}
		spec["strategy"] = strategy
	}
	typ, _ := strategy["type"].(string)
	if typ == "" {
		typ = "RollingUpdate"
		strategy["type"] = typ
		changed = true
	}
	if typ == "RollingUpdate" {
		if _, has := strategy["rollingUpdate"]; !has {
			strategy["rollingUpdate"] = map[string]any{
				"maxSurge": DefaultRolloutPercent, "maxUnavailable": DefaultRolloutPercent,
			}
			changed = true
		}
	}
	if _, has := spec["progressDeadlineSeconds"]; !has {
		spec["progressDeadlineSeconds"] = int(DefaultProgressDeadlineSeconds)
		changed = true
	}
	return changed
}

// Rule IDs. Stable: lint output, suppressions and tests name them.
const (
	RuleJobTTL      = "flux-job-ttl"
	RuleJobUnhashed = "flux-job-unhashed"
	RuleForceData   = "flux-force-on-data"
)

// FluxFinding is one object that is not safe under a reconciler.
type FluxFinding struct {
	Rule      string
	Kind      string
	Namespace string
	Name      string
	// Unit is the delivery unit the object is in ([DefaultDeliveryUnit] when it
	// declares none), so a caller that judges only the units a reconciler
	// applies can tell which findings are its own.
	Unit   string
	Reason string
	Fix    string
}

func (f FluxFinding) String() string {
	ref := f.Name
	if f.Namespace != "" {
		ref = f.Namespace + "/" + f.Name
	}
	return fmt.Sprintf("[%s] %s %s: %s — %s", f.Rule, f.Kind, ref, f.Reason, f.Fix)
}

// A Job name ends in a dash and a lowercase-hex hash of its spec. forge renders
// ten characters ([StandaloneJobHashLength] in pkg/deploy/v1alpha1); KCL-authored
// Jobs (control-plane's) use a sha256 prefix of the same length. Six is the
// floor, so a hand-written `-v2` is not mistaken for a hash.
var jobHashSuffix = regexp.MustCompile(`-[0-9a-f]{6,}$`)

// CheckFluxSafety returns every finding in a decoded document set, sorted.
// Each document is a decoded Kubernetes object (map[string]any); anything else
// is skipped, because parsing is the caller's job and it has refused those.
func CheckFluxSafety(docs []any) []FluxFinding {
	var out []FluxFinding
	for _, d := range docs {
		doc, ok := d.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := doc["kind"].(string)
		meta, _ := doc["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		ns, _ := meta["namespace"].(string)
		unit := DefaultDeliveryUnit
		if labels, _ := meta["labels"].(map[string]any); labels != nil {
			if u, _ := labels[LabelDeliveryUnit].(string); u != "" {
				unit = u
			}
		}
		add := func(rule, reason, fix string) {
			out = append(out, FluxFinding{Rule: rule, Kind: kind, Namespace: ns, Name: name, Unit: unit, Reason: reason, Fix: fix})
		}
		if kind == "Job" {
			spec, _ := doc["spec"].(map[string]any)
			if ttl, set := spec["ttlSecondsAfterFinished"]; set {
				add(RuleJobTTL,
					fmt.Sprintf("sets ttlSecondsAfterFinished=%v: the TTL controller deletes the finished Job and the next reconcile creates it again, re-running it every period", ttl),
					"remove ttlSecondsAfterFinished; a superseded Job is removed by its spec-hash name plus prune, never by a timer")
			}
			if !jobHashSuffix.MatchString(name) {
				add(RuleJobUnhashed,
					"its name carries no hash of its spec: a Job's spec is immutable, so after any change every reconcile fails instead of creating a new Job, and the old one is never pruned",
					"name it <name>-<10 hex of a hash of its spec> (forge's standalone Jobs already are)")
			}
		}
		if hasForce(meta) && !RecreatableKind(kind) {
			add(RuleForceData,
				"carries "+AnnotationFluxForce+" but holds data or owns everything under it: Flux would delete and re-create it on an immutable-field change, losing its state",
				"remove the annotation, and change the immutable field through a migration")
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return out
}

func hasForce(meta map[string]any) bool {
	ann, _ := meta["annotations"].(map[string]any)
	v, _ := ann[AnnotationFluxForce].(string)
	return strings.EqualFold(v, FluxForceEnabled)
}
