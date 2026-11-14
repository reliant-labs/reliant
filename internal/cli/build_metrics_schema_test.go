package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/observe/metrics"
)

func TestHostRunnable(t *testing.T) {
	other := "plan9"
	if runtime.GOOS == other {
		other = "linux"
	}
	otherArch := "mips"
	cases := []struct {
		name      string
		t         goBuildTarget
		crossArch string
		want      bool
	}{
		{"plain host build", goBuildTarget{}, "", true},
		{"pinned to the host", goBuildTarget{goos: runtime.GOOS, goarch: runtime.GOARCH}, "", true},
		{"pinned to another os", goBuildTarget{goos: other}, "", false},
		{"pinned to another arch", goBuildTarget{goarch: otherArch}, "", false},
		{"cross arch from the build", goBuildTarget{}, otherArch, false},
	}
	for _, tc := range cases {
		if got := hostRunnable(tc.t, tc.crossArch); got != tc.want {
			t.Errorf("%s: hostRunnable = %v, want %v", tc.name, got, tc.want)
		}
	}
	if runtime.GOOS != "linux" {
		// A set arch defaults GOOS to linux (buildGoTarget), so on any other
		// host the binary is not runnable even for the host's own arch.
		if hostRunnable(goBuildTarget{}, runtime.GOARCH) {
			t.Error("an arch-set build defaults to linux and cannot run on " + runtime.GOOS)
		}
	}
}

// fakeSchemaBinary writes an executable that prints schema JSON for
// `metrics schema --json`, so the build step is tested without compiling a
// project.
func fakeSchemaBinary(t *testing.T, dir, name, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary")
	}
	script := "#!/bin/sh\n[ \"$1 $2 $3\" = \"metrics schema --json\" ] || { echo \"unexpected args: $*\" >&2; exit 2; }\n" + body
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestEmitMetricsSchemaWritesTheBundleArtifact(t *testing.T) {
	out := t.TempDir()
	schema, _ := metrics.NewRegistry(nil).Schema("shop", "v1").JSON()
	fakeSchemaBinary(t, out, "shop", "cat <<'EOF'\n"+string(schema)+"EOF\n")

	var log bytes.Buffer
	emitMetricsSchema(context.Background(), &log, out, "", []goBuildTarget{{outputName: "shop"}})

	got, err := os.ReadFile(filepath.Join(out, "observability", "metrics-schema.json"))
	if err != nil {
		t.Fatalf("schema artifact not written: %v\n%s", err, log.String())
	}
	if _, err := metrics.ParseSchema(got); err != nil {
		t.Errorf("artifact is not a metrics schema: %v", err)
	}
	if !strings.Contains(log.String(), MetricsSchemaArtifact) {
		t.Errorf("build log does not name the artifact: %q", log.String())
	}
}

func TestEmitMetricsSchemaIsBestEffortAndNeverLeavesAStaleFile(t *testing.T) {
	out := t.TempDir()
	stale := filepath.Join(out, "observability", "metrics-schema.json")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A binary built before the registry: the command does not exist.
	fakeSchemaBinary(t, out, "old", "echo 'unknown command' >&2; exit 1\n")
	var log bytes.Buffer
	emitMetricsSchema(context.Background(), &log, out, "", []goBuildTarget{{outputName: "old"}})
	if _, err := os.Stat(stale); err == nil {
		t.Error("a stale schema was left behind for a binary that cannot print one")
	}
	if !strings.Contains(log.String(), "skipped") {
		t.Errorf("a failure should be reported, not swallowed: %q", log.String())
	}

	// Nothing runnable: no output, no file, no panic.
	log.Reset()
	emitMetricsSchema(context.Background(), &log, out, "mips", []goBuildTarget{{outputName: "x"}})
	if log.Len() != 0 {
		t.Errorf("cross-built targets are skipped silently, got %q", log.String())
	}

	// Garbage that is not a schema is rejected rather than shipped.
	fakeSchemaBinary(t, out, "bad", "echo '{\"schema_version\":\"nope\"}'\n")
	emitMetricsSchema(context.Background(), &log, out, "", []goBuildTarget{{outputName: "bad"}})
	if _, err := os.Stat(stale); err == nil {
		t.Error("an invalid schema was written")
	}
}

func TestBuiltTargetsKeepsOnlySuccesses(t *testing.T) {
	got := builtTargets(
		[]goBuildTarget{{outputName: "a"}, {outputName: "b"}},
		[]buildResult{{name: "a", kind: "service"}, {name: "b", kind: "frontend"}},
	)
	if len(got) != 1 || got[0].outputName != "a" {
		t.Errorf("builtTargets = %+v", got)
	}
}
