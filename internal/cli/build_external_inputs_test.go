package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/internal/config"
)

const (
	pushedDigest = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	otherDigest  = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
)

func TestShellBuildReusable(t *testing.T) {
	const fp = "sha256:aaaa"
	recorded := func(mut func(*buildtarget.State)) *buildtarget.State {
		st := &buildtarget.State{Service: "img", Image: "reg.example/img", Tag: "dev", Digest: pushedDigest, InputsFingerprint: fp}
		if mut != nil {
			mut(st)
		}
		return st
	}
	serves := func(digest string, err error) func(context.Context, string) (string, []string, error) {
		return func(_ context.Context, ref string) (string, []string, error) {
			if ref != "reg.example/img:dev" {
				t.Errorf("registry asked about %q, want the recorded ref", ref)
			}
			return digest, nil, err
		}
	}
	cases := []struct {
		name     string
		prev     *buildtarget.State
		fp       string
		noImage  bool
		registry func(context.Context, string) (string, []string, error)
		reuse    bool
		why      string
	}{
		{name: "match and the registry still serves the digest", prev: recorded(nil), fp: fp, registry: serves(pushedDigest, nil), reuse: true},
		{name: "no successful build recorded", prev: nil, fp: fp, why: "no successful build recorded"},
		{name: "recorded by a build without inputs", prev: recorded(func(s *buildtarget.State) { s.InputsFingerprint = "" }), fp: fp, why: "no inputs fingerprint"},
		{name: "inputs changed", prev: recorded(nil), fp: "sha256:bbbb", why: "inputs changed"},
		{name: "no image to verify", prev: recorded(nil), fp: fp, noImage: true, why: "declares no image"},
		{name: "a different destination", prev: recorded(func(s *buildtarget.State) { s.Tag = "old" }), fp: fp, why: "pushed reg.example/img:old"},
		{name: "digest never resolved", prev: recorded(func(s *buildtarget.State) { s.Digest = "" }), fp: fp, why: "never resolved"},
		{name: "registry unreachable or image gone", prev: recorded(nil), fp: fp, registry: serves("", errors.New("manifest unknown")), why: "manifest unknown"},
		{name: "the tag now names another image", prev: recorded(nil), fp: fp, registry: serves(otherDigest, nil), why: "not the " + pushedDigest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry := tc.registry
			if registry == nil {
				registry = func(context.Context, string) (string, []string, error) {
					t.Fatal("the registry was consulted although the recorded state already disagreed")
					return "", nil, nil
				}
			}
			reuse, why := shellBuildReusable(context.Background(), tc.prev, tc.fp, !tc.noImage, "reg.example/img", "dev", registry)
			if reuse != tc.reuse || !strings.Contains(why, tc.why) {
				t.Fatalf("shellBuildReusable = (%v, %q), want (%v, containing %q)", reuse, why, tc.reuse, tc.why)
			}
		})
	}
}

// inputsHarness replaces the shell runner and the registry with fakes and
// counts how often each workload's command ran.
type inputsHarness struct {
	runs map[string]*atomic.Int32
	fail atomic.Bool
	// served maps a repository to the digest the registry serves at any of
	// its tags; unset repositories serve pushedDigest.
	served sync.Map
}

// serve makes the registry answer digest for every tag of repo.
func (h *inputsHarness) serve(repo, digest string) { h.served.Store(repo, digest) }

func newInputsHarness(t *testing.T, names ...string) *inputsHarness {
	t.Helper()
	oldBuild, oldDigest, oldSource, oldStorage := externalShellBuild, externalImageDigestResolver, externalSourceCapture, checkBuildStorageFn
	t.Cleanup(func() {
		externalShellBuild, externalImageDigestResolver, externalSourceCapture, checkBuildStorageFn = oldBuild, oldDigest, oldSource, oldStorage
	})
	h := &inputsHarness{runs: map[string]*atomic.Int32{}}
	for _, n := range names {
		h.runs[n] = &atomic.Int32{}
	}
	checkBuildStorageFn = func(string) error { return nil }
	externalSourceCapture = func(context.Context, string, string, string) *buildtarget.Source { return nil }
	externalShellBuild = func(_ context.Context, spec buildtarget.Spec) buildtarget.BuildResult {
		h.runs[spec.Service].Add(1)
		res := buildtarget.BuildResult{Service: spec.Service, Tag: spec.Tag}
		if h.fail.Load() {
			res.Err = errors.New("mock build failed")
		}
		return res
	}
	externalImageDigestResolver = func(_ context.Context, ref string) (string, []string, error) {
		repo := ref[:strings.LastIndex(ref, ":")]
		if d, ok := h.served.Load(repo); ok {
			return d.(string), []string{"linux/arm64"}, nil
		}
		return pushedDigest, []string{"linux/arm64"}, nil
	}
	return h
}

