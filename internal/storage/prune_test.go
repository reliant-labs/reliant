package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// clusterWorld fakes the two sources Prune consults about a registered
// cluster: kubeconfig (kubectl config get-contexts -o name) and docker (any
// container, running or stopped, carrying the cluster's k3d label).
type clusterWorld struct {
	contexts   []string            // contexts present in kubeconfig
	containers map[string][]string // k3d cluster name -> container ids
	kubectlErr error               // kubeconfig unreadable
	dockerErr  error               // docker unreachable
}

func (w clusterWorld) command(_ context.Context, name string, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	switch {
	case name == "docker" && joined == "context inspect":
		return []byte(`[{"Endpoints":{"docker":{"Host":"unix:///var/run/docker.sock"}}}]`), nil
	case name == "kubectl" && joined == "config get-contexts -o name":
		if w.kubectlErr != nil {
			return nil, w.kubectlErr
		}
		return []byte(strings.Join(w.contexts, "\n") + "\n"), nil
	case name == "docker" && strings.HasPrefix(joined, "ps -aq --filter label=k3d.cluster="):
		if w.dockerErr != nil {
			return nil, w.dockerErr
		}
		cluster := strings.TrimPrefix(joined, "ps -aq --filter label=k3d.cluster=")
		return []byte(strings.Join(w.containers[cluster], "\n")), nil
	}
	return nil, fmt.Errorf("unexpected %s %s", name, joined)
}

// prunePolicy registers clusters and one registry that every one of them
// protects, then saves it at a test-owned path.
func prunePolicy(t *testing.T, clusters []string, projects ...string) (string, Policy) {
	t.Helper()
	p := DefaultPolicy()
	p.Clusters = clusters
	p.Projects = projects
	p.Registries = []Registry{{
		Container: "k3d-reg", Repositories: []string{"app"},
		Aliases: []string{"localhost:5051"}, Contexts: append([]string(nil), clusters...),
	}}
	path := filepath.Join(t.TempDir(), "storage.json")
	if err := Save(path, p); err != nil {
		t.Fatal(err)
	}
	return path, p
}

// TestPruneDropsOnlyClustersThatAreGoneEverywhere is the stale-context half of
// B3. The machine's policy still listed k3d-cp-daemon and k3d-control-plane
// after both were recreated as -v2, Converge copies every registered context
// into every registry, and the protected-set scan of a context that no longer
// exists fails — so registry GC refused on every pass, forever.
//
// A cluster is gone only when BOTH kubeconfig and docker say so. Anything
// else stays registered: an unreachable API server or stopped node containers
// keep registry deletion failing closed, and verifyConsumers independently
// refuses if an unregistered k3d server shares the registry's network.
func TestPruneDropsOnlyClustersThatAreGoneEverywhere(t *testing.T) {
	path, _ := prunePolicy(t, []string{"k3d-gone", "k3d-live", "k3d-stopped", "k3d-unreachable"})
	world := clusterWorld{
		// k3d-unreachable's context exists (its API server is down);
		// k3d-stopped's context was deleted but its node containers remain.
		contexts:   []string{"k3d-live", "k3d-unreachable", "k3d-v2"},
		containers: map[string][]string{"live": {"a"}, "stopped": {"b"}},
	}
	var out strings.Builder
	if err := (Runner{Out: &out, Command: world.command}).Prune(context.Background(), path); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "k3d-live,k3d-stopped,k3d-unreachable"
	if got := strings.Join(p.Clusters, ","); got != want {
		t.Errorf("clusters = %s, want %s", got, want)
	}
	if got := strings.Join(p.Registries[0].Contexts, ","); got != want {
		t.Errorf("registry contexts = %s, want %s (a pruned cluster must leave every registry too)", got, want)
	}
	if !strings.Contains(out.String(), "k3d-gone") {
		t.Errorf("the prune must say what it removed:\n%s", out.String())
	}
}

