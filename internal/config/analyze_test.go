package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadProject_DatabaseAnalyze(t *testing.T) {
	const head = "name: app\nmodule_path: example.com/app\ndatabase:\n  analyze:\n"
	cfg, err := LoadProject([]byte(head+`    target:
      kind: dsn
      dsn_env: FORGE_ANALYZE_DATABASE_URL
      statement_timeout: 60s
      lock_timeout: 5s
    table_rows:
      orders: 40000000
    large_table_rows: 1000000
    small_table_rows: 10000
`), "forge.yaml")
	if err != nil {
		t.Fatalf("database.analyze must load: %v", err)
	}
	a := cfg.Database.Analyze
	st, lt, err := a.Target.Timeouts()
	if err != nil || st != 60*time.Second || lt != 5*time.Second {
		t.Errorf("timeouts = %v, %v, %v", st, lt, err)
	}
	if a.Target.EffectiveKind() != AnalyzeTargetDSN || a.Target.DSNEnv != "FORGE_ANALYZE_DATABASE_URL" || a.TableRows["orders"] != 40_000_000 {
		t.Errorf("analyze = %+v", a)
	}
	if (AnalyzeTargetConfig{}).EffectiveKind() != AnalyzeTargetScratch {
		t.Error("the default target is not scratch")
	}

	for body, want := range map[string]string{
		"    target:\n      kind: prod\n":                      "database.analyze.target.kind",
		"    target:\n      dsn_env: postgres://u:p@prod/db\n": "NAME of an environment variable",
		"    target:\n      statement_timeout: forever\n":      "statement_timeout",
		"    target:\n      lock_timeout: -1s\n":               "lock_timeout",
		"    table_rows:\n      orders: -5\n":                  "table_rows.orders",
		"    small_table_rows: -1\n":                           "small_table_rows",
	} {
		_, err := LoadProject([]byte(head+body), "forge.yaml")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want one naming %q", body, err, want)
		}
	}
}
