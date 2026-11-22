package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// fakeReleaseRegistry is a registry that serves manifests by digest and tags
// pointing at them. Every push of the fake build makes a NEW digest: the build
// is not reproducible, which is the case reuse exists for.
type fakeReleaseRegistry struct {
	mu        sync.Mutex
	manifests map[string]bool   // <repo>@<digest>
	tags      map[string]string // <repo>:<tag> → digest
	pushes    int
	retags    []string
	retagErr  error
}

func (r *fakeReleaseRegistry) push(repo, tag string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pushes++
	d := "sha256:" + strings.Repeat(fmt.Sprintf("%x", r.pushes%16), 64)
	r.manifests[repo+"@"+d] = true
	r.tags[repo+":"+tag] = d
}

func (r *fakeReleaseRegistry) resolve(_ context.Context, ref string) (string, []string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i := strings.Index(ref, "@"); i >= 0 {
		if r.manifests[ref] {
			return ref[i+1:], []string{"linux/amd64"}, nil
		}
		return "", nil, errors.New("manifest unknown")
	}
	if d, ok := r.tags[ref]; ok && r.manifests[ref[:strings.LastIndex(ref, ":")]+"@"+d] {
		return d, []string{"linux/amd64"}, nil
	}
	return "", nil, errors.New("manifest unknown")
}

func (r *fakeReleaseRegistry) retag(_ context.Context, src, dst string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.retags = append(r.retags, dst+"="+src)
	if r.retagErr != nil {
		return r.retagErr
	}
	r.tags[dst] = src[strings.Index(src, "@")+1:]
	return nil
}

func (r *fakeReleaseRegistry) forget(repo, digest string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.manifests, repo+"@"+digest)
}

// releaseFixture cuts releases of ShellBuilds against a fake shell runner, a
// fake registry and an in-memory ledger: each cut harvests the build state
// exactly as `forge env build --release` does and records it newest first.
type releaseFixture struct {
	t       *testing.T
	projDir string
	h       *inputsHarness
	reg     *fakeReleaseRegistry
	ledger  []release.Release
}

func newReleaseFixture(t *testing.T, names ...string) *releaseFixture {
	t.Helper()
	f := &releaseFixture{t: t, projDir: filepath.Join(t.TempDir(), "proj"), h: newInputsHarness(t, names...),
		reg: &fakeReleaseRegistry{manifests: map[string]bool{}, tags: map[string]string{}}}
	writeInput(t, f.projDir, "src/main.go", "package main")
	writeInput(t, f.projDir, "src/README.md", "docs")
	oldRetag, oldCandidates := externalImageRetag, releaseReuseCandidatesFn
	t.Cleanup(func() { externalImageRetag, releaseReuseCandidatesFn = oldRetag, oldCandidates })
	run := externalShellBuild
	externalShellBuild = func(ctx context.Context, spec buildtarget.Spec) buildtarget.BuildResult {
		res := run(ctx, spec)
		if res.Err == nil {
			f.reg.push(imageRepository(spec.Image), spec.Tag)
		}
		return res
	}
	externalImageDigestResolver = f.reg.resolve
	externalImageRetag = f.reg.retag
	releaseReuseCandidatesFn = func(context.Context, string, string) ([]release.Release, error) {
		return append([]release.Release(nil), f.ledger...), nil
	}
	return f
}

// svc is the api ShellBuild as a release's render produces it: the release
// tag composed into the command (`TAG=${tag}`), plus the template the
// sentinel render produces for it (renderReleaseTemplates).
func (f *releaseFixture) svc(version string, inputs ...string) WorkloadEntity {
	return templated(func(tag string) WorkloadEntity {
		w := inputsSvc("api", inputs...)
		w.Build.Shell.Cmd = "TAG=" + tag + " ./scripts/build.sh"
		return w
	}, version)
}

// templated renders a workload for tag, with the template the release's
// sentinel render would attach: the same workload rendered for the sentinel.
func templated(render func(tag string) WorkloadEntity, tag string) WorkloadEntity {
	ents := &KCLEntities{Workloads: []WorkloadEntity{render(tag)}}
	attachReleaseTemplates(ents, &KCLEntities{Workloads: []WorkloadEntity{render(releaseTagSentinel)}})
	return ents.Workloads[0]
}

