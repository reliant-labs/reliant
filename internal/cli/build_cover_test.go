package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/internal/hostlaunch"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/testkit"
)

func coverGo(cmd string, coverage *bool) func(*WorkloadEntity) {
	return func(w *WorkloadEntity) {
		w.Build = BuildConfigEntity{Type: "go", Go: &GoBuild{Cmd: cmd, OutputName: filepath.Base(cmd), Coverage: coverage}}
	}
}

func boolp(b bool) *bool { return &b }

func coverByPackage(t *testing.T, e *KCLEntities, optIn bool) map[string]CoverBinary {
	t.Helper()
	bins, err := ResolveCoverBinaries(e, optIn)
	if err != nil {
		t.Fatalf("ResolveCoverBinaries: %v", err)
	}
	out := map[string]CoverBinary{}
	for _, b := range bins {
		out[b.Package] = b
	}
	return out
}

// The computed default: on for a local, ephemeral or preview env of a project
// that opts in through forge.yaml; off without the opt-in; never for a
// persistent env.
func TestResolveCoverBinaries_ComputedDefault(t *testing.T) {
	envs := []struct {
		name  string
		e     KCLEntities
		optIn bool
		want  bool
	}{
		{"local, opted in", KCLEntities{Env: "dev", Lifecycle: "local"}, true, true},
		{"ephemeral, opted in", KCLEntities{Env: "e2e", Lifecycle: "ephemeral"}, true, true},
		{"preview, opted in", KCLEntities{Env: "pr-1", Preview: &release.ShapePreview{Of: "staging"}}, true, true},
		{"local, not opted in (an existing project after a forge bump)", KCLEntities{Env: "dev", Lifecycle: "local"}, false, false},
		{"persistent, opted in", KCLEntities{Env: "prod"}, true, false},
		{"local, opted in, env says no", KCLEntities{Env: "dev", Lifecycle: "local", Tests: &TestsEntity{Coverage: boolp(false)}}, true, false},
		{"local, not opted in, env says yes", KCLEntities{Env: "dev", Lifecycle: "local", Tests: &TestsEntity{Coverage: boolp(true)}}, false, true},
	}
	for _, tc := range envs {
		t.Run(tc.name, func(t *testing.T) {
			e := tc.e
			e.Workloads = []WorkloadEntity{hostWL("api", coverGo("./cmd/shop", nil))}
			if got := coverByPackage(t, &e, tc.optIn)["./cmd/shop"].Enabled; got != tc.want {
				t.Errorf("coverage = %v, want %v", got, tc.want)
			}
		})
	}
}

// A build's own answer overrides the env's, and a DockerBuild's is keyed by
// the binary's package.
func TestResolveCoverBinaries_PerBinaryOverride(t *testing.T) {
	e := &KCLEntities{Env: "dev", Lifecycle: "local", Project: "shop", Workloads: []WorkloadEntity{
		hostWL("api", coverGo("./cmd/shop", nil)),
		hostWL("ctl", coverGo("./cmd/ctl", boolp(false))),
		clusterWL("edge", "k3d-x", "ns", pgoDocker(DockerBuild{Coverage: map[string]bool{"./cmd/proxy": false}}, "/app/proxy")),
	}}
	got := coverByPackage(t, e, true)
	if !got["./cmd/shop"].Enabled || got["./cmd/ctl"].Enabled || got["./cmd/proxy"].Enabled {
		t.Errorf("resolved %+v", got)
	}
}

// One binary, one build: workloads that run the same binary must agree, and
// an unset one takes the env's default before they are compared.
func TestResolveCoverBinaries_ConflictNamesBothWorkloads(t *testing.T) {
	e := &KCLEntities{Env: "dev", Lifecycle: "local", Workloads: []WorkloadEntity{
		hostWL("api", coverGo("./cmd/shop", nil)),
		hostWL("worker", coverGo("cmd/shop/", boolp(false))),
	}}
	_, err := ResolveCoverBinaries(e, true)
	if err == nil || !strings.Contains(err.Error(), `"api"`) || !strings.Contains(err.Error(), `"worker"`) || !strings.Contains(err.Error(), "./cmd/shop") {
		t.Fatalf("err = %v, want a refusal naming both workloads and the binary", err)
	}
	// The same declarations agree when the env's default is off.
	if _, err := ResolveCoverBinaries(e, false); err != nil {
		t.Errorf("api (default off) and worker (off) agree, got %v", err)
	}
}

