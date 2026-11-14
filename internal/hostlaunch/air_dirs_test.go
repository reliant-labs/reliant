package hostlaunch

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// An air older than v1.63.7 creates its tmp_dir with a non-recursive mkdir,
// so in a fresh checkout (no tmp/ yet) `tmp_dir = "tmp/admin-server"` failed
// before the first build and the workload never came up. forge creates the
// directories the config writes into before it launches air. These pin which
// directories that is: resolved the way air resolves them, and never one
// outside the directory air runs in.
func TestAirDirs(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("per-service.toml", `root = "."
tmp_dir = "tmp/admin-server"
[build]
  bin = "./tmp/admin-server/control-plane"
  cmd = "scripts/air-build.sh ./cmd/control-plane ./tmp/admin-server/control-plane"
`)
	write("defaults.toml", "[build]\n  cmd = \"go build -o ./bin/out/acme ./cmd/acme\"\n  entrypoint = [\"./build/acme\", \"server\"]\n")
	write("rooted.toml", "root = \"svc\"\ntmp_dir = \"tmp/x\"\n[build]\n  entrypoint = [\"app\"]\n")
	// An absolute path on this OS ("/elsewhere" is drive-relative on
	// Windows, which air joins under root), as a TOML literal string so a
	// Windows path's backslashes need no escaping.
	elsewhere := filepath.Join(filepath.Dir(dir), "elsewhere", "tmp")
	write("outside.toml", "tmp_dir = '"+elsewhere+"'\n[build]\n  bin = \"../sibling/bin/app\"\n  cmd = \"go build -o=./dist/app . && cp -o x y\"\n")
	write("broken.toml", "[build\n")

	for _, tc := range []struct {
		name string
		spec RunnerSpec
		want []string
	}{
		{
			name: "per-service tmp_dir, bin inside it: one directory",
			spec: RunnerSpec{Runner: "air", ProjectDir: dir, AirConfig: "per-service.toml"},
			want: []string{filepath.Join(dir, "tmp", "admin-server")},
		},
		{
			name: "air's default tmp, the go build -o output and the entrypoint's directory",
			spec: RunnerSpec{Runner: "air", ProjectDir: dir, AirConfig: "defaults.toml"},
			want: []string{filepath.Join(dir, "tmp"), filepath.Join(dir, "build"), filepath.Join(dir, "bin", "out")},
		},
		{
			name: "relative to root; a bare entrypoint names no directory",
			spec: RunnerSpec{Runner: "air", ProjectDir: dir, AirConfig: "rooted.toml"},
			want: []string{filepath.Join(dir, "svc", "tmp", "x")},
		},
		{
			name: "nothing outside the directory air runs in",
			spec: RunnerSpec{Runner: "air", ProjectDir: dir, AirConfig: "outside.toml"},
			want: []string{filepath.Join(dir, "dist")},
		},
		{
			name: "relative to the working dir",
			spec: RunnerSpec{Runner: "air", ProjectDir: filepath.Dir(dir), WorkingDir: filepath.Base(dir), AirConfig: "per-service.toml"},
			want: []string{filepath.Join(dir, "tmp", "admin-server")},
		},
	} {
		got, err := tc.spec.AirDirs()
		if err != nil {
			t.Errorf("%s: AirDirs() error: %v", tc.name, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: AirDirs() = %q, want %q", tc.name, got, tc.want)
		}
	}

	if _, err := (RunnerSpec{Runner: "air", ProjectDir: dir, AirConfig: "broken.toml"}).AirDirs(); err == nil {
		t.Error("an unparsable config must be an error, not an empty list")
	}
	if _, err := (RunnerSpec{Runner: "air", ProjectDir: dir, AirConfig: "nope.toml"}).AirDirs(); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing config: err = %v, want fs.ErrNotExist", err)
	}
}

func TestPrepareAirDirs(t *testing.T) {
	dir := t.TempDir()
	cfg := "tmp_dir = \"tmp/api\"\n[build]\n  bin = \"./tmp/api/app\"\n"
	if err := os.WriteFile(filepath.Join(dir, ".air.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := RunnerSpec{Runner: "air", ProjectDir: dir}
	for i := range 2 { // the second run finds it there: a no-op, not an error
		if err := spec.PrepareAirDirs(); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
		if st, err := os.Stat(filepath.Join(dir, "tmp", "api")); err != nil || !st.IsDir() {
			t.Fatalf("run %d: tmp/api not created: %v", i+1, err)
		}
	}
}
