package release

// Bundle LAYOUT v2: one tree per cluster × delivery unit × phase.
//
// A bundle used to publish one tree per cluster, so a Flux Kustomization per
// cluster applied the whole env in a single batch. That cannot express what
// forge's direct apply guarantees (pre-rollout Jobs complete before any
// workload changes; the daemon cluster before the hub). v2 splits each real
// cluster into trees a reconciler chains with dependsOn:
//
//	manifests/<cluster>/<unit>/<phase>/<NNN>-<kind>-<name>.yaml
//
// The grammar is enforced by [ValidateManifestsLayer]; this file decides WHICH
// object lands in which tree and how the trees depend on each other. Flat
// trees (manifests/<cluster>/<file>.yaml) remain for the two clusters nothing
// applies as a cluster: the unclustered tree and the hosted tree.
//
// THE GRAPH, from the owner-approved design (§4.2):
//
//   - within one (cluster, unit): pre → rollout → post, over the phases the
//     unit actually has;
//   - between units: a unit's first tree on a cluster waits for the LAST tree
//     of every unit it is `after`, on every cluster that unit has trees. A
//     unit declares `after`; an undeclared unit is after [base], and `base` is
//     after nothing. Declaring `after` REPLACES that default, so a unit that
//     must not wait for base says `after = []`.
//
// Cross-cluster ordering is therefore declared, never inferred from a context
// name: "daemon before hub" is `base after [daemon-substrate]`.

import (
	"fmt"
	"sort"
	"strings"
)

// DefaultDeliveryUnit is the unit an object belongs to when nothing declares
// one. It is also the unit every other undeclared unit waits for.
const DefaultDeliveryUnit = "base"

// LabelDeliveryUnit is the object label that places an object in a delivery
// unit. It rides the object itself so it survives every expansion between the
// KCL declaration and the rendered stream, and is sealed under the bundle's
// digest.
const LabelDeliveryUnit = "forge.dev/delivery-unit"

// Per-attempt Flux settings recorded on every tree. A reconcile holds one of a
// shared controller's workers for its health wait, so a tree never asks Flux
// to wait longer than one short attempt; the control plane's observer decides
// "failed" against the tree's own budget instead (design §8.3).
const (
	TreeTimeoutSeconds       = 90
	TreeRetryIntervalSeconds = 15
)

// DeliveryUnit declares one delivery unit's place in the apply order.
type DeliveryUnit struct {
	Name string `json:"name"`
	// After names the units whose trees must be Ready before this unit's
	// first tree applies. nil means "not declared" (the default applies);
	// a non-nil empty slice means "waits for nothing".
	After []string `json:"after"`
}

// BundleHealthCheck is one object a tree's readiness is judged on.
type BundleHealthCheck struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// BundleTree is one cluster × unit × phase tree in a v2 manifests layer. It is
// everything a reconciler needs to build ONE Kustomization: where to read,
// what to wait for, and what to judge health on.
type BundleTree struct {
	Cluster string `json:"cluster"`
	Unit    string `json:"unit"`
	Phase   Phase  `json:"phase"`
	// Path is [BundleTreePath]'s answer: Kustomization.spec.path, and the
	// tree's identity within the bundle.
	Path      string `json:"path"`
	Documents int    `json:"documents"`
	// DependsOn lists the Paths of the trees that must be Ready first, in
	// this bundle. Sorted.
	DependsOn []string `json:"depends_on,omitempty"`
	// TimeoutSeconds and RetryIntervalSeconds are the per-attempt Flux
	// settings ([TreeTimeoutSeconds], [TreeRetryIntervalSeconds]).
	TimeoutSeconds       int `json:"timeout_seconds"`
	RetryIntervalSeconds int `json:"retry_interval_seconds"`
	// Health is what the tree is ready on: the Jobs and awaited ExternalSecrets
	// of a pre or post tree, the Deployments and StatefulSets of a rollout tree.
	// A DaemonSet is not listed: a node-level rollout (an image prepull) is not
	// a gate, which is also what direct apply waits on. Sorted.
	Health []BundleHealthCheck `json:"health,omitempty"`
	// BudgetSeconds is how long the control plane lets this tree stay
	// "progressing" before it calls it failed. Flux's own wait is one short
	// attempt ([TreeTimeoutSeconds]); this is the whole-rollout allowance the
	// observer enforces instead (design §8.3), so a long rollout is never a
	// false alarm and a stuck one is never forgiven forever. It is derived
	// from the objects the tree is judged on ([TreeBudget]), recorded here so
	// the observer reads it rather than re-deriving it.
	BudgetSeconds int `json:"budget_seconds"`
}

