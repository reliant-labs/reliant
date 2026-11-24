package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/pkg/release"
)

const (
	runtimeDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	carriedDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

// builtFrom is shellSvc plus a built_images edge set.
func builtFrom(name, image string, edges map[string]string) WorkloadEntity {
	w := shellSvc(name, image, "build "+name, "", map[string]string{"DECLARED": "1"})
	w.Build.Shell.BuiltImages = edges
	return w
}

// externalBuildRecorder replaces the shell runner and the registry lookup. It
// records each unit's start, the environment it ran with, and whether every
// unit it is built FROM had already FINISHED when it started.
type externalBuildRecorder struct {
	mu       sync.Mutex
	started  []string
	env      map[string]map[string]string
	finished map[string]bool
	early    []string
	builds   atomic.Int32
}

func recordExternalBuilds(t *testing.T, failing map[string]bool, noDigest map[string]bool, deps map[string][]string) *externalBuildRecorder {
	t.Helper()
	oldBuild, oldDigest, oldSource, oldStorage := externalShellBuild, externalImageDigestResolver, externalSourceCapture, checkBuildStorageFn
	t.Cleanup(func() {
		externalShellBuild, externalImageDigestResolver, externalSourceCapture, checkBuildStorageFn = oldBuild, oldDigest, oldSource, oldStorage
	})
	rec := &externalBuildRecorder{env: map[string]map[string]string{}, finished: map[string]bool{}}
	checkBuildStorageFn = func(string) error { return nil }
	externalSourceCapture = func(context.Context, string, string, string) *buildtarget.Source { return nil }
	externalShellBuild = func(_ context.Context, spec buildtarget.Spec) buildtarget.BuildResult {
		rec.builds.Add(1)
		rec.mu.Lock()
		rec.started = append(rec.started, spec.Service)
		rec.env[spec.Service] = spec.BuildEnv
		for _, dep := range deps[spec.Service] {
			if !rec.finished[dep] {
				rec.early = append(rec.early, spec.Service+" before "+dep)
			}
		}
		rec.mu.Unlock()
		res := buildtarget.BuildResult{Service: spec.Service, Tag: spec.Tag}
		if failing[spec.Service] {
			res.Err = errors.New("mock push failed")
		}
		rec.mu.Lock()
		rec.finished[spec.Service] = true
		rec.mu.Unlock()
		return res
	}
	externalImageDigestResolver = func(_ context.Context, ref string) (string, []string, error) {
		name := strings.TrimPrefix(ref, "registry.example/")
		name = name[:strings.Index(name, ":")]
		if noDigest[name] {
			return "", nil, errors.New("manifest unknown")
		}
		if name == "runtime" {
			return runtimeDigest, []string{"linux/amd64"}, nil
		}
		return "sha256:" + strings.Repeat("f", 64), []string{"linux/amd64"}, nil
	}
	return rec
}

func resultsByWorkload(results []buildResult) map[string]buildResult {
	out := map[string]buildResult{}
	for _, r := range results {
		out[strings.TrimSuffix(r.name, " (external)")] = r
	}
	return out
}

// The dependent runs only after its parent FINISHED, and runs with the
// parent's pushed image — repository AND digest — in the variable it named.
// Independent of whether the dispatcher runs units in parallel.
func TestBuildExternalBuiltImagesRunsParentFirstWithItsDigest(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		t.Run(fmt.Sprintf("parallel=%v", parallel), func(t *testing.T) {
			deps := map[string][]string{"app": {"runtime"}, "tools": {"app", "runtime"}}
			rec := recordExternalBuilds(t, nil, nil, deps)
			app := builtFrom("app", "registry.example/app", map[string]string{"BASE": "runtime"})
			tools := builtFrom("tools", "registry.example/tools", map[string]string{"APP": "app", "BASE": "runtime"})
			runtime := builtFrom("runtime", "registry.example/runtime", nil)
			// Declared dependents first, so only the graph can put runtime first.
			services := []WorkloadEntity{tools, app, runtime}
			results := buildExternalServices(context.Background(), services, buildOptions{env: "prod", parallel: parallel}, "v1", t.TempDir())
			for _, r := range results {
				if r.err != nil {
					t.Fatalf("%s: %v", r.name, r.err)
				}
			}
			if len(rec.early) > 0 {
				t.Fatalf("built before the image it is built FROM finished: %v", rec.early)
			}
			wantBase := "registry.example/runtime@" + runtimeDigest
			if got := rec.env["app"]["BASE"]; got != wantBase {
				t.Fatalf("app BASE = %q, want %q", got, wantBase)
			}
			if got := rec.env["tools"]["APP"]; got != "registry.example/app@sha256:"+strings.Repeat("f", 64) {
				t.Fatalf("tools APP = %q", got)
			}
			if rec.env["app"]["DECLARED"] != "1" {
				t.Fatalf("declared env lost: %v", rec.env["app"])
			}
			if _, leaked := rec.env["runtime"]["BASE"]; leaked {
				t.Fatal("a build with no built_images received one")
			}
			// The declared env is shared with every reader of the entity; the
			// resolved digest must never be written back into it.
			if _, written := app.Build.Shell.Env["BASE"]; written {
				t.Fatal("resolved built image written into the declared env map")
			}
		})
	}
}

