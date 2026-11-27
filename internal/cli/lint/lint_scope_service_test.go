package lint

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/projectstore"
)

// A scope over one half of a service reports findings in its other half:
// --scope internal/handlers passed while the unscoped run failed
// forgeconv-activity-declared, which lands on the rpc's line in the .proto
// (Ranger build, friction #9). Reporting is all it widens: what --fix may
// write (contains) stays the asked scope.
func TestScopeReportsAServicesOtherHalf(t *testing.T) {
	root := scopeFixture(t)
	if err := os.MkdirAll(filepath.Join(root, "proto", "services", "billing", "v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	const jobsProto = "proto/services/jobs/v1/jobs.proto"
	const billingProto = "proto/services/billing/v1/billing.proto"
	const jobsHandler = "internal/handlers/jobs/service.go"

	cases := []struct {
		name           string
		scope          []string
		wantCompanions []string
		reported       []string
		notReported    []string
	}{
		{"handler dir reports its proto", []string{"internal/handlers/jobs"}, []string{"proto/services/jobs"},
			[]string{jobsHandler, jobsProto}, []string{billingProto, "internal/handlers/jobs2/service.go"}},
		{"handler file reports its proto", []string{jobsHandler}, []string{"proto/services/jobs"},
			[]string{jobsProto}, []string{billingProto, "internal/handlers/jobs/inner/x/inner.go"}},
		{"proto dir reports its handler", []string{"proto/services/jobs/v1"}, []string{"internal/handlers/jobs"},
			[]string{jobsHandler, jobsProto}, []string{"internal/handlers/jobs2/service.go"}},
		{"the handler tree reports every proto", []string{"internal/handlers"}, []string{"proto/services"},
			[]string{jobsProto, billingProto}, []string{"db/migrations/0001_init.up.sql"}},
		{"a parent of the handler tree reports every proto", []string{"internal"}, []string{"proto/services"},
			[]string{jobsProto, billingProto}, nil},
		{"a handler with no proto reports nothing more", []string{"internal/handlers/jobs2"}, nil,
			nil, []string{jobsProto, billingProto}},
		{"both halves named: nothing to add", []string{"internal/handlers/jobs", "proto/services/jobs"}, nil,
			[]string{jobsHandler, jobsProto}, []string{billingProto}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := parseLintScope(root, tc.scope)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(s.companions, tc.wantCompanions) {
				t.Errorf("companions = %v, want %v", s.companions, tc.wantCompanions)
			}
			for _, f := range tc.reported {
				if !s.reports(f) {
					t.Errorf("%s is not reported", f)
				}
			}
			for _, f := range tc.notReported {
				if s.reports(f) {
					t.Errorf("%s is reported", f)
				}
			}
			for _, c := range tc.wantCompanions {
				if s.contains(c + "/x") {
					t.Errorf("companion %s widened contains, which bounds what --fix writes", c)
				}
			}
		})
	}
}

// The real convention collector scans proto/ regardless of rc.paths or scope.
// Exercise both dispatch paths so a fabricated finding cannot hide a regression
// in collection, anchoring, filtering, or gating.
func TestScopedConventionCollectorFindsServiceProto(t *testing.T) {
	root := scopeFixture(t)
	for rel, body := range map[string]string{
		"proto/services/jobs/v1/jobs.proto": `syntax = "proto3";
package jobs.v1;
service JobService {
  rpc RotateKey(RotateKeyRequest) returns (RotateKeyResponse);
}
message RotateKeyRequest {}
message RotateKeyResponse {}
`,
		"gen/forge_descriptor/fixture.json": `{"services":[{"name":"JobService","package":"jobs.v1","ProtoFile":"services/jobs/v1/jobs.proto","methods":[{"name":"RotateKey","activity":{"mutating":true}}]}]}`,
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	// CLI normally registers this loader during init. This package's tests
	// run without that CLI bootstrap, including the changed-tests gate.
	previousLoader := factory.New().LoadProjectStore
	factory.SetProjectStoreLoader(func() (*projectstore.Store, error) {
		return nil, ErrProjectConfigNotFound
	})
	t.Cleanup(func() { factory.SetProjectStoreLoader(previousLoader) })
	var convention linterStep
	for _, step := range lintPipeline() {
		if step.name == "forge convention lint" {
			convention = step
		}
	}
	if convention.collect == nil {
		t.Fatal("convention lane missing from pipeline")
	}
	for _, paths := range [][]string{nil, {"internal/handlers/jobs"}, {"internal/handlers"}} {
		scope, err := parseLintScope(root, paths)
		if err != nil {
			t.Fatal(err)
		}
		rc := &lintRunCtx{ctx: context.Background(), cwd: root, paths: []string{"internal/handlers/jobs"}, scope: scope}
		full := collectLintOutcomesFrom(rc, []linterStep{convention})
		targeted, handled, err := collectTargetedOutcomes(rc.ctx, lintFlags{conventions: true}, rc.paths, root, nil, nil, scope, targetedOutcomeOutput{})
		if err != nil || !handled {
			t.Fatalf("targeted collection: handled=%v err=%v", handled, err)
		}
		for _, outcomes := range [][]lintStepOutcome{full, targeted} {
			found := false
			for _, o := range outcomes {
				for _, f := range o.findings {
					if f.Rule == "forgeconv-activity-declared" && f.File == "proto/services/jobs/v1/jobs.proto" {
						found = true
						if !o.gated || f.Severity != lintSevError {
							t.Fatalf("scope %v did not gate real proto error: %+v", paths, o)
						}
					}
				}
			}
			if !found {
				t.Fatalf("scope %v lost real activity finding: %+v", paths, outcomes)
			}
		}
	}
}

// End to end through the engine and the verdict: a by-file lane's error on
// the scoped handler's proto gates the scoped run, and an error outside the
// scope altogether does not gate it but is counted on the last line, so a
// scoped pass never reads as "the unscoped run passes too".
func TestScopedRunReportsServiceProtoAndCountsErrorsOutside(t *testing.T) {
	root := scopeFixture(t)
	scope, err := parseLintScope(root, []string{"internal/handlers/jobs"})
	if err != nil {
		t.Fatal(err)
	}
	always := func(*lintRunCtx) (bool, string) { return true, "" }
	protoErr := lintJSONFinding{File: "proto/services/jobs/v1/jobs.proto", Line: 7, Severity: lintSevError, Rule: "forgeconv-activity-declared"}
	migrationErr := lintJSONFinding{File: "db/migrations/0002_x.up.sql", Severity: lintSevError, Rule: "forge-db-grants-undecided"}
	steps := []linterStep{
		{
			name: "forge convention lint", gates: true, scope: scopeByFile, shouldRun: always,
			collect: func(*lintRunCtx) ([]lintJSONFinding, bool, error) { return []lintJSONFinding{protoErr}, true, nil },
		},
		{
			name: "db-grants lint", gates: true, scope: scopeByFile, shouldRun: always,
			collect: func(*lintRunCtx) ([]lintJSONFinding, bool, error) {
				return []lintJSONFinding{migrationErr}, true, nil
			},
		},
	}
	outcomes := collectLintOutcomesFrom(&lintRunCtx{cwd: root, paths: []string{"./..."}, scope: scope}, steps)
	if o := outcomes[0]; !o.gated || len(o.findings) != 1 || o.outside != 0 {
		t.Errorf("convention lane: gated=%v findings=%+v outside=%d; want the service's proto finding to gate", o.gated, o.findings, o.outside)
	}
	if o := outcomes[1]; o.gated || len(o.findings) != 0 || o.outside != 1 {
		t.Errorf("db-grants lane: gated=%v findings=%+v outside=%d; want no gate and one error counted outside", o.gated, o.findings, o.outside)
	}

	var buf bytes.Buffer
	err = renderLintOutcomes(&buf, outcomes, true, scope)
	if err == nil || !strings.Contains(err.Error(), "forge convention lint") {
		t.Fatalf("verdict err = %v; want the convention lane to fail the scoped run", err)
	}
	if want := "1 error outside the scope not shown (db-grants lint)"; !strings.Contains(err.Error(), want) {
		t.Errorf("verdict %q does not say %q", err.Error(), want)
	}

	// Only the out-of-scope lane: the scoped run passes, and says what an
	// unscoped run will report.
	buf.Reset()
	if err := renderLintOutcomes(&buf, outcomes[1:], true, scope); err != nil {
		t.Fatalf("scoped run with only out-of-scope errors failed: %v", err)
	}
	got := strings.TrimRight(buf.String(), "\n")
	for _, want := range []string{"1 error outside the scope not shown (db-grants lint)", "an unscoped `forge lint` reports them"} {
		if !strings.Contains(got, want) {
			t.Errorf("verdict %q missing %q", got, want)
		}
	}
}
