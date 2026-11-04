package release

import (
	"fmt"
	"sort"
	"strings"
)

// Comparing two releases from what the ledger recorded at cut: the commits
// between them, the artifacts whose identity moved, and the Kubernetes objects
// their bundles for one env add, change and remove. Every function here is
// pure over records, so a control plane with no repository and no registry
// access answers "what changed" exactly as forge would.

// ─── Commits ─────────────────────────────────────────────────────────────────

// CommitComparisonState says how a [CommitComparison] was assembled. Check it
// before reading Commits: an empty list means "nothing between them" only
// when the state is computed.
type CommitComparisonState string

const (
	// CommitsComputed: the later release's range was walked back, base by
	// base, to the earlier one.
	CommitsComputed CommitComparisonState = "computed"
	// CommitsSame: the two are one release.
	CommitsSame CommitComparisonState = "same"
	// CommitsByCutTime: no base chain joins the two (a release in between
	// was cut from a shallow clone, or before ranges were recorded), so the
	// commits are those recorded by the releases cut between them on the
	// later one's branch. Approximate, and marked Truncated when any of
	// those ranges was.
	CommitsByCutTime CommitComparisonState = "by_cut_time"
	// CommitsUnrecorded: no release between them recorded any commits.
	CommitsUnrecorded CommitComparisonState = "unrecorded"
)

// MaxComparedCommits caps the commits a comparison lists. Total still counts
// them all when every composed range could.
const MaxComparedCommits = 250

// maxBaseWalk bounds a base-chain walk, which also stops a corrupt ledger's
// cycle.
const maxBaseWalk = 1000

// CommitComparison is the source history between two releases.
type CommitComparison struct {
	State CommitComparisonState `json:"state"`
	// Reverts is true when To is the OLDER release. Commits are then the
	// ones From has and To does not: what moving From → To takes away.
	Reverts bool `json:"reverts,omitempty"`
	// Commits are newest first, at most MaxComparedCommits, never nil.
	Commits []Commit `json:"commits"`
	// Total is the number of commits between the two when every composed
	// range could count its own; 0 when one could not.
	Total int `json:"total,omitempty"`
	// Truncated says Commits is not every commit between the two.
	Truncated bool `json:"truncated,omitempty"`
	// Releases are the versions whose recorded ranges were composed, newest
	// first.
	Releases []string `json:"releases,omitempty"`
}

// CompareCommits composes the commits between from and to out of the ranges
// recorded on releases (the project's ledger; from and to need not be in it).
func CompareCommits(from, to Release, releases []Release) CommitComparison {
	if from.Version == to.Version {
		return CommitComparison{State: CommitsSame, Commits: []Commit{}}
	}
	index := make(map[string]Release, len(releases)+2)
	for _, r := range releases {
		index[r.Version] = r
	}
	index[from.Version], index[to.Version] = from, to
	if chain, ok := walkBases(to, from.Version, index); ok {
		return composeRanges(chain, CommitsComputed, false)
	}
	if chain, ok := walkBases(from, to.Version, index); ok {
		return composeRanges(chain, CommitsComputed, true)
	}
	return byCutTime(from, to, index)
}

// walkBases follows recorded bases from start until stop; ok is false when
// the chain breaks first.
func walkBases(start Release, stop string, index map[string]Release) ([]Release, bool) {
	var chain []Release
	cur := start
	for range maxBaseWalk {
		chain = append(chain, cur)
		if cur.Commits == nil || cur.Commits.BaseRelease == "" {
			return nil, false
		}
		if cur.Commits.BaseRelease == stop {
			return chain, true
		}
		next, ok := index[cur.Commits.BaseRelease]
		if !ok {
			return nil, false
		}
		cur = next
	}
	return nil, false
}

// byCutTime is the fallback: the ranges recorded by every release of the
// later one's project and branch cut after the earlier one, up to the later.
func byCutTime(from, to Release, index map[string]Release) CommitComparison {
	older, newer, reverts := from, to, false
	if to.CreatedAt.Before(from.CreatedAt) {
		older, newer, reverts = to, from, true
	}
	branch := ""
	if newer.Provenance != nil {
		branch = newer.Provenance.Branch
	}
	var window []Release
	for _, r := range index {
		if r.Project != newer.Project || !r.CreatedAt.After(older.CreatedAt) || r.CreatedAt.After(newer.CreatedAt) {
			continue
		}
		if branch != "" && (r.Provenance == nil || r.Provenance.Branch != branch) && r.Version != newer.Version {
			continue
		}
		window = append(window, r)
	}
	sort.Slice(window, func(i, j int) bool {
		if !window[i].CreatedAt.Equal(window[j].CreatedAt) {
			return window[i].CreatedAt.After(window[j].CreatedAt)
		}
		return window[i].Version > window[j].Version
	})
	out := composeRanges(window, CommitsByCutTime, reverts)
	if len(out.Commits) == 0 && out.Truncated {
		out.State = CommitsUnrecorded
	}
	if len(window) > 0 && window[len(window)-1].Commits != nil && window[len(window)-1].Commits.BaseRelease != older.Version {
		// The oldest range does not start at the earlier release, so the
		// window may have missed commits on the way there.
		out.Truncated, out.Total = true, 0
	}
	return out
}

