package cli

// BOUND vs LIVE, PER TREE.
//
// A promotion is INTENT: it binds the env to a release, and nothing has moved
// when it is written. Convergence is OBSERVED: the control plane's observer
// reads what each Kustomization actually applied. The two are different facts
// and `forge env status` and the deploy plan show them side by side, per tree,
// rather than collapsing them into "deployed" (owner decision 11: promotion is
// intent, convergence is observed; the binding is never moved late to make the
// two agree). A tree that is bound to v2 and live at v1 is behind, and that is
// the information, not a defect in the display.
//
// WHAT A TREE IS. One cluster x delivery unit x phase of the bound bundle
// (release.BundleTree). Its identity is its Path. A unit forge applies directly
// has trees in the bundle too, but nothing observes them, and they are shown as
// such: "forge applies" rather than a status that would read as agreement the
// control plane never checked.
//
// WHERE THE FACTS COME FROM, and what happens when one is missing:
//
//   - bound   the bound bundle's own shape (every object's Tree), so the
//             inventory is exactly what the bundle carries. Always available
//             when the bundle is recorded.
//   - live    the control plane's convergence record for the tree: the revision
//             its Kustomization last applied, and the reading. Control-plane C2
//             adds these per tree to ListConvergences/GetLiveView. UNTIL THEN a
//             control plane reports one reading per CLUSTER, which is read as
//             that cluster's trees' reading and labelled as cluster-level, so
//             the display never claims per-tree precision it does not have.
//   - status  derived: converged / progressing / failed / behind / unobserved /
//             forge-applied.
//
// Every absence degrades to a stated state ("not reported"), never to a guess.

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/release"
)

// Tree statuses.
const (
	// treeConverged: the tree's live revision is the bound bundle's digest and
	// the control plane reads it healthy.
	treeConverged = "converged"
	// treeBehind: the tree is live at a DIFFERENT revision than the one bound,
	// and nothing is failing: the hub has not got there yet.
	treeBehind = "behind"
	// treeFailed: the control plane reads the tree as failed.
	treeFailed = "failed"
	// treeUnobserved: the hub converges this tree and has reported nothing
	// about it, or the control plane reports nothing per tree. Not a failure
	// and not agreement.
	treeUnobserved = "unobserved"
	// treeForgeApplied: a unit forge applies directly. No reconciler watches
	// it, so there is no live revision to compare.
	treeForgeApplied = "forge-applied"
)

// envStatusTree is one tree of the bound bundle, with the bound and the live
// revision and what that adds up to.
type envStatusTree struct {
	// Path is the tree's identity in the bundle: <cluster>/<unit>/<phase>.
	Path    string `json:"path"`
	Cluster string `json:"cluster"`
	Unit    string `json:"unit"`
	Phase   string `json:"phase"`
	// Applier is who applies the tree's unit: "hub" or "forge".
	Applier string `json:"applier"`
	// Bound is the bundle digest the env's current promotion binds the tree to.
	Bound string `json:"bound,omitempty"`
	// Live is the revision the reconciler reports it applied, verbatim. Empty
	// when it has not reported one (see Source).
	Live string `json:"live,omitempty"`
	// Status is the verdict from the two.
	Status string `json:"status"`
	// Reason and Message are the reconciler's own words, passed through.
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
	// ObservedAt is when the control plane recorded the reading.
	ObservedAt string `json:"observed_at,omitempty"`
	// Source says how precise the reading is: "tree" (the control plane
	// reported this tree), "cluster" (it reported the tree's cluster as a
	// whole, because it predates per-tree reports), or "" (nothing reported,
	// or a unit forge applies).
	Source string `json:"source,omitempty"`
}

// treeReading is one control-plane observation, at whatever precision it was
// reported. Tree empty means the reading is about the whole cluster.
type treeReading struct {
	Cluster    string
	Tree       string // the tree's path, "" for a cluster-level reading
	State      string // "converged" | "failed"
	Revision   string
	Reason     string
	Message    string
	ObservedAt string
}

