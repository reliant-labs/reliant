package cluster

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/stsresize"
)

func TestStatefulSetResizeExplicitReleaseOwner(t *testing.T) {
	raw := []any{map[string]any{"name": "resize-hooks", "cluster": "prod", "release_inputs": map[string]any{"container:resize": stsresize.DefaultImage}}}
	opts := resizeJobOpts{ReleaseOwner: "resize-hooks", ReleaseConsumers: raw}
	got, err := expandStatefulSetResizeJobs([]any{resizeSTS("db", "data", "prod", "", "1Gi")}, opts)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, item := range got {
		doc := item.(map[string]any)
		if doc["kind"] == "StatefulSet" {
			continue
		}
		md := doc["metadata"].(map[string]any)
		ann, _ := md["annotations"].(map[string]any)
		if ann["forge.dev/release-owners"] != `["resize-hooks"]` {
			t.Fatalf("generated %s lacks exact owner: %+v", doc["kind"], ann)
		}
		count++
	}
	if count != 5 {
		t.Fatalf("expected five hook resources, got %d", count)
	}
	for _, tc := range []struct {
		name, cluster, image string
		raw                  any
	}{
		{"unknown owner", "prod", "", []any{}},
		{"wrong cluster", "other", "", raw},
		{"missing input", "prod", "", []any{map[string]any{"name": "resize-hooks", "cluster": "prod"}}},
		{"different image", "prod", "registry.example/resize:1", raw},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateResizeReleaseOwner("resize-hooks", tc.cluster, tc.image, tc.raw); err == nil {
				t.Fatal("invalid hook ownership contract accepted")
			}
		})
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	if !sameResizeImage("docker.io/acme/tool:1@"+digest, "docker.io/acme/tool@"+digest) {
		t.Fatal("equivalent immutable image spelling refused")
	}
	if sameResizeImage("docker.io/acme/tool@"+digest, "docker.io/foreign/tool@"+digest) {
		t.Fatal("same digest does not authorize a different repository")
	}
	_, _, owner, err := decodeStatefulSetResize(map[string]any{"release_owner": "resize-hooks"})
	if err != nil || owner != "resize-hooks" {
		t.Fatalf("JSON schema projection lost owner: %q %v", owner, err)
	}
}
