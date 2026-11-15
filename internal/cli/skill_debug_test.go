package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/obsbackend"
)

// Exercise the embedded skill through the same renderer the harness loads,
// not a project-local copy. Keep the telemetry example tied to its real CLI.
func TestDebugSkillRunningSystemExamples(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	body, err := RenderSkillContentAt("", "debug", SkillAudienceForge, "reliant forge")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"reliant forge obs status", "reliant forge db query", "--addr localhost:15432",
		"--backend cloud", "obs:read", "db:query", "unread", "truncated",
		"observability/query", "db/query", "observability/profiles",
		"not obs or db query tools", ".forge/logs/<env>/frontend_<name>.log",
		`\[browser:error\]`, "installDevLogging()",
		"obs dashboards audit", "debug/dashboards",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered debug skill lacks %q", want)
		}
	}
	if strings.Contains(text, "not collected locally yet") {
		t.Error("debug skill still claims continuous profiles are not collected")
	}
	general, err := RenderSkillContentAt("", "debug", SkillAudienceGeneral, "reliant forge")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(general), "### Ask the running system") {
		t.Error("Forge-specific investigation leaked into the general audience")
	}

	// Extract the literal SQL from the documented shell example, then run
	// the actual query command against an in-process fake (no network/auth).
	_, after, ok := strings.Cut(text, "'SELECT count() AS n FROM otel_logs\n")
	if !ok {
		t.Fatal("cannot find the documented telemetry SQL example")
	}
	rest, _, ok := strings.Cut(after, "'\n```")
	if !ok {
		t.Fatal("cannot find end of telemetry SQL example")
	}
	sql := "SELECT count() AS n FROM otel_logs\n" + rest
	runner := &scriptedRunner{answers: []scriptedAnswer{{
		contains: "otel_logs",
		res: obsbackend.Result{
			Columns: []obsbackend.Column{{Name: "n", Type: "UInt64"}},
			Rows:    [][]any{{jn("3")}},
		},
	}}}
	out, _, err := obsRun(t, devFacts(), runner, "query", "dev", "--since", "15m", "--service", "api", "--sql", sql, "--json")
	if err != nil {
		t.Fatalf("documented SQL: %v", err)
	}
	if len(runner.ran) != 1 || decode(t, out)["ok"] != true {
		t.Fatalf("documented query did not execute successfully: %s", out)
	}
}
