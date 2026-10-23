package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestShellBuild_CwdAndVerbatimCmd verifies the ShellBuild contract through the
// single dispatcher (buildExternalServices), against a REAL `sh -c`: the command
// runs from the resolved cwd (here the project root, since the ShellBuild
// declares no cwd), and the command string reaches the shell byte-for-byte.
//
// Two things are asserted that the substitution pass made impossible, and both
// are the point of the change:
//
//   - A `${FOO}` forge does not own arrives at the shell AS `${FOO}`. Under
//     substitution a known token was rewritten before the shell ever saw it, so
//     the script the author wrote was not the script that ran.
//   - `$PWD` works. The old test had to use `$(pwd)` instead and said why: the
//     substitution pass consumed a bare `$PWD` as an unknown token. That is a
//     forge bug visible in its own test's workaround, and it is gone — a shell
//     variable is now just a shell variable.
func TestShellBuild_CwdAndVerbatimCmd(t *testing.T) {
	if testing.Short() {
		t.Skip("runs shell build commands as subprocesses; runs in task test")
	}
	projDir := t.TempDir()
	// A relative script path under the project root — resolving it from
	// the project root is the whole point of the cwd contract.
	scriptsDir := filepath.Join(projDir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scriptsDir, "build-image.sh"), []byte("#!/bin/sh\necho ran\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	outFile := filepath.Join(projDir, "observed.txt")
	// The command exercises: (1) a relative scripts/ path that must resolve
	// against the project root, (2) $PWD reaching the shell intact, and (3) a
	// ${RETIRED} token forge must NOT touch — the shell expands it to empty
	// because nothing set it, which is exactly the semantics the lint rule
	// warns about and precisely what forge now does.
	cmd := "sh scripts/build-image.sh > /dev/null && " +
		"printf 'pwd=%s\\nliteral=%s\\nunset=%s\\n' " +
		"\"$PWD\" 'IMAGE-is-not-substituted' \"${TARGETARCH}\" > observed.txt"
	svcs := []WorkloadEntity{shellSvc("gw", "my-gw", cmd, "", nil)}

	opts := buildOptions{env: "dev", parallel: false, outputDir: "bin"}
	results := buildExternalServices(context.Background(), svcs, opts, "v1.2.3", projDir)
	if len(results) != 1 || results[0].err != nil {
		t.Fatalf("buildExternalServices: %+v", results)
	}

	raw, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read observed.txt (command did not run from project root?): %v", err)
	}
	got := string(raw)

	// macOS /tmp is a symlink to /private/tmp; resolve both sides before
	// comparing the cwd the shell observed against the project root.
	wantPWD, _ := filepath.EvalSymlinks(projDir)
	gotPWD, _ := filepath.EvalSymlinks(parseField(t, got, "pwd"))
	if gotPWD != wantPWD {
		t.Errorf("cwd: command ran from %q, want project root %q", gotPWD, wantPWD)
	}

	// The literal reached the shell unmodified.
	if g := parseField(t, got, "literal"); g != "IMAGE-is-not-substituted" {
		t.Errorf("literal: got %q, want it passed through untouched", g)
	}

	// ${TARGETARCH} was NOT substituted by forge: the shell resolved it, and
	// nothing set it, so it is empty. Under the old substitution pass this
	// would have been "arm64".
	if g := parseField(t, got, "unset"); g != "" {
		t.Errorf("unset: got %q, want empty — forge must not substitute ${TARGETARCH}; the shell owns it now", g)
	}
}

// A ShellBuild that declares a token-named key in its `env` map gets it from
// the SHELL, because forge merges the declared env onto the process
// environment. This is the documented way to keep a `${TARGETARCH}` spelling
// working, so it is worth proving end-to-end through a real shell rather than
// only at the unit level.
func TestShellBuild_DeclaredEnvResolvesInTheShell(t *testing.T) {
	projDir := t.TempDir()
	cmd := "printf 'arch=%s\\n' \"${TARGETARCH}\" > observed.txt"
	svcs := []WorkloadEntity{shellSvc("gw", "my-gw", cmd, "", map[string]string{"TARGETARCH": "arm64"})}

	results := buildExternalServices(context.Background(), svcs,
		buildOptions{env: "dev", outputDir: "bin"}, "v1", projDir)
	if len(results) != 1 || results[0].err != nil {
		t.Fatalf("buildExternalServices: %+v", results)
	}
	raw, err := os.ReadFile(filepath.Join(projDir, "observed.txt"))
	if err != nil {
		t.Fatalf("read observed.txt: %v", err)
	}
	if g := parseField(t, string(raw), "arch"); g != "arm64" {
		t.Errorf("arch: got %q, want arm64 from the declared env map", g)
	}
}

// TestShellBuild_NoopTrue confirms the no-op ShellBuild the reliant
// sibling services declare (`cmd = "true  # ..."`) still succeeds through
// the unified dispatcher: no relative paths, nothing pushed — so the digest
// lookup finds nothing and the build is a harmless success.
func TestShellBuild_NoopTrue(t *testing.T) {
	if testing.Short() {
		t.Skip("runs shell build commands as subprocesses; runs in task test")
	}
	projDir := t.TempDir()
	svcs := []WorkloadEntity{shellSvc("reliant-noop", "reliant", "true  # built upstream; nothing to do here", "", nil)}
	results := buildExternalServices(context.Background(), svcs,
		buildOptions{env: "dev", outputDir: "bin"}, "dev", projDir)
	if len(results) != 1 || results[0].err != nil {
		t.Fatalf("no-op ShellBuild should succeed, got: %+v", results)
	}
}

func parseField(t *testing.T, blob, key string) string {
	t.Helper()
	for _, line := range strings.Split(blob, "\n") {
		if strings.HasPrefix(line, key+"=") {
			return strings.TrimPrefix(line, key+"=")
		}
	}
	t.Fatalf("field %q not found in observed output:\n%s", key, blob)
	return ""
}
