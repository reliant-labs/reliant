package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/pprof/profile"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/pkg/profilequery"
)

func pgoGo(cmd string, pgo *PGOConfig) func(*WorkloadEntity) {
	return func(w *WorkloadEntity) {
		w.Build = BuildConfigEntity{Type: "go", Go: &GoBuild{Cmd: cmd, PGO: pgo}}
	}
}

func pgoDocker(d DockerBuild, command ...string) func(*WorkloadEntity) {
	return func(w *WorkloadEntity) {
		w.Build = BuildConfigEntity{Type: "docker", Docker: &d}
		w.Spec.Command = command
	}
}

func pgoOff() *PGOConfig { f := false; return &PGOConfig{Enabled: &f} }

func pgoByPackage(t *testing.T, e *KCLEntities) map[string]PGOBinary {
	t.Helper()
	bins, err := ResolvePGOBinaries(e)
	if err != nil {
		t.Fatalf("ResolvePGOBinaries: %v", err)
	}
	out := map[string]PGOBinary{}
	for _, b := range bins {
		out[b.Package] = b
	}
	return out
}

// A modular monolith: the server, a worker and a cron all run ./cmd/shop.
// They make ONE binary whose profile merges all three, with the defaults
// applied when none declares a PGO.
func TestResolvePGOBinaries_ModularMonolith(t *testing.T) {
	shop := pgoGo("./cmd/shop", nil)
	e := &KCLEntities{Project: "shop", Env: "prod", Workloads: []WorkloadEntity{
		clusterWL("api", "k3d-x", "ns", shop),
		clusterWL("worker", "k3d-x", "ns", pgoGo("cmd/shop/", &PGOConfig{})),
		clusterWL("nightly", "k3d-x", "ns", shop),
	}}
	bins, err := ResolvePGOBinaries(e)
	if err != nil {
		t.Fatal(err)
	}
	if len(bins) != 1 {
		t.Fatalf("want one binary, got %+v", bins)
	}
	b := bins[0]
	if b.Package != "./cmd/shop" || b.ProfilePath() != "cmd/shop/default.pgo" || b.Dir() != "shop" {
		t.Errorf("binary = %s (profile %s, dir %s)", b.Package, b.ProfilePath(), b.Dir())
	}
	if want := []string{"api", "nightly", "worker"}; !slices.Equal(b.Workloads, want) || !slices.Equal(b.Services, want) {
		t.Errorf("workloads %v services %v, want %v", b.Workloads, b.Services, want)
	}
	want := PGOSettings{Enabled: true, FromEnv: "prod", Since: 168 * time.Hour, MinWeight: 10 * time.Second}
	if !b.PGO.equal(want) {
		t.Errorf("PGO = %s, want the defaults %s", b.PGO, want)
	}
}

// One image, several binaries: the project Dockerfile builds every
// cmd/<bin>; a workload runs the primary by default and another one by
// naming /app/<bin> as its command. DockerBuild.pgo is keyed by package.
func TestResolvePGOBinaries_MultiBinaryImage(t *testing.T) {
	image := DockerBuild{PGO: map[string]PGOConfig{"./cmd/proxy": *pgoOff()}}
	e := &KCLEntities{Project: "shop", Env: "prod", Workloads: []WorkloadEntity{
		clusterWL("api", "k3d-x", "ns", pgoDocker(image)),
		clusterWL("edge", "k3d-x", "ns", pgoDocker(image, "/app/proxy", "--listen=:80")),
		clusterWL("ctl", "k3d-x", "ns", pgoDocker(DockerBuild{Dockerfile: "./Dockerfile", Context: "."}, "/app/bin/ctl")),
		// A command that is not one of the image's binaries runs none.
		clusterWL("shell", "k3d-x", "ns", pgoDocker(DockerBuild{}, "/bin/sh", "-c", "true")),
	}}
	got := pgoByPackage(t, e)
	if len(got) != 3 {
		t.Fatalf("want shop, proxy and ctl, got %+v", got)
	}
	if b := got["./cmd/shop"]; !slices.Equal(b.Workloads, []string{"api"}) || !b.PGO.Enabled {
		t.Errorf("./cmd/shop = %+v", b)
	}
	if b := got["./cmd/proxy"]; !slices.Equal(b.Workloads, []string{"edge"}) || b.PGO.Enabled {
		t.Errorf("./cmd/proxy = %+v, want disabled and run by edge", b)
	}
	if b := got["./cmd/ctl"]; !slices.Equal(b.Workloads, []string{"ctl"}) {
		t.Errorf("./cmd/ctl = %+v", b)
	}
}

