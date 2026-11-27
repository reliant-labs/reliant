package selfupdate

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func noEnv() detectEnv { return detectEnv{} }

func writeExe(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDetectSelfOwned(t *testing.T) {
	t.Parallel()
	exe := writeExe(t, filepath.Join(t.TempDir(), "bin", "app"))
	in, err := detect(DetectOptions{Path: exe}, noEnv())
	if err != nil {
		t.Fatal(err)
	}
	if !in.SelfOwned() || in.Kind != SelfOwned {
		t.Fatalf("got %+v, want self-owned", in)
	}
}

func TestDetectStampsAreAuthoritativeAndOrdered(t *testing.T) {
	t.Parallel()
	env := func(v string) func() (string, bool) { return func() (string, bool) { return v, v != "" } }

	cases := []struct {
		name      string
		stamp     string
		file      string
		envValue  string
		managed   bool
		wantKind  Kind
		wantOwner string
	}{
		{name: "build stamp", stamp: "homebrew", wantKind: Stamped, wantOwner: "homebrew"},
		{name: "stamp file", file: "aur\n", wantKind: Stamped, wantOwner: "aur"},
		{name: "empty stamp file still refuses", file: "\n", wantKind: Stamped, wantOwner: "unnamed owner"},
		{name: "environment", envValue: "systemd-unit", wantKind: Stamped, wantOwner: "systemd-unit"},
		{name: "stamp beats managed", stamp: "deb", managed: true, wantKind: Stamped, wantOwner: "deb"},
		{name: "managed predicate", managed: true, wantKind: Managed, wantOwner: "cloud"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := writeExe(t, filepath.Join(dir, "app"))
			if tc.file != "" {
				if err := os.WriteFile(p+".managed-by", []byte(tc.file), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			in, err := detect(DetectOptions{
				Path:    p,
				Managed: func() (string, bool) { return "cloud", tc.managed },
			}, detectEnv{stamp: tc.stamp, lookupEnv: env(tc.envValue)})
			if err != nil {
				t.Fatal(err)
			}
			if in.Kind != tc.wantKind || in.Owner != tc.wantOwner || in.SelfOwned() {
				t.Fatalf("got %+v, want %s owned by %q", in, tc.wantKind, tc.wantOwner)
			}
		})
	}
}

func TestDetectPathHeuristics(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cases := []struct {
		rel      string
		wantKind Kind
		wantHint string
	}{
		{rel: "Reliant.app/Contents/Resources/bin/reliant", wantKind: Bundle},
		{rel: "opt/Reliant/resources/server/reliant-backend", wantKind: Bundle},
		{rel: "usr/local/Cellar/reliant/1.2.0/bin/reliant", wantKind: PackageManager, wantHint: "brew upgrade reliant"},
		{rel: "Caskroom/reliant/1.2.0/reliant", wantKind: PackageManager, wantHint: "brew upgrade reliant"},
	}
	for _, tc := range cases {
		t.Run(tc.rel, func(t *testing.T) {
			exe := writeExe(t, filepath.Join(root, filepath.FromSlash(tc.rel)))
			in, err := detect(DetectOptions{Path: exe}, noEnv())
			if err != nil {
				t.Fatal(err)
			}
			if in.Kind != tc.wantKind || in.Hint != tc.wantHint {
				t.Fatalf("got %+v, want %s hint %q", in, tc.wantKind, tc.wantHint)
			}
		})
	}
}

func TestDetectResolvesSymlinksBeforeJudging(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	root := t.TempDir()
	real := writeExe(t, filepath.Join(root, "Cellar", "reliant", "1.0", "bin", "reliant"))
	link := filepath.Join(root, "bin", "reliant")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	in, err := detect(DetectOptions{Path: link}, noEnv())
	if err != nil {
		t.Fatal(err)
	}
	if in.Kind != PackageManager {
		t.Fatalf("a symlink into the Cellar must be Homebrew's, got %+v", in)
	}
	if want, _ := filepath.EvalSymlinks(real); in.Path != want {
		t.Fatalf("Path = %s, want the resolved %s", in.Path, want)
	}
}

func TestDetectPathMatchersWithoutFiles(t *testing.T) {
	t.Parallel()
	if _, ok := bundlePath("/tmp/.mount_ReliantXyz/usr/bin/reliant"); !ok {
		t.Error("an AppImage mount is a bundle")
	}
	for p, owner := range map[string]string{
		"/snap/reliant/12/bin/reliant":     "snap",
		"/nix/store/abc-reliant/bin/x":     "nix",
		"/home/linuxbrew/.linuxbrew/bin/x": "homebrew",
		"/opt/homebrew/bin/reliant":        "homebrew",
	} {
		if got, _, _, ok := packageManagerPath(p); !ok || got != owner {
			t.Errorf("packageManagerPath(%s) = %q, %v; want %q", p, got, ok, owner)
		}
	}
	for p, want := range map[string]bool{
		"/usr/bin/reliant":         true,
		"/usr/lib/reliant/reliant": true,
		"/usr/local/bin/reliant":   false,
		"/home/u/.local/bin/x":     false,
	} {
		if systemPath(p) != want {
			t.Errorf("systemPath(%s) = %v, want %v", p, !want, want)
		}
	}
}

func TestDetectAsksThePackageDatabaseOnlyForSystemPaths(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("no /bin/sh")
	}
	asked := ""
	env := detectEnv{runOwner: func(p string) (string, string, bool) {
		asked = p
		return "dpkg:dash", "sudo apt-get install --only-upgrade dash", true
	}}
	in, err := detect(DetectOptions{Path: "/bin/sh"}, env)
	if err != nil {
		t.Fatal(err)
	}
	if asked == "" || in.Kind != PackageManager || in.Owner != "dpkg:dash" {
		t.Fatalf("got %+v (asked %q), want the package database's answer", in, asked)
	}

	asked = ""
	exe := writeExe(t, filepath.Join(t.TempDir(), "app"))
	if _, err := detect(DetectOptions{Path: exe}, env); err != nil {
		t.Fatal(err)
	}
	if asked != "" {
		t.Fatalf("asked the package database about %s, which is not a system path", asked)
	}
}

func TestDetectReadOnlyDirectory(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a non-root unix user for directory permissions to bite")
	}
	dir := filepath.Join(t.TempDir(), "ro")
	exe := writeExe(t, filepath.Join(dir, "app"))
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	in, err := detect(DetectOptions{Path: exe}, noEnv())
	if err != nil {
		t.Fatal(err)
	}
	if in.Kind != ReadOnly {
		t.Fatalf("got %+v, want read-only", in)
	}
}

