package hostlaunch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// writeTree writes files (slash paths relative to root) with the given bodies.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

const scaffoldedAirBuild = `cmd = "forge storage check ./tmp && CGO_ENABLED=0 go build $FORGE_GO_BUILD_FLAGS -tags=dev -buildvcs=false -o ./tmp/acme.tmp ./cmd/acme && mv ./tmp/acme.tmp ./tmp/acme"`

// Editing examples/ — a separate binary the server never imports —
// restarted the server under air three times in one demo. forge derives, at
// launch, the top-level directories that hold Go packages the binary is not
// built from, and only those: a directory with no Go code, one the config
// already excludes, a hidden one, and one holding a dependency (or a file a
// dependency embeds) stay watched.
func TestAirUnwatchedDirs(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".air.toml":                  "root = \".\"\ntmp_dir = \"tmp\"\n[build]\n  " + scaffoldedAirBuild + "\n  exclude_dir = [\"tmp\", \"frontends\", \"examples-old\"]\n",
		"cmd/acme/main.go":           "package main",
		"internal/app/app.go":        "package app",
		"gen/services/v1/svc.go":     "package v1", // a nested module joined by go.work
		"examples/demo/main.go":      "package main",
		"benchmarks/bench_test.go":   "package benchmarks",
		"tools/go.mod":               "module tools",
		"tools/deep/nested/tool.go":  "package nested",
		"docs/config.yaml":           "a: 1", // no Go: left watched
		"frontends/web/x.go":         "package x",
		"examples-old/x.go":          "package x",
		".hidden/x.go":               "package x",
		"onlytestdata/testdata/x.go": "package x", // testdata is not a package
		"assets2/embed.go":           "package assets2",
		"assets2/files/logo.txt":     "logo",
	})
	var gotDir, gotTags, gotPkg string
	list := func(_ context.Context, dir, tags, pkg string) (GoDeps, error) {
		gotDir, gotTags, gotPkg = dir, tags, pkg
		return GoDeps{
			Main: filepath.Join(root, "cmd", "acme"),
			Paths: []string{
				filepath.Join(root, "internal", "app"),
				filepath.Join(root, "gen", "services", "v1"),
				filepath.Join("/usr", "lib", "go", "src", "fmt"),
				// The root package embedding a file from assets2/ keeps
				// assets2/ watched even though no package there is imported.
				filepath.Join(root, "assets2", "files", "logo.txt"),
				filepath.Join(root, "cmd", "acme"),
			},
		}, nil
	}
	spec := RunnerSpec{Runner: "air", ProjectDir: root, GoPkg: "./cmd/acme", GoBuildFlags: []string{"-trimpath"}}
	got, err := spec.AirUnwatchedDirs(context.Background(), list)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"benchmarks", "examples", "tools"}; !reflect.DeepEqual(got, want) {
		t.Errorf("AirUnwatchedDirs = %q, want %q", got, want)
	}
	if gotDir != root || gotPkg != "./cmd/acme" || gotTags != "dev" {
		t.Errorf("go list ran in %q for %q with tags %q, want %q, ./cmd/acme, dev", gotDir, gotPkg, gotTags, root)
	}
}