// Another Dockerfile: forge cannot see inside it, so a workload runs a
// binary only when its pgo names it — the command's, or its only key.
func TestResolvePGOBinaries_OtherDockerfile(t *testing.T) {
	e := &KCLEntities{Project: "shop", Workloads: []WorkloadEntity{
		clusterWL("one", "k3d-x", "ns", pgoDocker(DockerBuild{Dockerfile: "svc/Dockerfile", PGO: map[string]PGOConfig{"./cmd/svc": {}}})),
		clusterWL("two", "k3d-x", "ns", pgoDocker(DockerBuild{Dockerfile: "two/Dockerfile", PGO: map[string]PGOConfig{"./cmd/a": {}, "./cmd/b": {}}}, "/app/b")),
		clusterWL("opaque", "k3d-x", "ns", pgoDocker(DockerBuild{Dockerfile: "x/Dockerfile"})),
	}}
	got := pgoByPackage(t, e)
	if !slices.Equal(got["./cmd/svc"].Workloads, []string{"one"}) || !slices.Equal(got["./cmd/b"].Workloads, []string{"two"}) {
		t.Errorf("got %+v", got)
	}
	if b, ok := got["./cmd/a"]; !ok || len(b.Workloads) != 0 {
		t.Errorf("./cmd/a is named but run by nobody: %+v (present %v)", b, ok)
	}
	if len(got) != 3 {
		t.Errorf("the opaque image adds no binary: %+v", got)
	}
}

// One binary, one profile: two workloads of an env that build it with
// different PGO are refused, naming both. Running it without naming it is
// declaring the defaults.
func TestResolvePGOBinaries_ConflictNamesBothWorkloads(t *testing.T) {
	for name, e := range map[string]*KCLEntities{
		"go vs go": {Workloads: []WorkloadEntity{
			clusterWL("api", "k3d-x", "ns", pgoGo("./cmd/shop", &PGOConfig{Since: "72h"})),
			clusterWL("worker", "k3d-x", "ns", pgoGo("./cmd/shop", nil)),
		}},
		"go vs docker default": {Project: "shop", Workloads: []WorkloadEntity{
			clusterWL("api", "k3d-x", "ns", pgoGo("./cmd/shop", pgoOff())),
			clusterWL("worker", "k3d-x", "ns", pgoDocker(DockerBuild{})),
		}},
		"docker vs docker": {Workloads: []WorkloadEntity{
			clusterWL("api", "k3d-x", "ns", pgoDocker(DockerBuild{PGO: map[string]PGOConfig{"./cmd/proxy": *pgoOff()}})),
			clusterWL("worker", "k3d-x", "ns", pgoDocker(DockerBuild{PGO: map[string]PGOConfig{"./cmd/proxy": {}}})),
		}},
	} {
		_, err := ResolvePGOBinaries(e)
		if err == nil || !strings.Contains(err.Error(), `"api"`) || !strings.Contains(err.Error(), `"worker"`) || !strings.Contains(err.Error(), "one binary has one profile") {
			t.Errorf("%s: err = %v, want a conflict naming api and worker", name, err)
		}
	}
	// The same declaration twice is not a conflict, whatever the spelling.
	if _, err := ResolvePGOBinaries(&KCLEntities{Env: "prod", Workloads: []WorkloadEntity{
		clusterWL("api", "k3d-x", "ns", pgoGo("./cmd/shop", &PGOConfig{Since: "168h", FromEnv: "prod", Services: []string{"b", "a"}})),
		clusterWL("worker", "k3d-x", "ns", pgoGo("cmd/shop", &PGOConfig{Services: []string{"a", "b"}})),
	}}); err != nil {
		t.Errorf("equivalent declarations conflict: %v", err)
	}
}

