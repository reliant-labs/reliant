// Copyright (c) 2025 Reliant Labs
package validation

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	skillscore "github.com/reliant-labs/reliant/internal/skills/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func testResolver(names ...string) *SkillResolver {
	return &SkillResolver{
		Names:   names,
		Resolve: func(p string) bool { return skillscore.ResolveSkillPathIndex(names, p) >= 0 },
	}
}

func skillErrors(t *testing.T, wf *reliantv1.Workflow, r *SkillResolver) []*Error {
	t.Helper()
	result := NewResult()
	validateSkillReferences(wf, &ValidationOptions{SkillResolver: r}, result)
	return result.ByCategory(CategorySkillRef)
}

func workflowNode(id string, args *reliantv1.Node_Workflow) *reliantv1.Node {
	return &reliantv1.Node{Id: id, Type: "workflow", Args: args}
}

func skillsArg(values ...string) map[string]*structpb.Value {
	list := make([]*structpb.Value, 0, len(values))
	for _, v := range values {
		list = append(list, structpb.NewStringValue(v))
	}
	return map[string]*structpb.Value{
		"skills": structpb.NewListValue(&structpb.ListValue{Values: list}),
	}
}

// TestValidateSkillReferencesRejectsUnresolvable is the unit-level proof that
// the guard fails on a bad name. It reproduces the exact drift found in a real
// forge-one-shot run: forge surfaces service-layer at its BARE path, the
// charter asked for it under the synthetic namespace, and the preloader
// silently skipped it on every run.
func TestValidateSkillReferencesRejectsUnresolvable(t *testing.T) {
	wf := &reliantv1.Workflow{
		Name: "charter",
		Nodes: []*reliantv1.Node{
			workflowNode("build", &reliantv1.Node_Workflow{
				Workflow: &reliantv1.SubWorkflowArgs{
					Args: skillsArg("forge/architecture", "forge/service-layer"),
				},
			}),
		},
	}

	errs := skillErrors(t, wf, testResolver("forge/architecture", "service-layer"))
	require.Len(t, errs, 1, "exactly the unresolvable name should be reported")
	assert.Contains(t, errs[0].Message, `"forge/service-layer"`)
	assert.Contains(t, errs[0].Suggestion, `"service-layer"`, "should point at the path the skill is actually addressable by")
	assert.Equal(t, []string{"nodes", "build", "workflow", "args"}, errs[0].Path)
}

func TestValidateSkillReferencesAcceptsResolvable(t *testing.T) {
	wf := &reliantv1.Workflow{
		Nodes: []*reliantv1.Node{
			workflowNode("build", &reliantv1.Node_Workflow{
				Workflow: &reliantv1.SubWorkflowArgs{
					Args: skillsArg("forge/architecture", "service-layer"),
				},
			}),
			// The component-aligned suffix spelling forge's own CLI prints.
			workflowNode("polish", &reliantv1.Node_Workflow{
				Workflow: &reliantv1.SubWorkflowArgs{
					Args: skillsArg("frontend/design"),
				},
			}),
		},
	}

	assert.Empty(t, skillErrors(t, wf, testResolver("forge/architecture", "service-layer", "forge/frontend/design")))
}

// TestValidateSkillReferencesChecksCallLLMLiterals covers the terminal
// consumer: skills named directly on a call_llm node.
func TestValidateSkillReferencesChecksCallLLMLiterals(t *testing.T) {
	wf := &reliantv1.Workflow{
		Nodes: []*reliantv1.Node{{
			Id:   "ask",
			Type: "call_llm",
			Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{
				Skills: &reliantv1.CelStringList{
					Value: &reliantv1.CelStringList_Literal{
						Literal: &reliantv1.StringList{Values: []string{"nonexistent-skill"}},
					},
				},
			}},
		}},
	}

	errs := skillErrors(t, wf, testResolver("service-layer"))
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Message, "nonexistent-skill")
}

// TestValidateSkillReferencesChecksInputDefaults covers a `skills` input whose
// DEFAULT names a skill — just as silent a failure, and the shape
// scope-conversation.yaml ships.
func TestValidateSkillReferencesChecksInputDefaults(t *testing.T) {
	wf := &reliantv1.Workflow{
		Inputs: map[string]*reliantv1.Input{
			"skills": {Type: "array", Config: &reliantv1.Input_ArrayInput{
				ArrayInput: &reliantv1.ArrayInputConfig{
					Default: structpb.NewListValue(&structpb.ListValue{
						Values: []*structpb.Value{structpb.NewStringValue("ghost")},
					}),
				},
			}},
		},
	}

	errs := skillErrors(t, wf, testResolver("general-agent"))
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Message, "ghost")
}

// TestValidateSkillReferencesSkipsTemplates is the false-positive guard. A
// templated value names nothing until the run binds it, so it must not be
// guessed at — otherwise every forwarding workflow ("{{inputs.skills}}") would
// fail validation and the check would be turned off.
func TestValidateSkillReferencesSkipsTemplates(t *testing.T) {
	wf := &reliantv1.Workflow{
		Nodes: []*reliantv1.Node{
			workflowNode("forward", &reliantv1.Node_Workflow{
				Workflow: &reliantv1.SubWorkflowArgs{
					Args: map[string]*structpb.Value{
						"skills": structpb.NewStringValue("{{inputs.skills}}"),
					},
				},
			}),
			{
				Id:   "expr",
				Type: "call_llm",
				Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{
					Skills: &reliantv1.CelStringList{
						Value: &reliantv1.CelStringList_Expr{Expr: "inputs.skills"},
					},
				}},
			},
		},
	}

	assert.Empty(t, skillErrors(t, wf, testResolver("service-layer")))
}

// TestValidateSkillReferencesNoResolverIsNoOp pins the optional-capability
// contract: library callers that supply no catalog are not broken by this
// layer. The guard against that becoming a silent no-op everywhere is
// TestBuiltinWorkflowSkillsResolve, which always supplies a real one.
func TestValidateSkillReferencesNoResolverIsNoOp(t *testing.T) {
	wf := &reliantv1.Workflow{
		Nodes: []*reliantv1.Node{
			workflowNode("build", &reliantv1.Node_Workflow{
				Workflow: &reliantv1.SubWorkflowArgs{Args: skillsArg("totally-made-up")},
			}),
		},
	}

	result := NewResult()
	validateSkillReferences(wf, &ValidationOptions{}, result)
	assert.Empty(t, result.ByCategory(CategorySkillRef))
}

// TestValidateSkillReferencesChecksLiteralsInAMixedList: a list that mixes a
// template with literal names must still have its literals checked. Abandoning
// the whole list on the first unknowable entry would let a typo ride along
// beside any templated entry.
func TestValidateSkillReferencesChecksLiteralsInAMixedList(t *testing.T) {
	wf := &reliantv1.Workflow{
		Nodes: []*reliantv1.Node{
			workflowNode("build", &reliantv1.Node_Workflow{
				Workflow: &reliantv1.SubWorkflowArgs{
					Args: skillsArg("{{inputs.extra}}", "service-layer", "forge/testing"),
				},
			}),
		},
	}

	errs := skillErrors(t, wf, testResolver("service-layer", "testing"))
	require.Len(t, errs, 1, "the templated entry is skipped, the bad literal is not")
	assert.Contains(t, errs[0].Message, `"forge/testing"`)
	assert.Contains(t, errs[0].Suggestion, `"testing"`)
}