func (h *inputsHarness) build(t *testing.T, projDir string, opts buildOptions, svcs ...WorkloadEntity) map[string]buildResult {
	t.Helper()
	if opts.env == "" {
		opts.env = "dev"
	}
	results := resultsByWorkload(buildExternalServices(context.Background(), svcs, opts, "dev", projDir))
	return results
}

func inputsSvc(name string, inputs ...string) WorkloadEntity {
	w := shellSvc(name, "reg.example/"+name, "make "+name, "src", map[string]string{"MODE": "release"})
	w.Build.Shell.Inputs = inputs
	return w
}

func writeInput(t *testing.T, projDir, rel, body string) {
	t.Helper()
	p := filepath.Join(projDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildExternalServices_SkipsUnchangedInputs(t *testing.T) {
	projDir := t.TempDir()
	writeInput(t, projDir, "src/main.go", "package main")
	writeInput(t, projDir, "src/README.md", "docs")
	h := newInputsHarness(t, "api")
	svc := inputsSvc("api", "**/*.go")
	runs := func() int32 { return h.runs["api"].Load() }

	// First build: nothing recorded, so the command runs and its
	// fingerprint is recorded with the digest it pushed.
	r := h.build(t, projDir, buildOptions{}, svc)["api"]
	if r.err != nil || r.upToDate || runs() != 1 {
		t.Fatalf("first build: err=%v upToDate=%v runs=%d, want a real run", r.err, r.upToDate, runs())
	}
	st, err := buildtarget.ReadState(projDir, "dev", "api")
	if err != nil || st == nil || !strings.HasPrefix(st.InputsFingerprint, "sha256:") || st.Digest != pushedDigest {
		t.Fatalf("state after the first build = %+v (%v), want a fingerprint and the pushed digest", st, err)
	}

	// Unchanged: skipped, and the result carries the recorded image so
	// deploy (and any build FROM it) pins the same digest.
	r = h.build(t, projDir, buildOptions{}, svc)["api"]
	if r.err != nil || !r.upToDate || runs() != 1 {
		t.Fatalf("unchanged rebuild: err=%v upToDate=%v runs=%d, want a skip", r.err, r.upToDate, runs())
	}
	if r.digest != pushedDigest || r.repository != "reg.example/api" {
		t.Fatalf("skipped result = %s@%s, want the recorded image", r.repository, r.digest)
	}
	if bs, err := ReadBuildState(projDir, "dev"); err != nil || bs == nil || bs.Digest != pushedDigest {
		t.Fatalf("deploy build state after a skip = %+v (%v), want the recorded digest", bs, err)
	}

	// An undeclared file is not an input.
	writeInput(t, projDir, "src/README.md", "edited docs")
	if r = h.build(t, projDir, buildOptions{}, svc)["api"]; !r.upToDate || runs() != 1 {
		t.Fatalf("an undeclared edit ran the command (runs=%d)", runs())
	}

	// --rebuild forces the command.
	if r = h.build(t, projDir, buildOptions{rebuild: true}, svc)["api"]; r.upToDate || runs() != 2 {
		t.Fatalf("--rebuild: upToDate=%v runs=%d, want a real run", r.upToDate, runs())
	}

	// A different env is a different record.
	if r = h.build(t, projDir, buildOptions{env: "staging"}, svc)["api"]; r.upToDate || runs() != 3 {
		t.Fatalf("first build in another env: upToDate=%v runs=%d, want a real run", r.upToDate, runs())
	}

	// The registry no longer serves the recorded digest at the tag (another
	// checkout pushed over it, or the registry was reset): run.
	h.serve("reg.example/api", otherDigest)
	if r = h.build(t, projDir, buildOptions{}, svc)["api"]; r.upToDate || runs() != 4 {
		t.Fatalf("moved tag: upToDate=%v runs=%d, want a real run", r.upToDate, runs())
	}
	if r = h.build(t, projDir, buildOptions{}, svc)["api"]; !r.upToDate || runs() != 4 {
		t.Fatalf("after re-recording the moved tag: upToDate=%v runs=%d, want a skip", r.upToDate, runs())
	}
	if r.digest != otherDigest {
		t.Fatalf("skip after the re-run reused %s, want the digest that run recorded", r.digest)
	}

	// A changed input runs the command; a FAILED run records nothing, so
	// the last successful build stays the reference.
	before, _ := buildtarget.ReadState(projDir, "dev", "api")
	writeInput(t, projDir, "src/main.go", "package main // broken")
	h.fail.Store(true)
	if r = h.build(t, projDir, buildOptions{}, svc)["api"]; r.err == nil || runs() != 5 {
		t.Fatalf("changed input with a failing command: err=%v runs=%d, want a failed run", r.err, runs())
	}
	after, _ := buildtarget.ReadState(projDir, "dev", "api")
	if after.InputsFingerprint != before.InputsFingerprint {
		t.Fatal("a failed build replaced the recorded fingerprint")
	}
	h.fail.Store(false)
	if r = h.build(t, projDir, buildOptions{}, svc)["api"]; r.upToDate || runs() != 6 {
		t.Fatalf("changed input: upToDate=%v runs=%d, want a real run", r.upToDate, runs())
	}
	// Reverting to the inputs of an EARLIER success does not resurrect it:
	// the record is the latest successful build, whose image is at the tag.
	writeInput(t, projDir, "src/main.go", "package main")
	if r = h.build(t, projDir, buildOptions{}, svc)["api"]; r.upToDate || runs() != 7 {
		t.Fatalf("revert: upToDate=%v runs=%d, want a real run", r.upToDate, runs())
	}
}

func TestBuildExternalServices_InputsCannotProveTheOutput(t *testing.T) {
	projDir := t.TempDir()
	writeInput(t, projDir, "src/main.go", "package main")

	t.Run("no image", func(t *testing.T) {
		h := newInputsHarness(t, "tool")
		svc := inputsSvc("tool", "**")
		svc.Image = ""
		for i := 1; i <= 2; i++ {
			if r := h.build(t, projDir, buildOptions{}, svc)["tool"]; r.upToDate || h.runs["tool"].Load() != int32(i) {
				t.Fatalf("build %d of an image-less ShellBuild skipped the command", i)
			}
		}
	})
	t.Run("a pattern that matches nothing", func(t *testing.T) {
		h := newInputsHarness(t, "api")
		svc := inputsSvc("api", "**/*.go", "missing.lock")
		for i := 1; i <= 2; i++ {
			if r := h.build(t, projDir, buildOptions{}, svc)["api"]; r.upToDate || h.runs["api"].Load() != int32(i) {
				t.Fatalf("build %d with an unmatched pattern skipped the command", i)
			}
		}
		if st, _ := buildtarget.ReadState(projDir, "dev", "api"); st == nil || st.InputsFingerprint != "" {
			t.Fatalf("state = %+v, want a build recorded without a fingerprint", st)
		}
	})
	t.Run("a release always runs", func(t *testing.T) {
		v := checkShellBuildInputs(context.Background(), inputsSvc("api", "**"), buildtarget.Spec{}, filepath.Join(projDir, "src"),
			buildOptions{composition: &compositionRun{}}, projDir)
		if v.reuse != nil || v.fp != nil || !strings.Contains(v.reason, "release") {
			t.Fatalf("release verdict = %+v, want a run without fingerprinting", v)
		}
	})
}

// TestBuildExternalServices_InputsFollowBuiltImages: a child built FROM a
// skipped parent receives the parent's recorded digest and is skipped too,
// while a parent that rebuilds to a new digest rebuilds the child even though
// none of the child's own files changed.
func TestBuildExternalServices_InputsFollowBuiltImages(t *testing.T) {
	projDir := t.TempDir()
	writeInput(t, projDir, "src/base.go", "package base")
	h := newInputsHarness(t, "base", "app")
	base := inputsSvc("base", "base.go")
	app := inputsSvc("app", "base.go")
	app.Build.Shell.Env = nil
	app.Build.Shell.BuiltImages = map[string]string{"BASE": "base"}
	opts := buildOptions{parallel: true}

	h.build(t, projDir, opts, base, app)
	r := h.build(t, projDir, opts, base, app)
	if !r["base"].upToDate || !r["app"].upToDate || h.runs["base"].Load() != 1 || h.runs["app"].Load() != 1 {
		t.Fatalf("unchanged parent and child: base %v app %v, runs %d/%d, want both skipped",
			r["base"].upToDate, r["app"].upToDate, h.runs["base"].Load(), h.runs["app"].Load())
	}

	// The parent's tag now serves a new image, so it rebuilds; the child's
	// BASE changes with it, while the child's own image is still in place.
	h.serve("reg.example/base", otherDigest)
	r = h.build(t, projDir, opts, base, app)
	if r["base"].upToDate || r["app"].upToDate || h.runs["app"].Load() != 2 {
		t.Fatalf("rebuilt parent: base %v app %v (app runs %d), want both to run", r["base"].upToDate, r["app"].upToDate, h.runs["app"].Load())
	}
}

func TestShellBuildFingerprint_RecipeSensitivity(t *testing.T) {
	projDir := t.TempDir()
	writeInput(t, projDir, "src/main.go", "package main")
	cwd := filepath.Join(projDir, "src")
	fp := func(svc WorkloadEntity, opts buildOptions, tag string) string {
		t.Helper()
		spec := externalBuildSpec(svc, opts, tag, projDir)
		got, err := shellBuildFingerprint(svc, spec, cwd, projDir, opts)
		if err != nil {
			t.Fatal(err)
		}
		return got.Digest
	}
	base := inputsSvc("api", "**")
	want := fp(base, buildOptions{}, "dev")
	if got := fp(base, buildOptions{}, "dev"); got != want {
		t.Fatal("fingerprint is not stable")
	}

	cmd := inputsSvc("api", "**")
	cmd.Build.Shell.Cmd = "make api VERBOSE=1"
	env := inputsSvc("api", "**")
	env.Build.Shell.Env = map[string]string{"MODE": "debug"}
	image := inputsSvc("api", "**")
	image.Image = "reg.example/other"
	for name, got := range map[string]string{
		"command":     fp(cmd, buildOptions{}, "dev"),
		"env":         fp(env, buildOptions{}, "dev"),
		"image":       fp(image, buildOptions{}, "dev"),
		"tag":         fp(base, buildOptions{}, "v2"),
		"target arch": fp(base, buildOptions{targetArch: "arm64"}, "dev"),
	} {
		if got == want {
			t.Errorf("changing the %s left the fingerprint unchanged", name)
		}
	}
}

// sourceSvc is a ShellBuild whose cwd comes from a declared GitSource, already
// materialized into dir (as resolveShellBuildSource leaves it).
func sourceSvc(name, ref, dir string) WorkloadEntity {
	w := inputsSvc(name, "**")
	w.Build.Shell.Cwd = ""
	w.Build.Shell.Source = &config.GitSource{Repo: "github.com/acme/api", Ref: ref}
	w.Build.Shell.SourceDir = dir
	return w
}

// TestBuildExternalServices_InputsCoverTheArtifactIdentity is the regression
// for a skip keyed on less than the artifact identity: a build whose declared
// source or inputs_file changed must run even when every input file is
// byte-identical.
func TestBuildExternalServices_InputsCoverTheArtifactIdentity(t *testing.T) {
	projDir := t.TempDir()
	writeInput(t, projDir, "src/main.go", "package main")
	srcDir := filepath.Join(projDir, "src")

	t.Run("declared source", func(t *testing.T) {
		h := newInputsHarness(t, "api")
		h.build(t, projDir, buildOptions{}, sourceSvc("api", "v1.0.0", srcDir))
		if r := h.build(t, projDir, buildOptions{}, sourceSvc("api", "v1.0.0", srcDir))["api"]; !r.upToDate {
			t.Fatal("an unchanged declared source did not skip")
		}
		r := h.build(t, projDir, buildOptions{}, sourceSvc("api", "v2.0.0", srcDir))["api"]
		if r.upToDate || h.runs["api"].Load() != 2 {
			t.Fatalf("a changed declared source (same files) skipped the command: upToDate=%v runs=%d", r.upToDate, h.runs["api"].Load())
		}
	})
	t.Run("inputs_file content", func(t *testing.T) {
		h := newInputsHarness(t, "gw")
		svc := inputsSvc("gw", "**/*.go")
		svc.Build.InputsFile = "build-inputs.json"
		writeInput(t, projDir, "build-inputs.json", `{"recipe":"a"}`)
		h.build(t, projDir, buildOptions{}, svc)
		if r := h.build(t, projDir, buildOptions{}, svc)["gw"]; !r.upToDate {
			t.Fatal("an unchanged inputs_file did not skip")
		}
		writeInput(t, projDir, "build-inputs.json", `{"recipe":"b"}`)
		if r := h.build(t, projDir, buildOptions{}, svc)["gw"]; r.upToDate || h.runs["gw"].Load() != 2 {
			t.Fatalf("a changed inputs_file skipped the command (runs=%d)", h.runs["gw"].Load())
		}
		// An inputs_file that cannot be read cannot be hashed: reuse is refused.
		if err := os.Remove(filepath.Join(projDir, "build-inputs.json")); err != nil {
			t.Fatal(err)
		}
		for i := int32(3); i <= 4; i++ {
			if r := h.build(t, projDir, buildOptions{}, svc)["gw"]; r.upToDate || h.runs["gw"].Load() != i {
				t.Fatalf("build %d with an unreadable inputs_file skipped the command", i)
			}
		}
	})
	t.Run("recipe name and output name", func(t *testing.T) {
		base := inputsSvc("api", "**")
		fp := func(svc WorkloadEntity) string {
			t.Helper()
			got, err := shellBuildFingerprint(svc, externalBuildSpec(svc, buildOptions{}, "dev", projDir), srcDir, projDir, buildOptions{})
			if err != nil {
				t.Fatal(err)
			}
			return got.Digest
		}
		unit := inputsSvc("api", "**")
		unit.Build.Unit = "other-recipe"
		out := inputsSvc("api", "**")
		out.Build.Shell.OutputName = "api-bin"
		if fp(unit) == fp(base) || fp(out) == fp(base) {
			t.Fatal("unit or output_name left the fingerprint unchanged")
		}
	})
}

// TestShellBuildInputs_EscapingPatternRefusesReuse: a pattern reaching past
// the project's parent directory is refused, and the build runs (and says
// why) rather than keying a skip on files outside the workspace.
func TestShellBuildInputs_EscapingPatternRefusesReuse(t *testing.T) {
	ws := t.TempDir()
	projDir := filepath.Join(ws, "proj")
	writeInput(t, projDir, "src/main.go", "package main")
	writeInput(t, ws, "sibling/lib.go", "package lib")

	h := newInputsHarness(t, "api", "far")
	sib := inputsSvc("api", "**", "../../sibling/**")
	h.build(t, projDir, buildOptions{}, sib)
	if r := h.build(t, projDir, buildOptions{}, sib)["api"]; !r.upToDate {
		t.Fatal("a sibling under the project's parent should be a valid input")
	}

	far := inputsSvc("far", "**", "../../../**")
	v := checkShellBuildInputs(context.Background(), far, externalBuildSpec(far, buildOptions{env: "dev"}, "dev", projDir),
		filepath.Join(projDir, "src"), buildOptions{env: "dev"}, projDir)
	if v.reuse != nil || v.fp != nil || !strings.Contains(v.reason, "outside the allowed roots") {
		t.Fatalf("verdict = %+v, want reuse refused naming the roots", v)
	}
}