// A parent that fails, or pushes nothing forge can resolve to a digest, fails
// every build FROM it — without running that build on a guessed parent.
func TestBuildExternalBuiltImagesParentWithoutImageFailsDependent(t *testing.T) {
	cases := map[string]struct {
		failing, noDigest map[string]bool
		want              string
	}{
		"failed":    {failing: map[string]bool{"runtime": true}, want: "whose build failed"},
		"no digest": {noDigest: map[string]bool{"runtime": true}, want: "could not resolve the digest"},
	}
	for name, tc := range cases {
		for _, parallel := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/parallel=%v", name, parallel), func(t *testing.T) {
				rec := recordExternalBuilds(t, tc.failing, tc.noDigest, nil)
				services := []WorkloadEntity{
					builtFrom("app", "registry.example/app", map[string]string{"BASE": "runtime"}),
					builtFrom("runtime", "registry.example/runtime", nil),
				}
				got := resultsByWorkload(buildExternalServices(context.Background(), services, buildOptions{env: "prod", parallel: parallel}, "v1", t.TempDir()))
				if err := got["app"].err; err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("app err = %v, want %q", err, tc.want)
				}
				for _, s := range rec.started {
					if s == "app" {
						t.Fatal("dependent ran although its parent produced no image")
					}
				}
			})
		}
	}
}

// Edges that cannot be scheduled are refused before ANY build runs.
func TestBuildExternalBuiltImagesUnschedulableRefusedBeforeBuilding(t *testing.T) {
	cases := map[string]struct {
		services []WorkloadEntity
		want     string
	}{
		"cycle": {
			services: []WorkloadEntity{
				builtFrom("a", "registry.example/a", map[string]string{"B": "b"}),
				builtFrom("b", "registry.example/b", map[string]string{"A": "a"}),
				builtFrom("c", "registry.example/c", nil),
			},
			want: "form a cycle among a, b",
		},
		"not produced": {
			services: []WorkloadEntity{builtFrom("app", "registry.example/app", map[string]string{"BASE": "runtime"})},
			want:     `names "runtime", which this build does not produce`,
		},
		"itself": {
			services: []WorkloadEntity{builtFrom("app", "registry.example/app", map[string]string{"BASE": "app"})},
			want:     "cannot be built FROM itself",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := recordExternalBuilds(t, nil, nil, nil)
			results := buildExternalServices(context.Background(), tc.services, buildOptions{env: "prod", parallel: true}, "v1", t.TempDir())
			if len(results) != len(tc.services) {
				t.Fatalf("results = %d, want one per service", len(results))
			}
			for _, r := range results {
				if r.err == nil || !strings.Contains(r.err.Error(), tc.want) {
					t.Fatalf("%s err = %v, want %q", r.name, r.err, tc.want)
				}
			}
			if rec.builds.Load() != 0 {
				t.Fatalf("%d builds ran before the refusal", rec.builds.Load())
			}
		})
	}
}

// One image built FROM two different parents is two recipes, never one shared
// artifact that would silently pick one parent.
func TestBuildExternalBuiltImagesArePartOfArtifactIdentity(t *testing.T) {
	a := builtFrom("a", "registry.example/shared", map[string]string{"BASE": "r1"})
	b := builtFrom("b", "registry.example/shared", map[string]string{"BASE": "r2"})
	b.Build.Shell.Cmd = a.Build.Shell.Cmd
	if _, err := externalBuildArtifacts([]WorkloadEntity{a, b}, buildOptions{env: "prod"}, "v1", t.TempDir()); err == nil || !strings.Contains(err.Error(), "conflicting ShellBuild inputs") {
		t.Fatalf("err = %v, want a conflict", err)
	}
	b.Build.Shell.BuiltImages = map[string]string{"BASE": "r1"}
	if artifacts, err := externalBuildArtifacts([]WorkloadEntity{a, b}, buildOptions{env: "prod"}, "v1", t.TempDir()); err != nil || len(artifacts) != 1 {
		t.Fatalf("identical recipes did not share one artifact: %d, %v", len(artifacts), err)
	}
}

