// Copyright (c) 2025 Reliant Labs

package alerting

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDurationJSON(t *testing.T) {
	var s RuleSpec
	if err := json.Unmarshal([]byte(`{"alert":"a","expr":"SELECT 1","for":"5m","interval":90,"keep_firing_for":"10m"}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.KeepFiringFor.Std() != 10*time.Minute {
		t.Fatalf("keep_firing_for = %v", s.KeepFiringFor.Std())
	}
	if s.For.Std() != 5*time.Minute || s.Interval.Std() != 90*time.Second {
		t.Fatalf("for=%v interval=%v", s.For.Std(), s.Interval.Std())
	}
	b, _ := json.Marshal(s)
	if !strings.Contains(string(b), `"for":"5m0s"`) {
		t.Fatalf("encoded = %s", b)
	}
	for _, bad := range []string{`{"for":"soon"}`, `{"for":"-5m"}`, `{"for":true}`} {
		if err := json.Unmarshal([]byte(bad), &s); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestRuleSpecValidate(t *testing.T) {
	ok := RuleSpec{Alert: "slow", Expr: "SELECT 1 AS value", Labels: map[string]string{"severity": "critical"}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*RuleSpec){
		"no name":        func(s *RuleSpec) { s.Alert = "" },
		"slash in name":  func(s *RuleSpec) { s.Alert = "a/b" },
		"long name":      func(s *RuleSpec) { s.Alert = strings.Repeat("x", 200) },
		"write":          func(s *RuleSpec) { s.Expr = "DROP TABLE t" },
		"reserved label": func(s *RuleSpec) { s.Labels = map[string]string{"Reliant_org_id": "x"} },
		"bad label":      func(s *RuleSpec) { s.Labels = map[string]string{"a-b": "x"} },
		"bad source":     func(s *RuleSpec) { s.Source = "admin" },
	} {
		t.Run(name, func(t *testing.T) {
			s := ok
			mut(&s)
			if err := s.Validate(); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	if err := (RuleSet{ok, ok}).Validate(); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("duplicates: %v", err)
	}
}

func TestRuleBindsIdentityAndSource(t *testing.T) {
	r := RuleSpec{Alert: "a", Expr: "SELECT 1", For: Duration(time.Minute)}.Rule("t", "prod", map[string]string{"reliant_org_id": "t"})
	if r.Owner != "t" || r.Env != "prod" || r.Name != "a" || r.Source != SourceUser || r.Identity["reliant_org_id"] != "t" || r.For != time.Minute {
		t.Fatalf("rule = %+v", r)
	}
	if got := (RuleSpec{Alert: "a", Source: SourceDefault}).Rule("t", "e", nil).Source; got != SourceDefault {
		t.Fatalf("source = %v", got)
	}
}

func writeRules(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFileRuleSourceLoadsReloadsAndKeepsLastGood(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alerts.json")
	now := t0
	f := &FileRuleSource{Path: path, Owner: "self", Env: "prod", Identity: map[string]string{"reliant_org_id": "self"}, Recheck: time.Second, now: func() time.Time { return now }}
	ctx := context.Background()

	// no file yet: no rules, no error.
	if ts, err := f.Owners(ctx); err != nil || len(ts) != 0 {
		t.Fatalf("owners=%v err=%v", ts, err)
	}

	writeRules(t, path, `{"alerts":[{"alert":"slow","expr":"SELECT s AS value FROM x","for":"1m","labels":{"severity":"critical"},"source":"default"}],"routes":[{"ignored":true}]}`)
	now = now.Add(2 * time.Second)
	rules, _ := f.Rules(ctx, "self")
	if len(rules) != 1 || rules[0].Name != "slow" || rules[0].Env != "prod" || rules[0].Source != SourceDefault || rules[0].Identity["reliant_org_id"] != "self" {
		t.Fatalf("rules = %+v", rules)
	}
	if ts, _ := f.Owners(ctx); len(ts) != 1 || ts[0] != "self" {
		t.Fatalf("owners = %v", ts)
	}
	if other, _ := f.Rules(ctx, "someone-else"); len(other) != 0 {
		t.Fatal("a file source must serve only its own owner")
	}

	// within Recheck, an edit is not seen yet.
	writeRules(t, path, `{"alerts":[{"alert":"a","expr":"SELECT 1"},{"alert":"b","expr":"SELECT 2"}]}`)
	if rules, _ = f.Rules(ctx, "self"); len(rules) != 1 {
		t.Fatal("re-read before Recheck elapsed")
	}
	now = now.Add(2 * time.Second)
	if rules, _ = f.Rules(ctx, "self"); len(rules) != 2 {
		t.Fatalf("edit not picked up: %d rules", len(rules))
	}

	// a broken edit keeps the last good rules.
	writeRules(t, path, `{"alerts":[{"alert":"a","expr":"DROP TABLE x"}]}`)
	now = now.Add(2 * time.Second)
	if rules, _ = f.Rules(ctx, "self"); len(rules) != 2 {
		t.Fatalf("an invalid file replaced good rules: %d", len(rules))
	}
	writeRules(t, path, `not json at all`)
	now = now.Add(2 * time.Second)
	if rules, _ = f.Rules(ctx, "self"); len(rules) != 2 {
		t.Fatal("a malformed file replaced good rules")
	}
	// a deleted file also keeps them (a mount hiccup must not resolve every alert).
	_ = os.Remove(path)
	now = now.Add(2 * time.Second)
	if rules, _ = f.Rules(ctx, "self"); len(rules) != 2 {
		t.Fatal("a missing file replaced good rules")
	}

	// an explicit, valid, empty document removes everything.
	writeRules(t, path, `{"alerts":[]}`)
	now = now.Add(2 * time.Second)
	if rules, _ = f.Rules(ctx, "self"); len(rules) != 0 {
		t.Fatalf("an empty document must clear the rules: %d", len(rules))
	}
}

func TestRuleSpecDecodesForgesShapeAlert(t *testing.T) {
	// forge's release.ShapeAlert serializes to RuleSpec; the extras (default_id)
	// are ignored, and keep_firing_for is honoured.
	set, err := ParseRulesDocument([]byte(`{"alerts":[{"alert":"HighErrorRate","expr":"SELECT service, 1 AS value FROM t",
	  "for":"5m0s","interval":"1m0s","keep_firing_for":"10m0s","labels":{"severity":"critical"},
	  "annotations":{"summary":"x"},"source":"default","default_id":"service-error-rate"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	r := set[0].Rule("t", "e", nil)
	if r.KeepFiringFor != 10*time.Minute || r.For != 5*time.Minute || r.Source != SourceDefault {
		t.Fatalf("rule = %+v", r)
	}
}
