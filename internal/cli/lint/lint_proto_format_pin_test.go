package lint

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/toolcache"
	"github.com/reliant-labs/forge/internal/toolpin"
)

// protoFormatPinProtos are the files the fake PATH buf "would reformat".
var protoFormatPinProtos = []string{"proto/a/v1/a.proto", "proto/b/v1/b.proto", "proto/c/v1/c.proto"}

// writeFakeBuf writes a `buf` shell script into dir that reports version and,
// for `buf format -d`, prints a diff for each of diffs.
func writeFakeBuf(t *testing.T, dir, version string, diffs []string) string {
	t.Helper()
	var body strings.Builder
	for _, f := range diffs {
		fmt.Fprintf(&body, "printf '%%s\\n' 'diff -u %[1]s.orig %[1]s' '--- %[1]s.orig' '+++ %[1]s' '@@ -1 +1 @@' '-message   M {}' '+message M {}'\n", f)
	}
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  --version) echo " + version + " ;;\n" +
		"  format) " + strings.ReplaceAll(strings.TrimSpace(body.String()), "\n", "; ") + " ;;\n" +
		"esac\n"
	if len(diffs) == 0 {
		script = strings.Replace(script, "  format)  ;;", "  format) : ;;", 1)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "buf")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// The proto-format lane formats with the buf the project pins, never another
// release. reliant pins buf v1.57.0; on a machine whose PATH had 1.71.0 the
// lane reported 1.71's formatting as a diff in 29 protos the pin leaves as
// they are, and `forge project upgrade` turned each into a needs_human item.
// Now: the pin when it can be had (PATH's buf when it IS the pin), and when it
// cannot, one "could not run" gap instead of another release's diffs.
func TestProtoFormatLane_RunsThePinnedBuf(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake buf binaries are shell scripts")
	}
	ctx := context.Background()
	newRoot := func(t *testing.T) string {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "buf.yaml"), []byte("version: v2\nmodules:\n  - path: proto\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return root
	}
	// stub replaces the pin lookup and, when ensure is non-nil, the tool-cache
	// resolution; PATH holds only pathDir, so the real exec.LookPath finds
	// the fake PATH buf.
	stub := func(t *testing.T, pathDir string, ensure func(context.Context, string, toolpin.Buf) (string, error)) {
		oldPin, oldEnsure := lintBufPin, lintBufEnsure
		t.Cleanup(func() { lintBufPin, lintBufEnsure = oldPin, oldEnsure })
		lintBufPin = func(string) toolpin.Buf { return toolpin.Buf{Version: "v1.57.0", Source: toolpin.FromGoMod} }
		if ensure != nil {
			lintBufEnsure = ensure
		}
		t.Setenv("PATH", pathDir)
		t.Setenv(toolcache.EnvRoot, t.TempDir())
	}

	t.Run("PATH has another release and the pin is installed: the pin runs", func(t *testing.T) {
		root, tmp := newRoot(t), t.TempDir()
		pathDir, pinDir := filepath.Join(tmp, "path"), filepath.Join(tmp, "cache", "buf@v1.57.0")
		writeFakeBuf(t, pathDir, "1.71.0", protoFormatPinProtos)
		writeFakeBuf(t, pinDir, "1.57.0", nil)
		stub(t, pathDir, func(context.Context, string, toolpin.Buf) (string, error) { return pinDir, nil })

		fs, gated, err := collectProtoFormatJSON(ctx, root, nil, false)
		if err != nil || gated || len(fs) != 0 {
			t.Fatalf("want the pinned buf's clean verdict, got gated=%v err=%v %+v", gated, err, fs)
		}
	})

	t.Run("PATH has another release and the pin cannot be installed: one refusal", func(t *testing.T) {
		root, tmp := newRoot(t), t.TempDir()
		pathDir := filepath.Join(tmp, "path")
		pathBuf := writeFakeBuf(t, pathDir, "1.71.0", protoFormatPinProtos)
		stub(t, pathDir, func(context.Context, string, toolpin.Buf) (string, error) {
			return "", errors.New("go install: proxy.golang.org unreachable")
		})

		fs, gated, err := collectProtoFormatJSON(ctx, root, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if gated || len(fs) != 1 {
			t.Fatalf("want exactly one finding (no per-file diffs), got gated=%v %+v", gated, fs)
		}
		f := fs[0]
		if f.Rule != ruleBufLintUnavailable || f.File != "" {
			t.Errorf("finding = %+v, want a %s gap with no file", f, ruleBufLintUnavailable)
		}
		for _, want := range []string{pathBuf, "1.71.0", "pins buf 1.57.0", "proxy.golang.org unreachable"} {
			if !strings.Contains(f.Message, want) {
				t.Errorf("message does not name %q:\n%s", want, f.Message)
			}
		}
		if !strings.Contains(f.FixHint, "@v1.57.0") {
			t.Errorf("fix hint does not name the pin:\n%s", f.FixHint)
		}

		// Through the pipeline it is one gap, which is one needs_human item
		// in `forge project upgrade`, not one per proto.
		outcomes := collectLintOutcomesFrom(&lintRunCtx{ctx: ctx, cwd: root, paths: []string{"./..."}},
			[]linterStep{findStep(t, "proto-format lint")})
		if len(outcomes) != 1 || outcomes[0].gap == nil {
			t.Fatalf("want the lane reported as a gap, got %+v", outcomes)
		}
		if o := outcomes[0]; len(o.findings) != 1 || o.failedBeyondGap() {
			t.Errorf("want the gap as the lane's only finding, got failedBeyondGap=%v %+v", o.failedBeyondGap(), o.findings)
		}
	})

	t.Run("PATH's buf is the pin: PATH's runs", func(t *testing.T) {
		root, tmp := newRoot(t), t.TempDir()
		pathDir := filepath.Join(tmp, "path")
		writeFakeBuf(t, pathDir, "1.57.0", protoFormatPinProtos[:1])
		// The real resolver: it verifies PATH's buf by its --version, and
		// installs nothing when it is the pin.
		stub(t, pathDir, nil)

		fs, gated, err := collectProtoFormatJSON(ctx, root, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if !gated || len(fs) != 2 || fs[0].File != protoFormatPinProtos[0] || fs[0].Rule != ruleProtoFormat {
			t.Fatalf("want PATH's buf's one diff, got gated=%v %+v", gated, fs)
		}
	})
}
