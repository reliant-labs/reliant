//go:build windows

package selfupdate

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestReexecEndToEndWindows is TestReexecEndToEnd's Windows half, with a
// REAL binary and Windows' real lock on a running .exe: v1 stages v2, renames
// itself aside, moves v2 into place, starts it as a new process and exits;
// v2 confirms, or hangs until the watchdog puts v1 back through the same
// rename and starts it. The pid changes at every swap.
func TestReexecEndToEndWindows(t *testing.T) {
	testkit.Slow(t, "go-builds testdata/reexec three times and runs the binaries it builds",
		testkit.Inputs("pkg/selfupdate/**"))
	t.Parallel()

	bins := t.TempDir()
	build := func(name, version, mode string) []byte {
		out := filepath.Join(bins, name+".exe")
		ldflags := "-X main.version=" + version + " -X main.mode=" + mode
		if b, err := exec.Command("go", "build", "-o", out, "-ldflags", ldflags, "./testdata/reexec").CombinedOutput(); err != nil {
			t.Fatalf("go build %s: %v\n%s", name, err, b)
		}
		b, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	v1 := build("v1", "1.0.0", "confirm")
	v2 := map[string][]byte{
		"confirm": build("v2-confirm", "2.0.0", "confirm"),
		"hang":    build("v2-hang", "2.0.0", "hang"),
	}

	setup := func(t *testing.T, mode string) (exe string, run func(...string) (string, int)) {
		archive := tarGz(t, map[string][]byte{"reexec.exe": v2[mode]})
		files := map[string][]byte{"reexec_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz": archive}
		files["checksums.txt"] = checksumsOf(files)
		srv := newFakeRelease(t, "v2.0.0", files)

		dir := t.TempDir()
		ca := filepath.Join(dir, "ca.pem")
		if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.srv.Certificate().Raw}), 0o600); err != nil {
			t.Fatal(err)
		}
		exe = filepath.Join(dir, "bin", "reexec.exe")
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
			cmd := exec.CommandContext(ctx, exe, args...)
			// The output pipe stays open until every process of the chain
			// (each swap starts the next) has exited; bound that wait too.
			cmd.WaitDelay = time.Minute
			out, err := cmd.CombinedOutput()
			code := 0
			var ee *exec.ExitError
			if errors.As(err, &ee) {
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
		if boots[0].version != "1.0.0" || boots[1].version != "2.0.0" || boots[1].swapped != "true" {
			t.Fatalf("boots = %+v; output:\n%s", boots, out)
		}
		if boots[0].pid == boots[1].pid {
			t.Fatalf("the new binary must start as a new process; output:\n%s", out)
		}
		if got, _ := os.ReadFile(exe); !bytes.Equal(got, v2["confirm"]) {
			t.Fatal("the install does not hold v2")
		}
		st := readJSONState(t, filepath.Join(filepath.Dir(filepath.Dir(exe)), "state"))
		if st.Swap.Phase != phaseConfirmed {
			t.Fatalf("phase = %s", st.Swap.Phase)
		}
		if _, err := os.Stat(journalPath(filepath.Join(filepath.Dir(filepath.Dir(exe)), "state"))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("a completed swap left its move journal: %v", err)
		}
		// v2 is current: nothing to do, and this start sweeps the v1 image
		// set aside, whose process has exited.
		if out, code := run(); code != 0 || !strings.Contains(out, "skip:") {
			t.Fatalf("second run: exit %d\n%s", code, out)
		}
		if left := asides(t, exe); len(left) != 0 {
			t.Fatalf("images set aside outlived their processes: %v", left)
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
		if boots[1].version != "2.0.0" || boots[2].version != "1.0.0" || boots[2].reverted != "2.0.0" {
			t.Fatalf("boots = %+v", boots)
		}
		if got, _ := os.ReadFile(exe); !bytes.Equal(got, v1) {
			t.Fatal("v1 is not back in place")
		}
	})
}