// forge narrows the watch set only where it can see what the binary is
// built from; everywhere else air runs as configured, and go list is not run.
func TestAirUnwatchedDirsLeavesAirAlone(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"scaffolded.toml":    "[build]\n  " + scaffoldedAirBuild + "\n",
		"include-dir.toml":   "[build]\n  " + scaffoldedAirBuild + "\n  include_dir = [\"cmd\", \"internal\"]\n",
		"make.toml":          "[build]\n  cmd = \"make build\"\n",
		"cmd/acme/main.go":   "package main",
		"examples/x/x.go":    "package x",
		"sibling/cmd/a/a.go": "package main",
	})
	called := false
	list := func(context.Context, string, string, string) (GoDeps, error) {
		called = true
		return GoDeps{Main: filepath.Join(root, "cmd", "acme"), Paths: []string{filepath.Join(root, "cmd", "acme")}}, nil
	}
	for _, tc := range []struct {
		name string
		spec RunnerSpec
	}{
		{"not air", RunnerSpec{Runner: "go-run", ProjectDir: root, GoPkg: "./cmd/acme", AirConfig: "scaffolded.toml"}},
		{"no Go build", RunnerSpec{Runner: "air", ProjectDir: root, AirConfig: "scaffolded.toml"}},
		{"missing config", RunnerSpec{Runner: "air", ProjectDir: root, GoPkg: "./cmd/acme", AirConfig: "nope.toml"}},
		{"config chooses its watch set", RunnerSpec{Runner: "air", ProjectDir: root, GoPkg: "./cmd/acme", AirConfig: "include-dir.toml"}},
		{"build command does not name the package", RunnerSpec{Runner: "air", ProjectDir: root, GoPkg: "./cmd/acme", AirConfig: "make.toml"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called = false
			got, err := tc.spec.AirUnwatchedDirs(context.Background(), list)
			if err != nil || got != nil || called {
				t.Errorf("AirUnwatchedDirs = %q, %v (go list run: %v), want nothing and no go list", got, err, called)
			}
		})
	}

	t.Run("binary outside the air root", func(t *testing.T) {
		spec := RunnerSpec{Runner: "air", ProjectDir: root, WorkingDir: "sibling", GoPkg: "./cmd/acme", AirConfig: filepath.Join(root, "scaffolded.toml")}
		got, err := spec.AirUnwatchedDirs(context.Background(), list)
		if err != nil || got != nil {
			t.Errorf("AirUnwatchedDirs = %q, %v, want nothing: the binary is not built from the air root", got, err)
		}
	})

	t.Run("go list fails", func(t *testing.T) {
		boom := errors.New("go list ./cmd/acme: no Go files")
		spec := RunnerSpec{Runner: "air", ProjectDir: root, GoPkg: "./cmd/acme", AirConfig: "scaffolded.toml"}
		got, err := spec.AirUnwatchedDirs(context.Background(), func(context.Context, string, string, string) (GoDeps, error) { return GoDeps{}, boom })
		if !errors.Is(err, boom) || got != nil {
			t.Errorf("AirUnwatchedDirs = %q, %v, want the go list error", got, err)
		}
	})
}

// A custom root and the launch directory use different exclusion bases.
// Do not derive bare names that could exclude an imported sibling tree.
func TestAirUnwatchedDirsCustomRootLeavesAirAlone(t *testing.T) {
	for _, rootKind := range []string{"relative", "absolute", "parent"} {
		t.Run(rootKind, func(t *testing.T) {
			project := t.TempDir()
			root := "subdir"
			if rootKind == "absolute" {
				root = filepath.ToSlash(filepath.Join(project, "subdir"))
			} else if rootKind == "parent" {
				root = ".."
			}
			writeTree(t, project, map[string]string{
				".air.toml":               "root = '" + root + "'\n[build]\n cmd = 'go build ./subdir/cmd/acme'\n exclude_dir = ['tmp', 'my-own']\n",
				"subdir/cmd/acme/main.go": "package main",
				"subdir/examples/main.go": "package main",
				"examples/dependency.go":  "package examples",
			})
			spec := RunnerSpec{Runner: "air", ProjectDir: project, GoPkg: "./subdir/cmd/acme"}
			got, err := spec.AirUnwatchedDirs(context.Background(), func(context.Context, string, string, string) (GoDeps, error) {
				t.Fatal("custom roots must not run go list")
				return GoDeps{}, nil
			})
			if err != nil || len(got) != 0 {
				t.Fatalf("AirUnwatchedDirs = %q, %v, want no exclusions", got, err)
			}
			spec.AirExcludeDirs = got
			cmd, err := BuildCmd(context.Background(), "api", spec)
			if err != nil {
				t.Fatal(err)
			}
			if want := []string{"air", "-c", DefaultAirConfig}; !reflect.DeepEqual(cmd.Args, want) {
				t.Errorf("argv = %q, want unchanged %q", cmd.Args, want)
			}
		})
	}
}

