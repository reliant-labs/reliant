package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/release"
)

func TestSnapshotStatusIndependentConsumerSameRepository(t *testing.T) {
	old := "sha256:" + strings.Repeat("a", 64)
	next := "sha256:" + strings.Repeat("b", 64)
	s := release.Snapshot{Artifacts: map[release.ArtifactID]release.SnapshotArtifact{"old": {Location: "r.example/image", Content: old}, "new": {Location: "r.example/image", Content: next}}, Bindings: []release.ConsumerBinding{{Consumer: release.ConsumerKey{Cluster: "prod", Workload: "api"}, Slot: "container:api", Artifact: "old"}, {Consumer: release.ConsumerKey{Cluster: "prod", Workload: "worker"}, Slot: "container:worker", Artifact: "new"}}}
	running := []cluster.WorkloadImage{{Name: "api", Container: "api", Image: "r.example/image@" + old}, {Name: "worker", Container: "worker", Image: "r.example/image@" + next}}
	results := verifySnapshotBindings(s, "prod", running, nil)
	if len(results) != 2 || results[0].State != imageMatch || results[1].State != imageMatch {
		t.Fatalf("mixed repository observation collapsed: %+v", results)
	}
	running[1].Image = "r.example/image@" + old
	results = verifySnapshotBindings(s, "prod", running, nil)
	if results[0].State != imageMatch || results[1].State != imageDrift {
		t.Fatalf("worker drift hidden: %+v", results)
	}
	s.Bindings = append(s.Bindings, release.ConsumerBinding{Consumer: release.ConsumerKey{Cluster: "prod", Workload: "api"}, Slot: "init:api", Artifact: "old"})
	results = verifySnapshotBindings(s, "prod", running, nil)
	for _, r := range results {
		if r.Image == "api/init:api" && r.State != imageMissing {
			t.Fatal("main container satisfied init slot")
		}
	}
}
