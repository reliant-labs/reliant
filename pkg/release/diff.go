package release

import "sort"

// ShapeDiff is what changes between two shapes of one environment: the shape
// that is live (the applied bundle's, or the env's declared shape) and a
// candidate (a checkout's render, or the bundle about to be deployed).
//
// It is a SET comparison over per-object hashes, which is why a shape carries
// one hash per object: a Live-vs-candidate diff never fetches or parses a
// manifest.
type ShapeDiff struct {
	// LiveUnknown: there was no live shape to compare against (nothing
	// recorded yet). Every candidate object then appears in Added, and a
	// reader must say "no recorded config" rather than present the diff as
	// a real comparison — empty and unknown must never render alike.
	LiveUnknown bool `json:"live_unknown,omitempty"`
	// KindChanged is never set.
	//
	// Deprecated: an env has no placement kind any more — placement is per
	// workload (docs/design/per-workload-placement.md), and a part that moves
	// between placements is reported in RuntimeChanges. Kept only so a reader
	// built against an older forge still decodes the document; removed with
	// Shape.Kind.
	KindChanged *KindChange `json:"kind_changed,omitempty"`

	Added   []ShapeObject  `json:"added,omitempty"`
	Removed []ShapeObject  `json:"removed,omitempty"`
	Changed []ObjectChange `json:"changed,omitempty"`
	// Moved are objects present on both sides, in the same cluster, whose
	// delivery tree differs. Independent of Changed: a move can leave the
	// object's bytes untouched.
	Moved []ObjectMove `json:"moved,omitempty"`

	WorkloadsAdded   []string        `json:"workloads_added,omitempty"`
	WorkloadsRemoved []string        `json:"workloads_removed,omitempty"`
	RuntimeChanges   []RuntimeChange `json:"runtime_changes,omitempty"`
	SecretsAdded     []ShapeSecret   `json:"secrets_added,omitempty"`
	SecretsRemoved   []ShapeSecret   `json:"secrets_removed,omitempty"`
	DomainsAdded     []string        `json:"domains_added,omitempty"`
	DomainsRemoved   []string        `json:"domains_removed,omitempty"`
	ClustersAdded    []string        `json:"clusters_added,omitempty"`
	ClustersRemoved  []string        `json:"clusters_removed,omitempty"`
}

// ObjectMove is one object changing delivery tree between two shapes.
// From and To are "<unit>/<phase>"; "" is the v1 flat layout, so a move from ""
// is the layout migration.
type ObjectMove struct {
	Key  ObjectKey `json:"key"`
	From string    `json:"from"`
	To   string    `json:"to"`
}

// KindChange is a disagreement about an env's kind.
//
// Deprecated: see ShapeDiff.KindChanged.
type KindChange struct {
	Live      EnvKind `json:"live"`
	Candidate EnvKind `json:"candidate"`
}

// ObjectChange is one object present on both sides whose hash differs.
type ObjectChange struct {
	Live      ShapeObject `json:"live"`
	Candidate ShapeObject `json:"candidate"`
	// ConfigChanged: the non-image half of the object changed. When either
	// side lacks a ConfigHash the split cannot be made, and ConfigChanged
	// is true whenever the hashes differ (the conservative reading).
	ConfigChanged bool `json:"config_changed"`
	// Images are the release-bound images whose pinned digest changed.
	Images []ImageChange `json:"images,omitempty"`
}

// ImageChange is one artifact's digest moving.
type ImageChange struct {
	Artifact string `json:"artifact"`
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
}

// RuntimeChange is a workload moving between runtimes ("web: hosted →
// bucket") or clusters.
type RuntimeChange struct {
	Workload    string `json:"workload"`
	From        string `json:"from"`
	To          string `json:"to"`
	FromCluster string `json:"from_cluster,omitempty"`
	ToCluster   string `json:"to_cluster,omitempty"`
}

// Empty reports whether nothing differs. An unknown live side is never empty.
func (d ShapeDiff) Empty() bool {
	return !d.LiveUnknown &&
		len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Changed) == 0 && len(d.Moved) == 0 &&
		len(d.WorkloadsAdded) == 0 && len(d.WorkloadsRemoved) == 0 && len(d.RuntimeChanges) == 0 &&
		len(d.SecretsAdded) == 0 && len(d.SecretsRemoved) == 0 &&
		len(d.DomainsAdded) == 0 && len(d.DomainsRemoved) == 0 &&
		len(d.ClustersAdded) == 0 && len(d.ClustersRemoved) == 0
}