func TestNotSelfOwnedError(t *testing.T) {
	t.Parallel()
	err := error(&NotSelfOwnedError{Install: Install{Path: "/x", Kind: PackageManager, Owner: "homebrew", Hint: "brew upgrade x", Reason: "Cellar"}})
	if !errors.Is(err, ErrNotSelfOwned) {
		t.Fatal("errors.Is(ErrNotSelfOwned) must hold")
	}
	var target *NotSelfOwnedError
	if !errors.As(err, &target) || target.Install.Hint != "brew upgrade x" {
		t.Fatal("errors.As must reach the Install")
	}
}

func TestDetectRefusesContainers(t *testing.T) {
	t.Parallel()
	p := writeExe(t, filepath.Join(t.TempDir(), "bin", "app"))
	env := detectEnv{container: func() (string, string, string, bool) {
		return "kubernetes", "roll the workload", "running in a Kubernetes pod", true
	}}
	in, err := detect(DetectOptions{Path: p}, env)
	if err != nil {
		t.Fatal(err)
	}
	if in.Kind != Managed || in.Owner != "kubernetes" || in.Hint != "roll the workload" || in.SelfOwned() {
		t.Fatalf("got %+v, want managed by kubernetes", in)
	}
	// A stamp still wins: it is authoritative and names the real owner.
	in, err = detect(DetectOptions{Path: p}, detectEnv{stamp: "homebrew", container: env.container})
	if err != nil {
		t.Fatal(err)
	}
	if in.Kind != Stamped || in.Owner != "homebrew" {
		t.Fatalf("got %+v, want the stamp to win", in)
	}
}
