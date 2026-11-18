package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A server scaffold keeps its version vars in <module>/cmd/<name>/cmd
// (cmd-tree-version.go.tmpl), and `forge build` stamped only main.*: every
// such binary reported version "dev", and control-plane's prod Sentry events
// arrived as `control-plane@dev`.
func TestVersionStampPackages(t *testing.T) {
	t.Parallel()
	const mod = "github.com/acme/demo"
	cases := []struct {
		name, module, cmd string
		want              []string
	}{
		{"scaffold server cmd", mod, "./cmd/demo", []string{"main", mod + "/cmd/demo/cmd"}},
		{"uncleaned relative cmd", mod, "./cmd/demo/", []string{"main", mod + "/cmd/demo/cmd"}},
		{"module root", mod, ".", []string{"main", mod + "/cmd"}},
		{"absolute import path in module", mod, mod + "/cmd/demo", []string{"main", mod + "/cmd/demo/cmd"}},
		{"absolute import path outside module", mod, "github.com/other/tool", []string{"main"}},
		{"no go.mod", "", "./cmd/demo", []string{"main"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := versionStampPackages(tc.module, tc.cmd)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("versionStampPackages(%q, %q) = %v, want %v", tc.module, tc.cmd, got, tc.want)
			}
		})
	}
}

func TestVersionStampLdflags(t *testing.T) {
	t.Parallel()
	got := versionStampLdflags("github.com/acme/demo", "./cmd/demo", versionInfo{version: "v1.2.3", commit: "abc", date: "2026-10-08"})
	for _, want := range []string{
		"-X main.version=v1.2.3", "-X main.commit=abc", "-X main.date=2026-10-08",
		"-X github.com/acme/demo/cmd/demo/cmd.version=v1.2.3",
		"-X github.com/acme/demo/cmd/demo/cmd.commit=abc",
		"-X github.com/acme/demo/cmd/demo/cmd.date=2026-10-08",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ldflags %q missing %q", got, want)
		}
	}
}

// End to end: a binary laid out like the scaffold, built by buildGoTarget,
// reports the stamped version from the cobra-tree package AND from main.
func TestBuildGoTarget_StampsScaffoldVersionPackage(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: runs a real go build; runs in task test")
	}
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
	write("go.mod", "module example.com/demo\n\ngo 1.22\n")
	write("cmd/demo/cmd/version.go", "package cmd\n\nvar (\n\tversion = \"dev\"\n\tcommit  = \"none\"\n)\n\nfunc Version() string { return version + \"+\" + commit }\n")
	write("cmd/demo/main.go", "package main\n\nimport (\n\t\"fmt\"\n\n\t\"example.com/demo/cmd/demo/cmd\"\n)\n\nvar version = \"dev\"\n\nfunc main() { fmt.Println(cmd.Version(), version) }\n")

	t.Chdir(dir)
	res := buildGoTarget(context.Background(), goBuildTarget{cmd: "./cmd/demo", outputName: "demo"}, "bin", false, "",
		versionInfo{version: "v9.9.9", commit: "deadbeef", date: "2026-10-08"}, buildMemoryCaps{})
	if res.err != nil {
		t.Fatalf("build: %v", res.err)
	}
	bin := filepath.Join(dir, "bin", "demo")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	out, err := exec.Command(bin).Output()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "v9.9.9+deadbeef v9.9.9" {
		t.Errorf("binary reported %q, want %q: the cobra-tree package must be stamped, not only main", got, "v9.9.9+deadbeef v9.9.9")
	}
}
