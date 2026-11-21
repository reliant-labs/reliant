package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// The ClickStack collector file (a Tier-2 managed row, scaffolded once)
// gained the include that loads declared log parsing (forge.Telemetry
// `logs`), which `forge env up` writes under .forge/logs/.telemetry/. The row
// is how an existing project gets it: a pristine copy of the older render is
// updated by `forge project upgrade`, and a copy the user edited is reported
// with the include among the lines it lacks (and `forge env up` says so too).
func TestUpgradeBringsTheTelemetryIncludeToTheCollectorFile(t *testing.T) {
	testkit.Slow(t, "execs go")
	const rel = "deploy/observability/otel-collector.yaml"
	cfg := testProjectConfig()
	data := buildTemplateData(cfg, "")
	var row *managedFile
	for _, f := range managedFilesForCfg(cfg) {
		if f.destPath == rel {
			row = &f
			break
		}
	}
	if row == nil {
		t.Fatalf("%s is not a managed row: an existing project would never get the include", rel)
	}
	current, err := renderManagedFile(*row, data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(current), "/var/log/forge/.telemetry/logs-parse.yaml") {
		t.Fatalf("the current render lacks the include:\n%s", current)
	}
	// The render before the include: its three ${file:} lines gone and the
	// pipeline as it was.
	var lines []string
	for _, l := range strings.Split(string(current), "\n") {
		if !strings.Contains(l, "/var/log/forge/.telemetry/") {
			lines = append(lines, l)
		}
	}
	older := strings.Replace(strings.Join(lines, "\n"),
		"[memory_limiter, transform, transform/telemetry_identity, transform/telemetry_logs, batch]",
		"[memory_limiter, transform, batch]", 1)

	find := func(results []UpgradeResult) UpgradeResult {
		for _, r := range results {
			if r.Path == rel {
				return r
			}
		}
		t.Fatalf("%s not in the upgrade results", rel)
		return UpgradeResult{}
	}

	t.Run("pristine copy is updated", func(t *testing.T) {
		dir := t.TempDir()
		writeManagedRender(t, dir, rel, []byte(older), true)
		results, err := Upgrade(dir, cfg, false, false)
		if err != nil {
			t.Fatal(err)
		}
		if r := find(results); r.Status != UpgradeUpdated {
			t.Fatalf("status = %q, want %q", r.Status, UpgradeUpdated)
		}
		got, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(got), "/var/log/forge/.telemetry/logs-preserve.yaml") {
			t.Errorf("the updated file lacks the include:\n%s", got)
		}
	})

	t.Run("edited copy is reported, not overwritten", func(t *testing.T) {
		dir := t.TempDir()
		edited := older + "# my own pipeline tweak\n"
		writeManagedRender(t, dir, rel, []byte(edited), false)
		results, err := Upgrade(dir, cfg, false, false)
		if err != nil {
			t.Fatal(err)
		}
		r := find(results)
		if r.Status != UpgradeUserModified || r.Missing == 0 || !strings.Contains(r.Diff, ".telemetry/logs-parse.yaml") {
			t.Fatalf("status %q, missing %d, diff:\n%s\nwant user-modified with the include among the missing lines", r.Status, r.Missing, r.Diff)
		}
		got, _ := os.ReadFile(filepath.Join(dir, rel))
		if string(got) != edited {
			t.Error("an edited collector file was overwritten without --force")
		}
	})
}
