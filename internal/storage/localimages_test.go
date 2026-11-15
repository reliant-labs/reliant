package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type fakeContainer struct {
	name     string
	running  bool
	hostPort string // published host port ("" when none)
	imageID  string
	imageRef string
}

type fakeImageHost struct {
	containers []fakeContainer
	images     string
	removed    []string
	rmiFails   map[string]bool
}

func (f *fakeImageHost) command(_ context.Context, name string, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	switch {
	case name == "docker" && joined == "context inspect":
		return []byte(`[{"Endpoints":{"docker":{"Host":"unix:///var/run/docker.sock"}}}]`), nil
	case strings.HasPrefix(joined, "ps --format"):
		// Running containers only, and only they report published ports.
		var out strings.Builder
		for _, c := range f.containers {
			if !c.running {
				continue
			}
			ports := ""
			if c.hostPort != "" {
				ports = "0.0.0.0:" + c.hostPort + "->5000/tcp"
			}
			fmt.Fprintf(&out, `{"Names":%q,"Ports":%q}`+"\n", c.name, ports)
		}
		return []byte(out.String()), nil
	case joined == "ps -aq":
		var ids []string
		for i := range f.containers {
			ids = append(ids, fmt.Sprintf("c%d", i))
		}
		return []byte(strings.Join(ids, "\n")), nil
	case strings.HasPrefix(joined, "inspect --format"):
		var out []string
		for _, c := range f.containers {
			out = append(out, c.imageID+" "+c.imageRef)
		}
		return []byte(strings.Join(out, "\n")), nil
	case strings.HasPrefix(joined, "inspect "):
		var rows []map[string]any
		for _, c := range f.containers {
			bindings := map[string]any{}
			if c.hostPort != "" {
				bindings["5000/tcp"] = []map[string]string{{"HostPort": c.hostPort}}
			}
			rows = append(rows, map[string]any{
				"Name": "/" + c.name, "Image": c.imageID,
				"Config":     map[string]any{"Image": c.imageRef},
				"HostConfig": map[string]any{"PortBindings": bindings},
			})
		}
		b, _ := json.Marshal(rows)
		return b, nil
	case strings.HasPrefix(joined, "image ls"):
		return []byte(f.images), nil
	case strings.HasPrefix(joined, "rmi "):
		ref := strings.TrimPrefix(joined, "rmi ")
		if f.rmiFails[ref] {
			return nil, fmt.Errorf("image is being used")
		}
		f.removed = append(f.removed, ref)
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected %s %s", name, joined)
}

func imageRow(repo, tag, id string) string {
	return fmt.Sprintf(`{"Repository":%q,"Tag":%q,"ID":%q}`+"\n", repo, tag, id)
}

func newFakeImageHost() *fakeImageHost {
	return &fakeImageHost{
		containers: []fakeContainer{
			{name: "k3d-live-registry", running: true, hostPort: "5051", imageID: "sha256:reg", imageRef: "registry:2"},
			{name: "web", running: true, hostPort: "8080", imageID: "sha256:web", imageRef: "web:1"},
			{name: "user", running: false, imageID: "sha256:inuse", imageRef: "localhost:5061/inuse:1"},
		},
		images: imageRow("localhost:5061/workspace-base", "t1", "sha256:a") + // dead port
			imageRow("registry.localhost:5061/workspace-base", "t1", "sha256:a") + // same image, second tag
			imageRow("localhost:5051/workspace-base", "t2", "sha256:b") + // live port
			imageRow("k3d-gone-registry:5000/app", "x", "sha256:c") + // no such container
			imageRow("k3d-live-registry:5000/app", "x", "sha256:d") + // live container
			imageRow("localhost:5061/inuse", "1", "sha256:inuse") + // a container uses it
			imageRow("localhost:5061/untagged", "<none>", "sha256:e") +
			imageRow("ghcr.io/org/app", "v1", "sha256:f") + // not a local registry
			imageRow("localhost/app", "dev", "sha256:g") + // no port
			imageRow("localhost:8080/app", "dev", "sha256:h"), // live port of a non-registry container: kept
	}
}

func TestStaleLocalImagesSelectsOnlyDeadRegistryTagsNoContainerUses(t *testing.T) {
	f := newFakeImageHost()
	got, err := (Runner{Command: f.command}).StaleLocalImages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var refs []string
	for _, img := range got {
		refs = append(refs, img.Ref)
	}
	want := "localhost:5061/workspace-base:t1,registry.localhost:5061/workspace-base:t1,k3d-gone-registry:5000/app:x"
	if strings.Join(refs, ",") != want {
		t.Fatalf("stale = %v\nwant %s", refs, want)
	}
}

func TestLocalImagesDryRunRemovesNothingAndApplyUntags(t *testing.T) {
	f := newFakeImageHost()
	var out strings.Builder
	r := Runner{Command: f.command, Out: &out}
	if err := r.LocalImages(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if len(f.removed) != 0 || !strings.Contains(out.String(), "untag stale local-registry image localhost:5061/workspace-base:t1") {
		t.Fatalf("dry run removed=%v out=%s", f.removed, out.String())
	}
	f.rmiFails = map[string]bool{"k3d-gone-registry:5000/app:x": true}
	err := r.LocalImages(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), "k3d-gone-registry") {
		t.Fatalf("a refused rmi must be reported: %v", err)
	}
	if len(f.removed) != 2 {
		t.Fatalf("one refusal must not stop the rest: %v", f.removed)
	}
}

func TestStaleLocalImagesKeepsAliasesOfARunningRegisteredRegistry(t *testing.T) {
	f := newFakeImageHost()
	f.images = imageRow("registry.localhost:5000/app", "x", "sha256:z")
	r := Runner{Command: f.command, Policy: Policy{Registries: []Registry{{Container: "k3d-live-registry", Aliases: []string{"registry.localhost:5000"}}}}}
	got, err := r.StaleLocalImages(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("stale = %v, %v", got, err)
	}
}

// TestStaleLocalImagesKeepsImagesOfAStoppedRegistry: the full GC pass stops the
// registry for garbage-collect, and k3d registries can sit stopped after a
// Docker Desktop restart. A stopped container publishes no ports in `docker ps`
// output, so liveness must come from the container's own port bindings, and an
// alias is dead only when NO container, running or stopped, backs it.
func TestStaleLocalImagesKeepsImagesOfAStoppedRegistry(t *testing.T) {
	f := newFakeImageHost()
	f.containers = []fakeContainer{
		{name: "k3d-stopped-registry", running: false, hostPort: "5051", imageID: "sha256:reg", imageRef: "registry:2"},
		{name: "k3d-registered-registry", running: false, imageID: "sha256:reg", imageRef: "registry:2"},
	}
	f.images = imageRow("localhost:5051/workspace-base", "t", "sha256:a") +
		imageRow("registry.localhost:5051/workspace-base", "t", "sha256:a") +
		imageRow("k3d-stopped-registry:5000/app", "x", "sha256:b") +
		imageRow("registry.localhost:5099/app", "x", "sha256:c") + // registered, stopped, no known port
		imageRow("localhost:5077/gone", "x", "sha256:d") // nothing backs it
	r := Runner{Command: f.command, Policy: Policy{Registries: []Registry{{
		Container: "k3d-registered-registry", Aliases: []string{"registry.localhost:5099"},
	}}}}
	got, err := r.StaleLocalImages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Ref != "localhost:5077/gone:x" {
		t.Fatalf("stale = %+v; only the image nothing backs may be untagged", got)
	}
}
