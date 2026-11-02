package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// A Workspace custom resource nests its image at spec.template.image. Nothing in
// the core workload kinds reports it, so a kind allowlist leaves these tags
// unprotected — which is exactly how four workspace-base tags came to survive on
// age alone.
var workspaceCR = `{
  "apiVersion": "workspaces.reliant.dev/v1",
  "kind": "Workspace",
  "metadata": {"name": "ws-1", "namespace": "workspaces"},
  "spec": {
    "template": {"image": "k3d-forge.localhost:5000/workspace-base:v3"},
    "sidecars": [{"runtime": {"ref": "k3d-forge.localhost:5000/proxy@sha256:` + strings.Repeat("c", 64) + `"}}],
    "notes": "built from k3d-forge.localhost:5000/workspace-base:prev"
  }
}`

func TestReferenceCandidatesFindsImagesNestedInCustomResources(t *testing.T) {
	var doc any
	if err := json.Unmarshal([]byte(workspaceCR), &doc); err != nil {
		t.Fatal(err)
	}
	got := protectedRefs(referenceCandidates(doc), []string{"k3d-forge.localhost:5000"})
	if !got["workspace-base"]["v3"] {
		t.Fatalf("spec.template.image was not protected: %v", got)
	}
	if !got["workspace-base"]["prev"] {
		t.Fatalf("reference embedded in free text was not protected: %v", got)
	}
	if !got["*"]["sha256:"+strings.Repeat("c", 64)] {
		t.Fatalf("digest-pinned sidecar was not protected: %v", got)
	}
	if !got["proxy"]["sha256:"+strings.Repeat("c", 64)] {
		t.Fatalf("digest ref did not protect its repository: %v", got)
	}
}

func TestReferenceCandidatesExtraction(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name   string
		doc    string
		want   []string
		absent []string
	}{
		{
			name: "bare digest and kubelet imageID scheme",
			doc:  `{"status":{"containerStatuses":[{"imageID":"docker-pullable://reg:5000/app@` + digest + `","image":"reg:5000/app:dev"}]},"x":"` + digest + `"}`,
			want: []string{digest, "reg:5000/app@" + digest, "reg:5000/app:dev"},
		},
		{
			name:   "reference inside a container arg",
			doc:    `{"spec":{"containers":[{"args":["--image=reg:5000/tool:1.2","--replicas=3"]}]}}`,
			want:   []string{"reg:5000/tool:1.2"},
			absent: []string{"--replicas", "3"},
		},
		{
			name: "reference inside an embedded JSON annotation",
			doc:  `{"metadata":{"annotations":{"kubectl.kubernetes.io/last-applied-configuration":"{\"image\":\"reg:5000/old:v1\"}"}}}`,
			want: []string{"reg:5000/old:v1"},
		},
		{
			name:   "plain prose and paths are not references",
			doc:    `{"note":"see docs/design.md for details","cmd":"/usr/bin/env"}`,
			absent: []string{"docs/design.md", "/usr/bin/env", "usr/bin/env"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var doc any
			if err := json.Unmarshal([]byte(tc.doc), &doc); err != nil {
				t.Fatal(err)
			}
			got := referenceCandidates(doc)
			for _, want := range tc.want {
				if !contains(got, want) {
					t.Fatalf("missing %q in %v", want, got)
				}
			}
			for _, absent := range tc.absent {
				if contains(got, absent) {
					t.Fatalf("collected non-reference %q from %v", absent, got)
				}
			}
		})
	}
}

func TestListableResourcesSkipsSecretsEventsAndMetrics(t *testing.T) {
	out := []byte(strings.Join([]string{
		"pods", "secrets", "events", "events.events.k8s.io",
		"workspaces.workspaces.reliant.dev", "nodes.metrics.k8s.io", "pods", "",
	}, "\n"))
	got := listableResources(out)
	want := []string{"pods", "workspaces.workspaces.reliant.dev"}
	if !equalStrings(got, want) {
		t.Fatalf("listable resources = %v, want %v", got, want)
	}
}

func TestResourceBatchesCoverEveryKindExactlyOnce(t *testing.T) {
	var kinds []string
	for i := 0; i < 57; i++ {
		kinds = append(kinds, fmt.Sprintf("kind%02d", i))
	}
	var flat []string
	batches := resourceBatches(kinds, 25)
	for _, b := range batches {
		if len(b) > 25 {
			t.Fatalf("batch of %d exceeds the limit", len(b))
		}
		flat = append(flat, b...)
	}
	if len(batches) != 3 || !equalStrings(flat, kinds) {
		t.Fatalf("batches=%d flat=%d", len(batches), len(flat))
	}
}

// Fail closed: a resource that cannot be listed means the protected set is
// incomplete, and an incomplete protected set must never authorize a delete.
func TestClusterScanFailsClosedOnAnyListError(t *testing.T) {
	apiResources := []byte("pods\nworkspaces.workspaces.reliant.dev\n")
	for _, failing := range []string{"api-resources", "get"} {
		t.Run(failing, func(t *testing.T) {
			r := Runner{Command: func(_ context.Context, name string, args ...string) ([]byte, error) {
				if contains(args, failing) {
					return nil, fmt.Errorf("the server could not find the requested resource")
				}
				if contains(args, "api-resources") {
					return apiResources, nil
				}
				return []byte(`{"items":[]}`), nil
			}}
			if _, err := r.protected(context.Background(), Registry{Contexts: []string{"k3d-dev"}, Aliases: []string{"reg:5000"}}); err == nil {
				t.Fatal("incomplete protected set accepted")
			}
		})
	}
}

func TestClusterScanBatchesResourceListsAndCollectsCRImages(t *testing.T) {
	var kinds []string
	for i := 0; i < 60; i++ {
		kinds = append(kinds, fmt.Sprintf("kind%02d", i))
	}
	kinds = append(kinds, "workspaces.workspaces.reliant.dev")
	var listed []string
	r := Runner{Command: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "docker" {
			return []byte(""), nil
		}
		if contains(args, "api-resources") {
			return []byte(strings.Join(append(kinds, "secrets"), "\n")), nil
		}
		for _, a := range args {
			if strings.Contains(a, ",") || contains(kinds, a) {
				listed = append(listed, a)
				if strings.Contains(a, "workspaces.workspaces.reliant.dev") {
					return []byte(`{"items":[` + workspaceCR + `]}`), nil
				}
				return []byte(`{"items":[]}`), nil
			}
		}
		return nil, fmt.Errorf("unexpected kubectl args %v", args)
	}}
	got, err := r.protected(context.Background(), Registry{Contexts: []string{"k3d-dev"}, Aliases: []string{"k3d-forge.localhost:5000"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 3 {
		t.Fatalf("want 3 batched list calls for 61 kinds, got %d: %v", len(listed), listed)
	}
	var selected []string
	for _, call := range listed {
		selected = append(selected, strings.Split(call, ",")...)
	}
	sort.Strings(selected)
	if contains(selected, "secrets") {
		t.Fatal("secrets were read")
	}
	if !contains(selected, "workspaces.workspaces.reliant.dev") {
		t.Fatalf("custom resource was not scanned: %v", selected)
	}
	if !got["workspace-base"]["v3"] {
		t.Fatalf("Workspace CR image not protected: %v", got)
	}
}
