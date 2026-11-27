package prpolicy

import (
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/pipeline/config"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

func mod(p string, lines int) ChangedFile {
	return ChangedFile{Path: p, Status: FileModified, Additions: lines, Deletions: 0}
}

func withRegen(globs ...string) config.Resolved {
	cfg := config.Defaults()
	cfg.Generated = globs
	cfg.Regen = "task generate"
	return cfg
}

func TestAssessRisk(t *testing.T) {
	plain := config.Defaults()
	regen := withRegen("gen/**", "**/*_gen.go")
	noRegen := config.Defaults()
	noRegen.Generated = []string{"gen/**"}

	cases := []struct {
		name string
		diff Diff
		cfg  config.Resolved
		tier RiskTier
		rule ReviewRule
		// reason is a substring one of the reasons must contain.
		reason string
	}{
		{"no files", NewDiff(), plain, RiskLow, RuleNoModelLowRisk, "changes no files"},
		{"docs only", NewDiff(mod("README.md", 40), mod("docs/guide.mdx", 900), mod("LICENSE", 3)), plain,
			RiskLow, RuleNoModelLowRisk, "documentation"},
		{"a doc about auth is still a doc", NewDiff(mod("docs/auth/tokens.md", 10)), plain, RiskLow, RuleNoModelLowRisk, "documentation"},
		{"generated only, proven by regen", NewDiff(mod("gen/reliant/v1/a.pb.go", 5000), mod("internal/x/contract_gen.go", 30)), regen,
			RiskLow, RuleGeneratedOnly, "regeneration check (`task generate`)"},
		{"caller-marked generated, proven by regen", NewDiff(ChangedFile{Path: "web/src/api.ts", Additions: 3, Generated: true}), regen,
			RiskLow, RuleGeneratedOnly, "generated"},
		{"docs and generated", NewDiff(mod("README.md", 1), mod("gen/a.go", 1)), regen, RiskLow, RuleNoModelLowRisk, "documentation"},
		{"generated under an auth path is proven, not auth", NewDiff(mod("gen/auth/v1/auth.pb.go", 100)), regen, RiskLow, RuleGeneratedOnly, "generated"},
		{"generated without regen is read as code", NewDiff(mod("gen/a.go", 10)), noRegen, RiskMedium, RuleModel, "declares no `regen`"},
		{"agent instructions are not docs", NewDiff(mod("CLAUDE.md", 5)), plain, RiskMedium, RuleModel, "changed lines of code"},
		{"requirements.txt is not a doc", NewDiff(mod("requirements.txt", 1)), plain, RiskMedium, RuleModel, "changed lines of code"},
		{"small code", NewDiff(mod("internal/foo/foo.go", 30), mod("internal/foo/foo_test.go", 50), mod("README.md", 4)), plain,
			RiskMedium, RuleModel, "80 changed lines of code in 2 files"},
		{"author.go is not auth", NewDiff(mod("internal/blog/author.go", 5)), plain, RiskMedium, RuleModel, "none on a high-risk path"},
		{"tokenizer is not a token", NewDiff(mod("internal/llm/tokenizer.go", 5)), plain, RiskMedium, RuleModel, "none on a high-risk path"},
		{"migration", NewDiff(mod("db/migrations/20261125_x.up.sql", 3)), plain, RiskHigh, RuleModel, "a database migration (db/migrations/20261125_x.up.sql)"},
		{"migration readme in its directory is a doc", NewDiff(mod("db/migrations/README.md", 3)), plain, RiskLow, RuleNoModelLowRisk, "documentation"},
		{"removed migration", NewDiff(ChangedFile{Path: "db/migrations/1.up.sql", Status: FileRemoved, Deletions: 4}), plain, RiskHigh, RuleModel, "database migration"},
		{"renamed away from migrations", NewDiff(ChangedFile{Path: "old/1.sql", PreviousPath: "db/migrations/1.up.sql", Status: FileRenamed}), plain,
			RiskHigh, RuleModel, "database migration (old/1.sql)"},
		{"proto", NewDiff(mod("proto/reliant/v1/chat.proto", 2)), plain, RiskHigh, RuleModel, "API contract"},
		{"buf config", NewDiff(mod("buf.yaml", 1)), plain, RiskHigh, RuleModel, "API contract"},
		{"auth dir", NewDiff(mod("internal/auth/session.go", 2)), plain, RiskHigh, RuleModel, "auth or credential code"},
		{"oauth file", NewDiff(mod("internal/x/oauth_callback.go", 2)), plain, RiskHigh, RuleModel, "auth or credential code"},
		{"camelCase credential file", NewDiff(mod("frontends/web/src/lib/apiKeyStore.ts", 2)), plain, RiskHigh, RuleModel, "auth or credential code"},
		{"snake_case api key", NewDiff(mod("internal/x/api_key.go", 2)), plain, RiskHigh, RuleModel, "auth or credential code"},
		{"camelCase auth file", NewDiff(mod("frontends/web/src/lib/authStore.ts", 2)), plain, RiskHigh, RuleModel, "auth or credential code"},
		{"secrets", NewDiff(mod("deploy/secrets/prod.yaml", 2)), plain, RiskHigh, RuleModel, "auth or credential code"},
		{"github workflow", NewDiff(mod(".github/workflows/ci.yml", 2)), plain, RiskHigh, RuleModel, "CI configuration"},
		{"Taskfile", NewDiff(mod("Taskfile.yml", 2)), plain, RiskHigh, RuleModel, "CI configuration"},
		{"nested Taskfile is not CI", NewDiff(mod("tools/x/Taskfile.yml", 2)), plain, RiskMedium, RuleModel, "none on a high-risk path"},
		{"reliant workflow", NewDiff(mod(".reliant/workflows/pr.yaml", 2)), plain, RiskHigh, RuleModel, "Reliant configuration"},
		{"reliant prompt is not a doc", NewDiff(mod(".reliant/prompts/review.md", 2)), plain, RiskHigh, RuleModel, "Reliant configuration"},
		{"go.mod", NewDiff(mod("go.mod", 2)), plain, RiskHigh, RuleModel, "Go module definition"},
		{"nested go.mod", NewDiff(mod("tools/lint/go.mod", 2)), plain, RiskHigh, RuleModel, "Go module definition"},
		{"go.sum alone is not go.mod", NewDiff(mod("go.sum", 2)), plain, RiskMedium, RuleModel, "none on a high-risk path"},
		{"leading ./ is normalized", NewDiff(mod("./db/migrations/1.sql", 1)), plain, RiskHigh, RuleModel, "database migration"},
		{"large", NewDiff(mod("internal/a.go", 500), mod("internal/b.go", 301)), plain, RiskHigh, RuleModel, "801 changed lines of code (more than 800)"},
		{"large docs are still low", NewDiff(mod("docs/a.md", 5000)), plain, RiskLow, RuleNoModelLowRisk, "documentation"},
		{"large generated is still low", NewDiff(mod("gen/a.go", 50000)), regen, RiskLow, RuleGeneratedOnly, "generated"},
		{"incomplete file list", Diff{Files: []ChangedFile{mod("README.md", 1)}, ChangedFiles: 3001}, plain,
			RiskHigh, RuleModel, "changes 3001 files but only 1 were listed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := AssessRisk(c.diff, c.cfg)
			if a.Tier != c.tier || a.Rule != c.rule {
				t.Fatalf("got %s/%s, want %s/%s; reasons: %q", a.Tier, a.Rule, c.tier, c.rule, a.Reasons)
			}
			if a.NeedsModel() != (c.rule == RuleModel) {
				t.Errorf("NeedsModel = %v for rule %s", a.NeedsModel(), a.Rule)
			}
			if !containsReason(a.Reasons, c.reason) {
				t.Errorf("no reason contains %q: %q", c.reason, a.Reasons)
			}
			tier, reasons := ClassifyRisk(c.diff, c.cfg)
			if tier != a.Tier || !reflect.DeepEqual(reasons, a.Reasons) {
				t.Errorf("ClassifyRisk disagrees with AssessRisk: %s %q", tier, reasons)
			}
		})
	}
}

