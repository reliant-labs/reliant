package cli

import (
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// TestKCLBuildPlanHelpers covers the helpers runBuild uses to decide
// whether the env needs the project image and which arch to build it for,
// over the render-contract goldens.
func TestKCLBuildPlanHelpers(t *testing.T) {
	cluster, _ := loadContract(t, "cluster")
	if !envNeedsProjectImage(cluster) {
		t.Error("envNeedsProjectImage(cluster golden): want true (api is a go-built cluster workload)")
	}
	cluster.ClusterTarget.Platform = "arm64"
	if got := kclFirstClusterPlatform(cluster); got != "arm64" {
		t.Errorf("kclFirstClusterPlatform: got %q, want the declared cluster_target platform arm64", got)
	}
}

// TestEnvNeedsProjectImage_ByRuntimeAndBuild pins the one question
// runBuild asks before building the project image: is some go-built
// workload run FROM an image? Host workloads run their binary directly and
// a workload that only names a third-party image has nothing to build.
func TestEnvNeedsProjectImage_ByRuntimeAndBuild(t *testing.T) {
	noBuild := func(w *WorkloadEntity) { w.Build = BuildConfigEntity{} }
	goBuild := func(w *WorkloadEntity) {
		w.Build = BuildConfigEntity{Type: "go", Go: &GoBuild{Cmd: "./cmd/api", OutputName: "api"}}
	}
	cases := map[string]struct {
		workloads []WorkloadEntity
		want      bool
	}{
		"all host":                        {[]WorkloadEntity{hostWL("a"), hostWL("b")}, false},
		"compose only":                    {[]WorkloadEntity{composeWL("pg", "docker-compose.yml")}, false},
		"cluster, go build":               {[]WorkloadEntity{clusterWL("api", "k3d-dev", "dev")}, true},
		"cluster, prebuilt image":         {[]WorkloadEntity{clusterWL("nats", "k3d-dev", "dev", noBuild)}, false},
		"hosted, prebuilt image":          {[]WorkloadEntity{hostedWL("api")}, false},
		"hosted, declares a go build":     {[]WorkloadEntity{hostedWL("api", goBuild)}, true},
		"host api plus cluster go worker": {[]WorkloadEntity{hostWL("api"), clusterWL("w", "k3d-dev", "dev")}, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := envNeedsProjectImage(&KCLEntities{Workloads: tc.workloads}); got != tc.want {
				t.Errorf("envNeedsProjectImage = %v, want %v", got, tc.want)
			}
		})
	}
	if got := kclFirstClusterPlatform(&KCLEntities{Workloads: []WorkloadEntity{hostWL("a")}}); got != "" {
		t.Errorf("kclFirstClusterPlatform on no-cluster: got %q, want empty", got)
	}
}

// A frontend the rendered env declares with a cross-repo `source:` pin is
// absent from the CODEGEN inventory by design — forge must never project
// this project's TypeScript into a sibling repository's working tree (see
// config.KCLFrontend.OwnsFrontendCode). It is nonetheless a thing this
// project BUILDS and DEPLOYS: renderBuildEntities materializes its source
// into a local cache specifically so `npm run build` has a directory.
//
// Resolving `--target` against the codegen inventory alone conflated the
// two sets, so control-plane's `forge build prod --target reliant-web`
// printed the frontend in its own plan and then answered "target
// "reliant-web" not found in project config or in env "prod"'s KCL
// services".
func TestResolveNamedBuildTarget_CrossRepoFrontend(t *testing.T) {
	entities, err := parseKCLEntities([]byte(`{
  "frontends": [
    {"name": "reliant-web", "type": "vite",
     "path": "/cache/forge/sources/reliant/web",
     "source": {"repo": "github.com/reliant-labs/reliant", "ref": "v1.7.11"},
     "deploy": {"type": "firebase"}}
  ]
}`))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}

	cfg := &config.ProjectConfig{Name: "control-plane"}
	opts := buildOptions{buildTarget: "reliant-web", env: "prod"}

	// The codegen inventory is empty — exactly control-plane's state.
	frontends, buildBinary, err := resolveNamedBuildTarget(cfg, entities, &opts, nil)
	if err != nil {
		t.Fatalf("resolveNamedBuildTarget: %v\n"+
			"a frontend the env declares and whose source forge already materialized "+
			"must be resolvable as a build target", err)
	}
	if buildBinary {
		t.Error("buildBinary = true; naming a frontend must not also build the project binary")
	}
	if len(frontends) != 1 || frontends[0].Name != "reliant-web" {
		t.Fatalf("frontends = %+v, want exactly [reliant-web]", frontends)
	}
	// The materialized path is what `npm run build` shells into. Falling
	// back to the frontends/<name> convention would run the build in a
	// directory that does not exist.
	if got := frontends[0].DeclaredDir(); got != "/cache/forge/sources/reliant/web" {
		t.Errorf("Path = %q, want the resolved source directory", got)
	}
	if got := frontends[0].Type; got != "vite" {
		t.Errorf("Type = %q, want vite (carried from the KCL declaration)", got)
	}
}

