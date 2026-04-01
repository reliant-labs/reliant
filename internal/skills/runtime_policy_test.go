package skills

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type staticCatalog struct {
	result Result
}

func cloneResult(in Result) Result {
	out := Result{
		Skills:       make([]Skill, 0, len(in.Skills)),
		ByName:       make(map[string]Skill, len(in.ByName)),
		Diagnostics:  append([]Diagnostic(nil), in.Diagnostics...),
		ShadowedBy:   make(map[string]string, len(in.ShadowedBy)),
		ShadowedFrom: make(map[string]string, len(in.ShadowedFrom)),
	}
	for _, skill := range in.Skills {
		out.Skills = append(out.Skills, cloneTestSkill(skill))
	}
	for key, skill := range in.ByName {
		out.ByName[key] = cloneTestSkill(skill)
	}
	for key, value := range in.ShadowedBy {
		out.ShadowedBy[key] = value
	}
	for key, value := range in.ShadowedFrom {
		out.ShadowedFrom[key] = value
	}
	return out
}

func cloneTestSkill(in Skill) Skill {
	out := in
	if in.Metadata != nil {
		out.Metadata = make(map[string]string, len(in.Metadata))
		for key, value := range in.Metadata {
			out.Metadata[key] = value
		}
	}
	if in.AllowedTools != nil {
		out.AllowedTools = append([]string(nil), in.AllowedTools...)
	}
	if in.Files != nil {
		out.Files = append([]SupportingFile(nil), in.Files...)
	}
	return out
}

func (s staticCatalog) discover(_ context.Context, _ DiscoverInput) Result {
	return cloneResult(s.result)
}

type fixedSelector struct {
	active *Skill
}

func (s fixedSelector) resolveActiveSkill(_ Result, _ activationInput) (*Skill, []Notice) {
	if s.active == nil {
		return nil, nil
	}
	clone := *s.active
	return &clone, nil
}

type passthroughActivator struct{}

func (passthroughActivator) buildActiveSkillContext(active Skill, _ buildContextInput) (Skill, []Diagnostic) {
	return active, nil
}

type recordingPolicyEngine struct {
	calls       int
	lastActive  *Skill
	lastToolSet []string
	notices     []Notice
}

func (p *recordingPolicyEngine) filterAllowedToolNames(activeSkill *Skill, availableToolNames []string) ([]string, []Notice) {
	p.calls++
	if activeSkill != nil {
		clone := *activeSkill
		p.lastActive = &clone
	} else {
		p.lastActive = nil
	}
	if availableToolNames != nil {
		p.lastToolSet = append([]string(nil), availableToolNames...)
	} else {
		p.lastToolSet = nil
	}
	return append([]string(nil), availableToolNames...), append([]Notice(nil), p.notices...)
}

func TestRuntimeResolveTurn_PolicyEngineBoundary_ActiveSkillAndNotices(t *testing.T) {
	active := Skill{Name: "demo", NormalizedKey: "demo", Description: "demo skill", Scope: ScopeProject}
	discovered := Result{
		Skills: []Skill{active},
		ByName: map[string]Skill{"demo": active},
	}

	policy := &recordingPolicyEngine{
		notices: []Notice{{Level: NoticeLevelWarning, Message: "policy blocked Bash"}},
	}

	runtime := newRuntime(runtimeOptions{
		catalog:        staticCatalog{result: discovered},
		selector:       fixedSelector{active: &active},
		activator:      passthroughActivator{},
		promptRenderer: promptSectionRenderer{},
		policyEngine:   policy,
	})

	resolved := runtime.ResolveTurn(context.Background(), ResolveTurnInput{ProjectPath: "/tmp/project", AvailableToolNames: []string{"view", "edit"}})
	require.NotNil(t, resolved.ActiveSkill)
	require.Equal(t, "demo", resolved.ActiveSkill.Name)
	require.Equal(t, 1, policy.calls)
	require.NotNil(t, policy.lastActive)
	require.Equal(t, "demo", policy.lastActive.Name)
	require.Equal(t, []string{"view", "edit"}, policy.lastToolSet)
	require.Contains(t, resolved.Notices, Notice{Level: NoticeLevelWarning, Message: "policy blocked Bash"})
	require.Contains(t, resolved.WarningHints, "policy blocked Bash")
	require.Equal(t, []string{"view", "edit"}, resolved.AllowedToolNames)
}

func TestRuntimeResolveTurn_PolicyEngineBoundary_NoActiveSkill(t *testing.T) {
	discovered := Result{Skills: []Skill{}, ByName: map[string]Skill{}}
	policy := &recordingPolicyEngine{}

	runtime := newRuntime(runtimeOptions{
		catalog:        staticCatalog{result: discovered},
		selector:       fixedSelector{active: nil},
		activator:      passthroughActivator{},
		promptRenderer: promptSectionRenderer{},
		policyEngine:   policy,
	})

	resolved := runtime.ResolveTurn(context.Background(), ResolveTurnInput{ProjectPath: "/tmp/project", ActivationMode: "explicit", AvailableToolNames: []string{"view"}})
	require.Nil(t, resolved.ActiveSkill)
	require.Equal(t, 1, policy.calls)
	require.Nil(t, policy.lastActive)
	require.Equal(t, []string{"view"}, resolved.AllowedToolNames)
}