// A persistent env never instruments silently: a binary built with coverage
// there needs the env's reason, whether the env or the build turned it on.
func TestResolveCoverBinaries_PersistentEnvNeedsAReason(t *testing.T) {
	for name, e := range map[string]*KCLEntities{
		"build turns it on": {Env: "staging", Workloads: []WorkloadEntity{hostWL("api", coverGo("./cmd/shop", boolp(true)))}},
		"env turns it on":   {Env: "staging", Tests: &TestsEntity{Coverage: boolp(true)}, Workloads: []WorkloadEntity{hostWL("api", coverGo("./cmd/shop", nil))}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ResolveCoverBinaries(e, true)
			if err == nil || !strings.Contains(err.Error(), "persistent env") || !strings.Contains(err.Error(), "coverage_in_persistent_env") {
				t.Fatalf("err = %v, want the persistent-env refusal", err)
			}
			e.Tests = &TestsEntity{Coverage: e.Tests.coverage(), CoverageInPersistentEnv: "nightly e2e"}
			if got := coverByPackage(t, e, true)["./cmd/shop"]; !got.Enabled {
				t.Errorf("with a reason the env builds it instrumented, got %+v", got)
			}
		})
	}
}

func (t *TestsEntity) coverage() *bool {
	if t == nil {
		return nil
	}
	return t.Coverage
}