// BundleTreePath is the path, within the extracted manifests layer, of one v2
// tree. Like [BundleClusterPath] it is the contract both sides call: forge
// writes the tar entries under it and the control plane sets it as
// Kustomization.spec.path.
func BundleTreePath(cluster, unit string, phase Phase) string {
	return BundleClusterPath(cluster) + "/" + BundlePathSegment(unit) + "/" + string(phase)
}

// FlatCluster reports whether a cluster's tree stays in the v1 flat layout:
// the unclustered tree (objects nothing applies) and the hosted tree (records
// the control plane renders). Every other cluster is split into v2 trees.
func FlatCluster(cluster string) bool {
	return strings.TrimSpace(cluster) == "" || cluster == BundleHostedCluster
}

// TreeObject is one object's placement, as [PlanTrees] needs it.
type TreeObject struct {
	Cluster   string
	Unit      string
	Phase     Phase
	Kind      string
	Namespace string
	Name      string
	// APIVersion identifies ESO objects; SecretSync is their
	// [AnnotationSecretSync] value (the shadow/deferred opt-outs), exactly
	// as the direct applier reads them. A string, not the annotation map, so
	// TreeObject stays comparable.
	APIVersion string
	SecretSync string
	// DeadlineSeconds is the object's own time allowance, when it states one: a
	// Deployment's progressDeadlineSeconds, a Job's activeDeadlineSeconds.
	// Zero means it states none and the default for its kind applies.
	DeadlineSeconds int
}

// The budget model (design §8.3): a rollout tree is allowed the LARGEST
// progressDeadlineSeconds among its Deployments and StatefulSets plus a
// margin for the apply and the readiness probe's first pass; a Job tree is
// allowed the largest Job allowance plus the same margin.
const (
	// BudgetMarginSeconds is added to the largest deadline: two minutes.
	BudgetMarginSeconds = 120
	// DefaultJobBudgetSeconds is a Job's allowance when it declares no
	// activeDeadlineSeconds: ten minutes, which is what direct apply waits for
	// a Job before it fails the gate (cluster.DefaultRolloutTimeout is five;
	// control-plane's migrations are the Jobs that need the difference).
	DefaultJobBudgetSeconds = 600
	// DefaultRolloutBudgetSeconds is a workload's allowance when it states no
	// progressDeadlineSeconds (a StatefulSet has none): Kubernetes' ten
	// minutes.
	DefaultRolloutBudgetSeconds = int(DefaultProgressDeadlineSeconds)
	// MinBudgetSeconds is what a tree with nothing to judge it on is given: its
	// support objects apply in one attempt, so it only needs to outlast one.
	MinBudgetSeconds = BudgetMarginSeconds
)

// TreeBudget is a tree's budget: the largest allowance among the objects it is
// judged on, plus [BudgetMarginSeconds]. A tree it judges nothing on gets
// [MinBudgetSeconds].
func TreeBudget(phase Phase, objects []TreeObject) int {
	longest := 0
	for _, o := range objects {
		if !healthObject(phase, o) {
			continue
		}
		d := o.DeadlineSeconds
		if d <= 0 {
			if phase == PhaseRollout {
				d = DefaultRolloutBudgetSeconds
			} else {
				d = DefaultJobBudgetSeconds
			}
		}
		if d > longest {
			longest = d
		}
	}
	if longest == 0 {
		return MinBudgetSeconds
	}
	return longest + BudgetMarginSeconds
}

// healthObject selects the same awaited ExternalSecrets as direct apply. Their
// current-generation sync holds the pre tree, and thus its rollout dependents.
// Shadow copies and values written after deployment must never hold that gate.
func healthObject(phase Phase, o TreeObject) bool {
	if o.Kind == "ExternalSecret" && IsESOKind(o.APIVersion, o.Kind) {
		return SecretSyncAwaited(map[string]string{AnnotationSecretSync: o.SecretSync})
	}
	switch phase {
	case PhaseRollout:
		return o.Kind == "Deployment" || o.Kind == "StatefulSet"
	default:
		return o.Kind == "Job"
	}
}

type treeKey struct {
	cluster, unit string
	phase         Phase
}

