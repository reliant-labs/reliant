package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/pkg/release"
)

// dockerReleaseFixture cuts releases of one DockerBuild workload (api →
// reg.example/api) through buildKCLDockerShell against a fake docker, a fake
// registry and an in-memory ledger: each cut persists and harvests the build
// state exactly as `forge env build --release --push` does.
type dockerReleaseFixture struct {
	t       *testing.T
	projDir string
	reg     *fakeReleaseRegistry
	ledger  []release.Release
	runs    int
}

func newDockerReleaseFixture(t *testing.T) *dockerReleaseFixture {
	t.Helper()
	f := &dockerReleaseFixture{t: t, projDir: t.TempDir(),
		reg: &fakeReleaseRegistry{manifests: map[string]bool{}, tags: map[string]string{}}}
	writeInput(t, f.projDir, "forge.yaml", "name: shop\n")
	writeInput(t, f.projDir, "Dockerfile", "FROM scratch\nCOPY src /src\n")
	writeInput(t, f.projDir, "src/main.go", "package main")
	writeInput(t, f.projDir, "src/README.md", "docs")
	t.Chdir(f.projDir) // the build and the state writer resolve the project from the cwd
	oldBuilder, oldResolve, oldRetag, oldCandidates := serviceDockerBuilder, externalImageDigestResolver, externalImageRetag, releaseReuseCandidatesFn
	t.Cleanup(func() {
		serviceDockerBuilder, externalImageDigestResolver, externalImageRetag, releaseReuseCandidatesFn = oldBuilder, oldResolve, oldRetag, oldCandidates
	})
	// Every build pushes a NEW digest: the Dockerfile is not reproducible,
	// which is the case reuse exists for.
	serviceDockerBuilder = func(ctx context.Context, _ *config.ProjectConfig, svcName, repository string, d *DockerBuild, _ buildOptions, _, tag string) buildResult {
		f.runs++
		f.reg.push(repository, tag)
		digest, platforms, err := f.reg.resolve(ctx, repository+":"+tag)
		if err != nil {
			t.Fatalf("fake push: %v", err)
		}
		return buildResult{name: svcName + " (docker)", kind: "docker", image: svcName, repository: repository, tag: tag,
			digest: digest, platforms: platforms, source: d.sourceForRecord()}
	}
	externalImageDigestResolver = f.reg.resolve
	externalImageRetag = f.reg.retag
	releaseReuseCandidatesFn = func(context.Context, string, string) ([]release.Release, error) {
		return append([]release.Release(nil), f.ledger...), nil
	}
	return f
}

// workload is api's DockerBuild declaring inputs, after mutate.
func (f *dockerReleaseFixture) workload(mutate func(d *DockerBuild)) WorkloadEntity {
	d := &DockerBuild{Dockerfile: "Dockerfile", BuildArgs: map[string]string{"MODE": "prod"}, Inputs: []string{"src/**/*.go"}}
	if mutate != nil {
		mutate(d)
	}
	return WorkloadEntity{Name: "api", Image: "reg.example/api", Build: BuildConfigEntity{Type: "docker", Docker: d}}
}

// cut builds version and records the release the build state harvests.
func (f *dockerReleaseFixture) cut(version string, w WorkloadEntity, opts buildOptions) buildResult {
	f.t.Helper()
	opts.env, opts.release = "prod", version
	opts.pushPlan.push = true
	results := buildKCLDockerShell(context.Background(), &config.ProjectConfig{Name: "shop"},
		&KCLEntities{Workloads: []WorkloadEntity{w}}, opts, "", version)
	if len(results) != 1 || results[0].err != nil {
		f.t.Fatalf("cut %s: %+v", version, results)
	}
	persistImageBuildStates(opts, results)
	rel := release.Release{Version: version, Artifacts: harvestReleaseArtifacts(f.projDir, "prod")}
	if err := rel.Validate(); err != nil {
		f.t.Fatalf("cut %s harvested an invalid release: %v", version, err)
	}
	f.ledger = append([]release.Release{rel}, f.ledger...)
	return results[0]
}

func (f *dockerReleaseFixture) artifact(version string) release.Artifact {
	f.t.Helper()
	for _, r := range f.ledger {
		if r.Version == version {
			return r.Artifacts["reg.example/api"]
		}
	}
	f.t.Fatalf("no release %s", version)
	return release.Artifact{}
}

