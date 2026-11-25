package cli

import (
	"strings"
	"testing"
)

// The contract's workload decode is pinned against the real §9.1 goldens in
// render_contract_decode_test.go. This file pins the pieces below it: the
// build-union dispatch, the go-build target set, and the manifest-derived
// facts.

func TestDispatchBuild(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		wantType string
		check    func(t *testing.T, b BuildConfigEntity)
	}{
		{
			name:     "go",
			raw:      `{"type":"go","cmd":"./cmd/trader","goarch":"arm64","flags":["-cover"],"ldflags":["-s"],"env":{"CGO_ENABLED":"0"}}`,
			wantType: "go",
			check: func(t *testing.T, b BuildConfigEntity) {
				if b.Go == nil || b.Go.Cmd != "./cmd/trader" || b.Go.GOARCH != "arm64" {
					t.Errorf("go build = %+v", b.Go)
				}
				if len(b.Go.Flags) != 1 || b.Go.Flags[0] != "-cover" {
					t.Errorf("flags = %v", b.Go.Flags)
				}
			},
		},
		{
			name:     "docker",
			raw:      `{"type":"docker","dockerfile":"Dockerfile.x","platform":"linux/arm64","target":"runtime"}`,
			wantType: "docker",
			check: func(t *testing.T, b BuildConfigEntity) {
				if b.Docker == nil || b.Docker.Dockerfile != "Dockerfile.x" || b.Docker.Platform != "linux/arm64" {
					t.Errorf("docker build = %+v", b.Docker)
				}
			},
		},
		{
			name:     "shell",
			raw:      `{"type":"shell","cmd":"make build"}`,
			wantType: "shell",
			check: func(t *testing.T, b BuildConfigEntity) {
				if b.Shell == nil || b.Shell.Cmd != "make build" {
					t.Errorf("shell build = %+v", b.Shell)
				}
			},
		},
		{
			name:     "remote",
			raw:      `{"type":"remote","source":{"repo":"github.com/acme/api","ref":"v1.2.3","subdir":"svc"},"dockerfile":"docker/Dockerfile","platform":"linux/arm64","cpu_millicores":4000,"memory_bytes":17179869184,"cache_gib":50,"timeout_seconds":1800}`,
			wantType: "remote",
			check: func(t *testing.T, b BuildConfigEntity) {
				if b.Remote == nil {
					t.Fatal("remote build = nil")
				}
				// The SOURCE PIN is the field that makes a remote build
				// possible at all: a build service cannot read the caller's
				// filesystem, so losing repo/ref in decode would produce a
				// submission with nothing to build.
				if b.Remote.Source.Repo != "github.com/acme/api" || b.Remote.Source.Ref != "v1.2.3" {
					t.Errorf("remote source = %+v", b.Remote.Source)
				}
				if b.Remote.Source.Subdir != "svc" {
					t.Errorf("remote subdir = %q", b.Remote.Source.Subdir)
				}
				if b.Remote.Dockerfile != "docker/Dockerfile" || b.Remote.Platform != "linux/arm64" {
					t.Errorf("remote build = %+v", b.Remote)
				}
				// The size fields are the BILLING dimensions, so a decode
				// that dropped them would silently bill a schema default
				// rather than what was declared.
				if b.Remote.CPUMillicores != 4000 || b.Remote.MemoryBytes != 17179869184 {
					t.Errorf("remote resources = %dm / %d bytes", b.Remote.CPUMillicores, b.Remote.MemoryBytes)
				}
				if b.Remote.CacheGiB != 50 || b.Remote.TimeoutSeconds != 1800 {
					t.Errorf("remote cache/timeout = %d / %d", b.Remote.CacheGiB, b.Remote.TimeoutSeconds)
				}
			},
		},
		{
			name:     "null is absent",
			raw:      `null`,
			wantType: "",
			check:    func(t *testing.T, b BuildConfigEntity) {},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := dispatchBuild("svc", []byte(c.raw))
			if err != nil {
				t.Fatalf("dispatchBuild: %v", err)
			}
			if b.Type != c.wantType {
				t.Fatalf("type = %q, want %q", b.Type, c.wantType)
			}
			c.check(t, b)
		})
	}
}

func TestDispatchBuild_Errors(t *testing.T) {
	for _, c := range []struct{ name, raw, wantErr string }{
		{"missing type", `{"cmd":"x"}`, "build.type missing"},
		{"unknown type", `{"type":"rust"}`, "unrecognised build.type"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := dispatchBuild("svc", []byte(c.raw))
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("err = %v, want substring %q", err, c.wantErr)
			}
		})
	}
}

