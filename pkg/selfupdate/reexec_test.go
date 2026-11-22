//go:build unix

package selfupdate

import (
	"bytes"
	"context"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestReexecEndToEnd proves the whole upgrade with a REAL binary
// (testdata/reexec): it stages v2 from a fake release server, swaps it over
// itself and execs it in place, and the new image confirms, hangs (the
// watchdog reverts it) or crashes (the boot-loop guard reverts it). Every
// image prints its pid, which must not change across an exec.
func TestReexecEndToEnd(t *testing.T) {
	testkit.Slow(t, "go-builds testdata/reexec five times and runs the binaries it builds",
		testkit.Inputs("pkg/selfupdate/**"))
	if _, _, reason, ok := inContainer(); ok {
		t.Skip("needs a host outside a container: Detect refuses every swap " + reason)
	}
	t.Parallel()

	bins := t.TempDir()
	build := func(name, version, mode string, ldflags ...string) []byte {
		out := filepath.Join(bins, name)
		flags := append([]string{"-X main.version=" + version, "-X main.mode=" + mode}, ldflags...)
		cmd := exec.Command("go", "build", "-o", out, "-ldflags", strings.Join(flags, " "), "./testdata/reexec")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go build %s: %v\n%s", name, err, b)
		}
		b, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	v1 := build("v1", "1.0.0", "confirm")
	v1Stamped := build("v1-stamped", "1.0.0", "confirm", "-X github.com/reliant-labs/forge/pkg/selfupdate.managedBy=homebrew")
	v2 := map[string][]byte{
		"confirm": build("v2-confirm", "2.0.0", "confirm"),
		"hang":    build("v2-hang", "2.0.0", "hang"),
		"crash":   build("v2-crash", "2.0.0", "crash"),
	}

	// setup installs v1 and serves a v2 release whose binary behaves as mode.
	setup := func(t *testing.T, mode string) (exe string, run func(...string) (string, int)) {
		archive := tarGz(t, map[string][]byte{"reexec": v2[mode]})
		files := map[string][]byte{"reexec_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz": archive}
		files["checksums.txt"] = checksumsOf(files)
		srv := newFakeRelease(t, "v2.0.0", files)

		dir := t.TempDir()
		ca := filepath.Join(dir, "ca.pem")
		if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.srv.Certificate().Raw}), 0o600); err != nil {
			t.Fatal(err)
		}
		exe = filepath.Join(dir, "bin", "reexec")
		if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(exe, v1, 0o755); err != nil {
			t.Fatal(err)
		}
		state := filepath.Join(dir, "state")
		run = func(extra ...string) (string, int) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			args := append([]string{"--api", srv.srv.URL, "--ca", ca, "--state", state}, extra...)
			out, err := exec.CommandContext(ctx, exe, args...).CombinedOutput()
			code := 0
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else if err != nil {
				t.Fatalf("run: %v\n%s", err, out)
			}
			return string(out), code
		}
		return exe, run
	}

	t.Run("confirm", func(t *testing.T) {
		t.Parallel()
		exe, run := setup(t, "confirm")
		out, code := run()
		boots := bootLines(out)
		if code != 0 || len(boots) != 2 || !strings.Contains(out, "confirmed") {
			t.Fatalf("exit %d, boots %v; output:\n%s", code, boots, out)
		}
		samePID(t, boots, out)
		if boots[0].version != "1.0.0" || boots[1].version != "2.0.0" || boots[1].swapped != "true" {
			t.Fatalf("boots = %+v; output:\n%s", boots, out)
		}
		if got, _ := os.ReadFile(exe); !bytes.Equal(got, v2["confirm"]) {
			t.Fatal("the install does not hold v2")
		}
		st := readJSONState(t, filepath.Join(filepath.Dir(filepath.Dir(exe)), "state"))
		if st.Swap.Phase != phaseConfirmed {
			t.Fatalf("phase = %s", st.Swap.Phase)
		}
		if got, _ := os.ReadFile(st.Swap.Previous); !bytes.Equal(got, v1) {
			t.Fatal("previous/ does not hold v1")
		}
		// v2 is current now: nothing newer to do.
		if out, code := run(); code != 0 || !strings.Contains(out, "skip:") {
			t.Fatalf("second run: exit %d\n%s", code, out)
		}
	})

	t.Run("watchdog reverts a binary that never confirms", func(t *testing.T) {
		t.Parallel()
		exe, run := setup(t, "hang")
		out, code := run("--confirm-within", "500ms")
		boots := bootLines(out)
		if code != 0 || len(boots) != 3 || !strings.Contains(out, "reverted 2.0.0") {
			t.Fatalf("exit %d, boots %v; output:\n%s", code, boots, out)
		}
		samePID(t, boots, out)
		if boots[1].version != "2.0.0" || boots[2].version != "1.0.0" || boots[2].reverted != "2.0.0" {
			t.Fatalf("boots = %+v", boots)
		}
		if got, _ := os.ReadFile(exe); !bytes.Equal(got, v1) {
			t.Fatal("v1 is not back in place")
		}
		// The failed version is not offered again.
		if out, code := run(); code != 0 || !strings.Contains(out, "reverted 2.0.0") {
			t.Fatalf("rerun: exit %d\n%s", code, out)
		}
	})

	t.Run("a build stamped managed-by refuses to replace itself", func(t *testing.T) {
		t.Parallel()
		exe, run := setup(t, "confirm")
		if err := os.WriteFile(exe, v1Stamped, 0o755); err != nil {
			t.Fatal(err)
		}
		out, code := run()
		if code != 2 || !strings.Contains(out, "error prepare") || !strings.Contains(out, "owner homebrew") {
			t.Fatalf("exit %d; output:\n%s", code, out)
		}
		if got, _ := os.ReadFile(exe); !bytes.Equal(got, v1Stamped) {
			t.Fatal("a stamped install was replaced")
		}
	})

	t.Run("boot-loop guard reverts a binary that keeps crashing", func(t *testing.T) {
		t.Parallel()
		exe, run := setup(t, "crash")
		out, code := run()
		if boots := bootLines(out); code != 3 || len(boots) != 2 || boots[1].version != "2.0.0" {
			t.Fatalf("first run: exit %d, boots %v\n%s", code, boots, out)
		}
		// A supervisor restarts it: still v2, still crashing.
		out, code = run()
		if boots := bootLines(out); code != 3 || len(boots) != 1 || boots[0].version != "2.0.0" {
			t.Fatalf("second run: exit %d, boots %v\n%s", code, boots, out)
		}
		// Two unconfirmed deaths: the next start reverts before the app runs.
		out, code = run()
		boots := bootLines(out)
		if code != 0 || len(boots) != 1 || boots[0].version != "1.0.0" || boots[0].reverted != "2.0.0" {
			t.Fatalf("third run: exit %d, boots %v\n%s", code, boots, out)
		}
		if got, _ := os.ReadFile(exe); !bytes.Equal(got, v1) {
			t.Fatal("v1 is not back in place")
		}
	})
}

type bootLine struct{ pid, version, swapped, reverted string }

var bootRE = regexp.MustCompile(`(?m)^boot pid=(\d+) version=(\S+) swapped=(\S+) reverted=(\S*)$`)

func bootLines(out string) []bootLine {
	var lines []bootLine
	for _, m := range bootRE.FindAllStringSubmatch(out, -1) {
		lines = append(lines, bootLine{pid: m[1], version: m[2], swapped: m[3], reverted: m[4]})
	}
	return lines
}

func samePID(t *testing.T, boots []bootLine, out string) {
	t.Helper()
	for _, b := range boots[1:] {
		if b.pid != boots[0].pid {
			t.Fatalf("exec in place changed the pid (%s -> %s); output:\n%s", boots[0].pid, b.pid, out)
		}
	}
}