// The codegen inventory still WINS when it has the name: its entry
// carries the project's own declared type and path, and a KCL render is
// per-env while forge.yaml is not.
func TestResolveNamedBuildTarget_InventoryEntryWins(t *testing.T) {
	entities, err := parseKCLEntities([]byte(`{
  "frontends": [{"name": "web", "type": "nextjs", "path": "kcl/path"}]
}`))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}
	cfg := &config.ProjectConfig{Name: "proj"}
	opts := buildOptions{buildTarget: "web", env: "prod"}
	inventory := []config.FrontendConfig{config.FrontendConfig{Name: "web", Type: "nextjs"}.WithDir("frontends/web")}

	frontends, _, err := resolveNamedBuildTarget(cfg, entities, &opts, inventory)
	if err != nil {
		t.Fatalf("resolveNamedBuildTarget: %v", err)
	}
	if len(frontends) != 1 || frontends[0].DeclaredDir() != "frontends/web" {
		t.Fatalf("frontends = %+v, want the forge.yaml inventory entry", frontends)
	}
}

// An unknown name is still an error, and the message still names both
// places forge looked. Widening the frontend lookup must not turn a typo
// into a silent no-op build.
func TestResolveNamedBuildTarget_UnknownNameStillErrors(t *testing.T) {
	entities, err := parseKCLEntities([]byte(`{"frontends":[{"name":"web","path":"frontends/web"}]}`))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}
	cfg := &config.ProjectConfig{Name: "proj"}
	opts := buildOptions{buildTarget: "nope", env: "prod"}
	if _, _, err := resolveNamedBuildTarget(cfg, entities, &opts, nil); err == nil {
		t.Fatal("want an error for a target no frontend, binary or service matches")
	}
}

// Naming a frontend must narrow the ENTITY set too, not just pick the
// frontend. Every external-build service reads `entities`, so leaving it
// whole runs each one's build_cmd — the "one command rebuilt my whole
// stack" failure the service-name branch already existed to prevent.
//
// This was unreachable while a frontend resolved only from the codegen
// inventory (no external-build service is ever in it). Making KCL
// frontends resolvable opened it, and `forge build prod --target
// reliant-web` on control-plane hit it: the frontend built in 22s, then
// four unrelated docker pushes ran and failed.
func TestBuildTargetNarrowing_FrontendNameScopesEntities(t *testing.T) {
	entities, err := parseKCLEntities([]byte(`{"output": {
  "workloads": [
    {"name": "api", "kind": "service", "runtime": {"type": "cluster", "cluster": "k3d-dev", "namespace": "dev"},
     "build": {"type": "shell", "cmd": "docker push everything"}, "spec": {"kind": "service"}}
  ],
  "frontends": [
    {"name": "reliant-web", "type": "vite", "path": "web",
     "source": {"repo": "github.com/reliant-labs/reliant", "ref": "v1"}},
    {"name": "internal-console", "type": "nextjs", "path": "frontends/internal-console"}
  ]
}}`))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}

	got := filterEntitiesByTarget(entities, []string{"reliant-web"})
	if len(got.Workloads) != 0 {
		t.Errorf("workloads = %+v, want none — naming a frontend must not run a workload's shell build", got.Workloads)
	}
	if len(got.Frontends) != 1 || got.Frontends[0].Name != "reliant-web" {
		t.Errorf("frontends = %+v, want exactly [reliant-web]", got.Frontends)
	}
}

