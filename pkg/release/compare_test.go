package release

import (
	"strings"
	"testing"
	"time"
)

func sha(c string) string { return strings.Repeat(c, 40) }

func commit(c, subject string) Commit {
	return Commit{SHA: sha(c), Subject: subject, Author: "alice", Time: time.Unix(1700000000, 0).UTC()}
}

func rangedRelease(version string, cut int, base string, commits ...Commit) Release {
	r := Release{Version: version, CreatedAt: time.Unix(int64(1800000000+cut), 0).UTC(), Provenance: &Provenance{Branch: "main"}}
	r.Commits = &CommitRange{Commits: append([]Commit{}, commits...), Total: len(commits)}
	if base != "" {
		r.Commits.BaseRelease, r.Commits.BaseCommit = base, sha("f")
	}
	return r
}

func TestCompareCommitsWalksRecordedBases(t *testing.T) {
	v1 := rangedRelease("v1", 1, "")
	v2 := rangedRelease("v2", 2, "v1", commit("b", "c3"), commit("a", "c2"))
	v3 := rangedRelease("v3", 3, "v2", commit("c", "c4"))
	ledger := []Release{v1, v2, v3}

	got := CompareCommits(v1, v3, ledger)
	if got.State != CommitsComputed || got.Reverts || got.Total != 3 || got.Truncated {
		t.Fatalf("v1→v3 = %+v", got)
	}
	if strings.Join(subjects(got.Commits), ",") != "c4,c3,c2" {
		t.Fatalf("commits = %v, want newest first across both ranges", subjects(got.Commits))
	}
	if strings.Join(got.Releases, ",") != "v3,v2" {
		t.Fatalf("composed releases = %v", got.Releases)
	}

	back := CompareCommits(v3, v1, ledger)
	if back.State != CommitsComputed || !back.Reverts || back.Total != 3 {
		t.Fatalf("v3→v1 must list what the move takes away: %+v", back)
	}
	if same := CompareCommits(v2, v2, ledger); same.State != CommitsSame || len(same.Commits) != 0 {
		t.Fatalf("same release = %+v", same)
	}
}

// Releases cut in CI from shallow clones record no base, so no chain joins
// them; the commits come from every release cut between the two on the later
// one's branch, and the result says it may be incomplete.
func TestCompareCommitsFallsBackToCutTime(t *testing.T) {
	shallow := func(version string, cut int, c Commit) Release {
		r := rangedRelease(version, cut, "", c)
		r.Commits.Total, r.Commits.Truncated = 0, true
		return r
	}
	v1 := shallow("v1", 1, commit("a", "c1"))
	v2 := shallow("v2", 2, commit("b", "c2"))
	preview := shallow("pr-7", 3, commit("e", "feature"))
	preview.Provenance.Branch = "feat"
	v3 := shallow("v3", 4, commit("c", "c3"))

	got := CompareCommits(v1, v3, []Release{v1, v2, preview, v3})
	if got.State != CommitsByCutTime || !got.Truncated || got.Total != 0 {
		t.Fatalf("fallback = %+v", got)
	}
	if strings.Join(subjects(got.Commits), ",") != "c3,c2" {
		t.Fatalf("commits = %v, want v3's and v2's (not the preview branch's, not v1's own)", subjects(got.Commits))
	}

	none := CompareCommits(Release{Version: "a", CreatedAt: time.Unix(1, 0)}, Release{Version: "b", CreatedAt: time.Unix(2, 0)}, nil)
	if none.State != CommitsUnrecorded {
		t.Fatalf("nothing recorded = %+v", none)
	}
}

func TestCompareArtifacts(t *testing.T) {
	d := func(c string) string { return "sha256:" + strings.Repeat(c, 64) }
	oci := func(digest string) Artifact {
		return Artifact{Kind: KindOCI, Mode: ModeShared, Digests: map[string]string{SharedVariant: digest}}
	}
	from := Release{Artifacts: map[string]Artifact{"api": oci(d("a")), "web": oci(d("b")), "old": oci(d("c"))}}
	to := Release{Artifacts: map[string]Artifact{"api": oci(d("1")), "web": oci(d("b")), "new": oci(d("2"))}}
	changed, unchanged := CompareArtifacts(from, to)
	if unchanged != 1 || len(changed) != 3 {
		t.Fatalf("changed %+v, unchanged %d", changed, unchanged)
	}
	if c := changed[0]; c.Name != "api" || c.Change != ArtifactChanged || c.From != d("a") || c.To != d("1") {
		t.Fatalf("api = %+v", c)
	}
	if changed[1].Name != "new" || changed[1].Change != ArtifactAdded || changed[2].Name != "old" || changed[2].Change != ArtifactRemoved {
		t.Fatalf("added/removed = %+v", changed[1:])
	}
}