// TestPruneKeepsEverythingWhenItCannotLook: no answer is not a "gone" answer.
func TestPruneKeepsEverythingWhenItCannotLook(t *testing.T) {
	for _, tc := range []struct {
		name  string
		world clusterWorld
	}{
		{"kubeconfig unreadable", clusterWorld{kubectlErr: fmt.Errorf("no kubectl")}},
		{"docker unreachable", clusterWorld{contexts: []string{"k3d-live"}, dockerErr: fmt.Errorf("daemon down")}},
		{"kubeconfig lists nothing", clusterWorld{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, before := prunePolicy(t, []string{"k3d-gone"})
			if err := (Runner{Command: tc.world.command}).Prune(context.Background(), path); err != nil {
				t.Fatalf("Prune must not fail the pass over a fact it could not read: %v", err)
			}
			p, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(p.Clusters, ",") != strings.Join(before.Clusters, ",") {
				t.Fatalf("pruned on missing evidence: %v", p.Clusters)
			}
		})
	}
}

// TestPruneRemovesARegistryWhoseLastProtectorIsGone: a registry entry with no
// protecting context fails Validate, which would make the whole policy
// unloadable. Its protectors are gone, so it is no longer a cleanup target.
func TestPruneRemovesARegistryWhoseLastProtectorIsGone(t *testing.T) {
	path, _ := prunePolicy(t, []string{"k3d-gone"})
	world := clusterWorld{contexts: []string{"k3d-someone-elses"}}
	if err := (Runner{Command: world.command}).Prune(context.Background(), path); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatalf("the pruned policy no longer loads: %v", err)
	}
	if len(p.Clusters) != 0 || len(p.Registries) != 0 {
		t.Fatalf("clusters=%v registries=%v; want both empty", p.Clusters, p.Registries)
	}
}

// TestPruneDropsProjectsThatNoLongerExist: Policy.Projects only accumulated,
// so every deleted checkout and every test temp project stayed registered.
// Only a project whose directory is definitely absent is dropped.
func TestPruneDropsProjectsThatNoLongerExist(t *testing.T) {
	live := t.TempDir()
	gone := filepath.Join(t.TempDir(), "deleted-checkout")
	path, _ := prunePolicy(t, []string{"k3d-live"}, live, gone)
	world := clusterWorld{contexts: []string{"k3d-live"}}
	if err := (Runner{Command: world.command}).Prune(context.Background(), path); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.Projects, ",") != live {
		t.Fatalf("projects = %v, want only %s", p.Projects, live)
	}
}

// TestConvergeRefusesProjectsUnderTheTempDir: a project under $TMPDIR is a
// test fixture or a scratch scaffold — measured: 65 leaked TestBuildTag_*/001
// projects in one machine's policy — and Logs expires files under every
// registered project.
func TestConvergeRefusesProjectsUnderTheTempDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage.json")
	scratch := t.TempDir() // under os.TempDir()
	if err := Converge(path, Facts{Project: scratch, Contexts: []string{"k3d-live"}}); err != nil {
		t.Fatalf("Converge: %v", err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Projects) != 0 {
		t.Fatalf("a temp-dir project was registered: %v", p.Projects)
	}
	if len(p.Clusters) != 1 {
		t.Fatalf("the rest of the facts must still converge: %+v", p)
	}
}