// composeRanges concatenates the ranges of releases (newest first), deduped.
func composeRanges(releases []Release, state CommitComparisonState, reverts bool) CommitComparison {
	out := CommitComparison{State: state, Reverts: reverts, Commits: []Commit{}}
	seen := map[string]bool{}
	total, countKnown, all := 0, true, 0
	for _, r := range releases {
		out.Releases = append(out.Releases, r.Version)
		if r.Commits == nil {
			countKnown = false
			out.Truncated = true
			continue
		}
		if !r.Commits.CountKnown() {
			countKnown = false
		}
		total += r.Commits.Total
		if r.Commits.Truncated {
			out.Truncated = true
		}
		for _, c := range r.Commits.Commits {
			if seen[c.SHA] {
				continue
			}
			seen[c.SHA] = true
			all++
			if len(out.Commits) < MaxComparedCommits {
				out.Commits = append(out.Commits, c)
			}
		}
	}
	if all > len(out.Commits) {
		out.Truncated = true
	}
	if countKnown {
		out.Total = total
		out.Truncated = out.Truncated || total > len(out.Commits)
	}
	return out
}

// ─── Artifacts ───────────────────────────────────────────────────────────────

// ArtifactChangeKind is how one artifact differs between two releases.
type ArtifactChangeKind string

const (
	ArtifactAdded   ArtifactChangeKind = "added"
	ArtifactRemoved ArtifactChangeKind = "removed"
	ArtifactChanged ArtifactChangeKind = "changed"
)

// ArtifactChange is one artifact whose identity moved. For an OCI image the
// identity is the digest, one entry per variant ("web[staging]" for a variant
// image); for a source-built artifact the resolved commit; for a package its
// version.
type ArtifactChange struct {
	Name   string             `json:"name"`
	Kind   Kind               `json:"kind"`
	Change ArtifactChangeKind `json:"change"`
	From   string             `json:"from,omitempty"`
	To     string             `json:"to,omitempty"`
}

// CompareArtifacts lists the artifacts whose identity differs between from and
// to, sorted by name, and counts the ones that did not.
func CompareArtifacts(from, to Release) (changed []ArtifactChange, unchanged int) {
	a, b := artifactIdentities(from), artifactIdentities(to)
	names := map[string]bool{}
	for n := range a {
		names[n] = true
	}
	for n := range b {
		names[n] = true
	}
	for _, n := range sortedMapKeys(names) {
		was, inFrom := a[n]
		now, inTo := b[n]
		switch {
		case !inFrom:
			changed = append(changed, ArtifactChange{Name: n, Kind: now.kind, Change: ArtifactAdded, To: now.id})
		case !inTo:
			changed = append(changed, ArtifactChange{Name: n, Kind: was.kind, Change: ArtifactRemoved, From: was.id})
		case was != now:
			changed = append(changed, ArtifactChange{Name: n, Kind: now.kind, Change: ArtifactChanged, From: was.id, To: now.id})
		default:
			unchanged++
		}
	}
	return changed, unchanged
}

type artifactIdentity struct {
	kind Kind
	id   string
}

func artifactIdentities(r Release) map[string]artifactIdentity {
	out := map[string]artifactIdentity{}
	for name, a := range r.Artifacts {
		switch {
		case a.Kind == KindOCI && a.Mode == ModeVariant:
			for variant, d := range a.Digests {
				out[name+"["+variant+"]"] = artifactIdentity{a.Kind, d}
			}
		case a.Kind == KindOCI:
			out[name] = artifactIdentity{a.Kind, a.Digests[SharedVariant]}
		case a.Source != nil:
			out[name] = artifactIdentity{a.Kind, a.Source.Commit}
		default:
			id := a.Version
			if a.Integrity != "" {
				id = strings.TrimSpace(id + " " + a.Integrity)
			}
			if id == "" {
				id = a.URI
			}
			out[name] = artifactIdentity{a.Kind, id}
		}
	}
	return out
}

// ─── Objects ─────────────────────────────────────────────────────────────────

