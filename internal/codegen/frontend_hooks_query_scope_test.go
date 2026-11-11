package codegen

import (
	"strings"
	"testing"
)

// runService is reliant's RunService in miniature: a CRUD entity (Run, with
// GetRun/ListRuns/CreateRun) and two custom reads — LastRunPerWorkflow,
// which reads runs, and Health, which reads nothing of them.
func runService() ServiceDef {
	m := func(name string, noSideEffects bool) Method {
		return Method{Name: name, InputType: name + "Request", OutputType: name + "Response", NoSideEffects: noSideEffects}
	}
	return ServiceDef{
		Name:    "RunService",
		Package: "reliant.v1",
		Methods: []Method{
			m("GetRun", false), m("ListRuns", false), m("CreateRun", false),
			m("LastRunPerWorkflow", true), m("Health", true), m("PauseRun", false),
		},
		SchemaFiles: map[string]string{
			"reliant.v1.Run":      "proto/reliant/v1/run.proto",
			"reliant.v1.Workflow": "proto/reliant/v1/run.proto",
		},
	}
}

func methodByName(t *testing.T, data FrontendHookTemplateData, name string) FrontendHookMethod {
	t.Helper()
	for _, m := range data.Methods {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("no method %s", name)
	return FrontendHookMethod{}
}

// TestApplyQueryScopes_KeysACustomReadUnderItsEntity is the defect: a
// custom read sits at service scope, so CreateRun — which invalidates
// [service, "run"] — never refreshes it. Declared, it is keyed under run.
func TestApplyQueryScopes_KeysACustomReadUnderItsEntity(t *testing.T) {
	svc := runService()
	data := ServiceDefToHookData(svc)
	if got := methodByName(t, data, "LastRunPerWorkflow").EntityScope; got != "" {
		t.Fatalf("precondition: an undeclared custom read is unscoped, got %q", got)
	}
	if err := ApplyQueryScopes(&data, svc, map[string]string{"LastRunPerWorkflow": "Run"}); err != nil {
		t.Fatalf("ApplyQueryScopes: %v", err)
	}
	if got := methodByName(t, data, "LastRunPerWorkflow").EntityScope; got != "run" {
		t.Errorf("LastRunPerWorkflow scope = %q, want run", got)
	}
	if got := methodByName(t, data, "Health").EntityScope; got != "" {
		t.Errorf("an unmarked custom read must stay at service scope, got %q", got)
	}
	if strings.Join(data.EntityScopes, ",") != "run" {
		t.Errorf("EntityScopes = %v, want [run]", data.EntityScopes)
	}
}

// TestApplyQueryScopes_DeclaresAScopeNoCRUDRpcHas: a service with no CRUD
// for the entity (reliant's real RunService) still gets the scope key, so a
// caller can invalidate `<svc>Keys.workflow` by hand.
func TestApplyQueryScopes_DeclaresAScopeNoCRUDRpcHas(t *testing.T) {
	svc := runService()
	data := ServiceDefToHookData(svc)
	if err := ApplyQueryScopes(&data, svc, map[string]string{"Health": "Workflow"}); err != nil {
		t.Fatalf("ApplyQueryScopes: %v", err)
	}
	if strings.Join(data.EntityScopes, ",") != "run,workflow" {
		t.Errorf("EntityScopes = %v, want [run workflow]", data.EntityScopes)
	}
}

// TestApplyQueryScopes_RefusesWhatItCannotHonour: every refusal is a scope
// that would otherwise do nothing, or the wrong thing, in silence.
func TestApplyQueryScopes_RefusesWhatItCannotHonour(t *testing.T) {
	for _, tc := range []struct {
		name   string
		scopes map[string]string
		want   string
	}{
		{"no entity", map[string]string{"LastRunPerWorkflow": ""}, "names no entity"},
		{"on a mutation", map[string]string{"PauseRun": "Run"}, "generates a useMutation"},
		{"lowercase", map[string]string{"LastRunPerWorkflow": "run"}, "did you mean Run?"},
		{"typo", map[string]string{"LastRunPerWorkflow": "Runs"}, "did you mean Run?"},
		{"crud read under another entity", map[string]string{"GetRun": "Workflow"}, `already keyed under "run"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := runService()
			data := ServiceDefToHookData(svc)
			err := ApplyQueryScopes(&data, svc, tc.scopes)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestApplyQueryScopes_RedundantCRUDScopeIsAccepted: naming a CRUD read's
// own entity changes nothing and is not an error.
func TestApplyQueryScopes_RedundantCRUDScopeIsAccepted(t *testing.T) {
	svc := runService()
	data := ServiceDefToHookData(svc)
	if err := ApplyQueryScopes(&data, svc, map[string]string{"GetRun": "Run"}); err != nil {
		t.Fatalf("ApplyQueryScopes: %v", err)
	}
	if got := methodByName(t, data, "GetRun").EntityScope; got != "run" {
		t.Errorf("GetRun scope = %q", got)
	}
}
