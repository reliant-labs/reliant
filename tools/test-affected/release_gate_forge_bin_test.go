// Copyright (c) 2025 Reliant Labs
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// The release gate's test-go step builds the pinned forge before `task test`
// and hands it to the tests as FORGE_BIN, so no test (cmd/reliant/cmd's
// pinnedForge) builds forge inside its own package's 5m budget: on main at
// 922d5ed89 a cold build took 4m23s and timed the package out (#2005).
func TestReleaseGateBuildsThePinnedForgeBeforeTheGoTests(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "release-gate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	step := regexp.MustCompile(`(?s)\n    test-go\)\n(.*?);;`).FindStringSubmatch(string(b))
	if step == nil {
		t.Fatal("release-gate.sh has no test-go) step")
	}
	build, test := strings.Index(step[1], "pinned_forge_bin ||"), strings.Index(step[1], "task test -- $pkgs")
	if build < 0 || test < 0 || build > test {
		t.Fatalf("the test-go step must call pinned_forge_bin (failing the step if it cannot) before task test:\n%s", step[1])
	}
	kill, err := os.ReadFile(filepath.Join(repoRoot, "cmd", "reliant", "cmd", "transcript_kill_switch_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(kill), `os.Getenv("FORGE_BIN")`) {
		t.Fatal("pinnedForge no longer reads FORGE_BIN, so the gate's prebuilt forge would go unused")
	}
}

// pinned_forge_bin itself, run against a stub go: it builds forge once and
// exports FORGE_BIN, keeps a FORGE_BIN it was given, and fails when the build does.
func TestReleaseGatePinnedForgeBin(t *testing.T) {
	testkit.Slow(t, "runs release-gate.sh's pinned_forge_bin in bash with a stub go",
		testkit.Inputs("scripts/release-gate.sh"))
	b, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "release-gate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	fn := regexp.MustCompile(`(?s)\npinned_forge_bin\(\) \{\n.*?\n\}\n`).FindString(string(b))
	if fn == "" {
		t.Fatal("release-gate.sh has no pinned_forge_bin function")
	}
	for _, tc := range []struct {
		name, given, goExit string
		wantBuilds          int
		wantOK              bool
	}{
		{"builds once and exports it", "", "0", 1, true},
		{"keeps a FORGE_BIN it was given", "/given/forge", "0", 0, true},
		{"fails when the build fails", "", "7", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			stub := "#!/usr/bin/env bash\necho \"$GOFLAGS $*\" >>\"$LOG\"\n[ \"$GO_EXIT\" = 0 ] || exit \"$GO_EXIT\"\n" +
				"while [ $# -gt 0 ]; do [ \"$1\" = -o ] && { touch \"$2\"; shift; }; shift; done\n"
			if err := os.WriteFile(filepath.Join(dir, "go"), []byte(stub), 0o755); err != nil {
				t.Fatal(err)
			}
			script := fn + "\nif pinned_forge_bin; then echo \"OK $FORGE_BIN\"; else echo FAIL; fi\n"
			cmd := exec.Command("bash", "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "LOG="+filepath.Join(dir, "log"),
				"GO_EXIT="+tc.goExit, "FORGE_BIN="+tc.given, "TMPDIR="+dir)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			log, _ := os.ReadFile(filepath.Join(dir, "log"))
			builds := strings.Count(string(log), "build -o ")
			if builds != tc.wantBuilds {
				t.Fatalf("go build ran %d times, want %d: %s", builds, tc.wantBuilds, log)
			}
			if tc.wantBuilds > 0 && !strings.Contains(string(log), "-trimpath build -o ") || tc.wantBuilds > 0 && !strings.Contains(string(log), "github.com/reliant-labs/forge/cmd/forge") {
				t.Fatalf("not a -trimpath build of the pinned forge: %s", log)
			}
			got := strings.TrimSpace(string(out))
			switch {
			case !tc.wantOK && got != "FAIL":
				t.Fatalf("out = %q, want FAIL", got)
			case tc.wantOK && tc.given != "" && got != "OK "+tc.given:
				t.Fatalf("out = %q, want the given FORGE_BIN kept", got)
			case tc.wantOK && tc.given == "" && (!strings.HasPrefix(got, "OK "+dir) || !strings.HasSuffix(got, "/forge")):
				t.Fatalf("out = %q, want FORGE_BIN exported to the built forge", got)
			}
		})
	}
}