// TestGCPrunesBeforeTheRegistryLayer: the point of pruning is that the
// registry layer stops failing on a dead context, so GC must prune first.
func TestGCPrunesBeforeTheRegistryLayer(t *testing.T) {
	path, p := prunePolicy(t, []string{"k3d-gone"})
	gone := map[string]bool{}
	r := Runner{
		Policy: p, PolicyPath: path, TempRoot: t.TempDir(), SourceCacheRoot: t.TempDir(),
		Command: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			joined := strings.Join(args, " ")
			if name == "kubectl" && strings.Contains(joined, "--context k3d-gone") {
				gone["scanned"] = true
				return nil, fmt.Errorf("context k3d-gone does not exist")
			}
			if joined == "context inspect" {
				return []byte(`[{"Endpoints":{"docker":{"Host":"unix:///var/run/docker.sock"}}}]`), nil
			}
			if strings.HasPrefix(joined, "buildx inspect") {
				return []byte(buildxInspectText("default", "docker-container", "unix:///var/run/docker.sock")), nil
			}
			if strings.HasPrefix(joined, "ps --format") || joined == "ps -aq" || strings.HasPrefix(joined, "image ls") || strings.HasPrefix(joined, "volume ls") {
				return nil, nil
			}
			return clusterWorld{contexts: []string{"k3d-other"}}.command(ctx, name, args...)
		},
	}
	r.Policy.Builders = nil
	if err := r.GC(context.Background(), false); err != nil {
		t.Fatalf("GC failed on a cluster that no longer exists: %v", err)
	}
	if gone["scanned"] {
		t.Fatal("GC scanned a pruned context")
	}
}

// TestPruneDropsRegistriesWhoseContainerIsGone: an e2e cluster's registry dies
// with the cluster but stayed in the policy, and `docker inspect` of each one
// failed every pass — 24 failures on the machine this was found on. Only a
// clean, empty `docker ps -a` answer drops one; an unreadable answer keeps it.
func TestPruneDropsRegistriesWhoseContainerIsGone(t *testing.T) {
	p := DefaultPolicy()
	p.Clusters = []string{"k3d-live"}
	p.Registries = []Registry{
		{Container: "k3d-live-registry", Repositories: []string{"app"}, Aliases: []string{"localhost:1"}, Contexts: []string{"k3d-live"}},
		{Container: "k3d-stopped-registry", Repositories: []string{"app"}, Aliases: []string{"localhost:2"}, Contexts: []string{"k3d-live"}},
		{Container: "k3d-dead-registry", Repositories: []string{"app"}, Aliases: []string{"localhost:3"}, Contexts: []string{"k3d-live"}},
	}
	listing := func(dockerErr error) func(context.Context, string, ...string) ([]byte, error) {
		return func(ctx context.Context, name string, args ...string) ([]byte, error) {
			joined := strings.Join(args, " ")
			if name == "docker" && strings.HasPrefix(joined, "ps -aq --filter name=^/") {
				if dockerErr != nil {
					return nil, dockerErr
				}
				if strings.Contains(joined, "dead") {
					return nil, nil
				}
				return []byte("abc123\n"), nil
			}
			return clusterWorld{contexts: []string{"k3d-live"}}.command(ctx, name, args...)
		}
	}
	got, changed := (Runner{Policy: p, Command: listing(nil)}).pruned(context.Background())
	if !changed || len(got.Registries) != 2 || got.Registries[0].Container != "k3d-live-registry" || got.Registries[1].Container != "k3d-stopped-registry" {
		t.Fatalf("registries = %+v", got.Registries)
	}
	got, _ = (Runner{Policy: p, Command: listing(fmt.Errorf("daemon down"))}).pruned(context.Background())
	if len(got.Registries) != 3 {
		t.Fatalf("dropped a registry on an unreadable docker answer: %+v", got.Registries)
	}
}

// TestRegistryGCSkipsARegistryWhoseContainerIsGone: a registered registry that
// vanished between the prune and the pass must not fail the pass.
func TestRegistryGCSkipsARegistryWhoseContainerIsGone(t *testing.T) {
	r := Runner{Policy: DefaultPolicy(), Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		return nil, fmt.Errorf("docker %v: exit status 1: Error: No such object: k3d-x-registry", args)
	}}
	if err := r.RegistryGC(context.Background(), Registry{Container: "k3d-x-registry", Repositories: []string{"app"}, Contexts: []string{"k3d-x"}}, true); err != nil {
		t.Fatalf("a vanished registry failed the pass: %v", err)
	}
}