// treeInventory lists the trees a bound bundle carries, from its shape: one per
// distinct (cluster, unit/phase) among its objects on a v2 cluster. Sorted by
// path. A bundle with no v2 cluster has no trees.
func treeInventory(shape release.Shape) []envStatusTree {
	seen := map[string]envStatusTree{}
	for _, o := range shape.Objects {
		if o.Tree == "" || release.FlatCluster(o.Cluster) {
			continue
		}
		unit, phase, ok := strings.Cut(o.Tree, "/")
		if !ok {
			continue
		}
		path := release.BundleTreePath(o.Cluster, unit, release.Phase(phase))
		if _, dup := seen[path]; dup {
			continue
		}
		seen[path] = envStatusTree{Path: path, Cluster: o.Cluster, Unit: unit, Phase: phase}
	}
	out := make([]envStatusTree, 0, len(seen))
	for _, t := range seen {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// judgeTrees combines the inventory, the split and the control plane's readings
// into one row per tree. boundDigest is the bound bundle's digest.
//
// A reading for the tree wins over one for its cluster; a cluster-level reading
// is used only for a tree that has none of its own, and then labelled so. A
// forge-applied tree takes no reading at all: nothing observes it, and showing
// another unit's cluster reading against it would invent agreement.
func judgeTrees(trees []envStatusTree, split unitSplit, boundDigest string, readings []treeReading) []envStatusTree {
	byTree := map[string]treeReading{}
	byCluster := map[string]treeReading{}
	for _, r := range readings {
		if r.Tree != "" {
			byTree[r.Tree] = r
		} else if _, ok := byCluster[r.Cluster]; !ok {
			byCluster[r.Cluster] = r
		}
	}
	out := make([]envStatusTree, len(trees))
	for i, t := range trees {
		t.Applier = split.Applier(t.Unit)
		t.Bound = boundDigest
		if t.Applier == release.UnitApplierForge {
			t.Status = treeForgeApplied
			out[i] = t
			continue
		}
		r, ok := byTree[t.Path]
		t.Source = "tree"
		if !ok {
			r, ok = byCluster[t.Cluster]
			t.Source = "cluster"
		}
		if !ok {
			t.Status, t.Source = treeUnobserved, ""
			out[i] = t
			continue
		}
		t.Live, t.Reason, t.Message, t.ObservedAt = r.Revision, r.Reason, r.Message, r.ObservedAt
		switch {
		case r.State == "failed":
			t.Status = treeFailed
		case r.State == "converged" && boundDigest != "" && r.Revision != "" && r.Revision != boundDigest:
			t.Status = treeBehind
		case r.State == "converged":
			t.Status = treeConverged
		default:
			t.Status = treeUnobserved
		}
		out[i] = t
	}
	return out
}

// shortTreeRev abbreviates a sha256 revision for the text table; --json carries the
// whole value.
func shortTreeRev(rev string) string {
	rev = strings.TrimPrefix(rev, "sha256:")
	if rev == "" {
		return "-"
	}
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

// writeTreeTable renders the per-tree view. It says, once, that bound is intent
// and live is observed, because the two columns disagreeing is a state and not a
// rendering bug.
func writeTreeTable(w io.Writer, trees []envStatusTree) {
	if len(trees) == 0 {
		return
	}
	fmt.Fprintf(w, "\nTrees (bound is the promotion's intent; live is what the hub's reconciler reports it applied)\n")
	width := 0
	for _, t := range trees {
		if n := len(t.Path); n > width {
			width = n
		}
	}
	clusterLevel := false
	for _, t := range trees {
		note := ""
		switch {
		case t.Status == treeForgeApplied:
			note = "applied by forge from a machine; no reconciler observes it"
		case t.Status == treeUnobserved:
			note = "the hub converges this tree and has not reported it"
		case t.Source == "cluster":
			note = "reading is for the tree's cluster as a whole"
			clusterLevel = true
		}
		if t.Reason != "" && (t.Status == treeFailed || t.Status == treeBehind) {
			note = strings.TrimSpace(strings.Join(nonEmptyStrings(t.Reason, oneLine(t.Message, 160)), ": "))
		}
		fmt.Fprintf(w, "  %-*s  %-5s  bound %-12s  live %-12s  %-13s %s\n",
			width, t.Path, t.Applier, shortTreeRev(t.Bound), shortTreeRev(t.Live), strings.ToUpper(t.Status), note)
	}
	if clusterLevel {
		fmt.Fprintf(w, "  (this control plane reports convergence per cluster; per-tree readings need a newer one)\n")
	}
}