// cut builds version and records the release the build state harvests.
func (f *releaseFixture) cut(version string, svc WorkloadEntity, opts buildOptions) buildResult {
	f.t.Helper()
	opts.env, opts.release = "prod", version
	r := f.h.build(f.t, f.projDir, opts, svc)["api"]
	if r.err != nil {
		f.t.Fatalf("cut %s: %v", version, r.err)
	}
	rel := release.Release{Version: version, Artifacts: harvestReleaseArtifacts(f.projDir, "prod")}
	if err := rel.Validate(); err != nil {
		f.t.Fatalf("cut %s harvested an invalid release: %v", version, err)
	}
	f.ledger = append([]release.Release{rel}, f.ledger...)
	return r
}

func (f *releaseFixture) runs() int32 { return f.h.runs["api"].Load() }

func (f *releaseFixture) artifact(version string) release.Artifact {
	for _, r := range f.ledger {
		if r.Version == version {
			return r.Artifacts["reg.example/api"]
		}
	}
	f.t.Fatalf("no release %s", version)
	return release.Artifact{}
}

func TestReleaseBuild_ReusesThePreviousReleasesImageWhenInputsAreUnchanged(t *testing.T) {
	f := newReleaseFixture(t, "api")

	// The first release has nothing to compare with: it runs and records
	// what the next one compares against.
	r1 := f.cut("v1", f.svc("v1", "**/*.go"), buildOptions{})
	if r1.upToDate || f.runs() != 1 {
		t.Fatalf("first release: upToDate=%v runs=%d, want a real run", r1.upToDate, f.runs())
	}
	v1 := f.artifact("v1")
	if !release.ValidDigest(v1.InputsDigest) || v1.CarriedFrom != "" {
		t.Fatalf("first release's artifact = %+v, want an inputs digest and no carry", v1)
	}

	// Unchanged inputs, a new tag in the command: no build, the same
	// digest, and the new release's tag points at it.
	r2 := f.cut("v2", f.svc("v2", "**/*.go"), buildOptions{})
	if !r2.upToDate || f.runs() != 1 {
		t.Fatalf("unchanged release: upToDate=%v runs=%d, want the image reused and no build", r2.upToDate, f.runs())
	}
	v1Digest := v1.Digests[release.SharedVariant]
	if r2.digest != v1Digest || r2.repository != "reg.example/api" {
		t.Fatalf("reused result = %s@%s, want reg.example/api@%s", r2.repository, r2.digest, v1Digest)
	}
	if got, _, err := f.reg.resolve(context.Background(), "reg.example/api:v2"); err != nil || got != v1Digest {
		t.Fatalf("reg.example/api:v2 = %s (%v), want it tagged onto %s", got, err, v1Digest)
	}
	v2 := f.artifact("v2")
	if v2.Digests[release.SharedVariant] != v1Digest || v2.CarriedFrom != "v1" || v2.InputsDigest != v1.InputsDigest {
		t.Fatalf("second release's artifact = %+v, want v1's digest carried from v1 with the same inputs digest", v2)
	}
	st, err := buildtarget.ReadState(f.projDir, "prod", "api")
	if err != nil || st == nil || st.Tag != "v2" || st.Digest != v1Digest || st.CarriedFrom != "v1" {
		t.Fatalf("build state after a reuse = %+v (%v), want tag v2 on v1's digest", st, err)
	}

	// An undeclared file is not an input.
	writeInput(t, f.projDir, "src/README.md", "edited docs")
	if r := f.cut("v3", f.svc("v3", "**/*.go"), buildOptions{}); !r.upToDate || f.runs() != 1 {
		t.Fatalf("an undeclared edit rebuilt (runs=%d)", f.runs())
	}
}

