package fluxdelivery_test

import (
	"encoding/json"
	"strings"
	"testing"

	"cel.dev/cel-go/cel"

	"github.com/reliant-labs/forge/pkg/fluxdelivery"
)

// fluxStatus evaluates e the way Flux's CEL status reader does (RFC 0009): a
// stale observedGeneration is InProgress, then InProgress, Failed, Current in
// that order; nothing true is InProgress. Fields are top-level variables.
func fluxStatus(t *testing.T, e fluxdelivery.HealthCheckExpr, obj map[string]any) string {
	t.Helper()
	meta, _ := obj["metadata"].(map[string]any)
	st, _ := obj["status"].(map[string]any)
	if og, ok := st["observedGeneration"]; ok && og != meta["generation"] {
		return "InProgress"
	}
	return exprStatus(t, e, obj)
}

// exprStatus is fluxStatus without Flux's stale-generation pre-check: what
// the expressions alone decide.
func exprStatus(t *testing.T, e fluxdelivery.HealthCheckExpr, obj map[string]any) string {
	t.Helper()
	env, err := cel.NewEnv()
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ expr, status string }{
		{e.InProgress, "InProgress"}, {e.Failed, "Failed"}, {e.Current, "Current"},
	} {
		if step.expr == "" {
			continue
		}
		ast, issues := env.Parse(step.expr)
		if issues != nil && issues.Err() != nil {
			t.Fatalf("%s does not parse: %v", step.status, issues.Err())
		}
		program, err := env.Program(ast)
		if err != nil {
			t.Fatal(err)
		}
		out, _, err := program.Eval(obj)
		if err != nil {
			t.Fatalf("%s: %v", step.status, err)
		}
		if out.Value() == true {
			return step.status
		}
	}
	return "InProgress"
}

func tierObject(generation int64, conditions ...map[string]any) map[string]any {
	conds := make([]any, len(conditions))
	for i, c := range conditions {
		conds[i] = c
	}
	return map[string]any{
		"metadata": map[string]any{"name": "api", "generation": generation},
		"status":   map[string]any{"observedGeneration": generation, "conditions": conds},
	}
}

func tierCond(typ, status, reason string, generation int64) map[string]any {
	return map[string]any{"type": typ, "status": status, "reason": reason, "observedGeneration": generation}
}

// A TIER SUSPENDED BY ITS OWNER IS SETTLED. The shape is control-plane prod's
// egresscanary Workload (2026-11-01): Ready=False/UserSuspended beside
// Suspended=True. kstatus alone reads it as InProgress, forever.
func TestForgeTierHealthExprs_AStoppedTierIsSettled(t *testing.T) {
	exprs := fluxdelivery.ForgeTierHealthExprs()
	kinds := map[string]bool{}
	for _, e := range exprs {
		if want, ok := fluxdelivery.HealthAPIVersion(e.Kind); !ok || e.APIVersion != want {
			t.Fatalf("%s carries apiVersion %q; HealthAPIVersion says %q (known %v)", e.Kind, e.APIVersion, want, ok)
		}
		kinds[e.Kind] = true
	}
	for _, k := range []string{"Workload", "ManagedDatabase", "StaticSite"} {
		if !kinds[k] {
			t.Fatalf("no tier health expression for %s", k)
		}
	}
	e := exprs[0]
	cases := map[string]struct {
		obj  map[string]any
		want string
	}{
		"suspended by its owner": {tierObject(1,
			tierCond("Ready", "False", "UserSuspended", 1), tierCond("ComputeEntitled", "True", "PlanActive", 1),
			tierCond("Suspended", "True", "UserSuspended", 1)), "Current"},
		"ready":                 {tierObject(1, tierCond("Ready", "True", "Ready", 1)), "Current"},
		"rolling out":           {tierObject(1, tierCond("Ready", "False", "Rollout", 1), tierCond("Suspended", "False", "PlanActive", 1)), "InProgress"},
		"stalled (fail fast)":   {tierObject(1, tierCond("Ready", "False", "NotReadyTimeout", 1), tierCond("Stalled", "True", "NotReadyTimeout", 1)), "Failed"},
		"reconciling":           {tierObject(1, tierCond("Reconciling", "True", "Progressing", 1), tierCond("Ready", "True", "Ready", 1)), "InProgress"},
		"no conditions yet":     {tierObject(1), "InProgress"},
		"suspension is stale":   {tierObject(2, tierCond("Ready", "False", "UserSuspended", 1), tierCond("Suspended", "True", "UserSuspended", 1)), "InProgress"},
		"generation unobserved": {map[string]any{"metadata": map[string]any{"generation": int64(2)}, "status": map[string]any{"observedGeneration": int64(1)}}, "InProgress"},
	}
	for name, tc := range cases {
		if got := fluxStatus(t, e, tc.obj); got != tc.want {
			t.Errorf("%s: Flux reads %s, want %s", name, got, tc.want)
		}
	}
}

// A READY=TRUE LEFT FROM A PREVIOUS SPEC IS NOT CURRENT. Current itself checks
// status.observedGeneration, without leaning on Flux's own stale-generation
// pre-check, so it is evaluated here alone.
func TestForgeTierCurrent_StaleReadyIsNotCurrent(t *testing.T) {
	current := fluxdelivery.HealthCheckExpr{Current: fluxdelivery.ForgeTierCurrent}
	stale := tierObject(1, tierCond("Ready", "True", "Ready", 1))
	stale["metadata"].(map[string]any)["generation"] = int64(2)
	unobserved := tierObject(2, tierCond("Ready", "True", "Ready", 2))
	delete(unobserved["status"].(map[string]any), "observedGeneration")
	cases := map[string]struct {
		obj  map[string]any
		want string
	}{
		"ready at the current generation":         {tierObject(2, tierCond("Ready", "True", "Ready", 2)), "Current"},
		"ready for a previous generation":         {stale, "InProgress"},
		"ready with no observedGeneration at all": {unobserved, "InProgress"},
	}
	for name, tc := range cases {
		if got := exprStatus(t, current, tc.obj); got != tc.want {
			t.Errorf("%s: Current reads %s, want %s", name, got, tc.want)
		}
	}
}

// The new fields are optional on the wire: an expression that sets only
// Current marshals exactly as before, so no existing applier changes.
func TestHealthCheckExpr_CurrentOnlyIsUnchangedOnTheWire(t *testing.T) {
	b, err := json.Marshal(fluxdelivery.HealthCheckExpr{APIVersion: "external-secrets.io/v1", Kind: "ExternalSecret", Current: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); strings.Contains(s, "inProgress") || strings.Contains(s, "failed") {
		t.Fatalf("a Current-only expression gained fields: %s", s)
	}
}
