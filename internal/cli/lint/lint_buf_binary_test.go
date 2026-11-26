package lint

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/reliant-labs/forge/internal/toolpin"
)

// testBufPin is a go.mod pin of buf v1.57.0 (reliant's), or forge's own pin
// for a project that declares none.
func testBufPin(declared bool) toolpin.Buf {
	if declared {
		return toolpin.Buf{Version: "v1.57.0", Source: toolpin.FromGoMod}
	}
	return toolpin.ForgeBuf()
}

// lintBufBinary runs the buf the project pins, as forge generate and the gate
// do. It used to take PATH's buf first, so a local `forge lint --proto-format`
// with buf 1.71 passed a tree that CI's go.mod-pinned buf 1.57 rejected.
func TestLintBufBinary_RunsTheProjectsPin(t *testing.T) {
	cached := filepath.Join(t.TempDir(), "buf@v1.57.0")
	cachedBin := filepath.Join(cached, "buf")
	if runtime.GOOS == "windows" {
		cachedBin += ".exe"
	}
	for name, tc := range map[string]struct {
		onPath    bool
		declared  bool
		ensureDir string
		ensureErr error
		want      string
		wantOK    bool
		ensured   bool
	}{
		"declared pin differs from PATH: the cached pin":     {onPath: true, declared: true, ensureDir: cached, want: cachedBin, wantOK: true, ensured: true},
		"declared pin is PATH's buf: PATH":                   {onPath: true, declared: true, ensureDir: "", want: "/usr/bin/buf", wantOK: true, ensured: true},
		"declared pin cannot be built: PATH, with a warning": {onPath: true, declared: true, ensureErr: errors.New("offline"), want: "/usr/bin/buf", wantOK: true, ensured: true},
		"no declaration: PATH's buf, no build":               {onPath: true, declared: false, want: "/usr/bin/buf", wantOK: true},
		"no buf on PATH: forge's pin from the tool cache":    {onPath: false, declared: false, ensureDir: cached, want: cachedBin, wantOK: true, ensured: true},
		"no buf on PATH and the pin cannot be built: no buf": {onPath: false, declared: true, ensureErr: errors.New("offline"), wantOK: false, ensured: true},
	} {
		t.Run(name, func(t *testing.T) {
			ensured := false
			oldLook, oldPin, oldEnsure := lintBufLookPath, lintBufPin, lintBufEnsure
			t.Cleanup(func() { lintBufLookPath, lintBufPin, lintBufEnsure = oldLook, oldPin, oldEnsure })
			lintBufLookPath = func(string) (string, error) {
				if tc.onPath {
					return "/usr/bin/buf", nil
				}
				return "", exec.ErrNotFound
			}
			lintBufPin = func(string) toolpin.Buf { return testBufPin(tc.declared) }
			lintBufEnsure = func(context.Context, string, toolpin.Buf) (string, error) {
				ensured = true
				return tc.ensureDir, tc.ensureErr
			}
			got, ok := lintBufBinary(context.Background())
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("lintBufBinary = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.wantOK)
			}
			if ensured != tc.ensured {
				t.Errorf("resolved the pin = %v, want %v", ensured, tc.ensured)
			}
		})
	}
}
