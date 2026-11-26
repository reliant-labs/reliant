package model

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

func lit(s string) *reliantv1.CelString {
	return &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: s}}
}

func reviewNode(prompt string) *reliantv1.Node {
	return &reliantv1.Node{
		Id:              "review",
		Type:            NodeTypeReview,
		Role:            RoleReviewer,
		TolerateFailure: true,
		Condition:       &reliantv1.DirectCelBool{Expr: "inputs.go"},
		Args: &reliantv1.Node_Review{Review: &reliantv1.ReviewArgs{
			Prompt:   lit(prompt),
			With:     map[string]*structpb.Value{"head": structpb.NewStringValue("{{ inputs.head }}")},
			Model:    &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Literal{Literal: &reliantv1.ModelSelector{Tags: []string{"fast"}}}},
			MaxTurns: &reliantv1.CelInt{Value: &reliantv1.CelInt_Literal{Literal: 12}},
		}},
	}
}

func noPrompts(string) (string, error) { return "", errors.New("none") }

func TestLowerReviewNode_RunsTheReviewerInAFreshSession(t *testing.T) {
	t.Parallel()
	lowered, err := LowerReviewNode(reviewNode("Review it."), noPrompts)
	require.NoError(t, err)

	assert.Equal(t, NodeTypeWorkflow, lowered.GetType())
	assert.Equal(t, RoleReviewer, lowered.GetRole())
	assert.Equal(t, "review", lowered.GetId())
	assert.True(t, lowered.GetTolerateFailure(), "base fields carry over")
	assert.Equal(t, "inputs.go", lowered.GetCondition().GetExpr())

	args := lowered.GetWorkflow()
	require.NotNil(t, args)
	assert.Equal(t, ReviewAgentRef, CelStringValue(args.GetRef()))
	assert.Equal(t, ThreadModeNew, args.GetThread().GetMode())
	assert.False(t, CelBoolValue(args.GetThread().GetMemo()), "a new session every loop iteration")
	assert.True(t, CelBoolIsSet(args.GetThread().GetMemo()))
	assert.True(t, args.GetThread().GetExcludeAmbientMemory())
	assert.Nil(t, args.GetThread().GetInject(), "the session starts empty; the brief is the body's first node")

	assert.Equal(t, "Review it.", args.GetArgs()["prompt"].GetStringValue())
	assert.Equal(t, "{{ inputs.head }}", args.GetArgs()["with"].GetStructValue().GetFields()["head"].GetStringValue())
	assert.Equal(t, []interface{}{"fast"}, args.GetArgs()["model"].GetStructValue().AsMap()["tags"])
	assert.Equal(t, float64(12), args.GetArgs()["max_turns"].GetNumberValue())
	assert.NotContains(t, args.GetArgs(), "tools")
	assert.Empty(t, args.GetPresets())

	assert.True(t, IsLoweredReviewNode(lowered))
}

func TestLowerReviewNode_ResolvesABuiltinPrompt(t *testing.T) {
	t.Parallel()
	var asked string
	prompts := func(name string) (string, error) { asked = name; return "the review charter", nil }
	lowered, err := LowerReviewNode(reviewNode("builtin://pipeline/review.md"), prompts)
	require.NoError(t, err)
	assert.Equal(t, "pipeline/review.md", asked)
	assert.Equal(t, "the review charter", lowered.GetWorkflow().GetArgs()["prompt"].GetStringValue())

	_, err = LowerReviewNode(reviewNode("builtin://pipeline/missing.md"), noPrompts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "builtin://pipeline/missing.md")
}

func TestLowerReviewNode_RequiresTheReviewerRole(t *testing.T) {
	t.Parallel()
	n := reviewNode("Review it.")
	n.Role = ""
	_, err := LowerReviewNode(n, noPrompts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "role: reviewer")
}