func TestResolvePGOBinaries_ServicesOverrideDiscovery(t *testing.T) {
	cfg := &PGOConfig{FromEnv: "prod", Services: []string{"shop-api", "shop-api", "shop-worker"}}
	got := pgoByPackage(t, &KCLEntities{Env: "staging", Workloads: []WorkloadEntity{
		clusterWL("api", "k3d-x", "ns", pgoGo("./cmd/shop", cfg)),
	}})["./cmd/shop"]
	if !slices.Equal(got.Services, []string{"shop-api", "shop-worker"}) || !slices.Equal(got.Workloads, []string{"api"}) {
		t.Errorf("services %v workloads %v", got.Services, got.Workloads)
	}
	if got.PGO.FromEnv != "prod" {
		t.Errorf("from_env = %q, want prod", got.PGO.FromEnv)
	}
}

// A hand-written env document can carry what KCL would have refused.
func TestResolvePGOBinaries_RefusesABadDocument(t *testing.T) {
	for _, cfg := range []*PGOConfig{{Since: "7d"}, {Since: "721h"}, {Since: "0s"}, {MinWeight: "10"}} {
		_, err := ResolvePGOBinaries(&KCLEntities{Workloads: []WorkloadEntity{clusterWL("api", "k3d-x", "ns", pgoGo("./cmd/shop", cfg))}})
		if err == nil || !strings.Contains(err.Error(), `workload "api"`) {
			t.Errorf("%+v: err = %v", cfg, err)
		}
	}
}

