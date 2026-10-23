package kclrender

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

func TestSnapshotContextUsesQuotedJSONBinding(t *testing.T) {
	d := release.SpecDigest([]byte("image"))
	a := release.SnapshotArtifact{Kind: release.KindOCI, Location: "registry.test/reliant", Content: d, Platforms: []string{"linux/amd64"}, Origin: release.ArtifactOrigin{Release: "v1", External: &release.ExternalArtifact{Verifier: "registry/v1", DescriptorDigest: d}}}
	id, err := release.ArtifactIdentity(a)
	if err != nil {
		t.Fatal(err)
	}
	key := release.ConsumerKey{Cluster: "cluster", Workload: "api"}
	s := release.Snapshot{Schema: release.SnapshotSchema, Project: "app", Environment: "prod", Source: release.Source{Repo: "github.com/acme/app", Commit: strings.Repeat("a", 40)}, PolicyDigest: release.SpecDigest([]byte("policy")), Selected: []release.ConsumerKey{key}, Closure: []release.ConsumerKey{key}, Artifacts: map[release.ArtifactID]release.SnapshotArtifact{id: a}, Consumers: []release.Consumer{{Key: key, Slots: []string{"container:api"}}}, Bindings: []release.ConsumerBinding{{Consumer: key, Slot: "container:api", Artifact: id}}, Decisions: []release.CompositionDecision{{Consumer: key, Action: "build", Reason: "bootstrap"}}}
	ctx, err := WithSnapshot(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	want, err := SnapshotDArgs(s, "cluster")
	if err != nil {
		t.Fatal(err)
	}
	got := ReleaseDArgs(ctx)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("context differs from explicit binding: got %q want %q", got, want)
	}
	var encoded string
	if err := json.Unmarshal([]byte(strings.TrimPrefix(got[0], "forge_workload_image_digests=")), &encoded); err != nil {
		t.Fatalf("KCL option must be JSON STRING, not object: %v", err)
	}
	if !strings.Contains(encoded, "container:api") {
		t.Fatal("named slot missing")
	}
}

// Repo/native-KCL inputs are invisible to Go's cache; gate this actual render.
func TestSnapshotWorkloadSlotRender(t *testing.T) {
	if testing.Short() {
		t.Skip("native KCL fixture render; runs in task test")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	pins := map[string]map[string]string{"api": {"container:api": "registry.test/reliant@sha256:" + strings.Repeat("a", 64)}, "worker": {"container:worker": "registry.test/reliant@sha256:" + strings.Repeat("b", 64)}}
	encoded, _ := json.Marshal(pins)
	quoted, _ := json.Marshal(string(encoded))
	source := filepath.Join(root, "kcl", "tests", "fixture_snapshot_bindings.k")
	// Record the test fixture read as an additional cache-sound guard if this
	// test is ever promoted into the short tier.
	if _, err := os.ReadFile(source); err != nil {
		t.Fatal(err)
	}
	args := []string{"forge_workload_image_digests=" + string(quoted), "forge_immutable_release=true"}
	out, err := Run(root, source, args)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"assert_api_old", "assert_worker_new", "assert_recipe", "assert_cohort"} {
		if result[key] != true {
			t.Fatalf("%s failed: %s", key, out)
		}
	}
	delete(pins, "worker")
	encoded, _ = json.Marshal(pins)
	quoted, _ = json.Marshal(string(encoded))
	args[0] = "forge_workload_image_digests=" + string(quoted)
	if _, err := Run(root, source, args); err == nil {
		t.Fatal("immutable release used tag/repository fallback for missing worker slot")
	}
}
