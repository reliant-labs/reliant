package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The codemods an upgrade runs are keyed by the release that broke the
// shape, exactly as migration skills are: a baseline below the release (or
// no baseline at all) crosses it, one at or past it does not, and a target
// below it (a downgrade) never runs it.
func TestUpgradeCodemods_KeyedByRelease(t *testing.T) {
	names := func(from, to string) string {
		var out []string
		for _, c := range upgradeCodemods(from, to) {
			out = append(out, c.Name)
		}
		return strings.Join(out, ",")
	}
	for _, tc := range []struct {
		from, to string
		want     bool
	}{
		{"v0.1.45", "v0.1.47", true},
		{"v0.1.46-0.20261019044744-357579470b0d", "v0.1.47", true}, // control-plane's pin: a pre-release of v0.1.46
		{"", "v0.1.47", true},
		{"dev", "v0.1.46", true},
		{"v0.1.46", "v0.1.47", false},
		{"v0.1.45", "v0.1.45", false},
		{"v0.1.40", "v0.1.45", false},
	} {
		got := strings.Contains(names(tc.from, tc.to), "operatorkit")
		if got != tc.want {
			t.Errorf("upgrade %q → %q runs operatorkit = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

// Under --check the chain reports what it would rewrite and writes nothing;
// otherwise it rewrites, and a second run finds nothing.
func TestRunCodemodChain_CheckThenWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "internal", "app", "lifecycle.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const src = "package app\n\nimport \"github.com/reliant-labs/forge/pkg/lifecyclekit\"\n\nvar _ lifecyclekit.Options\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	dry, err := runCodemodChain(dir, "v0.1.45", "v0.1.47", false)
	if err != nil {
		t.Fatal(err)
	}
	if !dry.Changed() {
		t.Fatal("--check reported nothing to rewrite")
	}
	if got, _ := os.ReadFile(path); string(got) != src {
		t.Fatalf("--check wrote the file:\n%s", got)
	}

	wrote, err := runCodemodChain(dir, "v0.1.45", "v0.1.47", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(wrote.Rewrites) != 1 {
		t.Fatalf("Rewrites = %q, want one", wrote.Rewrites)
	}
	if got, _ := os.ReadFile(path); !strings.Contains(string(got), "operatorkit.Options") {
		t.Fatalf("not rewritten:\n%s", got)
	}
	again, err := runCodemodChain(dir, "v0.1.45", "v0.1.47", true)
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed() {
		t.Errorf("second run rewrote %q", again.Rewrites)
	}
}