func TestLowerReviewNodes_EveryDepthAndStripsStrayRoles(t *testing.T) {
	t.Parallel()
	stray := &reliantv1.Node{Id: "fix", Type: NodeTypeWorkflow, Role: RoleReviewer, Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{Ref: lit("builtin://agent")}}}
	wf := &reliantv1.Workflow{
		Name: "pr",
		Nodes: []*reliantv1.Node{
			reviewNode("Top."),
			stray,
			{Id: "rounds", Type: NodeTypeLoop, Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{Inline: &reliantv1.Workflow{
				Nodes: []*reliantv1.Node{reviewNode("In a loop.")},
			}}}},
		},
		Finally: &reliantv1.Workflow{Nodes: []*reliantv1.Node{reviewNode("Finally.")}},
	}

	lowered, err := LowerReviewNodes(wf, noPrompts)
	require.NoError(t, err)
	assert.True(t, IsLoweredReviewNode(lowered.GetNodes()[0]))
	assert.Empty(t, lowered.GetNodes()[1].GetRole(), "a role on any other node is not passed on")
	assert.True(t, IsLoweredReviewNode(lowered.GetNodes()[2].GetLoop().GetInline().GetNodes()[0]))
	assert.True(t, IsLoweredReviewNode(lowered.GetFinally().GetNodes()[0]))

	// The input is untouched, and lowering twice changes nothing.
	assert.Equal(t, NodeTypeReview, wf.GetNodes()[0].GetType())
	assert.Equal(t, RoleReviewer, stray.GetRole())
	again, err := LowerReviewNodes(lowered, noPrompts)
	require.NoError(t, err)
	assert.Same(t, lowered, again)

	plain := &reliantv1.Workflow{Name: "plain", Nodes: []*reliantv1.Node{{Id: "a", Type: NodeTypeCallLLM}}}
	same, err := LowerReviewNodes(plain, noPrompts)
	require.NoError(t, err)
	assert.Same(t, plain, same, "a definition with nothing to lower comes back as is")
}

func TestIsLoweredReviewNode_OnlyTheLoweredShape(t *testing.T) {
	t.Parallel()
	fresh := func() *reliantv1.Node {
		n, err := LowerReviewNode(reviewNode("x"), noPrompts)
		require.NoError(t, err)
		return n
	}
	inherit := fresh()
	inherit.GetWorkflow().Thread.Mode = ThreadModeInherit
	otherRef := fresh()
	otherRef.GetWorkflow().Ref = lit("builtin://agent")
	noRole := fresh()
	noRole.Role = ""

	assert.True(t, IsLoweredReviewNode(fresh()))
	for name, n := range map[string]*reliantv1.Node{"inherited thread": inherit, "other agent": otherRef, "no role": noRole} {
		assert.False(t, IsLoweredReviewNode(n), name)
	}
}

func TestClampReviewerTools_TheDefinitionCannotWidenIt(t *testing.T) {
	t.Parallel()
	list := func(v ...string) *reliantv1.CelStringList {
		return &reliantv1.CelStringList{Value: &reliantv1.CelStringList_Literal{Literal: &reliantv1.StringList{Values: v}}}
	}
	n := &reliantv1.Node{Id: "call_llm", Type: NodeTypeCallLLM, Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{
		ToolsConfig: &reliantv1.ToolsConfig{
			PreloadedTools: list("view", "write", "shell", "tag:coding:default", "*", "github__pr_get", "github__pr_create", "view"),
			LoadableTools:  list("*"),
			Spawn:          list("spawn(builtin://agent, [general])"),
			Tools: map[string]*structpb.Struct{
				"shell": {}, "view": {},
			},
		},
		ResponseTool: &reliantv1.ResponseTool{Name: lit(ReviewResponseTool)},
	}}}

	ClampReviewerTools(n)
	tc := n.GetCallLlm().GetToolsConfig()
	assert.Equal(t, []string{"view", "github__pr_get"}, CelStringListValue(tc.GetPreloadedTools()))
	assert.True(t, CelStringListIsSet(tc.GetLoadableTools()))
	assert.Empty(t, CelStringListValue(tc.GetLoadableTools()))
	assert.Empty(t, CelStringListValue(tc.GetSpawn()))
	assert.Contains(t, tc.GetTools(), "view")
	assert.NotContains(t, tc.GetTools(), "shell")
	assert.Equal(t, ReviewResponseTool, CelStringValue(n.GetCallLlm().GetResponseTool().GetName()), "the verdict tool stays")

	none := &reliantv1.Node{Id: "c", Type: NodeTypeCallLLM, Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{}}}
	ClampReviewerTools(none)
	assert.Nil(t, none.GetCallLlm().GetToolsConfig(), "no tools_config means no tools, and stays that way")
}

func TestReviewerTools_NoWriteTool(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, name := range ReviewerTools {
		assert.False(t, seen[name], "duplicate %s", name)
		seen[name] = true
	}
	for _, write := range []string{"write", "edit", "find_replace", "move_code", "shell", "shell_kill", "agent", "load_tool",
		"fetch", "worktree", "github__pr_create", "github__pr_update", "github__pr_review_create", "github__issue_comment",
		"github__issue_create", "github__issue_labels_add", "github__workflow_dispatch", "notes", "report_bug"} {
		assert.False(t, IsReviewerTool(write), write)
	}
}