// air's -build.exclude_dir REPLACES the config's exclude_dir, so the flag
// forge passes carries the config's own entries first: a user's edits to
// .air.toml stay in force, and the file is never rewritten.
func TestBuildCmdAirExcludeDirKeepsTheConfigsList(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"own.toml":     "[build]\n  exclude_dir = [\"tmp\", \"examples\", \"my-own\"]\n",
		"default.toml": "tmp_dir = \"build\"\n[build]\n  cmd = \"go build ./cmd/acme\"\n",
		"broken.toml":  "[build\n",
	})
	for _, tc := range []struct {
		name, cfg string
		extra     []string
		want      []string
	}{
		{"merged after the config's entries", "own.toml", []string{"examples", "tools"},
			[]string{"air", "-c", "own.toml", "-build.exclude_dir", "tmp,examples,my-own,tools"}},
		{"air's default list, tmp renamed to tmp_dir", "default.toml", []string{"tools"},
			[]string{"air", "-c", "default.toml", "-build.exclude_dir", "assets,build,vendor,testdata,tools"}},
		{"nothing new: no flag", "own.toml", []string{"examples"}, []string{"air", "-c", "own.toml"}},
		{"nothing derived: no flag", "own.toml", nil, []string{"air", "-c", "own.toml"}},
		{"unreadable config: no flag", "broken.toml", []string{"tools"}, []string{"air", "-c", "broken.toml"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := BuildCmd(context.Background(), "api", RunnerSpec{Runner: "air", AirConfig: tc.cfg, ProjectDir: root, AirExcludeDirs: tc.extra})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cmd.Args, tc.want) {
				t.Errorf("argv = %q, want %q", cmd.Args, tc.want)
			}
		})
	}
}

func TestBuildTagsAndGoBuildArgs(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, ""},
		{[]string{"-trimpath"}, ""},
		{[]string{"-tags", "a,b"}, "a,b"},
		{[]string{"-tags=a", "--tags=b"}, "b"},
		{[]string{"-tags='x y'"}, "x y"},
	} {
		if got := buildTags(tc.args); got != tc.want {
			t.Errorf("buildTags(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
	got := goBuildArgs("forge storage check ./tmp && CGO_ENABLED=0 go build -tags dev -o ./tmp/a ./cmd/a && mv a b")
	if want := []string{"-tags", "dev", "-o", "./tmp/a", "./cmd/a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("goBuildArgs = %q, want %q", got, want)
	}
}

// The real go list, over a go.work that joins a nested gen/ module the
// binary imports: gen/ is compared by directory, so it stays watched, while
// examples/ (its own main) and tools/ (a module outside the workspace) do
// not.
func TestAirUnwatchedDirsRealGoList(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go list over a temp workspace; runs in task test")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".air.toml":             "[build]\n  cmd = \"go build -o ./tmp/acme ./cmd/acme\"\n  exclude_dir = [\"tmp\"]\n",
		"go.work":               "go 1.21\n\nuse (\n\t.\n\t./gen\n)\n",
		"go.mod":                "module example.com/acme\n\ngo 1.21\n",
		"gen/go.mod":            "module example.com/acme/gen\n\ngo 1.21\n",
		"gen/api/api.go":        "package api\n\nconst Name = \"acme\"\n",
		"internal/app/app.go":   "package app\n\nimport \"example.com/acme/gen/api\"\n\nfunc Name() string { return api.Name }\n",
		"cmd/acme/main.go":      "package main\n\nimport \"example.com/acme/internal/app\"\n\nfunc main() { println(app.Name()) }\n",
		"examples/demo/main.go": "package main\n\nfunc main() {}\n",
		"tools/go.mod":          "module tools\n\ngo 1.21\n",
		"tools/tool.go":         "package tools\n",
	})
	t.Setenv("GOWORK", "")
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOTOOLCHAIN", "local")
	spec := RunnerSpec{Runner: "air", ProjectDir: root, GoPkg: "./cmd/acme"}
	got, err := spec.AirUnwatchedDirs(context.Background(), GoListDeps)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"examples", "tools"}; !reflect.DeepEqual(got, want) {
		t.Errorf("AirUnwatchedDirs = %q, want %q", got, want)
	}
}