func TestReleaseBuild_RebuildsWhenItCannotProveReuse(t *testing.T) {
	cases := []struct {
		name string
		// prepare runs after a first release v1 is cut and returns the
		// workload v2 builds.
		prepare func(f *releaseFixture) WorkloadEntity
		opts    buildOptions
		// unfingerprinted: the release cannot record an inputs digest.
		unfingerprinted bool
	}{
		{name: "a declared input changed", prepare: func(f *releaseFixture) WorkloadEntity {
			writeInput(f.t, f.projDir, "src/main.go", "package main // edited")
			return f.svc("v2", "**/*.go")
		}},
		{name: "the command changed", prepare: func(f *releaseFixture) WorkloadEntity {
			return templated(func(tag string) WorkloadEntity {
				w := f.svc(tag, "**/*.go")
				w.Build.Shell.Cmd += " --no-cache"
				return w
			}, "v2")
		}},
		{name: "the env changed", prepare: func(f *releaseFixture) WorkloadEntity {
			return templated(func(tag string) WorkloadEntity {
				w := f.svc(tag, "**/*.go")
				w.Build.Shell.Env = map[string]string{"MODE": "debug"}
				return w
			}, "v2")
		}},
		{name: "the template render is missing", unfingerprinted: true, prepare: func(f *releaseFixture) WorkloadEntity {
			w := f.svc("v2", "**/*.go")
			w.releaseTemplate = nil
			return w
		}},
		{name: "the registry no longer has the digest", prepare: func(f *releaseFixture) WorkloadEntity {
			f.reg.forget("reg.example/api", f.artifact("v1").Digests[release.SharedVariant])
			return f.svc("v2", "**/*.go")
		}},
		{name: "the release tag cannot be pointed at it", prepare: func(f *releaseFixture) WorkloadEntity {
			f.reg.retagErr = errors.New("denied")
			return f.svc("v2", "**/*.go")
		}},
		{name: "the previous release recorded no inputs digest", prepare: func(f *releaseFixture) WorkloadEntity {
			old := f.ledger[0].Artifacts["reg.example/api"]
			old.InputsDigest = ""
			f.ledger[0].Artifacts["reg.example/api"] = old
			return f.svc("v2", "**/*.go")
		}},
		{name: "no previous release", prepare: func(f *releaseFixture) WorkloadEntity {
			f.ledger = nil
			return f.svc("v2", "**/*.go")
		}},
		{name: "--rebuild", opts: buildOptions{rebuild: true}, prepare: func(f *releaseFixture) WorkloadEntity {
			return f.svc("v2", "**/*.go")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newReleaseFixture(t, "api")
			v1 := f.cut("v1", f.svc("v1", "**/*.go"), buildOptions{})
			r := f.cut("v2", tc.prepare(f), tc.opts)
			if r.upToDate || f.runs() != 2 {
				t.Fatalf("upToDate=%v runs=%d, want the command run", r.upToDate, f.runs())
			}
			if r.digest == "" || r.digest == v1.digest {
				t.Fatalf("rebuilt digest = %q, want the new build's (not v1's %s)", r.digest, v1.digest)
			}
			if v2 := f.artifact("v2"); v2.CarriedFrom != "" || release.ValidDigest(v2.InputsDigest) == tc.unfingerprinted {
				t.Fatalf("rebuilt release's artifact = %+v, want no carry, and an inputs digest unless it could not be fingerprinted", v2)
			}
		})
	}
}

// A step with no inputs behaves as it always has: every release runs it, and
// its release records no inputs digest.
func TestReleaseBuild_NoDeclaredInputsAlwaysRuns(t *testing.T) {
	f := newReleaseFixture(t, "api")
	f.cut("v1", f.svc("v1"), buildOptions{})
	r := f.cut("v2", f.svc("v2"), buildOptions{})
	if r.upToDate || f.runs() != 2 {
		t.Fatalf("upToDate=%v runs=%d, want every release to run a step without inputs", r.upToDate, f.runs())
	}
	if a := f.artifact("v2"); a.InputsDigest != "" || a.CarriedFrom != "" {
		t.Fatalf("artifact = %+v, want no inputs evidence", a)
	}
	if len(f.reg.retags) != 0 {
		t.Fatalf("retagged %v for a step without inputs", f.reg.retags)
	}
}