func TestCompareObjectsNamesWhatChanged(t *testing.T) {
	h := func(c string) string { return "sha256:" + strings.Repeat(c, 64) }
	part := func(c string) string { return strings.Repeat(c, PartDigestLen) }
	deploy := func(hash, config, image string) ShapeObject {
		return ShapeObject{Cluster: "hub", Kind: "Deployment", Name: "workers", Workload: "workers",
			Hash: hash, ConfigHash: config, Images: map[string]string{"control-plane": image}}
	}
	cm := ShapeObject{Cluster: "hub", Kind: "ConfigMap", Name: "settings", Hash: h("c")}
	rule := ShapeObject{Cluster: "hub", Kind: "PrometheusRule", Name: "noop", Hash: h("d")}
	cron := ShapeObject{Cluster: "hub", Kind: "CronJob", Name: "force", Hash: h("e")}

	from := BundleRecord{
		Shape: Shape{Objects: []ShapeObject{deploy(h("1"), h("2"), h("a")), cm, cron}},
		Objects: []ObjectSummary{
			{Key: deploy("", "", "").Key(), Summary: "2 replicas · containers workers", Parts: map[string]string{"workers.env.FORCE_RECONCILE": part("a"), "workers.env.LOG": part("b")}},
			{Key: cm.Key(), Summary: "keys force_reconcile", Parts: map[string]string{"data.force_reconcile": part("a")}},
			{Key: cron.Key(), Summary: "schedule */5 * * * * · containers run"},
		},
	}
	cmNow := cm
	cmNow.Hash = h("f")
	to := BundleRecord{
		Shape: Shape{Objects: []ShapeObject{deploy(h("3"), h("4"), h("b")), cmNow, rule}},
		Objects: []ObjectSummary{
			{Key: deploy("", "", "").Key(), Summary: "2 replicas · containers workers", Parts: map[string]string{"workers.env.LOG": part("b")}},
			{Key: cm.Key(), Summary: "keys force_reconcile", Parts: map[string]string{"data.force_reconcile": part("b")}},
			{Key: rule.Key(), Summary: "alerts DeployConvergeForcedReconcile"},
		},
	}
	got := CompareObjects(from, to)
	if got.SummariesMissing || got.Unchanged != 0 || len(got.Deltas) != 4 {
		t.Fatalf("comparison = %+v", got)
	}
	want := []struct {
		change  ObjectChangeKind
		name    string
		summary string
	}{
		{ObjectAdded, "noop", "alerts DeployConvergeForcedReconcile"},
		{ObjectModified, "settings", "data.force_reconcile changed"},
		{ObjectModified, "workers", "image control-plane; env FORCE_RECONCILE removed"},
		{ObjectRemoved, "force", "schedule */5 * * * * · containers run"},
	}
	for i, w := range want {
		d := got.Deltas[i]
		if d.Change != w.change || d.Key.Name != w.name || d.Summary != w.summary {
			t.Errorf("delta %d = %s %s %q, want %s %s %q", i, d.Change, d.Key.Name, d.Summary, w.change, w.name, w.summary)
		}
	}

	// A bundle that predates summaries still diffs, and says what it lacks.
	bare := from
	bare.Objects = nil
	if old := CompareObjects(bare, to); !old.SummariesMissing || old.Deltas[2].Summary != "image control-plane; config changed" {
		t.Fatalf("no summaries on one side = %+v", old)
	}
}

func TestDescribeObjectChangeGroupsAndNamesContainers(t *testing.T) {
	from := ShapeObject{Hash: "x", ConfigHash: "y"}
	to := ShapeObject{Hash: "z", ConfigHash: "w", Tree: "app/workloads"}
	got := DescribeObjectChange(from, to, []PartChange{
		{Part: "api.env.A", Change: ObjectRemoved},
		{Part: "api.env.B", Change: ObjectRemoved},
		{Part: "sidecar.resources", Change: ObjectModified},
		{Part: "replicas", Change: ObjectModified},
	})
	if want := "moves flat → app/workloads; env A, B removed (api); resources changed (sidecar); replicas changed"; got != want {
		t.Fatalf("DescribeObjectChange =\n %q\nwant\n %q", got, want)
	}
}