// `--target <project>` builds the PROJECT image and nothing else. It is the
// command a CI job runs to publish that one image (forge's own scaffolded
// build-images.yml: `forge env build <env> --target <project> --push`), so every
// other artifact the env declares is out of scope: a ShellBuild's build_cmd
// (control-plane's sibling-repo reliant images, which need a ../reliant
// checkout CI does not have), a DockerBuild workload (a containerised
// frontend), and the frontends' `npm run build`.
//
// Before the fix the project name narrowed NOTHING: the service and frontend
// names did, the project name fell through, and `forge build prod --target
// control-plane --push --plan` planned the reliant ShellBuilds and the
// internal-console DockerBuild beside the project image — and failed on the
// missing sibling checkout. resolveNamedBuildTarget had the same hole for the
// frontends: its comment said "with its frontends already filtered away" and
// it returned every one.
func TestBuildTargetNarrowing_ProjectNameScopesToTheProjectImage(t *testing.T) {
	entities, err := parseKCLEntities([]byte(`{"output": {
  "workloads": [
    {"name": "admin-server", "kind": "service", "image": "control-plane",
     "runtime": {"type": "cluster", "cluster": "gke-prod", "namespace": "prod"},
     "build": {"type": "go", "cmd": "./cmd/control-plane", "output_name": "control-plane"}, "spec": {"kind": "service"}},
    {"name": "reliant-api-server", "kind": "service", "image": "reliant",
     "runtime": {"type": "cluster", "cluster": "gke-prod", "namespace": "prod"},
     "build": {"type": "shell", "cmd": "docker push reliant", "cwd": "../reliant"}, "spec": {"kind": "service"}},
    {"name": "internal-console", "kind": "service", "image": "internal-console",
     "runtime": {"type": "cluster", "cluster": "gke-prod", "namespace": "prod"},
     "build": {"type": "docker", "dockerfile": "frontends/internal-console/Dockerfile"}, "spec": {"kind": "service"}}
  ],
  "frontends": [
    {"name": "reliant-web", "type": "vite", "path": "web",
     "source": {"repo": "github.com/reliant-labs/reliant", "ref": "v1"}}
  ]
}}`))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}

	got := narrowBuildEntities("control-plane", entities, buildOptions{buildTarget: "control-plane", env: "prod"})
	var names []string
	for _, w := range got.Workloads {
		names = append(names, w.Name)
	}
	if len(names) != 1 || names[0] != "admin-server" {
		t.Errorf("workloads = %v, want [admin-server] — only the workloads the project image is built for", names)
	}
	if svcs := externalBuildServices(got); len(svcs) != 0 {
		t.Errorf("external builds = %d, want 0 — naming the project must not run a ShellBuild's build_cmd", len(svcs))
	}
	if len(got.Frontends) != 0 {
		t.Errorf("frontends = %+v, want none", got.Frontends)
	}
	// The project image itself must survive the narrowing: the binary is
	// still compiled and the image still built.
	if !envNeedsProjectImage(got) {
		t.Error("envNeedsProjectImage = false — the narrowing dropped the project image it was asked for")
	}
	if targets := goBuildTargetsFromKCL(got); len(targets) != 1 || targets[0].cmd != "./cmd/control-plane" {
		t.Errorf("go targets = %+v, want exactly ./cmd/control-plane", targets)
	}
	// The declared set is untouched: the registry and every other env-wide
	// fact are read from the FULL render.
	if len(entities.Workloads) != 3 {
		t.Errorf("narrowing mutated the declared render: %d workloads, want 3", len(entities.Workloads))
	}

	cfg := &config.ProjectConfig{Name: "control-plane"}
	opts := buildOptions{buildTarget: "control-plane", env: "prod"}
	inventory := []config.FrontendConfig{config.FrontendConfig{Name: "internal-console", Type: "nextjs"}.WithDir("frontends/internal-console")}
	frontends, buildBinary, err := resolveNamedBuildTarget(cfg, got, &opts, inventory)
	if err != nil {
		t.Fatalf("resolveNamedBuildTarget: %v", err)
	}
	if !buildBinary {
		t.Error("buildBinary = false — naming the project must build its binary")
	}
	if len(frontends) != 0 {
		t.Errorf("frontends = %+v, want none — the project target builds no frontend", frontends)
	}
	if !opts.skipFrontends {
		t.Error("skipFrontends = false, want true for the project target")
	}
}

