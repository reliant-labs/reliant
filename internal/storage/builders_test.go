package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuilderRegistrationRejectsUnknownOrRemoteNodes(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	for _, tc := range []struct {
		name, info           string
		remoteContext, valid bool
	}{
		{name: "local socket", info: buildxInspectText("relbuild", "docker-container", "unix:///var/run/docker.sock"), valid: true},
		{name: "local named context", info: buildxInspectText("relbuild", "docker", "desktop-linux"), valid: true},
		{name: "no nodes", info: buildxInspectText("relbuild", "docker-container")},
		{name: "empty endpoint", info: buildxInspectText("relbuild", "docker-container", "")},
		{name: "remote endpoint", info: buildxInspectText("relbuild", "docker-container", "ssh://prod")},
		{name: "remote named context", info: buildxInspectText("relbuild", "docker-container", "prod"), remoteContext: true},
		{name: "remote driver", info: buildxInspectText("relbuild", "remote", "unix:///var/run/buildkit.sock")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "storage.json")
			r := Runner{Command: func(_ context.Context, name string, args ...string) ([]byte, error) {
				joined := strings.Join(args, " ")
				switch {
				case joined == "context show":
					return []byte("desktop-linux\n"), nil
				case strings.Contains(joined, "context inspect"):
					if tc.remoteContext && strings.Contains(joined, "--context prod ") {
						return []byte(`[{"Endpoints":{"docker":{"Host":"ssh://prod"}}}]`), nil
					}
					return []byte(`[{"Endpoints":{"docker":{"Host":"unix:///var/run/docker.sock"}}}]`), nil
				case strings.HasSuffix(joined, "buildx inspect relbuild"):
					return []byte(tc.info), nil
				default:
					t.Fatalf("unexpected command %s %s", name, joined)
					return nil, nil
				}
			}}
			err := r.RegisterBuilder(context.Background(), path, "relbuild")
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				if err := r.RegisterBuilder(context.Background(), path, "relbuild"); err != nil {
					t.Fatal(err)
				}
				p, err := Load(path)
				if err != nil {
					t.Fatal(err)
				}
				if p.DockerContext != "desktop-linux" || strings.Join(p.Builders, ",") != "default,relbuild" {
					t.Fatalf("policy: %+v", p)
				}
			} else {
				if err == nil {
					t.Fatal("unsafe builder registered")
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("failed registration wrote policy: %v", err)
				}
			}
		})
	}
}

func TestGCContinuesHealthyBuilderAfterIndependentFailure(t *testing.T) {
	p := DefaultPolicy()
	p.Builders = []string{"gone", "healthy"}
	var pruned []string
	r := Runner{Policy: p, Command: func(_ context.Context, name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case joined == "context inspect":
			return []byte(`[{"Endpoints":{"docker":{"Host":"unix:///var/run/docker.sock"}}}]`), nil
		case strings.HasPrefix(joined, "ps ") || strings.HasPrefix(joined, "image ls") || strings.HasPrefix(joined, "volume ls"):
			return nil, nil
		case joined == "buildx inspect gone":
			return nil, fmt.Errorf("builder no longer exists")
		case joined == "buildx inspect healthy":
			return []byte(buildxInspectText("healthy", "docker-container", "unix:///var/run/docker.sock")), nil
		case strings.HasPrefix(joined, "buildx prune --builder healthy "):
			pruned = append(pruned, "healthy")
			return nil, nil
		default:
			t.Fatalf("unexpected command: %s %s", name, joined)
			return nil, nil
		}
	}}
	err := r.GC(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), "gone") {
		t.Fatalf("missing target failure: %v", err)
	}
	if strings.Join(pruned, ",") != "healthy" {
		t.Fatalf("pruned %v", pruned)
	}
}