// A DockerBuild that declares inputs reuses the previous release's image when
// they — and the Dockerfile and the declaration — are unchanged, exactly as a
// ShellBuild does (report e9e441ad: it rebuilt on every cut).
func TestDockerReleaseBuild_ReusesThePreviousReleasesImageWhenInputsAreUnchanged(t *testing.T) {
	f := newDockerReleaseFixture(t)

	r1 := f.cut("v1", f.workload(nil), buildOptions{})
	if r1.upToDate || f.runs != 1 {
		t.Fatalf("first release: upToDate=%v runs=%d, want a real build", r1.upToDate, f.runs)
	}
	v1 := f.artifact("v1")
	if !release.ValidDigest(v1.InputsDigest) || v1.CarriedFrom != "" {
		t.Fatalf("first release's artifact = %+v, want an inputs digest and no carry", v1)
	}

	// Unchanged: no docker build, v1's digest, and the v2 tag points at it.
	r2 := f.cut("v2", f.workload(nil), buildOptions{})
	v1Digest := v1.Digests[release.SharedVariant]
	if !r2.upToDate || f.runs != 1 || r2.digest != v1Digest || r2.repository != "reg.example/api" || r2.tag != "v2" {
		t.Fatalf("unchanged release: %+v runs=%d, want v1's image reused as reg.example/api:v2 without a build", r2, f.runs)
	}
	if got, _, err := f.reg.resolve(context.Background(), "reg.example/api:v2"); err != nil || got != v1Digest {
		t.Fatalf("reg.example/api:v2 = %s (%v), want it tagged onto %s", got, err, v1Digest)
	}
	if v2 := f.artifact("v2"); v2.Digests[release.SharedVariant] != v1Digest || v2.CarriedFrom != "v1" || v2.InputsDigest != v1.InputsDigest {
		t.Fatalf("second release's artifact = %+v, want v1's digest carried from v1 with the same inputs digest", v2)
	}
	if st, err := buildtarget.ReadState(f.projDir, "prod", "api"); err != nil || st == nil || st.Tag != "v2" || st.Digest != v1Digest || st.CarriedFrom != "v1" {
		t.Fatalf("build state after a reuse = %+v (%v), want tag v2 on v1's digest carried from v1", st, err)
	}

	// An undeclared file is not an input.
	writeInput(t, f.projDir, "src/README.md", "edited docs")
	if r := f.cut("v3", f.workload(nil), buildOptions{}); !r.upToDate || f.runs != 1 {
		t.Fatalf("an undeclared edit rebuilt (runs=%d)", f.runs)
	}

	// Each of these must build. Every case is compared with the release
	// before it, so each one proves a single change rebuilds.
	steps := []struct {
		name    string
		prepare func() WorkloadEntity
		opts    buildOptions
	}{
		{"a declared input changed", func() WorkloadEntity {
			writeInput(t, f.projDir, "src/main.go", "package main // edited")
			return f.workload(nil)
		}, buildOptions{}},
		{"the Dockerfile changed, though no input names it", func() WorkloadEntity {
			writeInput(t, f.projDir, "Dockerfile", "FROM scratch\nCOPY src /app\n")
			return f.workload(nil)
		}, buildOptions{}},
		{"a build arg changed", func() WorkloadEntity {
			return f.workload(func(d *DockerBuild) { d.BuildArgs["MODE"] = "debug" })
		}, buildOptions{}},
		{"the target changed", func() WorkloadEntity {
			return f.workload(func(d *DockerBuild) { d.BuildArgs["MODE"] = "debug"; d.Target = "runtime" })
		}, buildOptions{}},
		{"the platform changed", func() WorkloadEntity {
			return f.workload(func(d *DockerBuild) { d.BuildArgs["MODE"] = "debug"; d.Target = "runtime"; d.Platform = "linux/arm64" })
		}, buildOptions{}},
		{"--rebuild", func() WorkloadEntity {
			return f.workload(func(d *DockerBuild) { d.BuildArgs["MODE"] = "debug"; d.Target = "runtime"; d.Platform = "linux/arm64" })
		}, buildOptions{rebuild: true}},
	}
	for i, s := range steps {
		before := f.runs
		version := "v" + string(rune('4'+i))
		if r := f.cut(version, s.prepare(), s.opts); r.upToDate || f.runs != before+1 {
			t.Fatalf("%s: upToDate=%v runs=%d→%d, want a build", s.name, r.upToDate, before, f.runs)
		}
		if a := f.artifact(version); !release.ValidDigest(a.InputsDigest) || a.CarriedFrom != "" {
			t.Fatalf("%s: artifact = %+v, want a fresh inputs digest for the next release", s.name, a)
		}
	}

	// An image the registry no longer serves is never reused (v8 and the
	// forced v9 both recorded these inputs).
	for _, v := range []string{"v8", "v9"} {
		f.reg.forget("reg.example/api", f.artifact(v).Digests[release.SharedVariant])
	}
	before := f.runs
	w := f.workload(func(d *DockerBuild) { d.BuildArgs["MODE"] = "debug"; d.Target = "runtime"; d.Platform = "linux/arm64" })
	if r := f.cut("v10", w, buildOptions{}); r.upToDate || f.runs != before+1 {
		t.Fatalf("a digest gone from the registry was reused (runs=%d→%d)", before, f.runs)
	}
}