// The narrowing predicate itself: a KCL frontend name must be recognised
// as a narrowable target. Guards the condition in renderBuildEntities
// that decides whether to filter at all.
func TestKCLFrontendAsBuildTarget_RecognisesFrontendNames(t *testing.T) {
	entities, err := parseKCLEntities([]byte(
		`{"frontends":[{"name":"reliant-web","type":"vite","path":"web"}]}`))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}
	if kclFrontendAsBuildTarget(entities, "reliant-web") == nil {
		t.Error("a frontend the env declares must be recognised as a build target")
	}
	if kclFrontendAsBuildTarget(entities, "nope") != nil {
		t.Error("an unknown name must not be recognised")
	}
	if kclFrontendAsBuildTarget(nil, "reliant-web") != nil {
		t.Error("a nil entity set (no --env) must not resolve anything")
	}
}

// TestNamedKCLServiceTargetStillBuildsItsBinary pins the fix for a silent
// no-op: `forge build <env> --target <kcl-service>` reported success having
// compiled nothing.
//
// resolveNamedBuildTarget returned buildBinary=false for a KCL service, and
// resolveGoTargets(false, ...) returns nil unconditionally — so no go target
// survived. The command printed a 0s summary with no build lines, which reads
// exactly like a cache hit. In control-plane that shipped a stale admin-server
// image to production against a freshly migrated database; every RPC touching
// the users table 500'd until the real image was built.
//
// The service here mirrors that shape: a go-build service whose cmd is the
// SHARED project binary (control-plane's admin-server runs
// ["./control-plane","public-api"]), which is why "did the target resolve"
// and "did anything get compiled" are different questions.
func TestNamedKCLServiceTargetStillBuildsItsBinary(t *testing.T) {
	entities := &KCLEntities{
		Workloads: []WorkloadEntity{{
			Name:  "admin-server",
			Image: "control-plane",
			Build: BuildConfigEntity{
				Type: "go",
				Go:   &GoBuild{Cmd: "./cmd/control-plane", OutputName: "control-plane"},
			},
		}},
	}
	cfg := &config.ProjectConfig{Name: "control-plane"}
	opts := &buildOptions{buildTarget: "admin-server", env: "prod"}

	frontends, buildBinary, err := resolveNamedBuildTarget(cfg, entities, opts, nil)
	if err != nil {
		t.Fatalf("resolveNamedBuildTarget: %v", err)
	}
	if len(frontends) != 0 {
		t.Errorf("frontends = %d, want 0 — a named service target builds no frontends", len(frontends))
	}
	if !opts.skipFrontends {
		t.Error("skipFrontends = false, want true for a named service target")
	}
	if !buildBinary {
		t.Fatal("buildBinary = false — the named service's binary would not be built, " +
			"so the command succeeds having compiled nothing and a stale image ships")
	}

	// The contract that matters is the one runBuild consumes: resolving the
	// target must yield real work. Asserting the flag alone would pass even if
	// resolveGoTargets later dropped it.
	got := resolveGoTargets(buildBinary, entities, cfg)
	if len(got) == 0 {
		t.Fatal("resolveGoTargets returned no targets for a named go-build service")
	}
	if got[0].cmd != "./cmd/control-plane" {
		t.Errorf("go target cmd = %q, want the service's own ./cmd/control-plane", got[0].cmd)
	}
}
