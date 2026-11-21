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
		{"air", RunnerSpec{Runner: "air", CoverDir: dir, GoBuildFlags: coverFlags}, nil,
			[]string{"GOCOVERDIR=" + dir, "FORGE_GO_BUILD_FLAGS=-cover -covermode=atomic -coverpkg=example.com/shop/internal/..."}},
		{"the user's own wins", RunnerSpec{Runner: "air", CoverDir: dir, GoBuildFlags: coverFlags}, []string{"GOCOVERDIR=/mine", "FORGE_GO_BUILD_FLAGS=-race"},
			[]string{"GOCOVERDIR=/mine", "FORGE_GO_BUILD_FLAGS=-race"}},
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