// The render's tests block and per-build answers decode, and an env that
// declares nothing decodes with no Tests: nothing about it changed.
func TestCoverDecodesFromTheRender(t *testing.T) {
	e, err := parseKCLEntities([]byte(`{"project":"shop","env":"dev","lifecycle":"local",
		"tests":{"coverage":true,"coverage_packages":["./internal/..."],"coverage_in_persistent_env":null},
		"workloads":[
		{"name":"api","runtime":{"type":"host"},"spec":{},"build":{"type":"go","cmd":"./cmd/shop","coverage":false}},
		{"name":"edge","runtime":{"type":"cluster","cluster":"k3d-x","namespace":"ns"},"spec":{"image":"r/edge"},
		 "build":{"type":"docker","dockerfile":"Dockerfile","coverage":{"./cmd/proxy":true}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if e.Tests == nil || !*e.Tests.Coverage || !slices.Equal(e.Tests.CoveragePackages, []string{"./internal/..."}) {
		t.Errorf("tests = %+v", e.Tests)
	}
	if c := e.Workloads[0].Build.Go.Coverage; c == nil || *c {
		t.Errorf("GoBuild.coverage = %v, want false", c)
	}
	if !e.Workloads[1].Build.Docker.Coverage["./cmd/proxy"] {
		t.Errorf("DockerBuild.coverage = %v", e.Workloads[1].Build.Docker.Coverage)
	}
	plain, err := parseKCLEntities([]byte(`{"project":"shop","env":"dev","workloads":[]}`))
	if err != nil || plain.Tests != nil {
		t.Errorf("an env with no tests block must decode with nil Tests (err %v)", err)
	}
}

// The render resolves coverage with forge.yaml's opt-in and marks each
// instrumented workload, which is what the host build and launch read; a
// project without the opt-in renders and builds exactly as before.
func TestRenderKCLWith_ResolvesCoverageFromForgeYAML(t *testing.T) {
	render := []byte(`{"project":"shop","env":"dev","lifecycle":"local","workloads":[
		{"name":"api","runtime":{"type":"host","runner":"air"},"spec":{"args":["server"]},"build":{"type":"go","cmd":"./cmd/shop"}},
		{"name":"tool","runtime":{"type":"host"},"spec":{"command":["sh"]}}]}`)
	prev := renderKCLRawHook
	t.Cleanup(func() { renderKCLRawHook = prev })
	renderKCLRawHook = func(context.Context, string, string, ...string) ([]byte, error) { return render, nil }

	for _, tc := range []struct {
		forgeYAML string
		want      bool
	}{
		{"name: shop\nmodule_path: example.com/shop\ntests:\n  coverage: true\n", true},
		{"name: shop\nmodule_path: example.com/shop\nci:\n  test:\n    coverage: true\n", true},
		{"name: shop\nmodule_path: example.com/shop\n", false},
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(tc.forgeYAML), 0o644); err != nil {
			t.Fatal(err)
		}
		e, err := RenderKCLWith(context.Background(), dir, "dev", nil)
		if err != nil {
			t.Fatalf("%q: %v", tc.forgeYAML, err)
		}
		if got := e.Workloads[0].CoverBuild(); got != tc.want {
			t.Errorf("%q: api CoverBuild = %v, want %v", tc.forgeYAML, got, tc.want)
		}
		if e.Workloads[1].CoverBuild() {
			t.Errorf("a workload with no Go build is never a cover build")
		}
		targets := goBuildTargetsFromKCL(e)
		if len(targets) != 1 || targets[0].cover != tc.want {
			t.Errorf("%q: go targets %+v, want cover=%v", tc.forgeYAML, targets, tc.want)
		}
	}
}

// A cover build's flags reach `go build`, and the user's own -cover wins.
func TestGoBuildArgs_Cover(t *testing.T) {
	flags := []string{"-cover", "-covermode=atomic", "-coverpkg=example.com/shop/internal/..."}
	args := goBuildArgs(goBuildTarget{cmd: "./cmd/shop", outputName: "shop", cover: true, coverFlags: flags}, "bin/shop", false, versionInfo{})
	if i := slices.Index(args, "-cover"); i < 0 || !slices.Equal(args[i:i+3], flags) || args[len(args)-1] != "./cmd/shop" {
		t.Errorf("args = %v, want the cover flags before the package", args)
	}
	// An explicit -cover is the user's: forge adds nothing.
	for _, tt := range []goBuildTarget{
		{flags: []string{"-cover"}},
		{flags: []string{"-cover=false"}},
		{env: map[string]string{"GOFLAGS": "-mod=mod -cover"}},
	} {
		if got := mergeCoverFlags(flags, tt.flags, tt.env); got != nil {
			t.Errorf("flags %v env %v: forge added %v over the user's own -cover", tt.flags, tt.env, got)
		}
	}
	// A -covermode or -coverpkg alone does not own instrumentation: forge
	// still adds -cover, and keeps the user's value in place of its own.
	for _, tc := range []struct {
		flags []string
		env   map[string]string
		want  []string
	}{
		{flags: []string{"-coverpkg=./pkg/..."}, want: []string{"-cover", "-covermode=atomic"}},
		{flags: []string{"-covermode=count"}, want: []string{"-cover", "-coverpkg=example.com/shop/internal/..."}},
		{env: map[string]string{"GOFLAGS": "-covermode=atomic -coverpkg=./..."}, want: []string{"-cover"}},
		{flags: []string{"-race", "-tags=e2e"}, env: map[string]string{"GOFLAGS": "-mod=mod"}, want: flags},
	} {
		if got := mergeCoverFlags(flags, tc.flags, tc.env); !slices.Equal(got, tc.want) {
			t.Errorf("flags %v env %v: forge adds %v, want %v", tc.flags, tc.env, got, tc.want)
		}
	}
	// And the build carries both: forge's -cover, then the user's own value.
	tt := goBuildTarget{cmd: "./cmd/shop", outputName: "shop", cover: true, flags: []string{"-coverpkg=./pkg/..."},
		coverFlags: mergeCoverFlags(flags, []string{"-coverpkg=./pkg/..."}, nil)}
	args = goBuildArgs(tt, "bin/shop", false, versionInfo{})
	if !slices.Contains(args, "-cover") || !slices.Contains(args, "-coverpkg=./pkg/...") || slices.Contains(args, "-coverpkg=example.com/shop/internal/...") {
		t.Errorf("args = %v, want -cover with the user's -coverpkg only", args)
	}
}

// "main" is the module's top-level package directories as patterns, gen/
// left out, other modules' packages ignored.
func TestMainCoverPatternsOf(t *testing.T) {
	got := mainCoverPatternsOf("example.com/shop", []string{
		"example.com/shop",
		"example.com/shop/cmd/shop",
		"example.com/shop/internal/handlers/orders",
		"example.com/shop/internal/db",
		"example.com/shop/gen/services/orders/v1",
		"example.com/shop/gen",
		"example.com/shop/pkg/money",
		"example.com/shopify/x", // a sibling module that shares the prefix
	})
	want := []string{"example.com/shop", "example.com/shop/cmd/...", "example.com/shop/internal/...", "example.com/shop/pkg/..."}
	if !slices.Equal(got, want) {
		t.Errorf("patterns = %v, want %v", got, want)
	}
}

// A host workload whose binary is instrumented launches with GOCOVERDIR
// under .forge/tests/covdata/<env>/<workload>/ (created), the cover flags on
// go-run's argv and, under air, in FORGE_GO_BUILD_FLAGS.
func TestWithHostCover(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("forge.yaml", []byte("name: shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".air.toml", []byte("[build]\ncmd = \"go build $FORGE_GO_BUILD_FLAGS -o ./tmp/shop ./cmd/shop\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pkgs := []string{"./internal/..."}
	for _, runner := range []string{"go-run", "air", "binary"} {
		w := hostWL("api", coverGo("./cmd/shop", nil), func(w *WorkloadEntity) { w.Runtime.Host.Runner = runner })
		w.cover = &workloadCover{packages: pkgs}
		spec := hostRunnerSpec(w)
		if err := withHostCover(context.Background(), &spec, w, "dev"); err != nil {
			t.Fatalf("%s: %v", runner, err)
		}
		if info, err := os.Stat(spec.CoverDir); !strings.HasSuffix(spec.CoverDir, filepath.Join(".forge", "tests", "covdata", "dev", "api")) || err != nil || !info.IsDir() {
			t.Errorf("%s: CoverDir %q, want .forge/tests/covdata/dev/api, created (stat: %v)", runner, spec.CoverDir, err)
		}
		env := hostlaunch.WithCoverage(spec, nil)
		if !slices.Contains(env, "GOCOVERDIR="+spec.CoverDir) {
			t.Errorf("%s: env %v has no GOCOVERDIR", runner, env)
		}
		switch runner {
		case "go-run":
			cmd, err := hostlaunch.BuildCmd(context.Background(), "api", spec)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(cmd.Args, "-coverpkg=./internal/...") || !slices.Contains(cmd.Args, "-covermode=atomic") {
				t.Errorf("go-run argv %v lacks the cover flags", cmd.Args)
			}
		case "air":
			if !slices.Contains(env, "FORGE_GO_BUILD_FLAGS=-cover -covermode=atomic -coverpkg=./internal/...") {
				t.Errorf("air env %v lacks FORGE_GO_BUILD_FLAGS", env)
			}
		case "binary":
			if len(spec.GoBuildFlags) != 0 {
				t.Errorf("binary runs what forge build compiled; it needs no build flags, got %v", spec.GoBuildFlags)
			}
		}
	}

	// A workload the env does not instrument launches exactly as before.
	plain := hostWL("api", coverGo("./cmd/shop", nil))
	spec := hostRunnerSpec(plain)
	if err := withHostCover(context.Background(), &spec, plain, "dev"); err != nil || spec.CoverDir != "" || spec.GoBuildFlags != nil {
		t.Errorf("uninstrumented spec = %+v (err %v)", spec, err)
	}
}

// The flags a cover build gets really instrument the module minus gen/: a
// binary built with them writes coverage to GOCOVERDIR at exit, for the
// module's packages and not its generated ones.
func TestCoverBuildFlags_RealBuild(t *testing.T) {
	testkit.Slow(t, "runs go list, go build -cover and go tool covdata on a scratch module")
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/shop\n\ngo 1.22\n")
	write("cmd/shop/main.go", "package main\n\nimport (\n\t\"example.com/shop/gen/api\"\n\t\"example.com/shop/internal/orders\"\n)\n\nfunc main() { println(orders.Total(api.Two())) }\n")
	write("internal/orders/orders.go", "package orders\n\nfunc Total(n int) int { return n * 2 }\n")
	write("gen/api/api.go", "package api\n\nfunc Two() int { return 2 }\n")

	flags, err := coverBuildFlags(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "-coverpkg=example.com/shop/cmd/...,example.com/shop/internal/..."; !slices.Contains(flags, want) {
		t.Fatalf("flags = %v, want %s", flags, want)
	}
	bin := filepath.Join(dir, "shop")
	build := exec.Command("go", append(append([]string{"build"}, flags...), "-o", bin, "./cmd/shop")...)
	build.Dir = dir
	build.Env = goexec.Env("GOFLAGS=-mod=mod", "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build %v: %v\n%s", flags, err, out)
	}
	covdir := t.TempDir()
	run := exec.Command(bin)
	run.Env = append(goexec.Env(), "GOCOVERDIR="+covdir)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	list := exec.Command("go", "tool", "covdata", "pkglist", "-i="+covdir)
	list.Env = goexec.Env()
	out, err := list.CombinedOutput()
	if err != nil {
		t.Fatalf("covdata pkglist: %v\n%s", err, out)
	}
	pkgs := strings.Fields(string(out))
	if !slices.Contains(pkgs, "example.com/shop/internal/orders") || slices.Contains(pkgs, "example.com/shop/gen/api") {
		t.Errorf("instrumented packages = %v, want internal/orders and not gen/api", pkgs)
	}
}