func TestAssessRiskWideChange(t *testing.T) {
	var files []ChangedFile
	for i := 0; i <= HighRiskFiles; i++ {
		files = append(files, mod("internal/pkg/f"+strings.Repeat("x", i)+".go", 1))
	}
	a := AssessRisk(NewDiff(files...), config.Defaults())
	if a.Tier != RiskHigh || !containsReason(a.Reasons, "41 files of code (more than 40)") {
		t.Fatalf("got %s %q", a.Tier, a.Reasons)
	}
}

func TestAssessRiskReasonsNameEveryKindInAFixedOrder(t *testing.T) {
	d := NewDiff(mod("go.mod", 1), mod(".github/workflows/x.yml", 1), mod("db/migrations/2.sql", 1), mod("db/migrations/1.sql", 1),
		mod("db/migrations/3.sql", 1), mod("db/migrations/4.sql", 1))
	a := AssessRisk(d, config.Defaults())
	want := []string{
		"touches a database migration (db/migrations/2.sql, db/migrations/1.sql, db/migrations/3.sql and 1 more): ",
		"touches CI configuration (.github/workflows/x.yml): ",
		"touches a Go module definition (go.mod): ",
	}
	if len(a.Reasons) != len(want) {
		t.Fatalf("reasons: %q", a.Reasons)
	}
	for i, w := range want {
		if !strings.HasPrefix(a.Reasons[i], w) {
			t.Errorf("reason %d = %q, want prefix %q", i, a.Reasons[i], w)
		}
	}
	// Deterministic: the same diff in another order gives the same tier and reason kinds.
	rev := NewDiff(d.Files[5], d.Files[4], d.Files[3], d.Files[2], d.Files[1], d.Files[0])
	b := AssessRisk(rev, config.Defaults())
	if b.Tier != a.Tier || len(b.Reasons) != len(a.Reasons) || !strings.HasPrefix(b.Reasons[2], "touches a Go module") {
		t.Errorf("reordered diff: %q", b.Reasons)
	}
}

func TestNewDiffTotals(t *testing.T) {
	d := NewDiff(ChangedFile{Path: "a", Additions: 2, Deletions: 3}, ChangedFile{Path: "b", Additions: 5})
	if d.Additions != 7 || d.Deletions != 3 || d.ChangedFiles != 2 {
		t.Fatalf("%+v", d)
	}
}

// The tier values are the ones a review node's output carries.
func TestRiskTiersMatchReviewOutput(t *testing.T) {
	for got, want := range map[RiskTier]string{RiskLow: model.ReviewRiskLow, RiskMedium: model.ReviewRiskMedium, RiskHigh: model.ReviewRiskHigh} {
		if string(got) != want {
			t.Errorf("%q != %q", got, want)
		}
	}
}

func TestSplitWords(t *testing.T) {
	for in, want := range map[string][]string{
		"oauth_callback": {"oauth", "callback"},
		"authStore":      {"auth", "store"},
		"apiKeyStore":    {"api", "key", "store"},
		"HTTPServer":     {"httpserver"},
		"v2Token":        {"v2", "token"},
		"author":         {"author"},
	} {
		if got := splitWords(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitWords(%q) = %q, want %q", in, got, want)
		}
	}
}

func containsReason(reasons []string, sub string) bool {
	for _, r := range reasons {
		if strings.Contains(r, sub) {
			return true
		}
	}
	return false
}