// The rendered JSON decodes into the build: a default PGO is absent, so an
// env that declares none decodes with nil and resolves to the defaults.
func TestPGODecodesFromTheRender(t *testing.T) {
	e, err := parseKCLEntities([]byte(`{"project":"shop","env":"prod","workloads":[
		{"name":"api","runtime":{"type":"cluster","cluster":"k3d-x","namespace":"ns"},"spec":{"image":"r/shop"},
		 "build":{"type":"go","cmd":"./cmd/shop","pgo":{"enabled":false,"from_env":"","since":"168h","min_weight":"10s","services":[]}}},
		{"name":"edge","runtime":{"type":"cluster","cluster":"k3d-x","namespace":"ns"},"spec":{"image":"r/edge","command":["/app/proxy"]},
		 "build":{"type":"docker","dockerfile":"Dockerfile","pgo":{"./cmd/proxy":{"enabled":true,"since":"72h"}}}},
		{"name":"plain","runtime":{"type":"cluster","cluster":"k3d-x","namespace":"ns"},"spec":{"image":"r/plain"},
		 "build":{"type":"go","cmd":"./cmd/plain"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if e.Workloads[2].Build.Go.PGO != nil {
		t.Errorf("an absent pgo must decode as nil")
	}
	got := pgoByPackage(t, e)
	if got["./cmd/shop"].PGO.Enabled || !got["./cmd/plain"].PGO.Enabled || got["./cmd/proxy"].PGO.Since != 72*time.Hour {
		t.Errorf("decoded %+v", got)
	}
	if off := pgoDisabledPackages(e); !off["./cmd/shop"] || len(off) != 1 {
		t.Errorf("disabled = %v", off)
	}
}

func TestGoBuildArgs_PGOOff(t *testing.T) {
	e := &KCLEntities{Workloads: []WorkloadEntity{
		hostWL("off", func(w *WorkloadEntity) {
			w.Build = BuildConfigEntity{Type: "go", Go: &GoBuild{Cmd: "./cmd/off", OutputName: "off", PGO: pgoOff()}}
		}),
		hostWL("on", func(w *WorkloadEntity) {
			w.Build = BuildConfigEntity{Type: "go", Go: &GoBuild{Cmd: "./cmd/on", OutputName: "on"}}
		}),
	}}
	targets := map[string]goBuildTarget{}
	for _, tt := range goBuildTargetsFromKCL(e) {
		targets[tt.outputName] = tt
	}
	// Dev builds too: the host runner spec carries it to go-run / air.
	if !hostRunnerSpec(e.Workloads[0]).PGOOff || hostRunnerSpec(e.Workloads[1]).PGOOff {
		t.Errorf("host runner spec PGOOff must follow the workload's GoBuild.pgo")
	}
	if !slices.Contains(goBuildArgs(targets["off"], "bin/off", false, versionInfo{}), "-pgo=off") {
		t.Errorf("a disabled binary must build with -pgo=off")
	}
	if args := goBuildArgs(targets["on"], "bin/on", false, versionInfo{}); slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "-pgo") }) {
		t.Errorf("an enabled binary keeps Go's default -pgo=auto, got %v", args)
	}
	// The user's own -pgo always wins, as a flag or in GOFLAGS.
	for _, tt := range []goBuildTarget{
		{cmd: "./cmd/off", outputName: "off", pgoOff: true, flags: []string{"-pgo=cpu.pprof"}},
		{cmd: "./cmd/off", outputName: "off", pgoOff: true, flags: []string{"-pgo", "cpu.pprof"}},
		{cmd: "./cmd/off", outputName: "off", pgoOff: true, env: map[string]string{"GOFLAGS": "-mod=mod -pgo=auto"}},
	} {
		if args := goBuildArgs(tt, "bin/off", false, versionInfo{}); slices.Contains(args, "-pgo=off") {
			t.Errorf("forge's -pgo=off overrode the user's choice: %v", args)
		}
	}
	if got := pgoOffBuildArgs(projectImagePGOOff(goBuildTargetsFromKCL(e)), nil); !slices.Equal(got, []string{"--build-arg", "FORGE_PGO_OFF=off"}) {
		t.Errorf("project image pgo args = %v", got)
	}
}

func TestServiceDockerBuildArgs_PGOOff(t *testing.T) {
	cfg := &config.ProjectConfig{Name: "shop"}
	d := &DockerBuild{
		BuildArgs: map[string]string{"FORGE_PGO_KEEP": "auto"},
		PGO: map[string]PGOConfig{
			"./cmd/edge-proxy": *pgoOff(),
			"./cmd/keep":       *pgoOff(), // build_args names it: the declared arg wins
			"./cmd/app":        {},
		},
	}
	argv, _ := serviceDockerBuildArgs(cfg, "svc", "Dockerfile", d, buildOptions{}, "", "")
	args := strings.Join(argv, " ")
	if !strings.Contains(args, "--build-arg FORGE_PGO_EDGE_PROXY=off") {
		t.Errorf("args lack the disabled binary's arg: %s", args)
	}
	if strings.Contains(args, "FORGE_PGO_KEEP=off") || strings.Contains(args, "FORGE_PGO_APP") {
		t.Errorf("args override a declared build arg or name an enabled binary: %s", args)
	}
}

func TestPGOProfileNote(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	write := func(bin string, body []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, "cmd", bin), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cmd", bin, "default.pgo"), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// forge's own profile: the age comes from its recorded data, not the
	// file's mtime (which is "now" here, as after a fresh clone).
	var forgePGO bytes.Buffer
	if err := profilequery.WritePGO(&forgePGO, profilequery.PGOProfile{
		Services: []string{"api"}, Env: "prod",
		From: now.Add(-14 * 24 * time.Hour), To: now.Add(-6 * 24 * time.Hour),
		Edges:  []profilequery.Edge{{Caller: "main.main", Callee: "main.work", CallSiteOffset: 2, Weight: 1e9}},
		Oldest: now.Add(-14 * 24 * time.Hour), Newest: now.Add(-6*24*time.Hour - time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	write("app", forgePGO.Bytes())
	// A pprof forge did not write: no provenance comment.
	var hand bytes.Buffer
	fn := &profile.Function{ID: 1, Name: "main.main"}
	hp := &profile.Profile{
		SampleType: []*profile.ValueType{{Type: "cpu", Unit: "nanoseconds"}},
		Function:   []*profile.Function{fn},
		Location:   []*profile.Location{{ID: 1, Line: []profile.Line{{Function: fn, Line: 3}}}},
	}
	hp.Sample = []*profile.Sample{{Location: hp.Location, Value: []int64{1}}}
	if err := hp.Write(&hand); err != nil {
		t.Fatal(err)
	}
	write("hand", hand.Bytes())
	write("junk", []byte("not a profile"))

	on := PGOSettings{Enabled: true}
	cases := []struct {
		pkg  string
		pgo  PGOSettings
		want string
	}{
		{"./cmd/app", on, "cmd/app/default.pgo (from prod, data up to 6d old)"},
		{"./cmd/hand", on, "cmd/hand/default.pgo (not written by forge)"},
		{"./cmd/ctl", on, "none for cmd/ctl (forge pgo pull writes one)"},
		{"./cmd/app", PGOSettings{}, "off for cmd/app"},
	}
	for _, c := range cases {
		if got := pgoProfileNote(dir, PGOBinary{Package: c.pkg, PGO: c.pgo}, now); got != c.want {
			t.Errorf("%s: %q, want %q", c.pkg, got, c.want)
		}
	}
	if got := pgoProfileNote(dir, PGOBinary{Package: "./cmd/junk", PGO: on}, now); !strings.HasPrefix(got, "cmd/junk/default.pgo (unreadable: ") {
		t.Errorf("junk: %q", got)
	}
	var out strings.Builder
	printPGOPlan(&out, dir, &KCLEntities{Workloads: []WorkloadEntity{clusterWL("api", "k3d-x", "ns", pgoGo("./cmd/app", nil))}}, now)
	if out.String() != "[build]   pgo:      cmd/app/default.pgo (from prod, data up to 6d old)\n" {
		t.Errorf("plan line = %q", out.String())
	}
}

// Two binaries whose Dockerfile PGO args share a name ("./cmd/my-app" and
// "./cmd/my_app" are both FORGE_PGO_MY_APP) are refused only at an image
// build where disabling one would disable the other. The render never
// refuses them: a project that declares no PGO renders as it did before
// PGO existed.
func TestPGOBuildArgCollision(t *testing.T) {
	// Render: two host binaries whose args collide, no PGO declared, and
	// even one disabled — nothing builds them into one image here.
	for _, myApp := range []*PGOConfig{nil, pgoOff()} {
		if _, err := ResolvePGOBinaries(&KCLEntities{Project: "my-app", Workloads: []WorkloadEntity{
			hostWL("a", pgoGo("./cmd/my-app", myApp)),
			hostWL("b", pgoGo("./cmd/my_app", nil)),
		}}); err != nil {
			t.Errorf("render refused colliding binaries (my-app pgo %v): %v", myApp, err)
		}
	}

	dir := t.TempDir()
	for _, bin := range []string{"my-app", "my_app", "ctl"} {
		if err := os.MkdirAll(filepath.Join(dir, "cmd", bin), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cmd", bin, "main.go"), []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The project image builds every cmd/<dir>.
	for _, c := range []struct {
		name    string
		off     map[string]bool
		refused bool
	}{
		{"one of the pair off", map[string]bool{"./cmd/my-app": true}, true},
		{"both off: nothing else is affected", map[string]bool{"./cmd/my-app": true, "./cmd/my_app": true}, false},
		{"an unshared arg off", map[string]bool{"./cmd/ctl": true}, false},
		{"nothing off", nil, false},
	} {
		err := projectImagePGOCollision(dir, c.off)
		if c.refused != (err != nil) {
			t.Errorf("project image, %s: err = %v", c.name, err)
		}
		if err != nil && (!strings.Contains(err.Error(), "./cmd/my-app and ./cmd/my_app") || !strings.Contains(err.Error(), "FORGE_PGO_MY_APP")) {
			t.Errorf("project image, %s: the refusal must name both binaries and the arg: %v", c.name, err)
		}
	}
	// A DockerBuild workload's image builds what its pgo names and, on the
	// project Dockerfile, every cmd/<dir>.
	for _, c := range []struct {
		name    string
		d       DockerBuild
		refused bool
	}{
		{"pgo names both", DockerBuild{Dockerfile: "svc/Dockerfile", PGO: map[string]PGOConfig{"./cmd/my-app": *pgoOff(), "./cmd/my_app": {}}}, true},
		{"project Dockerfile builds the other", DockerBuild{Dockerfile: "./Dockerfile", Context: "./", PGO: map[string]PGOConfig{"./cmd/my-app": *pgoOff()}}, true},
		{"another Dockerfile names one", DockerBuild{Dockerfile: "svc/Dockerfile", PGO: map[string]PGOConfig{"./cmd/my-app": *pgoOff()}}, false},
		{"build_args sets the arg itself", DockerBuild{Dockerfile: "Dockerfile", BuildArgs: map[string]string{"FORGE_PGO_MY_APP": "auto"}, PGO: map[string]PGOConfig{"./cmd/my-app": *pgoOff()}}, false},
		{"nothing off", DockerBuild{Dockerfile: "Dockerfile", PGO: map[string]PGOConfig{"./cmd/my-app": {}}}, false},
	} {
		if err := serviceImagePGOCollision(dir, &c.d); c.refused != (err != nil) {
			t.Errorf("service image, %s: err = %v", c.name, err)
		}
	}
}

// The render (kcl/render.k _pgo_docker_binary) and the build
// (dockerWorkloadBinary) decide independently which binary a DockerBuild
// workload runs: one sets FORGE_PROFILES_PGO=off on the workload, the other
// passes FORGE_PGO_<BIN>=off to its build. This renders the KCL fixture
// that pins the render's side and requires the build to resolve the same
// workloads to the same disabled binary.
func TestPGODockerBinaryAgreesWithKCL(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "kcl", "tests", "positive_build_pgo_docker_binary.k"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := parseKCLEntities(renderKCLProject(t, writeKCLProject(t, string(src))))
	if err != nil {
		t.Fatal(err)
	}
	bins, err := ResolvePGOBinaries(e)
	if err != nil {
		t.Fatal(err)
	}
	var buildOff []string
	runs := map[string]string{}
	for _, b := range bins {
		for _, w := range b.Workloads {
			runs[w] = b.Package
			if !b.PGO.Enabled {
				buildOff = append(buildOff, w)
			}
		}
	}
	var renderOff []string
	for _, w := range e.Workloads {
		for _, v := range w.Spec.Env {
			if v.Name == "FORGE_PROFILES_PGO" && v.Value == "off" {
				renderOff = append(renderOff, w.Name)
			}
		}
	}
	slices.Sort(buildOff)
	slices.Sort(renderOff)
	if !slices.Equal(buildOff, renderOff) {
		t.Errorf("the build disables PGO for %v, the render for %v", buildOff, renderOff)
	}
	if want := []string{"app-bin", "app-root", "default-paths", "other-df", "rel-bin"}; !slices.Equal(buildOff, want) {
		t.Errorf("disabled workloads = %v, want %v", buildOff, want)
	}
	for w, want := range map[string]string{"rel-bin": "./cmd/app", "app-bin": "./cmd/app", "app-root": "./cmd/app", "primary": "./cmd/shop", "foreign-cmd": "", "nested": "", "other-df-unnamed": ""} {
		if runs[w] != want {
			t.Errorf("%s runs %q, want %q", w, runs[w], want)
		}
	}
}

func TestPGOCommandBinaryAndProjectPath(t *testing.T) {
	for in, want := range map[string]string{
		"/app/app": "app", "/app/bin/app": "app", "bin/app": "app", "/app/bin": "bin",
		"app": "", "./app": "", "/usr/bin/app": "", "/app/bin/x/app": "", "/app/": "", "bin/": "",
	} {
		if got := commandBinary(in); got != want {
			t.Errorf("commandBinary(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"": "", ".": "", "./": "", " . ": "", ".//": "", "Dockerfile": "Dockerfile", "./Dockerfile": "Dockerfile",
		".//Dockerfile/": "Dockerfile", "svc/./Dockerfile": "svc/Dockerfile", "/Dockerfile": "/Dockerfile", "a/../Dockerfile": "a/../Dockerfile",
	} {
		if got := pgoProjectPath(in); got != want {
			t.Errorf("pgoProjectPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizePGOPackage(t *testing.T) {
	for in, want := range map[string]string{
		"./cmd/app": "./cmd/app", "cmd/app": "./cmd/app", "./cmd/app/": "./cmd/app",
		"../sib/cmd/x": "../sib/cmd/x", "example.com/x/cmd/y": "example.com/x/cmd/y", "": "",
	} {
		if got := normalizePGOPackage(in); got != want {
			t.Errorf("normalizePGOPackage(%q) = %q, want %q", in, got, want)
		}
	}
}