// A DockerBuild with no inputs builds on every release, as before.
func TestDockerReleaseBuild_NoDeclaredInputsAlwaysBuilds(t *testing.T) {
	f := newDockerReleaseFixture(t)
	noInputs := f.workload(func(d *DockerBuild) { d.Inputs = nil })
	for _, v := range []string{"v1", "v2"} {
		if r := f.cut(v, noInputs, buildOptions{}); r.upToDate {
			t.Fatalf("%s reused an image though the build declares no inputs", v)
		}
		if a := f.artifact(v); a.InputsDigest != "" {
			t.Fatalf("%s recorded an inputs digest %q for a build that declares no inputs", v, a.InputsDigest)
		}
	}
	if f.runs != 2 {
		t.Fatalf("runs = %d, want every release to build", f.runs)
	}
}

// Under a GitSource context the inputs are read inside the resolved checkout
// (where the Dockerfile is), never the project's own tree.
func TestDockerReleaseBuild_GitSourceContextInputsResolveInTheCheckout(t *testing.T) {
	f := newDockerReleaseFixture(t)
	checkout := filepath.Join(t.TempDir(), "billing")
	writeInput(t, checkout, "Dockerfile", "FROM python\nCOPY . /app\n")
	writeInput(t, checkout, "app.py", "print('v1')")
	writeInput(t, f.projDir, "app.py", "the project's own file of the same name")
	writeOverrides(t, f.projDir, map[string]string{"github.com/acme/billing": checkout})
	prev := captureDockerSource
	captureDockerSource = func(context.Context, string, string, string) *buildtarget.Source { return nil }
	t.Cleanup(func() { captureDockerSource = prev })
	pinned := func() WorkloadEntity {
		w := f.workload(func(d *DockerBuild) {
			d.ContextSource = &config.GitSource{Repo: "github.com/acme/billing", Ref: "v2.3.1"}
			d.Inputs = []string{"**/*.py"}
		})
		if err := resolveDockerBuildSources(context.Background(), f.projDir, w.Name, w.Build.Docker); err != nil {
			t.Fatal(err)
		}
		if got := serviceDockerfile(w.Build.Docker); got != filepath.Join(checkout, "Dockerfile") {
			t.Fatalf("Docker would build %q, want the checkout Dockerfile", got)
		}
		return w
	}
	f.cut("v1", pinned(), buildOptions{})
	writeInput(t, f.projDir, "app.py", "edited in the project, not the checkout")
	writeInput(t, f.projDir, "Dockerfile", "FROM scratch\nLABEL unused=changed\n")
	if r := f.cut("v2", pinned(), buildOptions{}); !r.upToDate || f.runs != 1 {
		t.Fatalf("an edit outside the checkout rebuilt (runs=%d)", f.runs)
	}
	writeInput(t, checkout, "app.py", "print('v2')")
	if r := f.cut("v3", pinned(), buildOptions{}); r.upToDate || f.runs != 2 {
		t.Fatalf("an edit inside the checkout was reused (runs=%d)", f.runs)
	}
	writeInput(t, checkout, "Dockerfile", "FROM python\nCOPY . /new-app\n")
	if r := f.cut("v4", pinned(), buildOptions{}); r.upToDate || f.runs != 3 {
		t.Fatalf("the resolved Dockerfile changed without a rebuild (runs=%d)", f.runs)
	}
	// The pin is part of the declaration: another ref is another image.
	moved := pinned()
	moved.Build.Docker.ContextSource = &config.GitSource{Repo: "github.com/acme/billing", Ref: "v2.4.0"}
	if r := f.cut("v5", moved, buildOptions{}); r.upToDate || f.runs != 4 {
		t.Fatalf("a moved GitSource ref was reused (runs=%d)", f.runs)
	}
}