// Naming one workload builds the images it is built FROM too, transitively; a
// composition's explicit selection is left alone (its parents are carried).
func TestNarrowBuildEntitiesKeepsBuiltImageParents(t *testing.T) {
	entities := &KCLEntities{Workloads: []WorkloadEntity{
		builtFrom("toolchain", "registry.example/toolchain", nil),
		builtFrom("runtime", "registry.example/runtime", map[string]string{"TOOLCHAIN": "toolchain"}),
		builtFrom("app", "registry.example/app", map[string]string{"BASE": "runtime"}),
		builtFrom("unrelated", "registry.example/unrelated", nil),
	}}
	names := func(e *KCLEntities) string {
		var out []string
		for _, w := range e.Workloads {
			out = append(out, w.Name)
		}
		return strings.Join(out, ",")
	}
	if got := names(narrowBuildEntities("demo", entities, buildOptions{buildTarget: "app"})); got != "toolchain,runtime,app" {
		t.Fatalf("--target app built %q", got)
	}
	if got := names(narrowBuildEntities("demo", entities, buildOptions{targets: []string{"runtime"}})); got != "toolchain,runtime" {
		t.Fatalf("targets [runtime] built %q", got)
	}
	if got := names(narrowBuildEntities("demo", entities, buildOptions{buildTarget: "unrelated"})); got != "unrelated" {
		t.Fatalf("--target unrelated built %q", got)
	}
	composed := buildOptions{targets: []string{"app"}, composition: &compositionRun{}}
	if got := names(narrowBuildEntities("demo", entities, composed)); got != "app" {
		t.Fatalf("composition selection widened to %q", got)
	}
}

// In a composition, a parent that is not selected is CARRIED: the dependent is
// built FROM the image the base release binds for it, not refused and not
// rebuilt.
func TestBuildExternalBuiltImagesCarriedParentFromCompositionBase(t *testing.T) {
	rec := recordExternalBuilds(t, nil, nil, nil)
	base := &release.Snapshot{
		Bindings: []release.ConsumerBinding{{Consumer: release.ConsumerKey{Cluster: "build-only", Workload: "runtime"}, Slot: "build-only", Artifact: "rt"}},
		Artifacts: map[release.ArtifactID]release.SnapshotArtifact{
			"rt": {Kind: release.KindOCI, Location: "registry.example/runtime", Content: carriedDigest},
		},
	}
	opts := buildOptions{env: "prod", parallel: true, composition: &compositionRun{BaseSnapshot: base}}
	services := []WorkloadEntity{builtFrom("app", "registry.example/app", map[string]string{"BASE": "runtime"})}
	for _, r := range buildExternalServices(context.Background(), services, opts, "v1", t.TempDir()) {
		if r.err != nil && !strings.Contains(r.err.Error(), "receipt") {
			t.Fatalf("%s: %v", r.name, r.err)
		}
	}
	if got := rec.env["app"]["BASE"]; got != "registry.example/runtime@"+carriedDigest {
		t.Fatalf("app BASE = %q, want the base's carried runtime", got)
	}
}

// --plan runs the same schedule: parents first, each edge named, and an edge
// the build would refuse fails the plan before anything is built.
func TestPlanExternalBuildsShowsBuiltImages(t *testing.T) {
	project := t.TempDir()
	services := []WorkloadEntity{
		builtFrom("app", "registry.example/app", map[string]string{"BASE": "runtime"}),
		builtFrom("runtime", "registry.example/runtime", nil),
	}
	steps := planExternalBuilds(planInputs{entities: &KCLEntities{Workloads: services}, resolvedTag: "v1", projectDir: project})
	if len(steps) != 2 || steps[0].name != "runtime" || steps[1].name != "app" {
		t.Fatalf("plan order = %+v, want runtime before app", steps)
	}
	if !strings.Contains(steps[1].what, "built FROM after runtime (BASE)") || steps[1].problem != "" {
		t.Fatalf("app step = %+v", steps[1])
	}
	cyclic := []WorkloadEntity{
		builtFrom("a", "registry.example/a", map[string]string{"B": "b"}),
		builtFrom("b", "registry.example/b", map[string]string{"A": "a"}),
	}
	steps = planExternalBuilds(planInputs{entities: &KCLEntities{Workloads: cyclic}, resolvedTag: "v1", projectDir: project})
	if len(steps) != 1 || !strings.Contains(steps[0].problem, "cycle") {
		t.Fatalf("cyclic plan = %+v, want one refusal", steps)
	}
}

func TestDispatchBuildDecodesBuiltImages(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"type": "shell", "cmd": "make", "built_images": map[string]string{"BASE": "runtime"}})
	b, err := dispatchBuild("app", raw)
	if err != nil {
		t.Fatal(err)
	}
	w := WorkloadEntity{Build: b}
	if got := w.EffectiveBuiltImages()["BASE"]; got != "runtime" {
		t.Fatalf("built_images not decoded: %+v", w.EffectiveBuiltImages())
	}
}
