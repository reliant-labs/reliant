// Copyright (c) 2025 Reliant Labs
package workflowcheck

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
)

const (
	headSHA  = "6dcb09b5b57875f334f61aebed695e2e4193db5e"
	otherSHA = "0123456789abcdef0123456789abcdef01234567"
)

func payload(data map[string]any) map[string]any {
	return map[string]any{"attributes": map[string]any{"repository": "reliant-labs/reliant"}, "data": data}
}

func scopeOf(p map[string]any) triggerspec.Scope {
	return triggerspec.Scope{Trigger: map[string]any{"kind": "integration", "payload": p}}
}

func TestDefaultHeadSHAPerEvent(t *testing.T) {
	for name, data := range map[string]map[string]any{
		"pull_request":  {"pull_request": map[string]any{"head": map[string]any{"sha": headSHA}}},
		"check_suite":   {"check_suite": map[string]any{"head_sha": headSHA}},
		"check_run":     {"check_run": map[string]any{"head_sha": headSHA}},
		"workflow_run":  {"workflow_run": map[string]any{"head_sha": headSHA}},
		"push":          {"after": headSHA},
		"upper-cased":   {"after": strings.ToUpper(headSHA)},
		"pr beats push": {"pull_request": map[string]any{"head": map[string]any{"sha": headSHA}}, "after": otherSHA},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, headSHA, DefaultHeadSHA(payload(data)))
		})
	}
	require.Empty(t, DefaultHeadSHA(payload(map[string]any{"after": zeroSHA})), "a deleted branch names no commit")
	require.Empty(t, DefaultHeadSHA(map[string]any{"scheduled_for": "2026-11-18T00:00:00Z"}), "a schedule names no commit")
}

func TestResolveTarget(t *testing.T) {
	p := payload(map[string]any{"check_suite": map[string]any{"head_sha": headSHA}, "alt": otherSHA})

	got, err := ResolveTarget(&Spec{Name: "ci"}, p, scopeOf(p))
	require.NoError(t, err)
	require.Equal(t, Target{Owner: "reliant-labs", Repo: "reliant", HeadSHA: headSHA}, got)

	got, err = ResolveTarget(&Spec{Name: "ci", HeadSHA: "{{ trigger.payload.data.alt }}", Repository: "octo/hello.world"}, p, scopeOf(p))
	require.NoError(t, err)
	require.Equal(t, Target{Owner: "octo", Repo: "hello.world", HeadSHA: otherSHA}, got)

	for name, spec := range map[string]*Spec{
		"template that fails":  {HeadSHA: "{{ trigger.payload.data.missing }}"},
		"not a sha":            {HeadSHA: "main"},
		"not a repository":     {Repository: "reliant"},
		"repository with path": {Repository: "a/b/c"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ResolveTarget(spec, p, scopeOf(p))
			require.Error(t, err)
		})
	}
	_, err = ResolveTarget(&Spec{}, map[string]any{}, scopeOf(nil))
	require.ErrorContains(t, err, "names no repository")
}

func TestSpecOfAndTimeout(t *testing.T) {
	spec, err := SpecOf(&reliantv1.Workflow{})
	require.NoError(t, err)
	require.Nil(t, spec)

	spec, err = SpecOf(&reliantv1.Workflow{Check: &reliantv1.WorkflowCheck{Name: " reliant/ci "}})
	require.NoError(t, err)
	require.Equal(t, "reliant/ci", spec.Name)
	require.Equal(t, DefaultTimeout, spec.Timeout)

	for value, want := range map[string]time.Duration{"1m": time.Minute, "2h": 2 * time.Hour, "72h": 72 * time.Hour} {
		got, err := ParseTimeout(value)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	for _, bad := range []string{"30s", "73h", "soon", "-1h"} {
		_, err := ParseTimeout(bad)
		require.Error(t, err, bad)
	}
}

func TestValidateName(t *testing.T) {
	require.NoError(t, ValidateName("reliant/ci"))
	require.Error(t, ValidateName(""))
	require.Error(t, ValidateName("{{ inputs.name }}"))
	require.Error(t, ValidateName(strings.Repeat("x", 256)))
	require.NoError(t, ValidateHeadSHA("{{ trigger.payload.data.after }}"))
	require.Error(t, ValidateHeadSHA("HEAD"))
	require.NoError(t, ValidateRepository("reliant-labs/reliant"))
	require.Error(t, ValidateRepository("reliant"))
}
