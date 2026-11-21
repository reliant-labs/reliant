package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/forgecompat"
	"github.com/reliant-labs/forge/internal/forgedelegate"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// handOffStreamsHelperEnv makes TestHandOffStreamsHelper act as a forge
// process instead of a test: "entrypoint" runs Execute (the decision cmd/forge
// makes before the tree parses), "tree" runs the command tree alone (the
// decision the root's PersistentPreRun makes for a host that did not call
// HandOffToPinned).
const handOffStreamsHelperEnv = "FORGE_TEST_HANDOFF_STREAMS"

// handOffChildStdout is everything the stand-in pinned forge prints on stdout:
// a JSON document, as `forge kcl eval --format json` would.
const handOffChildStdout = `{"kind":"Deployment","metadata":{"name":"exact-owner"}}` + "\n"

// Forge report 2186e95b: control-plane's TestRawRuntimeExplicitSlotProjection
// parsed `forge kcl eval … --format json` and found the hand-off notice
// ("forge: this forge is vX, but the project wants forge_version vY — running
// the project's .tools/bin/forge instead") in front of the document. The
// notice, and every other line forge prints before it hands a command over,
// belong on stderr: stdout of a handed-off command is exactly the pinned
// forge's stdout, byte for byte. This runs the real hand-off — a forge process
// that execs the project's .tools/bin/forge — and compares the bytes.
func TestHandOff_StdoutIsExactlyThePinnedForgesStdout(t *testing.T) {
	testkit.Slow(t, "execs the forge binary as a child process")
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in pinned forge is a POSIX shell script")
	}
	dir := t.TempDir()
	writeForgeYAML(t, dir, "name: demo\nmodule_path: github.com/example/demo\nforge_version: "+skewTestPin+"\n")
	bin := filepath.Join(dir, ".tools", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = version ]; then echo 'forge version " + skewTestPin + "'; exit 0; fi\n" +
		"printf '%s' '" + handOffChildStdout + "'\n" +
		"echo 'pinned forge: a notice of its own' >&2\n"
	if err := os.WriteFile(filepath.Join(bin, "forge"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, mode := range []string{"entrypoint", "tree"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestHandOffStreamsHelper$")
			cmd.Dir = dir
			cmd.Env = handOffHelperEnv(t, mode)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("helper forge: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.Bytes(), stderr.Bytes())
			}
			if got := stdout.String(); got != handOffChildStdout {
				t.Errorf("stdout is not exactly the pinned forge's output:\ngot:  %q\nwant: %q\nstderr:\n%s", got, handOffChildStdout, stderr.Bytes())
			}
			if !strings.Contains(stderr.String(), "running the project's .tools/bin/forge instead") {
				t.Errorf("the hand-off notice is not on stderr:\n%s", stderr.Bytes())
			}
			if !strings.Contains(stderr.String(), "pinned forge: a notice of its own") {
				t.Errorf("the pinned forge's stderr did not reach stderr:\n%s", stderr.Bytes())
			}
		})
	}
}

// handOffHelperEnv is the helper's environment: this one, minus everything
// that would stop or redirect the hand-off, with TMPDIR inside the test's own
// temp dir (the exec'd process never runs TestMain's cleanup).
func handOffHelperEnv(t *testing.T, mode string) []string {
	t.Helper()
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case forgedelegate.MarkerEnv, forgecompat.SkewOverrideEnv, "TMPDIR", forgedelegate.CacheDirEnv:
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		handOffStreamsHelperEnv+"="+mode,
		"TMPDIR="+t.TempDir(),
		forgedelegate.CacheDirEnv+"="+t.TempDir(),
	)
}

// TestHandOffStreamsHelper is not a test: it is the forge process
// TestHandOff_StdoutIsExactlyThePinnedForgesStdout runs. A successful
// hand-off replaces it, so returning at all is the failure.
func TestHandOffStreamsHelper(t *testing.T) {
	mode := os.Getenv(handOffStreamsHelperEnv)
	if mode == "" {
		t.Skip("helper process for TestHandOff_StdoutIsExactlyThePinnedForgesStdout")
	}
	pinDelegationDisabled = false
	buildinfo.Set(skewTestBinary, "", "6c0c3edb1322")
	args := []string{"kcl", "eval", "deploy/kcl/release_runtime_test.k", "-S", "output", "--format", "json"}
	os.Args = append([]string{"forge"}, args...)
	var err error
	switch mode {
	case "entrypoint":
		err = Execute()
	case "tree":
		root := newTestRootCmd(t)
		root.SetArgs(args)
		err = root.Execute()
	}
	fmt.Fprintf(os.Stderr, "the command was not handed to the pinned forge (mode %s): %v\n", mode, err)
	os.Exit(3)
}