// buildxInspectText renders `docker buildx inspect <name>` the way buildx
// actually prints it (v0.37: there is no --format flag; this text is the only
// output). Captured from a Docker Desktop Mac, nested Labels/Devices sections
// included: a docker-driver node's Devices block carries its own indented
// `Name:` line, which once read as a phantom second node with no endpoint.
func buildxInspectText(name, driver string, endpoints ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Name:          %s\nDriver:        %s\nLast Activity: 2026-10-02 05:08:33 +0000 UTC\n\nNodes:\n", name, driver)
	for i, endpoint := range endpoints {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "Name:             %s%d\nEndpoint:         %s\nStatus:           running\nBuildKit version: v0.33.0\n", name, i, endpoint)
		b.WriteString("Platforms:        linux/arm64, linux/amd64\nLabels:\n org.mobyproject.buildkit.worker.moby.host-gateway-ip: 192.168.65.254\n")
		b.WriteString("Devices:\n Name:                  docker.com/gpu=webgpu\n Automatically allowed: false\n")
		b.WriteString("GC Policy rule#0:\n All:            false\n Filters:        type==source.local\n Keep Duration:  48h0m0s\n")
	}
	return b.String()
}

// TestParseBuildxInspectMatchesRealOutput pins the parser to buildx's real
// text, nested sections and all. The node count is load-bearing twice over:
// localBuilder rejects any node without an endpoint, and builderRunner only
// pins a docker-driver builder to its context when it has exactly one node.
func TestParseBuildxInspectMatchesRealOutput(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       builderInfo
	}{
		{"docker driver", buildxInspectText("default", "docker", "default"), builderInfo{"docker", []string{"default"}}},
		{"docker-container, two nodes", buildxInspectText("relbuild", "docker-container", "desktop-linux", "unix:///var/run/docker.sock"),
			builderInfo{"docker-container", []string{"desktop-linux", "unix:///var/run/docker.sock"}}},
		{"node without an endpoint", "Name: x\nDriver: docker-container\n\nNodes:\nName: x0\nStatus: inactive\n", builderInfo{"docker-container", []string{""}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseBuildxInspect([]byte(tc.text))
			if err != nil {
				t.Fatal(err)
			}
			if got.Driver != tc.want.Driver || strings.Join(got.Endpoints, "|") != strings.Join(tc.want.Endpoints, "|") {
				t.Fatalf("parseBuildxInspect = %+v, want %+v", got, tc.want)
			}
		})
	}
	if _, err := parseBuildxInspect([]byte("ERROR: something else\n")); err == nil {
		t.Fatal("output with no Driver line was accepted")
	}
}

// realBuildx emulates the two buildx behaviours storage maintenance has to get
// right, both observed on a Docker Desktop Mac with contexts `default` and
// `desktop-linux` (current):
//
//   - `buildx inspect` has no --format flag and fails with "unknown flag";
//   - a `docker`-driver builder is the built-in builder OF one context, and
//     buildx refuses `du`/`prune --builder <it>` from any other context.
type realBuildx struct {
	current  string            // the current Docker context
	builders map[string]string // builder name -> inspect text
	drivers  map[string]string // builder name -> driver
	ran      []string
}