// DiffShapes compares a live shape (nil = nothing recorded) with a candidate.
// Pure, and shared by `forge env diff`, the deploy plan, and any backend that
// needs the same answer: one implementation, so a CLI and a server cannot
// disagree about what changed. Every list in the result is sorted.
func DiffShapes(live *Shape, candidate Shape) ShapeDiff {
	cand := candidate.Canonical()
	if live == nil {
		return ShapeDiff{
			LiveUnknown:    true,
			Added:          cand.Objects,
			WorkloadsAdded: workloadNames(cand.Workloads),
			SecretsAdded:   cand.Secrets,
			DomainsAdded:   cand.Domains,
			ClustersAdded:  cand.Clusters,
		}
	}
	l := live.Canonical()
	var d ShapeDiff

	liveObjs := map[ObjectKey]ShapeObject{}
	for _, o := range l.Objects {
		liveObjs[o.Key()] = o
	}
	candObjs := map[ObjectKey]bool{}
	for _, o := range cand.Objects {
		candObjs[o.Key()] = true
		lo, ok := liveObjs[o.Key()]
		switch {
		case !ok:
			d.Added = append(d.Added, o)
		case lo.Hash != o.Hash:
			d.Changed = append(d.Changed, objectChange(lo, o))
		}
		if ok && lo.Tree != o.Tree {
			d.Moved = append(d.Moved, ObjectMove{Key: o.Key(), From: lo.Tree, To: o.Tree})
		}
	}
	for _, o := range l.Objects {
		if !candObjs[o.Key()] {
			d.Removed = append(d.Removed, o)
		}
	}

	liveWL := map[string]ShapeWorkload{}
	for _, w := range l.Workloads {
		liveWL[w.Name] = w
	}
	candWL := map[string]bool{}
	for _, w := range cand.Workloads {
		candWL[w.Name] = true
		lw, ok := liveWL[w.Name]
		switch {
		case !ok:
			d.WorkloadsAdded = append(d.WorkloadsAdded, w.Name)
		case lw.Runtime != w.Runtime || lw.Cluster != w.Cluster:
			d.RuntimeChanges = append(d.RuntimeChanges, RuntimeChange{
				Workload: w.Name, From: lw.Runtime, To: w.Runtime, FromCluster: lw.Cluster, ToCluster: w.Cluster,
			})
		}
	}
	for _, w := range l.Workloads {
		if !candWL[w.Name] {
			d.WorkloadsRemoved = append(d.WorkloadsRemoved, w.Name)
		}
	}

	liveSec := map[string]bool{}
	for _, s := range l.Secrets {
		liveSec[s.Name] = true
	}
	candSec := map[string]bool{}
	for _, s := range cand.Secrets {
		candSec[s.Name] = true
		if !liveSec[s.Name] {
			d.SecretsAdded = append(d.SecretsAdded, s)
		}
	}
	for _, s := range l.Secrets {
		if !candSec[s.Name] {
			d.SecretsRemoved = append(d.SecretsRemoved, s)
		}
	}

	d.DomainsAdded, d.DomainsRemoved = setDiff(l.Domains, cand.Domains)
	d.ClustersAdded, d.ClustersRemoved = setDiff(l.Clusters, cand.Clusters)
	return d
}

func objectChange(live, cand ShapeObject) ObjectChange {
	c := ObjectChange{Live: live, Candidate: cand}
	if live.ConfigHash != "" && cand.ConfigHash != "" {
		c.ConfigChanged = live.ConfigHash != cand.ConfigHash
	} else {
		c.ConfigChanged = true
	}
	artifacts := map[string]bool{}
	for a := range live.Images {
		artifacts[a] = true
	}
	for a := range cand.Images {
		artifacts[a] = true
	}
	names := make([]string, 0, len(artifacts))
	for a := range artifacts {
		names = append(names, a)
	}
	sort.Strings(names)
	for _, a := range names {
		if live.Images[a] != cand.Images[a] {
			c.Images = append(c.Images, ImageChange{Artifact: a, From: live.Images[a], To: cand.Images[a]})
		}
	}
	return c
}

// setDiff returns (in b not a, in a not b), sorted.
func setDiff(a, b []string) (added, removed []string) {
	inA := map[string]bool{}
	for _, s := range a {
		inA[s] = true
	}
	inB := map[string]bool{}
	for _, s := range b {
		inB[s] = true
		if !inA[s] {
			added = append(added, s)
		}
	}
	for _, s := range a {
		if !inB[s] {
			removed = append(removed, s)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

func workloadNames(ws []ShapeWorkload) []string {
	if len(ws) == 0 {
		return nil
	}
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.Name
	}
	sort.Strings(out)
	return out
}