func validVerdict() map[string]interface{} {
	return map[string]interface{}{
		"verdict": "changes",
		"findings": []interface{}{
			map[string]interface{}{"id": "b1", "severity": "blocker", "path": "main.go", "line": float64(12), "body": "nil deref"},
			map[string]interface{}{"id": "n1", "severity": "nit", "body": "naming"},
		},
		"risk_tier": "medium",
		"summary":   "One blocker.",
	}
}

func TestBuildReviewOutput_Valid(t *testing.T) {
	t.Parallel()
	out, err := BuildReviewOutput(validVerdict(), "thread-r")
	require.NoError(t, err)
	assert.Equal(t, "changes", out["verdict"])
	assert.Equal(t, "medium", out["risk_tier"])
	assert.Equal(t, "One blocker.", out["summary"])
	assert.Equal(t, "thread-r", out["session_id"])
	assert.Equal(t, RoleReviewer, out["role"])
	findings := out["findings"].([]interface{})
	require.Len(t, findings, 2)
	assert.Equal(t, map[string]interface{}{"id": "b1", "severity": "blocker", "path": "main.go", "line": int64(12), "body": "nil deref"}, findings[0])
	assert.Equal(t, map[string]interface{}{"id": "n1", "severity": "nit", "path": "", "line": int64(0), "body": "naming"}, findings[1])

	pass, err := BuildReviewOutput(map[string]interface{}{"verdict": "pass", "findings": []interface{}{}, "summary": "Fine."}, "t")
	require.NoError(t, err)
	assert.Equal(t, "", pass["risk_tier"])
	assert.Empty(t, pass["findings"])
}

func TestBuildReviewOutput_RefusesEverythingElse(t *testing.T) {
	t.Parallel()
	mutate := func(f func(m map[string]interface{})) map[string]interface{} {
		m := validVerdict()
		f(m)
		return m
	}
	finding := func(f func(m map[string]interface{})) map[string]interface{} {
		return mutate(func(m map[string]interface{}) {
			fs := m["findings"].([]interface{})
			first := map[string]interface{}{}
			for k, v := range fs[0].(map[string]interface{}) {
				first[k] = v
			}
			f(first)
			m["findings"] = []interface{}{first}
		})
	}
	cases := map[string]interface{}{
		"nil":                     nil,
		"not an object":           "pass",
		"no verdict":              mutate(func(m map[string]interface{}) { delete(m, "verdict") }),
		"bad verdict":             mutate(func(m map[string]interface{}) { m["verdict"] = "lgtm" }),
		"session smuggled in":     mutate(func(m map[string]interface{}) { m["session_id"] = "someone-else" }),
		"role smuggled in":        mutate(func(m map[string]interface{}) { m["role"] = "reviewer" }),
		"bad risk":                mutate(func(m map[string]interface{}) { m["risk_tier"] = "none" }),
		"empty summary":           mutate(func(m map[string]interface{}) { m["summary"] = "  " }),
		"findings missing":        mutate(func(m map[string]interface{}) { delete(m, "findings") }),
		"findings not a list":     mutate(func(m map[string]interface{}) { m["findings"] = "none" }),
		"finding extra field":     finding(func(f map[string]interface{}) { f["fixed"] = true }),
		"finding no id":           finding(func(f map[string]interface{}) { delete(f, "id") }),
		"finding bad severity":    finding(func(f map[string]interface{}) { f["severity"] = "major" }),
		"finding no body":         finding(func(f map[string]interface{}) { f["body"] = "" }),
		"finding fractional line": finding(func(f map[string]interface{}) { f["line"] = 1.5 }),
		"finding zero line":       finding(func(f map[string]interface{}) { f["line"] = float64(0) }),
		"finding path not string": finding(func(f map[string]interface{}) { f["path"] = 3 }),
		"duplicate ids": mutate(func(m map[string]interface{}) {
			m["findings"] = []interface{}{
				map[string]interface{}{"id": "a", "severity": "blocker", "body": "x"},
				map[string]interface{}{"id": "a", "severity": "nit", "body": "y"},
			}
		}),
		"pass with blocker":   mutate(func(m map[string]interface{}) { m["verdict"] = "pass" }),
		"changes, no blocker": mutate(func(m map[string]interface{}) { m["findings"] = []interface{}{} }),
	}
	for name, resp := range cases {
		_, err := BuildReviewOutput(resp, "thread-r")
		assert.Error(t, err, name)
	}

	_, err := BuildReviewOutput(validVerdict(), "")
	assert.Error(t, err, "a verdict with no session is not one a server can check")
}