func (f *realBuildx) command(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != "docker" {
		return nil, fmt.Errorf("unexpected command %s %v", name, args)
	}
	active := f.current
	if len(args) >= 2 && args[0] == "--context" {
		active, args = args[1], args[2:]
	}
	joined := strings.Join(args, " ")
	f.ran = append(f.ran, "["+active+"] "+joined)
	switch {
	case joined == "context inspect":
		return []byte(`[{"Endpoints":{"docker":{"Host":"unix:///var/run/docker.sock"}}}]`), nil
	case joined == "context show":
		return []byte(f.current + "\n"), nil
	case strings.HasPrefix(joined, "ps ") || strings.HasPrefix(joined, "image ls") || strings.HasPrefix(joined, "volume ls"):
		return nil, nil
	case joined == "system df":
		return []byte("TYPE TOTAL\n"), nil
	case strings.HasPrefix(joined, "buildx inspect "):
		if strings.Contains(joined, "--format") {
			return nil, fmt.Errorf("exit status 125: unknown flag: --format")
		}
		text, ok := f.builders[strings.TrimPrefix(joined, "buildx inspect ")]
		if !ok {
			return nil, fmt.Errorf("no builder %q found", joined)
		}
		return []byte(text), nil
	case strings.HasPrefix(joined, "buildx du --builder "), strings.HasPrefix(joined, "buildx prune --builder "):
		builder := strings.Fields(joined)[3]
		if f.drivers[builder] == "docker" && builder != active {
			return nil, fmt.Errorf("exit status 1: ERROR: use `docker --context=%s buildx` to switch to context %q", builder, builder)
		}
		return []byte("Total:\t\t5.05GB\n"), nil
	}
	return nil, fmt.Errorf("unexpected docker %v", args)
}

func dockerDesktopBuildx() *realBuildx {
	return &realBuildx{
		current: "desktop-linux",
		builders: map[string]string{
			"default":       buildxInspectText("default", "docker", "default"),
			"desktop-linux": buildxInspectText("desktop-linux", "docker", "desktop-linux"),
			"relbuild":      buildxInspectText("relbuild", "docker-container", "desktop-linux"),
		},
		drivers: map[string]string{"default": "docker", "desktop-linux": "docker", "relbuild": "docker-container"},
	}
}

// TestGCPrunesEveryBuilderAgainstRealBuildx pins builder maintenance to the
// buildx CLI that actually ships, not to a convenient fake. Before this,
// localBuilder asked for `buildx inspect --format '{{json .}}'` (no such flag),
// and pruned the policy's default builder — `default`, a docker-driver builder
// of a context that is not current on Docker Desktop — from the current
// context, which buildx refuses. Either failure alone meant `forge storage gc`
// never pruned BuildKit on that machine, the layer the absolute 20 GiB budget
// exists for.
func TestGCPrunesEveryBuilderAgainstRealBuildx(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	fake := dockerDesktopBuildx()
	p := DefaultPolicy()
	p.Builders = []string{"default", "relbuild"}
	var out strings.Builder
	r := Runner{Policy: p, Out: &out, Command: fake.command}
	if err := r.GC(context.Background(), true); err != nil {
		t.Fatalf("GC against real buildx behaviour: %v\ncommands:\n%s", err, strings.Join(fake.ran, "\n"))
	}
	var pruned []string
	for _, c := range fake.ran {
		if strings.Contains(c, "buildx prune --builder ") {
			pruned = append(pruned, c)
		}
	}
	want := []string{"[default] buildx prune --builder default", "[desktop-linux] buildx prune --builder relbuild"}
	if len(pruned) != len(want) {
		t.Fatalf("pruned %v, want both builders", pruned)
	}
	for i := range want {
		if !strings.HasPrefix(pruned[i], want[i]) {
			t.Errorf("prune %d ran as %q, want %q… (a docker-driver builder runs in its own context)", i, pruned[i], want[i])
		}
	}
}

// TestStatusReportsEveryBuilderAgainstRealBuildx: `forge storage status` died
// at the builder step on a Docker Desktop Mac with buildx's own refusal, so the
// node GC configuration below it was never reported either.
func TestStatusReportsEveryBuilderAgainstRealBuildx(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	fake := dockerDesktopBuildx()
	p := DefaultPolicy()
	p.Builders = []string{"default", "relbuild"}
	var out strings.Builder
	r := Runner{Policy: p, Out: &out, Command: fake.command}
	if err := r.Status(context.Background()); err != nil {
		t.Fatalf("Status against real buildx behaviour: %v\ncommands:\n%s", err, strings.Join(fake.ran, "\n"))
	}
	for _, builder := range p.Builders {
		if !strings.Contains(out.String(), "builder "+builder+" (budget") {
			t.Errorf("status did not report builder %s:\n%s", builder, out.String())
		}
	}
}