// The release tag is not part of a DockerBuild's release fingerprint (forge
// adds -t itself), but every declared field docker reads is.
func TestDockerReleaseInputsFingerprint_KeysTheDeclarationNotTheTag(t *testing.T) {
	projDir := t.TempDir()
	writeInput(t, projDir, "Dockerfile", "FROM scratch\n")
	writeInput(t, projDir, "src/main.go", "package main")
	writeInput(t, projDir, "inputs.json", `{"recipe":"image"}`)
	cfg := &config.ProjectConfig{Name: "shop"}
	base := func() WorkloadEntity {
		return WorkloadEntity{Name: "api", Image: "reg.example/api", Build: BuildConfigEntity{Type: "docker",
			Docker: &DockerBuild{Dockerfile: "Dockerfile", Inputs: []string{"src/**"}}}}
	}
	digest := func(w WorkloadEntity, opts buildOptions, repo string) string {
		t.Helper()
		fp, err := dockerReleaseInputsFingerprint(cfg, w, repo, opts, "", projDir)
		if err != nil {
			t.Fatalf("fingerprint: %v", err)
		}
		return fp.Digest
	}
	want := digest(base(), buildOptions{release: "v1"}, "reg.example/api")
	if got := digest(base(), buildOptions{release: "v2"}, "reg.example/api"); got != want {
		t.Fatalf("the release tag changed the fingerprint: %s vs %s", got, want)
	}
	changes := map[string]func(w *WorkloadEntity, opts *buildOptions, repo *string){
		"repository": func(_ *WorkloadEntity, _ *buildOptions, repo *string) { *repo = "reg.example/other" },
		"context":    func(w *WorkloadEntity, _ *buildOptions, _ *string) { w.Build.Docker.Context = "src" },
		"build_contexts": func(w *WorkloadEntity, _ *buildOptions, _ *string) {
			w.Build.Docker.BuildContexts = map[string]config.BuildContext{"shared": {Path: "../shared"}}
		},
		"pgo": func(w *WorkloadEntity, _ *buildOptions, _ *string) {
			w.Build.Docker.PGO = map[string]PGOConfig{"./cmd/api": {}}
		},
		"output_name": func(w *WorkloadEntity, _ *buildOptions, _ *string) { w.Build.Docker.OutputName = "api-bin" },
		"patterns": func(w *WorkloadEntity, _ *buildOptions, _ *string) {
			w.Build.Docker.Inputs = []string{"src/**", "!src/x"}
		},
		"unit":        func(w *WorkloadEntity, _ *buildOptions, _ *string) { w.Build.Unit = "api-image" },
		"inputs_file": func(w *WorkloadEntity, _ *buildOptions, _ *string) { w.Build.InputsFile = "inputs.json" },
		"target arch": func(_ *WorkloadEntity, opts *buildOptions, _ *string) { opts.targetArch = "arm64" },
	}
	for name, change := range changes {
		w, opts, repo := base(), buildOptions{release: "v1"}, "reg.example/api"
		change(&w, &opts, &repo)
		if got := digest(w, opts, repo); got == want {
			t.Errorf("changing %s did not change the fingerprint", name)
		}
	}
	// forge.yaml's build contexts count when the build declares none.
	withProjectContexts := &config.ProjectConfig{Name: "shop"}
	withProjectContexts.Docker.BuildContexts = map[string]config.BuildContext{"shared": {Path: "../shared"}}
	fp, err := dockerReleaseInputsFingerprint(withProjectContexts, base(), "reg.example/api", buildOptions{release: "v1"}, "", projDir)
	if err != nil || fp.Digest == want {
		t.Fatalf("forge.yaml's build contexts did not change the fingerprint (%v)", err)
	}
}
