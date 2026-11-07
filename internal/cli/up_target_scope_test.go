package cli

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// targetScopeEntities is a stack with both sides of the split declared: two
// cluster services (which only a full run should build and push), one host
// service, and two frontends. It is the shape that made `--target reliant-web`
// docker-build the whole project.
func targetScopeEntities() *KCLEntities {
	return &KCLEntities{
		Workloads: []WorkloadEntity{
			hostWL("admin-server"),
			clusterWL("workspace-proxy", "k3d-x", "ns"),
			clusterWL("daemon-gateway", "k3d-x", "ns"),
		},
		Frontends: []FrontendEntity{
			{Name: "reliant-web", Port: 3000},
			{Name: "settings-web", Port: 3001},
		},
	}
}

// TestUpTargetRejectsUnknownName pins the typo case. `forge env up --target`
// used to accept any string: inTargetSet simply matched nothing, so the run
// tore the stack down (the pre-flight is unconditional) and then started
// nothing in its place. The available-name list is what makes the error
// actionable, and it must include frontends — the names most likely to be
// targeted are exactly the ones the deploy-side validator was never asked
// about.
func TestUpTargetRejectsUnknownName(t *testing.T) {
	e := targetScopeEntities()

	if err := validateDeployTargets(e, nil); err != nil {
		t.Errorf("empty target set must be a no-op: %v", err)
	}
	if err := validateDeployTargets(e, []string{"reliant-web"}); err != nil {
		t.Errorf("a declared frontend must be a valid target: %v", err)
	}

	err := validateDeployTargets(e, []string{"reliant-wbe"})
	if err == nil {
		t.Fatal("a target that names nothing was accepted; the run would tear the stack down and start nothing")
	}
	// The message has to name what IS available, or the user is left
	// guessing at the spelling that just cost them their stack.
	for _, want := range []string{"reliant-wbe", "reliant-web", "admin-server"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestUpTargetReachesTheBuildPhase pins the WIRING, not the filter. The
// narrowing helper (filterEntitiesByTarget) already existed and worked — the
// defect was that `forge env up`'s build phase never passed it anything, so
// `--target reliant-web` docker-built and pushed every cluster image on the
// way to starting one Vite dev server. Asserting on the helper alone passes
// even with the wiring removed, so this asserts on the buildOptions the up
// path actually constructs.
func TestUpTargetReachesTheBuildPhase(t *testing.T) {
	// upBuildCluster is the up path's entry into runBuild. Its options are
	// what decide whether the build phase can scope at all; a targets field
	// that never arrives is the bug.
	opts := upBuildOptionsFor("dev", false, []string{"reliant-web"})

	if len(opts.targets) != 1 || opts.targets[0] != "reliant-web" {
		t.Fatalf("--target never reached the build phase (buildOptions.targets = %v); "+
			"the docker build+push runs unscoped", opts.targets)
	}

	// And with no target, the build stays unscoped — a bare `forge env up`
	// must still build everything.
	if got := upBuildOptionsFor("dev", false, nil); len(got.targets) != 0 {
		t.Errorf("unscoped run picked up targets %v", got.targets)
	}
}

// TestUpBuildPushesToTheDeclaredRegistry pins that `forge env up` PUSHES the
// image it builds to the registry that image's own reference names — the one
// its cluster pulls from. Asserted on the real runBuild (in --plan mode), not on the
// options struct, because the regression lived between the two: env up handed
// runBuild a registry but never switched push on, and runBuild's one resolver
// (resolvePushPlan) reads "no --push" as "push nothing" and overwrote it.
// The build then printed "tagged locally, not pushed", the cluster kept
// pulling whatever image last sat at that tag, and the rollout ran the old
// code with nothing in the output saying so.
func TestUpBuildPushesToTheDeclaredRegistry(t *testing.T) {
	planProject(t, declaredRegistryFixture)

	opts := upBuildOptionsFor("prod", true, nil)
	opts.plan = true
	opts.tag = "t1"

	var runErr error
	out := captureStdout(t, func() { runErr = runBuild(context.Background(), opts) })
	if runErr != nil {
		t.Fatalf("env up's build phase: %v\n%s", runErr, out)
	}
	if !strings.Contains(out, "push registry.example/prod/pt:t1") {
		t.Errorf("env up built the image but did not push it to the env-declared registry; plan output:\n%s", out)
	}
	if strings.Contains(out, "not pushed") {
		t.Errorf("env up's build header says the image is not pushed; plan output:\n%s", out)
	}
}

// TestUpBuildWithoutDeclaredRegistryBuildsLocally is the other half: env up
// is also the host-only dev loop, and an env that declares no registry has no
// cluster to pull from. Its build must succeed and push nothing — unlike
// `forge env build <env> --push`, where an undeclared registry is a runbook error.
func TestUpBuildWithoutDeclaredRegistryBuildsLocally(t *testing.T) {
	planProject(t, `{
  "output": {
    "workloads": [
      {
        "name": "pt",
        "kind": "service",
        "image": "pt",
        "build": {"type": "go", "cmd": "./cmd/pt", "output_name": "pt"},
        "runtime": {"type": "cluster", "cluster": "c", "namespace": "n"},
        "spec": {"kind": "service"}
      }
    ]
  }
}`)

	opts := upBuildOptionsFor("dev", true, nil)
	opts.plan = true
	opts.tag = "t1"

	var runErr error
	out := captureStdout(t, func() { runErr = runBuild(context.Background(), opts) })
	if runErr != nil {
		t.Fatalf("env up against an env that declares no registry must build locally, got: %v\n%s", runErr, out)
	}
	if strings.Contains(out, "push ") {
		t.Errorf("an env that declares no registry must push nothing; plan output:\n%s", out)
	}
}

// TestUpTargetScopesTheBuildSet covers what the narrowed set then means for
// the build decisions downstream of it: the go-build targets, the docker
// build+push, and the skipProjectDocker guard all read "does this env still
// declare a cluster service" off the filtered entities.
func TestUpTargetScopesTheBuildSet(t *testing.T) {
	e := targetScopeEntities()

	// Sanity: unfiltered, this env has cluster services, so a full run
	// legitimately builds and pushes an image.
	if !envNeedsProjectImage(e) {
		t.Fatal("fixture is wrong: the unfiltered env must declare a cluster service")
	}

	scoped := filterEntitiesByTarget(e, []string{"reliant-web"})

	if envNeedsProjectImage(scoped) {
		t.Error("targeting a frontend left an image-shipping entity in the build set — the docker build+push still runs")
	}
	if len(scoped.Frontends) != 1 || scoped.Frontends[0].Name != "reliant-web" {
		t.Errorf("targeted frontend not preserved: %+v", scoped.Frontends)
	}

	// Targeting a cluster service keeps its build: scoping must narrow, not
	// disable, or `--target workspace-proxy` would deploy a stale image.
	clusterScoped := filterEntitiesByTarget(e, []string{"workspace-proxy"})
	if !envNeedsProjectImage(clusterScoped) {
		t.Error("targeting a cluster service dropped it from the build set")
	}
	if len(clusterScoped.Workloads) != 1 {
		t.Errorf("expected exactly the targeted workload, got %+v", clusterScoped.Workloads)
	}
}

// TestTargetPhaseRequirements pins the phase-level half of --target scoping.
// Filtering the eventual manifest stream is too late: cluster creation and
// cross-cluster kubeconfig minting happen before render/apply, and calling the
// deploy pipeline with a host-only target reaches its empty-manifest fallback.
func TestTargetPhaseRequirements(t *testing.T) {
	controller := clusterWL("controller", "dev", "ns")
	controller.Kind = "operator"
	e := &KCLEntities{
		Workloads: []WorkloadEntity{
			hostWL("api"),
			{Name: "cli", Kind: "tool", Runtime: RuntimeEntity{Type: RuntimeBuildOnly, BuildOnly: &BuildOnlyDeploy{}}},
			composeWL("compose-dep", "docker-compose.yml"),
			clusterWL("cluster-api", "dev", "ns"),
			controller,
			hostedWL("hosted-api"),
		},
		Infra:      []HostInfraEntity{{Name: "postgres", Engine: "postgres"}},
		HelmCharts: []HelmChartEntity{{Name: "gateway"}},
		Frontends:  []FrontendEntity{{Name: "web"}},
	}

	tests := []struct {
		name    string
		targets []string
		want    upPhaseRequirements
	}{
		{name: "unscoped full reconcile", want: upPhaseRequirements{deploy: true, cluster: true}},
		{name: "host and dev frontend", targets: []string{"api", "web"}, want: upPhaseRequirements{}},
		{name: "build only", targets: []string{"cli"}, want: upPhaseRequirements{}},
		{name: "compose without cluster", targets: []string{"compose-dep"}, want: upPhaseRequirements{deploy: true}},
		{name: "host infra without cluster", targets: []string{"postgres"}, want: upPhaseRequirements{deploy: true}},
		// A hosted workload is published by `forge env deploy`, never by
		// the dev loop.
		{name: "hosted workload", targets: []string{"hosted-api"}, want: upPhaseRequirements{}},
		{name: "cluster service", targets: []string{"cluster-api"}, want: upPhaseRequirements{deploy: true, cluster: true}},
		{name: "operator", targets: []string{"controller"}, want: upPhaseRequirements{deploy: true, cluster: true}},
		{name: "platform chart", targets: []string{"gateway"}, want: upPhaseRequirements{deploy: true, cluster: true}},
		{name: "mixed host and cluster", targets: []string{"api", "cluster-api"}, want: upPhaseRequirements{deploy: true, cluster: true}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := targetPhaseRequirements(e, tc.targets); got != tc.want {
				t.Fatalf("targetPhaseRequirements(%v) = %+v, want %+v", tc.targets, got, tc.want)
			}
		})
	}
}

// TestFilterRootsByService covers the scoped-teardown selection rule without
// real processes: frontends are stamped `frontend:<name>` but targeted by
// bare name, and an unattributable process is never signalled by a scoped
// stop (it is not evidence the targeted service is running).
func TestFilterRootsByService(t *testing.T) {
	facts := &fakeProcFacts{env: map[int][]string{
		10: {forgeUpServiceVar + "=admin-server"},
		11: {forgeUpServiceVar + "=frontend:reliant-web"},
		12: {forgeUpServiceVar + "=workspace-proxy"},
		// 13 has no readable environment at all.
	}}

	got := filterRootsByService([]int{10, 11, 12, 13}, []string{"reliant-web"}, facts)
	if len(got) != 1 || got[0] != 11 {
		t.Errorf("scoped teardown selected %v; want just the frontend pid 11", got)
	}

	got = filterRootsByService([]int{10, 11, 12, 13}, []string{"admin-server", "reliant-web"}, facts)
	if len(got) != 2 {
		t.Errorf("multi-target teardown selected %v; want pids 10 and 11", got)
	}

	if got := filterRootsByService([]int{13}, []string{"admin-server"}, facts); len(got) != 0 {
		t.Errorf("an unattributable process was selected for a scoped teardown: %v", got)
	}
}

// fakeProcFacts serves a fixed environment per pid, with no process table.
type fakeProcFacts struct {
	env map[int][]string
}

func (f *fakeProcFacts) environ(pid int) ([]string, bool) {
	e, ok := f.env[pid]
	return e, ok
}
func (f *fakeProcFacts) parent(int) (int, bool) { return 0, false }
func (f *fakeProcFacts) argv(int) ([]string, bool) {
	return nil, false
}

// TestUpPreflight_ScopedTargetLeavesOtherServicesRunning is the destructive
// half of the defect, pinned against real marked processes.
//
// `forge env up dev --target reliant-web` on a live stack used to SIGTERM
// every service in the env — admin-server, the API server, the worker — and
// then start only the frontend. The user asked to restart one service and
// silently lost five, with nothing in the output saying so.
//
// The predecessor of the TARGETED service must still be stopped: that is the
// "one stack per (project, env)" rule the reclaim exists to enforce, and
// leaving it would put two copies of one service on the same port.
func TestUpPreflight_ScopedTargetLeavesOtherServicesRunning(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns and stops real processes; runs in task test")
	}
	requireProcInspection(t)
	dir, projectID, env := testStack(t)

	targeted := spawnMarked(t, projectID, env, "frontend:reliant-web")
	bystander := spawnMarked(t, projectID, env, "admin-server")
	otherFrontend := spawnMarked(t, projectID, env, "frontend:settings-web")

	reg := newProcRegistry(projectID, dir, env)
	reg.processes = []*managedProcess{
		{name: "frontend:reliant-web", pid: targeted.pid(), cmd: &exec.Cmd{}},
		{name: "admin-server", pid: bystander.pid(), cmd: &exec.Cmd{}},
		{name: "frontend:settings-web", pid: otherFrontend.pid(), cmd: &exec.Cmd{}},
	}
	reg.persist()

	port := freePort(t)
	if err := upPreflight(projectID, env, entitiesOnPort(port), []string{"reliant-web"}, true); err != nil {
		t.Fatalf("upPreflight with --target: %v", err)
	}

	if !targeted.waitExit(15 * time.Second) {
		t.Errorf("the targeted service's predecessor (pid %d) survived — two copies now race for its port", targeted.pid())
	}
	if !bystander.alive() {
		t.Error("--target reliant-web killed admin-server: a scoped run must not tear down services it was not asked to restart")
	}
	if !otherFrontend.alive() {
		t.Error("--target reliant-web killed settings-web: only the named service may be replaced")
	}

	// The ledger must still describe the survivors. It is what
	// `forge env down` and `forge env ps` read; dropping it under a scope
	// would strand every process the scoped run deliberately left alive.
	statePath, err := upStatePath(projectID, env)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("scoped teardown removed the ledger describing still-running services: %v", err)
	}
	if !strings.Contains(string(data), "admin-server") {
		t.Errorf("ledger lost the surviving admin-server, stranding it: %q", data)
	}
}