// ObjectChangeKind is how one object differs between two bundles.
type ObjectChangeKind string

const (
	ObjectAdded    ObjectChangeKind = "added"
	ObjectModified ObjectChangeKind = "modified"
	ObjectRemoved  ObjectChangeKind = "removed"
)

// PartChange is one section of an object that moved.
type PartChange struct {
	Part   string           `json:"part"`
	Change ObjectChangeKind `json:"change"`
}

// ObjectDelta is one object two bundles disagree on.
type ObjectDelta struct {
	Change   ObjectChangeKind `json:"change"`
	Key      ObjectKey        `json:"key"`
	Workload string           `json:"workload,omitempty"`
	// Summary is the one line: the object's own summary when it was added
	// or removed, and what changed ([DescribeObjectChange]) when modified.
	Summary string `json:"summary,omitempty"`
	// Images are the release-bound images whose digest moved in it.
	Images []ImageChange `json:"images,omitempty"`
	// Parts are the sections that moved, when both sides recorded them.
	Parts []PartChange `json:"parts,omitempty"`
}

// ObjectComparison is the object half of a release comparison.
type ObjectComparison struct {
	Deltas    []ObjectDelta `json:"deltas"`
	Unchanged int           `json:"unchanged"`
	// SummariesMissing says at least one bundle carried no object
	// summaries (it was built before they existed), so added, removed and
	// modified objects are named without saying what they are or what in
	// them changed.
	SummariesMissing bool `json:"summaries_missing,omitempty"`
}

// CompareObjects diffs two bundles of one env, object by object: added and
// removed by key, modified by hash.
func CompareObjects(from, to BundleRecord) ObjectComparison {
	out := ObjectComparison{Deltas: []ObjectDelta{}}
	out.SummariesMissing = (len(from.Shape.Objects) > 0 && len(from.Objects) == 0) ||
		(len(to.Shape.Objects) > 0 && len(to.Objects) == 0)
	fromSums, toSums := summariesByKey(from.Objects), summariesByKey(to.Objects)
	was := map[ObjectKey]ShapeObject{}
	for _, o := range from.Shape.Objects {
		was[o.Key()] = o
	}
	seen := map[ObjectKey]bool{}
	for _, now := range to.Shape.Objects {
		key := now.Key()
		seen[key] = true
		old, existed := was[key]
		switch {
		case !existed:
			out.Deltas = append(out.Deltas, ObjectDelta{
				Change: ObjectAdded, Key: key, Workload: now.Workload, Summary: toSums[key].Summary,
			})
		case old.Hash == now.Hash && old.Tree == now.Tree:
			out.Unchanged++
		default:
			d := ObjectDelta{Change: ObjectModified, Key: key, Workload: now.Workload,
				Images: imageChanges(old.Images, now.Images)}
			fs, fok := fromSums[key]
			ts, tok := toSums[key]
			if fok && tok && configMoved(old, now) {
				d.Parts = partChanges(fs.Parts, ts.Parts)
			}
			d.Summary = DescribeObjectChange(old, now, d.Parts)
			out.Deltas = append(out.Deltas, d)
		}
	}
	for _, old := range from.Shape.Objects {
		if key := old.Key(); !seen[key] {
			seen[key] = true
			out.Deltas = append(out.Deltas, ObjectDelta{
				Change: ObjectRemoved, Key: key, Workload: old.Workload, Summary: fromSums[key].Summary,
			})
		}
	}
	order := map[ObjectChangeKind]int{ObjectAdded: 0, ObjectModified: 1, ObjectRemoved: 2}
	sort.SliceStable(out.Deltas, func(i, j int) bool {
		a, b := out.Deltas[i], out.Deltas[j]
		if order[a.Change] != order[b.Change] {
			return order[a.Change] < order[b.Change]
		}
		return a.Key.String() < b.Key.String()
	})
	return out
}

func summariesByKey(in []ObjectSummary) map[ObjectKey]ObjectSummary {
	out := make(map[ObjectKey]ObjectSummary, len(in))
	for _, s := range in {
		out[s.Key] = s
	}
	return out
}

// configMoved reports whether anything but the images changed. Without a
// ConfigHash on both sides it cannot split the two, and says yes when the
// hashes differ (the conservative reading, as in [ObjectChange]).
func configMoved(a, b ShapeObject) bool {
	if a.ConfigHash != "" && b.ConfigHash != "" {
		return a.ConfigHash != b.ConfigHash
	}
	return a.Hash != b.Hash
}

func imageChanges(a, b map[string]string) []ImageChange {
	var out []ImageChange
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	for _, k := range sortedMapKeys(keys) {
		if a[k] != b[k] {
			out = append(out, ImageChange{Artifact: k, From: a[k], To: b[k]})
		}
	}
	return out
}

