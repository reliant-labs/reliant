package goexec

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestCheckEnv_SharesTheBuildCacheAcrossDirectories is the property CheckEnv
// exists for, end to end: one module at two paths (two worktrees, or a
// checkout and verify-generated's scratch copy) compiles to ONE build-cache
// entry under CheckEnv, and to two under plain Env. `go list -export` names
// the cache file a package's export data was written to, so equal names mean
// the second directory reused the first's compile.
func TestCheckEnv_SharesTheBuildCacheAcrossDirectories(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a module with the go toolchain twice per mode; runs in task test")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go on PATH")
	}
	// The test's own GOFLAGS must not decide the answer either way.
	t.Setenv("GOFLAGS", "")
	parent := t.TempDir()
	// Unique content: the cache must not already hold this package from an
	// earlier run, or both modes would agree for the wrong reason.
	src := "package m\n\n// " + filepath.Base(parent) + "\nfunc F() int { return 1 }\n"
	var dirs []string
	for _, name := range []string{"worktree-a", "worktree-b"} {
		dir := filepath.Join(parent, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for file, body := range map[string]string{"go.mod": "module example.com/m\n\ngo 1.22\n", "m.go": src} {
			if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		dirs = append(dirs, dir)
	}
	export := func(dir string, env []string) string {
		t.Helper()
		cmd := exec.Command("go", "list", "-export", "-f", "{{.Export}}", ".")
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go list -export in %s: %v\n%s", dir, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if a, b := export(dirs[0], Env("GOFLAGS=-trimpath=false")), export(dirs[1], Env("GOFLAGS=-trimpath=false")); a == b {
		t.Fatalf("without -trimpath both directories compiled to %s: the cache key no longer includes the directory, so this test proves nothing", a)
	}
	if a, b := export(dirs[0], CheckEnv()), export(dirs[1], CheckEnv()); a != b {
		t.Errorf("under CheckEnv the two directories compiled to different cache entries:\n  %s\n  %s\nso a second worktree recompiles everything", a, b)
	}
}

func TestWithTrimpath_MergesIntoTheEffectiveGOFLAGS(t *testing.T) {
	for _, tc := range []struct {
		name      string
		env       []string
		fileFlags string // what `go env -w` / go.env set
		want      []string
	}{
		{
			name: "nothing set anywhere: -trimpath alone",
			env:  []string{"HOME=/h"},
			want: []string{"HOME=/h", "GOFLAGS=-trimpath"},
		},
		{
			// The env's GOFLAGS replaces the file layer for cmd/go, so the
			// merge must carry the file layer's flags or it drops them.
			name:      "no env GOFLAGS: the go env -w flags are carried over",
			env:       []string{"HOME=/h"},
			fileFlags: "-tags=integration",
			want:      []string{"HOME=/h", "GOFLAGS=-tags=integration -trimpath"},
		},
		{
			name:      "env GOFLAGS wins over the file layer, as in cmd/go",
			env:       []string{"GOFLAGS=-mod=vendor", "A=1"},
			fileFlags: "-tags=ignored",
			want:      []string{"A=1", "GOFLAGS=-mod=vendor -trimpath"},
		},
		{
			name:      "an empty env GOFLAGS is unset to cmd/go: the file layer applies",
			env:       []string{"GOFLAGS="},
			fileFlags: "-tags=x",
			want:      []string{"GOFLAGS=-tags=x -trimpath"},
		},
		{
			name: "already -trimpath in the env: unchanged",
			env:  []string{"GOFLAGS=-trimpath -tags=x"},
			want: []string{"GOFLAGS=-trimpath -tags=x"},
		},
		{
			// The Reliant workspace image's go.env.
			name:      "already -trimpath in go.env: unchanged",
			env:       []string{"HOME=/h"},
			fileFlags: "-trimpath",
			want:      []string{"HOME=/h"},
		},
		{
			name: "-trimpath=false is an opt-out and is respected",
			env:  []string{"GOFLAGS=--trimpath=false"},
			want: []string{"GOFLAGS=--trimpath=false"},
		},
		{
			name: "the last GOFLAGS assignment is the one merged, and the only one left",
			env:  []string{"GOFLAGS=-tags=a", "GOFLAGS=-tags=b"},
			want: []string{"GOFLAGS=-tags=b -trimpath"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := append([]string(nil), tc.env...)
			got := withTrimpath(tc.env, func() string { return tc.fileFlags })
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("withTrimpath(%q, file=%q) = %q, want %q", tc.env, tc.fileFlags, got, tc.want)
			}
			if !reflect.DeepEqual(tc.env, base) {
				t.Errorf("env was mutated: %q, was %q", tc.env, base)
			}
		})
	}
}