// TestPersistCarriesForwardSurvivingEntries pins the ledger merge. A scoped
// run rewrites the ledger after starting only its own services; without the
// carry-forward it would erase the entries for services it deliberately left
// running, making them unreachable to `forge env down`.
func TestPersistCarriesForwardSurvivingEntries(t *testing.T) {
	requireProcInspection(t)
	dir, projectID, env := testStack(t)

	survivor := spawnMarked(t, projectID, env, "admin-server")

	// The ledger as the previous full run left it.
	prev := newProcRegistry(projectID, dir, env)
	prev.processes = []*managedProcess{
		{name: "admin-server", pid: survivor.pid(), cmd: &exec.Cmd{}},
		{name: "frontend:reliant-web", pid: 999999, cmd: &exec.Cmd{}}, // dead: must be dropped
	}
	prev.persist()

	// The scoped run starts only the frontend, under a new pid.
	scoped := newProcRegistry(projectID, dir, env)
	scoped.processes = []*managedProcess{
		{name: "frontend:reliant-web", pid: survivor.pid(), cmd: &exec.Cmd{}},
	}
	scoped.persist()

	entries := trackedStack(projectID, env)
	byName := map[string]int{}
	for _, e := range entries {
		byName[e.name] = e.pid
	}
	if byName["admin-server"] != survivor.pid() {
		t.Errorf("the surviving service was dropped from the ledger: %+v", entries)
	}
	if byName["frontend:reliant-web"] != survivor.pid() {
		t.Errorf("the restarted service did not take the new pid: %+v", entries)
	}
	if len(entries) != 2 {
		t.Errorf("expected exactly the survivor + the restarted service, got %+v", entries)
	}
}