func partChanges(a, b map[string]string) []PartChange {
	var out []PartChange
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	for _, k := range sortedMapKeys(keys) {
		was, inA := a[k]
		now, inB := b[k]
		switch {
		case !inA:
			out = append(out, PartChange{Part: k, Change: ObjectAdded})
		case !inB:
			out = append(out, PartChange{Part: k, Change: ObjectRemoved})
		case was != now:
			out = append(out, PartChange{Part: k, Change: ObjectModified})
		}
	}
	return out
}

// maxChangePhrases bounds a change description; the rest are counted.
const maxChangePhrases = 6

// DescribeObjectChange is the one line for a modified object: which images
// moved, which sections changed, and whether it moved between delivery
// trees — "image control-plane; env FORCE_RECONCILE removed". parts are the
// sections that moved, or nil when either side recorded none; then a config
// change is reported as just that.
func DescribeObjectChange(from, to ShapeObject, parts []PartChange) string {
	var phrases []string
	for _, img := range imageChanges(from.Images, to.Images) {
		switch {
		case img.From == "":
			phrases = append(phrases, "image "+img.Artifact+" added")
		case img.To == "":
			phrases = append(phrases, "image "+img.Artifact+" removed")
		default:
			phrases = append(phrases, "image "+img.Artifact)
		}
	}
	if from.Tree != to.Tree {
		phrases = append(phrases, fmt.Sprintf("moves %s → %s", treeName(from.Tree), treeName(to.Tree)))
	}
	if configMoved(from, to) {
		described := describeParts(parts)
		if len(described) == 0 {
			described = []string{"config changed"}
		}
		phrases = append(phrases, described...)
	}
	if len(phrases) == 0 {
		phrases = []string{"changed"}
	}
	if len(phrases) > maxChangePhrases {
		phrases = append(phrases[:maxChangePhrases-1], fmt.Sprintf("+%d more", len(phrases)-maxChangePhrases+1))
	}
	return oneLine(strings.Join(phrases, "; "), 2*MaxObjectSummaryBytes)
}

func treeName(t string) string {
	if t == "" {
		return "flat"
	}
	return t
}

// containerSectionNames are the per-container part suffixes SummarizeObject
// writes, used to read "<container>.<section>" back.
var containerSectionNames = map[string]bool{
	"command": true, "resources": true, "ports": true, "probes": true, "volumeMounts": true,
	"envFrom": true, "securityContext": true, "other": true,
}

// describeParts turns part changes into phrases, grouping env variables by
// container and verb ("env A, B removed") and naming the container only when
// the object has more than one.
func describeParts(parts []PartChange) []string {
	type group struct {
		container string
		change    ObjectChangeKind
	}
	containers := map[string]bool{}
	envs := map[group][]string{}
	var groupOrder []group
	var phrases []string
	type sectionChange struct {
		container, section string
		change             ObjectChangeKind
	}
	var sections []sectionChange
	for _, p := range parts {
		if c, rest, ok := strings.Cut(p.Part, ".env."); ok && !strings.Contains(c, ".") {
			containers[c] = true
			g := group{c, p.Change}
			if _, seen := envs[g]; !seen {
				groupOrder = append(groupOrder, g)
			}
			envs[g] = append(envs[g], rest)
			continue
		}
		if c, section, ok := strings.Cut(p.Part, "."); ok && containerSectionNames[section] {
			containers[c] = true
			sections = append(sections, sectionChange{c, section, p.Change})
			continue
		}
		phrases = append(phrases, partLabel(p.Part)+" "+verb(p.Change))
	}
	suffix := func(c string) string {
		if len(containers) > 1 {
			return " (" + c + ")"
		}
		return ""
	}
	var out []string
	for _, g := range groupOrder {
		out = append(out, "env "+strings.Join(envs[g], ", ")+" "+verb(g.change)+suffix(g.container))
	}
	for _, s := range sections {
		out = append(out, s.section+" "+verb(s.change)+suffix(s.container))
	}
	return append(out, phrases...)
}

func partLabel(part string) string {
	switch part {
	case "metadata.labels":
		return "labels"
	case "metadata.annotations":
		return "annotations"
	case "pod.labels":
		return "pod labels"
	case "pod.annotations":
		return "pod annotations"
	case PartOverflow:
		return "other sections"
	}
	return part
}

func verb(c ObjectChangeKind) string {
	switch c {
	case ObjectAdded:
		return "added"
	case ObjectRemoved:
		return "removed"
	}
	return "changed"
}