// PlanTrees groups objects into trees and wires their dependencies. The result
// is a pure function of its inputs, sorted by Path, with every list sorted:
// the same render is the same trees, byte for byte.
//
// Flat clusters ([FlatCluster]) are not trees and must not be passed.
func PlanTrees(objects []TreeObject, units []DeliveryUnit) ([]BundleTree, error) {
	declared := map[string]DeliveryUnit{}
	for _, u := range units {
		if u.Name == "" {
			return nil, fmt.Errorf("%w: a delivery unit has no name", ErrInvalid)
		}
		if BundlePathSegment(u.Name) != u.Name {
			return nil, fmt.Errorf("%w: delivery unit %q must use only letters, digits, '.', '_' and '-' (it is a path segment)", ErrInvalid, u.Name)
		}
		if _, dup := declared[u.Name]; dup {
			return nil, fmt.Errorf("%w: delivery unit %q is declared twice", ErrInvalid, u.Name)
		}
		declared[u.Name] = u
	}

	byKey := map[treeKey]*BundleTree{}
	members := map[treeKey][]TreeObject{}
	segOwner := map[string]string{}
	for _, o := range objects {
		if FlatCluster(o.Cluster) {
			return nil, fmt.Errorf("%w: %s/%s is on flat cluster %q, which has no trees", ErrInvalid, o.Kind, o.Name, o.Cluster)
		}
		if !o.Phase.Valid() {
			return nil, fmt.Errorf("%w: %s/%s has phase %q (expected pre, rollout or post)", ErrInvalid, o.Kind, o.Name, o.Phase)
		}
		unit := o.Unit
		if unit == "" {
			unit = DefaultDeliveryUnit
		}
		// Two unit names that sanitize to one path segment would share a
		// tree; refuse rather than merge them.
		seg := BundlePathSegment(unit)
		if prior, ok := segOwner[seg]; ok && prior != unit {
			return nil, fmt.Errorf("%w: delivery units %q and %q map to the same path segment %q", ErrInvalid, prior, unit, seg)
		}
		segOwner[seg] = unit
		k := treeKey{o.Cluster, unit, o.Phase}
		t := byKey[k]
		if t == nil {
			t = &BundleTree{
				Cluster: o.Cluster, Unit: unit, Phase: o.Phase,
				Path:                 BundleTreePath(o.Cluster, unit, o.Phase),
				TimeoutSeconds:       TreeTimeoutSeconds,
				RetryIntervalSeconds: TreeRetryIntervalSeconds,
			}
			byKey[k] = t
		}
		t.Documents++
		members[k] = append(members[k], o)
		if healthObject(o.Phase, o) {
			t.Health = append(t.Health, BundleHealthCheck{Kind: o.Kind, Namespace: o.Namespace, Name: o.Name})
		}
	}

	// Which units exist, per cluster, with their first and last tree.
	spans := map[string]map[string]*treeSpan{} // unit → cluster → span
	for _, phase := range Phases {             // apply order, so first/last are right
		for k, t := range byKey {
			if k.phase != phase {
				continue
			}
			if spans[k.unit] == nil {
				spans[k.unit] = map[string]*treeSpan{}
			}
			s := spans[k.unit][k.cluster]
			if s == nil {
				s = &treeSpan{first: t}
				spans[k.unit][k.cluster] = s
			}
			if s.last != nil {
				t.DependsOn = append(t.DependsOn, s.last.Path)
			}
			s.last = t
		}
	}

	for unit, perCluster := range spans {
		after, err := unitAfter(unit, declared, spans)
		if err != nil {
			return nil, err
		}
		for _, dep := range after {
			depSpans, ok := spans[dep]
			if !ok {
				continue
			}
			for _, s := range perCluster {
				for _, ds := range depSpans {
					s.first.DependsOn = append(s.first.DependsOn, ds.last.Path)
				}
			}
		}
	}

	out := make([]BundleTree, 0, len(byKey))
	for k, t := range byKey {
		t.BudgetSeconds = TreeBudget(k.phase, members[k])
		t.DependsOn = sortedUnique(t.DependsOn)
		sort.Slice(t.Health, func(i, j int) bool {
			a, b := t.Health[i], t.Health[j]
			if a.Kind != b.Kind {
				return a.Kind < b.Kind
			}
			if a.Namespace != b.Namespace {
				return a.Namespace < b.Namespace
			}
			return a.Name < b.Name
		})
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	if err := validateTreeGraph(out); err != nil {
		return nil, err
	}
	return out, nil
}

// treeSpan is one unit's first and last tree on one cluster, in apply order.
type treeSpan struct{ first, last *BundleTree }

// unitAfter resolves the units one unit waits for: its declaration, else
// [DefaultDeliveryUnit] for every unit but base itself.
//
// A declared dependency on a unit that is neither declared nor rendered is an
// error: a typo would otherwise silently drop an ordering the author wrote.
// The implicit dependency on base is not checked, because an env with no base
// objects has nothing to wait for.
func unitAfter(unit string, declared map[string]DeliveryUnit, spans map[string]map[string]*treeSpan) ([]string, error) {
	d, ok := declared[unit]
	if !ok || d.After == nil {
		if unit == DefaultDeliveryUnit {
			return nil, nil
		}
		return []string{DefaultDeliveryUnit}, nil
	}
	for _, dep := range d.After {
		if dep == unit {
			return nil, fmt.Errorf("%w: delivery unit %q is after itself", ErrInvalid, unit)
		}
		if _, isDeclared := declared[dep]; !isDeclared && spans[dep] == nil {
			return nil, fmt.Errorf("%w: delivery unit %q is after %q, which nothing declares or renders", ErrInvalid, unit, dep)
		}
	}
	return d.After, nil
}

func sortedUnique(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// validateTreeGraph checks what a reconciler would otherwise discover as a
// deadlock: every dependency names a tree in the set, and there is no cycle.
func validateTreeGraph(trees []BundleTree) error {
	byPath := map[string]*BundleTree{}
	for i := range trees {
		if byPath[trees[i].Path] != nil {
			return fmt.Errorf("%w: tree %s appears twice", ErrInvalid, trees[i].Path)
		}
		byPath[trees[i].Path] = &trees[i]
	}
	for _, t := range trees {
		for _, d := range t.DependsOn {
			if d == t.Path {
				return fmt.Errorf("%w: tree %s depends on itself", ErrInvalid, t.Path)
			}
			if byPath[d] == nil {
				return fmt.Errorf("%w: tree %s depends on %s, which this bundle does not carry", ErrInvalid, t.Path, d)
			}
		}
	}
	const (
		visiting = 1
		done     = 2
	)
	state := map[string]int{}
	var stack []string
	var visit func(p string) error
	visit = func(p string) error {
		switch state[p] {
		case done:
			return nil
		case visiting:
			return fmt.Errorf("%w: delivery units form a dependency cycle: %s → %s (a unit that must not wait for %q declares after = [])", ErrInvalid, strings.Join(stack, " → "), p, DefaultDeliveryUnit)
		}
		state[p] = visiting
		stack = append(stack, p)
		for _, d := range byPath[p].DependsOn {
			if err := visit(d); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		state[p] = done
		return nil
	}
	for _, t := range trees {
		if err := visit(t.Path); err != nil {
			return err
		}
	}
	return nil
}

// validateTrees checks the v2 layout a document claims against the clusters
// it publishes. It is the check a backend runs on bytes it was handed, so it
// trusts none of it: every path is recomputed, every dependency must resolve,
// and a tree set that would deadlock is refused before a reconciler finds out.
func validateTrees(clusters []BundleClusterTree, trees []BundleTree) error {
	if len(trees) == 0 {
		return nil
	}
	docs := map[string]int{}
	for _, c := range clusters {
		docs[c.Cluster] = c.Documents
	}
	sum := map[string]int{}
	for _, t := range trees {
		if FlatCluster(t.Cluster) {
			return fmt.Errorf("%w: tree %s is on flat cluster %q, which has no trees", ErrInvalid, t.Path, t.Cluster)
		}
		if !t.Phase.Valid() {
			return fmt.Errorf("%w: tree %s has phase %q (expected pre, rollout or post)", ErrInvalid, t.Path, t.Phase)
		}
		if want := BundleTreePath(t.Cluster, t.Unit, t.Phase); t.Path != want {
			return fmt.Errorf("%w: tree claims path %q, but the layout puts cluster %q unit %q phase %s at %q", ErrInvalid, t.Path, t.Cluster, t.Unit, t.Phase, want)
		}
		if t.Documents <= 0 {
			return fmt.Errorf("%w: tree %s holds %d documents; an empty path would reconcile green over nothing", ErrInvalid, t.Path, t.Documents)
		}
		if t.TimeoutSeconds <= 0 || t.RetryIntervalSeconds <= 0 {
			return fmt.Errorf("%w: tree %s has no per-attempt timeout or retry interval", ErrInvalid, t.Path)
		}
		if t.BudgetSeconds < MinBudgetSeconds {
			return fmt.Errorf("%w: tree %s has a %ds budget, under the %ds minimum: the control plane would fail it before one attempt could finish", ErrInvalid, t.Path, t.BudgetSeconds, MinBudgetSeconds)
		}
		sum[t.Cluster] += t.Documents
	}
	for cluster, n := range sum {
		have, ok := docs[cluster]
		if !ok {
			return fmt.Errorf("%w: trees name cluster %q, which the bundle does not publish", ErrInvalid, cluster)
		}
		if have != n {
			return fmt.Errorf("%w: cluster %q publishes %d documents but its trees hold %d", ErrInvalid, cluster, have, n)
		}
	}
	for _, c := range clusters {
		if !FlatCluster(c.Cluster) && sum[c.Cluster] == 0 {
			return fmt.Errorf("%w: cluster %q has documents but no trees, though trees are published", ErrInvalid, c.Cluster)
		}
	}
	if !sort.SliceIsSorted(trees, func(i, j int) bool { return trees[i].Path < trees[j].Path }) {
		return fmt.Errorf("%w: trees are not sorted by path", ErrInvalid)
	}
	return validateTreeGraph(trees)
}
