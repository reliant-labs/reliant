package hostlaunch

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

var coverFlags = []string{"-cover", "-covermode=atomic", "-coverpkg=example.com/shop/internal/..."}

// go-run compiles on launch, so the cover flags go on its `go run`, before
// the package; every other runner's argv is unchanged.
func TestBuildCmd_GoRunCarriesBuildFlags(t *testing.T) {
	cmd, err := BuildCmd(context.Background(), "api", RunnerSpec{GoPkg: "./cmd/shop", Args: []string{"server"}, GoBuildFlags: coverFlags})
	if err != nil {
		t.Fatal(err)
	}
	want := append(append([]string{"go", "run"}, coverFlags...), "./cmd/shop", "server")
	if !slices.Equal(cmd.Args, want) {
		t.Errorf("argv = %v, want %v", cmd.Args, want)
	}
	cmd, err = BuildCmd(context.Background(), "api", RunnerSpec{Runner: "binary", GoPkg: "./cmd/shop", GoBuildFlags: coverFlags})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(cmd.Args, "-cover") {
		t.Errorf("binary runs what forge build compiled; argv %v", cmd.Args)
	}
}

func TestWithCoverage(t *testing.T) {
	const dir = "/p/.forge/tests/covdata/dev/api"
	cases := []struct {
		name string
		spec RunnerSpec
		env  []string
		want []string
	}{
		{"not instrumented", RunnerSpec{Runner: "air", GoBuildFlags: coverFlags}, []string{"A=1"}, []string{"A=1"}},
		{"go-run", RunnerSpec{GoPkg: "./cmd/shop", CoverDir: dir, GoBuildFlags: coverFlags}, []string{"A=1"}, []string{"A=1", "GOCOVERDIR=" + dir}},
		{"binary", RunnerSpec{Runner: "binary", CoverDir: dir}, nil, []string{"GOCOVERDIR=" + dir}},
		{"air", RunnerSpec{Runner: "air", CoverDir: dir, GoBuildFlags: coverFlags}, nil, []string{"GOCOVERDIR=" + dir}},
		{"the user's own wins", RunnerSpec{Runner: "air", CoverDir: dir, GoBuildFlags: coverFlags}, []string{"GOCOVERDIR=/mine"},
			[]string{"GOCOVERDIR=/mine"}},
		{"a spec.command runs verbatim", RunnerSpec{Command: []string{"sh"}, CoverDir: dir}, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := slices.Clone(tc.env)
			if got := WithCoverage(tc.spec, tc.env); !slices.Equal(got, tc.want) {
				t.Errorf("env = %v, want %v", got, tc.want)
			}
			if !slices.Equal(in, tc.env) {
				t.Errorf("WithCoverage modified its input")
			}
		})
	}
}

// air gets the build's flags (declared GoBuild.flags, then a cover build's)
// through FORGE_GO_BUILD_FLAGS, which its config's `go build` expands; a
// value the user exported wins, and a flag containing whitespace, which the
// variable's word splitting would break apart, is left out and named.
func TestWithGoBuildFlags(t *testing.T) {
	declared := []string{"-trimpath"}
	cases := []struct {
		name        string
		spec        RunnerSpec
		env         []string
		want        []string
		wantDropped []string
	}{
		{"air gets the declared flags", RunnerSpec{Runner: "air", GoBuildFlags: declared}, []string{"A=1"}, []string{"A=1", "FORGE_GO_BUILD_FLAGS=-trimpath"}, nil},
		{"air, declared then cover", RunnerSpec{Runner: "air", GoBuildFlags: append(slices.Clone(declared), coverFlags...)}, nil,
			[]string{"FORGE_GO_BUILD_FLAGS=-trimpath -cover -covermode=atomic -coverpkg=example.com/shop/internal/..."}, nil},
		{"the user's own wins", RunnerSpec{Runner: "air", GoBuildFlags: declared}, []string{"FORGE_GO_BUILD_FLAGS=-race"}, []string{"FORGE_GO_BUILD_FLAGS=-race"}, nil},
		{"a flag with a space is left out", RunnerSpec{Runner: "air", GoBuildFlags: []string{"-trimpath", "-ldflags=-s -w"}}, nil,
			[]string{"FORGE_GO_BUILD_FLAGS=-trimpath"}, []string{"-ldflags=-s -w"}},
		{"go-run carries them on argv instead", RunnerSpec{GoPkg: "./cmd/shop", GoBuildFlags: declared}, []string{"A=1"}, []string{"A=1"}, nil},
		{"no flags", RunnerSpec{Runner: "air"}, nil, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := slices.Clone(tc.env)
			got, dropped := WithGoBuildFlags(tc.spec, tc.env)
			if !slices.Equal(got, tc.want) || !slices.Equal(dropped, tc.wantDropped) {
				t.Errorf("env = %q, dropped %q; want %q, dropped %q", got, dropped, tc.want, tc.wantDropped)
			}
			if !slices.Equal(in, tc.env) {
				t.Errorf("WithGoBuildFlags modified its input")
			}
		})
	}
}

// A declared flag reaches go-run's argv as ONE argument however many spaces
// it holds: exec passes argv verbatim, with no shell to split it.
func TestBuildCmd_GoRunKeepsAFlagWithSpacesWhole(t *testing.T) {
	flags := []string{"-trimpath", "-ldflags=-s -w -X main.version=dev"}
	cmd, err := BuildCmd(context.Background(), "api", RunnerSpec{GoPkg: "./cmd/shop", Args: []string{"server"}, GoBuildFlags: flags})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"go", "run", "-trimpath", "-ldflags=-s -w -X main.version=dev", "./cmd/shop", "server"}
	if !slices.Equal(cmd.Args, want) {
		t.Errorf("argv = %q, want %q", cmd.Args, want)
	}
}

// The scaffolded .air.toml expands $FORGE_GO_BUILD_FLAGS in both its build
// commands, so forge's cover flags reach air's `go build`; an older config
// does not, and forge says so instead of reporting coverage it never built.
func TestAirExpandsBuildFlags(t *testing.T) {
	tmpl, err := filepath.Abs(filepath.Join("..", "templates", "project", "air.toml.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	if !(RunnerSpec{AirConfig: tmpl}).AirExpandsBuildFlags() {
		t.Errorf("the scaffolded air.toml.tmpl does not expand $%s", GoBuildFlagsEnv)
	}
	dir := t.TempDir()
	old := filepath.Join(dir, ".air.toml")
	if err := os.WriteFile(old, []byte("[build]\ncmd = \"go build -o ./tmp/app ./cmd/app\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if (RunnerSpec{AirConfig: old}).AirExpandsBuildFlags() {
		t.Error("a config without $FORGE_GO_BUILD_FLAGS was read as expanding it")
	}
	if (RunnerSpec{AirConfig: filepath.Join(dir, "missing.toml")}).AirExpandsBuildFlags() {
		t.Error("an unreadable config was read as expanding it")
	}
}