// The release fingerprint leaves out exactly what the KCL composed from the
// release tag, and the checkout's location only through the cwd forge
// resolved. A literal that equals the tag, or a path, is part of it.
func TestReleaseInputsFingerprint_IsStructural(t *testing.T) {
	type cut struct {
		projDir, tag string
		// render is the workload as the KCL composes it for a tag.
		render func(tag string) WorkloadEntity
		// overlay is the built_images env the build adds.
		overlay map[string]string
	}
	fingerprint := func(c cut) string {
		t.Helper()
		writeInput(t, c.projDir, "src/main.go", "package main")
		w := templated(c.render, c.tag)
		opts := buildOptions{env: "prod", release: c.tag}
		spec := externalBuildSpec(w, opts, c.tag, c.projDir)
		if len(c.overlay) > 0 {
			merged := map[string]string{}
			for k, v := range spec.BuildEnv {
				merged[k] = v
			}
			for k, v := range c.overlay {
				merged[k] = v
			}
			spec.BuildEnv = merged
		}
		fp, err := releaseInputsFingerprint(w, spec, filepath.Join(c.projDir, "src"), c.projDir, opts)
		if err != nil {
			t.Fatal(err)
		}
		return fp.Digest
	}
	// literal is an argument the author wrote, not the tag the KCL composed.
	render := func(literal string) func(string) WorkloadEntity {
		return func(tag string) WorkloadEntity {
			w := inputsSvc("api", "**/*.go")
			w.Build.Shell.Cmd = "TAG=" + tag + " ./scripts/build.sh --base " + literal
			w.Build.Shell.Env = map[string]string{"VERSION_TAG": tag, "MODE": "release"}
			return w
		}
	}
	dir := func() string { return filepath.Join(t.TempDir(), "proj") }

	base := fingerprint(cut{projDir: dir(), tag: "v1.0.0", render: render("stable")})
	if other := fingerprint(cut{projDir: filepath.Join(t.TempDir(), "elsewhere", "proj"), tag: "v1.0.1", render: render("stable")}); other != base {
		t.Errorf("only the release tag and the checkout location changed, yet the fingerprint did: %s vs %s", base, other)
	}

	// A literal that happens to equal the tag is the author's: when it
	// changes, the fingerprint changes, though every value moved with the tag.
	if a, b := fingerprint(cut{projDir: dir(), tag: "v1.0.0", render: render("v1.0.0")}),
		fingerprint(cut{projDir: dir(), tag: "v1.0.1", render: render("v1.0.1")}); a == b {
		t.Error("a literal equal to the tag changed with it, but the fingerprint did not: a stale image would be reused")
	}

	// The same for a literal that happens to equal the checkout's path.
	p1, p2 := dir(), filepath.Join(t.TempDir(), "elsewhere", "proj")
	if a, b := fingerprint(cut{projDir: p1, tag: "v1.0.0", render: render(p1)}),
		fingerprint(cut{projDir: p2, tag: "v1.0.1", render: render(p2)}); a == b {
		t.Error("a path literal equal to the checkout changed with it, but the fingerprint did not")
	}

	// The effective env: a declared value and a built_images overlay value.
	envChanged := func(tag string) WorkloadEntity {
		w := render("stable")(tag)
		w.Build.Shell.Env["MODE"] = "debug"
		return w
	}
	if got := fingerprint(cut{projDir: dir(), tag: "v1.0.1", render: envChanged}); got == base {
		t.Error("a changed env value must change the fingerprint")
	}
	o1 := fingerprint(cut{projDir: dir(), tag: "v1.0.1", render: render("stable"), overlay: map[string]string{"BASE": "reg/base@" + pushedDigest}})
	o2 := fingerprint(cut{projDir: dir(), tag: "v1.0.2", render: render("stable"), overlay: map[string]string{"BASE": "reg/base@" + otherDigest}})
	if o1 == o2 || o1 == base {
		t.Error("a built_images overlay in the effective env must change the fingerprint")
	}

	// A command the template does not reproduce is refused, not guessed at.
	w := templated(render("stable"), "v1.0.0")
	w.releaseTemplate.Cmd = "something else"
	p := dir()
	writeInput(t, p, "src/main.go", "package main")
	opts := buildOptions{env: "prod", release: "v1.0.0"}
	if _, err := releaseInputsFingerprint(w, externalBuildSpec(w, opts, "v1.0.0", p), filepath.Join(p, "src"), p, opts); err == nil {
		t.Error("a command the template does not reproduce must not be fingerprinted")
	}
}

// A hosted ledger carries the inputs evidence on the wire, and a malformed
// value from a server never makes the release unreadable.
func TestHostedWire_CarriesInputsEvidence(t *testing.T) {
	d := "sha256:" + strings.Repeat("a", 64)
	r := release.Release{Version: "v2", Artifacts: map[string]release.Artifact{"reg.example/api": {
		Kind: release.KindOCI, Mode: release.ModeShared, Digests: map[string]string{release.SharedVariant: pushedDigest},
		InputsDigest: d, CarriedFrom: "v1",
	}}}
	rows := releaseToWire(r)
	back, err := releaseFromWire(wireRelease{Version: "v2", Artifacts: rows})
	if err != nil || back.Artifacts["reg.example/api"].InputsDigest != d || back.Artifacts["reg.example/api"].CarriedFrom != "v1" {
		t.Fatalf("round trip = %+v (%v), want the inputs evidence kept", back.Artifacts, err)
	}
	rows[0].InputsDigest = "not-a-digest"
	back, err = releaseFromWire(wireRelease{Version: "v2", Artifacts: rows})
	if err != nil || back.Artifacts["reg.example/api"].InputsDigest != "" {
		t.Fatalf("malformed inputs digest = %+v (%v), want it dropped and the release readable", back.Artifacts, err)
	}
}