// TestStatusReportsAStaleClusterAndContinues: status is a read-only report,
// and Policy.Clusters only ever accumulates — on the machine this was found on
// it still listed k3d-cp-daemon after the cluster had been recreated as
// k3d-cp-daemon-v2. One deleted context aborted the whole report, hiding every
// healthy cluster's kubelet GC configuration after it. (GC safety does not
// depend on this list: the registry protected set reads Registry.Contexts and
// still fails closed, and ConfigureNodes — which restarts nodes — still aborts.)
func TestStatusReportsAStaleClusterAndContinues(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	fake := dockerDesktopBuildx()
	p := DefaultPolicy()
	p.Builders = nil
	p.Clusters = []string{"k3d-gone", "k3d-live"}
	var out strings.Builder
	r := Runner{Policy: p, Out: &out, Command: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "kubectl" {
			return fake.command(ctx, name, args...)
		}
		joined := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "--context k3d-gone "):
			return nil, fmt.Errorf("exit status 1: Error in configuration: context was not found for specified context: k3d-gone")
		case strings.HasSuffix(joined, "get nodes -o json"):
			return []byte(`{"items":[{"metadata":{"name":"k3d-live-server-0"}}]}`), nil
		case strings.Contains(joined, "/proxy/configz"):
			return []byte(`{"kubeletconfig":{"imageMaximumGCAge":"168h0m0s"}}`), nil
		}
		return nil, fmt.Errorf("unexpected kubectl %v", args)
	}}
	err := r.Status(context.Background())
	if !strings.Contains(out.String(), "node k3d-live-server-0 GC configuration") {
		t.Fatalf("a stale cluster hid the healthy one's report (err=%v):\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "cluster k3d-gone: unavailable") {
		t.Errorf("the stale cluster was not reported:\n%s", out.String())
	}
	if err != nil {
		t.Errorf("status of a reachable machine with one stale cluster failed: %v", err)
	}
}

func TestBuildSpaceChecksScratchAndNewOutputDirectories(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"GOTMPDIR", "GOCACHE", "GOMODCACHE"} {
		t.Setenv(name, filepath.Join(root, name, "new"))
	}
	output := filepath.Join(root, "new", "bin")
	// This test asserts WHICH filesystems are measured, not how full the host
	// is, so the reserve is the policy minimum: a real runner disk below the
	// 20 GiB default must not fail it.
	policy := DefaultPolicy()
	policy.HostReserveGiB = 1
	got, err := CheckBuildSpace(policy, output)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, disk := range got {
		seen[disk.Path] = true
	}
	for _, name := range []string{"GOTMPDIR", "GOCACHE", "GOMODCACHE"} {
		if !seen[os.Getenv(name)] {
			t.Fatalf("%s mount was not checked: %+v", name, got)
		}
	}
	if !seen[os.TempDir()] {
		t.Fatal("OS scratch filesystem was not checked")
	}
	if got[0].Path != output {
		t.Fatalf("checked %s instead of output", got[0].Path)
	}
	existing, err := existingStoragePath(output)
	if err != nil || existing != root {
		t.Fatalf("existing output parent=%q, err=%v", existing, err)
	}
	if _, err := os.Stat(filepath.Dir(output)); !os.IsNotExist(err) {
		t.Fatalf("space check created output: %v", err)
	}
}

func TestGCRejectsRemoteDockerHostOverride(t *testing.T) {
	t.Setenv("DOCKER_HOST", "ssh://prod")
	t.Setenv("DOCKER_CONTEXT", "")
	r := Runner{Policy: DefaultPolicy(), Command: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("remote endpoint reached")
		return nil, nil
	}}
	if err := r.GC(context.Background(), true); err == nil {
		t.Fatal("remote DOCKER_HOST accepted")
	}
}