// forge never SYNTHESIZES a build. The lowering writes `build` explicitly for
// a workload forge builds (ADR 0002 §5), so a workload with no build block —
// a compose service, a third-party image, a sibling binary, an image-less
// infra workload — is simply not built. The old synthesized ./cmd/<name>
// default is what sent `forge env build --release` at packages that did not
// exist.
func TestNoSynthesizedBuild(t *testing.T) {
	for _, w := range []WorkloadEntity{
		composeWL("dev-infra", "docker-compose.yml"),
		hostedWL("api"),
		{Name: "prod-daemon-cluster", Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{}}},
	} {
		if w.Build.Type != "" || w.GoBuild() != nil {
			t.Errorf("%s: build %+v, want none", w.Name, w.Build)
		}
		if targets := goBuildTargetsFromKCL(&KCLEntities{Workloads: []WorkloadEntity{w}}); len(targets) != 0 {
			t.Errorf("%s became a go-build target: %+v", w.Name, targets)
		}
	}
	// An explicit ShellBuild is the shell escape hatch, surfaced by
	// EffectiveBuildCmd.
	sb := hostWL("sib2", func(w *WorkloadEntity) {
		w.Build = BuildConfigEntity{Type: "shell", Shell: &ShellBuild{Cmd: "make foo", Cwd: "../sib"}}
	})
	if sb.EffectiveBuildCmd() != "make foo" || sb.EffectiveBuildCwd() != "../sib" {
		t.Errorf("shell build accessors = %q / %q", sb.EffectiveBuildCmd(), sb.EffectiveBuildCwd())
	}
}

func TestGoBuildTargetsFromKCL_DedupsSharedBinary(t *testing.T) {
	// Two workloads that map to the same shared ./cmd/proj binary collapse
	// to ONE go-build target; a distinct binary stays separate; a docker
	// build is not a go target. Every runtime contributes (a host go-run
	// still needs its module to build).
	shared := func(w *WorkloadEntity) {
		w.Build = BuildConfigEntity{Type: "go", Go: &GoBuild{Cmd: "./cmd/proj", OutputName: "proj"}}
	}
	e := &KCLEntities{Workloads: []WorkloadEntity{
		hostWL("users", shared),
		clusterWL("orders", "k3d-x", "ns", shared),
		hostWL("gateway", func(w *WorkloadEntity) {
			w.Build = BuildConfigEntity{Type: "go", Go: &GoBuild{Cmd: "./cmd/gateway", OutputName: "gateway"}}
		}),
		clusterWL("image", "k3d-x", "ns", func(w *WorkloadEntity) { w.Build = BuildConfigEntity{Type: "docker", Docker: &DockerBuild{}} }),
	}}
	targets := goBuildTargetsFromKCL(e)
	if len(targets) != 2 {
		t.Fatalf("want 2 deduped go targets, got %d: %+v", len(targets), targets)
	}
	got := map[string]bool{}
	for _, tt := range targets {
		got[tt.cmd] = true
	}
	if !got["./cmd/proj"] || !got["./cmd/gateway"] {
		t.Errorf("targets = %+v, want ./cmd/proj + ./cmd/gateway (docker excluded)", targets)
	}
}

func TestParseKCLEntities_EmptyJSON(t *testing.T) {
	entities, err := parseKCLEntities([]byte("  "))
	if err != nil {
		t.Fatalf("parseKCLEntities on empty: %v", err)
	}
	if len(entities.Workloads) != 0 {
		t.Errorf("expected empty entities, got %+v", entities)
	}
}

// The namespace tally over output.manifests ignores cluster-scoped
// (namespace-less) objects and picks the dominant namespace
// deterministically when more than one appears; raw Service names are
// collected for the ingress audit.
func TestParseKCLEntities_ManifestDerivedFacts(t *testing.T) {
	e, err := parseKCLEntities([]byte(`{"output":{"workloads":[],"manifests":[
	  {"kind": "ClusterRole", "metadata": {"name": "x"}},
	  {"kind": "Deployment", "metadata": {"name": "a", "namespace": "main-ns"}},
	  {"kind": "Service", "metadata": {"name": "op-api", "namespace": "main-ns"}},
	  {"kind": "Secret", "metadata": {"name": "s", "namespace": "other-ns"}}
	]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if e.ManifestNamespace != "main-ns" {
		t.Errorf("ManifestNamespace = %q, want main-ns", e.ManifestNamespace)
	}
	if strings.Join(e.ManifestServiceNames, ",") != "op-api" {
		t.Errorf("ManifestServiceNames = %v, want [op-api]", e.ManifestServiceNames)
	}
	if manifestNamespace(nil) != "" {
		t.Error("empty manifests: want no namespace")
	}
}
