package wfyaml

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"google.golang.org/protobuf/proto"
)

// ---------------------------------------------------------------------------
// CEL wrapper tests
// ---------------------------------------------------------------------------

func TestCelString_Literal(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: n1
    type: save_message
    args:
      role: assistant
      content: Hello world
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	node := wf.Nodes[0]
	args := node.GetSaveMessageNode()
	if args == nil {
		t.Fatal("expected SaveMessageNodeArgs")
	}
	if args.Role.GetLiteral() != "assistant" {
		t.Errorf("role: got %q, want %q", args.Role.GetLiteral(), "assistant")
	}
	if args.Content.GetLiteral() != "Hello world" {
		t.Errorf("content: got %q, want %q", args.Content.GetLiteral(), "Hello world")
	}
}

func TestCelString_Expr(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: n1
    type: save_message
    args:
      role: "{{output.message.role}}"
      content: "{{output.message.text}}"
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	args := wf.Nodes[0].GetSaveMessageNode()
	if args.Role.GetExpr() != "{{output.message.role}}" {
		t.Errorf("role expr: got %q", args.Role.GetExpr())
	}
	if args.Content.GetExpr() != "{{output.message.text}}" {
		t.Errorf("content expr: got %q", args.Content.GetExpr())
	}
}

func TestCelBool_Literal(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: n1
    type: create_worktree
    args:
      name: test-wt
      force: true
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	args := wf.Nodes[0].GetCreateWorktree()
	if args.Force.GetLiteral() != true {
		t.Errorf("force: got %v, want true", args.Force.GetLiteral())
	}
}

func TestCelBool_Expr(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: n1
    type: create_worktree
    args:
      name: test-wt
      force: "{{inputs.force}}"
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	args := wf.Nodes[0].GetCreateWorktree()
	if args.Force.GetExpr() != "{{inputs.force}}" {
		t.Errorf("force expr: got %q", args.Force.GetExpr())
	}
}

func TestCelDouble_Literal(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: n1
    type: call_llm
    args:
      model:
        tags: [flagship]
      temperature: 0.7
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	args := wf.Nodes[0].GetCallLlm()
	if args.Temperature.GetLiteral() != 0.7 {
		t.Errorf("temperature: got %v, want 0.7", args.Temperature.GetLiteral())
	}
}

func TestCelDouble_Expr(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: n1
    type: call_llm
    args:
      model:
        tags: [flagship]
      temperature: "{{inputs.temperature}}"
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	args := wf.Nodes[0].GetCallLlm()
	if args.Temperature.GetExpr() != "{{inputs.temperature}}" {
		t.Errorf("temperature expr: got %q", args.Temperature.GetExpr())
	}
}

func TestCelInt_Literal(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: n1
    type: call_llm
    args:
      model:
        tags: [flagship]
      max_tokens: 4096
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	args := wf.Nodes[0].GetCallLlm()
	if args.MaxTokens.GetLiteral() != 4096 {
		t.Errorf("max_tokens: got %v, want 4096", args.MaxTokens.GetLiteral())
	}
}

func TestCelModelSelector_Literal_Tags(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: n1
    type: call_llm
    args:
      model:
        tags: [flagship, fast]
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	args := wf.Nodes[0].GetCallLlm()
	ms := args.Model.GetLiteral()
	if ms == nil {
		t.Fatal("expected model selector literal")
	}
	if len(ms.Tags) != 2 || ms.Tags[0] != "flagship" || ms.Tags[1] != "fast" {
		t.Errorf("tags: got %v", ms.Tags)
	}
}

func TestCelModelSelector_Literal_ID(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: n1
    type: call_llm
    args:
      model:
        id: claude-4-sonnet
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	args := wf.Nodes[0].GetCallLlm()
	ms := args.Model.GetLiteral()
	if ms == nil {
		t.Fatal("expected model selector literal")
	}
	if ms.Id != "claude-4-sonnet" {
		t.Errorf("id: got %q", ms.Id)
	}
}

func TestCelModelSelector_Expr(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: n1
    type: call_llm
    args:
      model: "{{inputs.model}}"
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	args := wf.Nodes[0].GetCallLlm()
	if args.Model.GetExpr() != "{{inputs.model}}" {
		t.Errorf("model expr: got %q", args.Model.GetExpr())
	}
}

func TestDirectCelBool(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: n1
    type: loop
    while: outputs.stop_reason != 'end_turn'
    inline:
      name: inner
      entry: [llm]
      nodes:
        - id: llm
          type: call_llm
          args:
            model:
              tags: [flagship]
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	args := wf.Nodes[0].GetLoop()
	if args.While.GetExpr() != "outputs.stop_reason != 'end_turn'" {
		t.Errorf("while: got %q", args.While.GetExpr())
	}
}

// ---------------------------------------------------------------------------
// Nil CelString field tests (regression: typed nil in proto.Message interface)
// ---------------------------------------------------------------------------

func TestCelString_NullContent_NoPanic(t *testing.T) {
	// Regression: a save_message node with null content caused a panic because
	// unmarshalCelString returns (*CelString)(nil) which, assigned to a
	// proto.Message interface, passes the `val == nil` check (Go typed-nil gotcha)
	// and then panics on val.ProtoReflect().
	yaml := `
name: test
nodes:
  - id: n1
    type: save_message
    args:
      role: assistant
      content:
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	args := wf.Nodes[0].GetSaveMessageNode()
	if args == nil {
		t.Fatal("expected SaveMessageNodeArgs")
	}
	if args.Content != nil {
		t.Errorf("content: expected nil, got %v", args.Content)
	}
}

func TestCelString_EmptyContent_NoPanic(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: n1
    type: save_message
    args:
      role: assistant
      content: ""
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	args := wf.Nodes[0].GetSaveMessageNode()
	if args == nil {
		t.Fatal("expected SaveMessageNodeArgs")
	}
	if args.Content != nil {
		t.Errorf("content: expected nil, got %v", args.Content)
	}
}

func TestCelBool_Null_NoPanic(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: n1
    type: create_worktree
    args:
      name: test-wt
      force:
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	args := wf.Nodes[0].GetCreateWorktree()
	if args == nil {
		t.Fatal("expected CreateWorktreeArgs")
	}
	if args.Force != nil {
		t.Errorf("force: expected nil, got %v", args.Force)
	}
}

func TestCelDouble_Null_NoPanic(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: n1
    type: call_llm
    args:
      model:
        tags: [flagship]
      temperature:
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	args := wf.Nodes[0].GetCallLlm()
	if args == nil {
		t.Fatal("expected CallLLMArgs")
	}
	if args.Temperature != nil {
		t.Errorf("temperature: expected nil, got %v", args.Temperature)
	}
}

func TestCelInt_Null_NoPanic(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: n1
    type: call_llm
    args:
      model:
        tags: [flagship]
      max_tokens:
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	args := wf.Nodes[0].GetCallLlm()
	if args == nil {
		t.Fatal("expected CallLLMArgs")
	}
	if args.MaxTokens != nil {
		t.Errorf("max_tokens: expected nil, got %v", args.MaxTokens)
	}
}

// ---------------------------------------------------------------------------
// V2Node dispatch tests
// ---------------------------------------------------------------------------

func TestNodeDispatch_CallLLM(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: llm
    type: call_llm
    args:
      model:
        tags: [flagship]
      system_prompt: You are helpful
      tools_config:
        filter: [view, edit]
        permission: readonly
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	node := wf.Nodes[0]
	if node.Type != "call_llm" {
		t.Errorf("type: got %q", node.Type)
	}
	args := node.GetCallLlm()
	if args == nil {
		t.Fatal("expected CallLLMArgs")
	}
	if args.SystemPrompt.GetLiteral() != "You are helpful" {
		t.Errorf("system_prompt: got %q", args.SystemPrompt.GetLiteral())
	}
	tc := args.GetToolsConfig()
	if tc == nil {
		t.Fatal("expected ToolsConfig")
	}
	toolFilter := model.CelStringListValue(tc.GetFilter())
	if len(toolFilter) != 2 || toolFilter[0] != "view" || toolFilter[1] != "edit" {
		t.Errorf("tools_config.filter: got %v", toolFilter)
	}
	if tc.GetPermission().GetLiteral() != "readonly" {
		t.Errorf("tools_config.permission: got %q", tc.GetPermission().GetLiteral())
	}
}

func TestParseDraftWorkflow_LegacyTopLevelActivityArgs(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: llm
    type: call_llm
    model:
      id: claude-4-sonnet
    system_prompt: You are helpful
`
	wf, err := ParseDraftWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse draft: %v", err)
	}
	args := wf.Nodes[0].GetCallLlm()
	if args == nil {
		t.Fatal("expected CallLLMArgs")
	}
	if got := args.Model.GetLiteral().GetId(); got != "claude-4-sonnet" {
		t.Fatalf("model id: got %q", got)
	}
	if got := args.SystemPrompt.GetLiteral(); got != "You are helpful" {
		t.Fatalf("system_prompt: got %q", got)
	}
}

func TestParseDraftWorkflow_ExplicitArgsTakePrecedence(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: llm
    type: call_llm
    model:
      id: legacy-model
    args:
      model:
        id: explicit-model
      system_prompt: explicit value
`
	wf, err := ParseDraftWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse draft: %v", err)
	}
	args := wf.Nodes[0].GetCallLlm()
	if args == nil {
		t.Fatal("expected CallLLMArgs")
	}
	if got := args.Model.GetLiteral().GetId(); got != "explicit-model" {
		t.Fatalf("model id: got %q", got)
	}
	if got := args.SystemPrompt.GetLiteral(); got != "explicit value" {
		t.Fatalf("system_prompt: got %q", got)
	}
}

func TestNodeDispatch_ExecuteTools(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: et
    type: execute_tools
    args:
      tool_calls: "{{nodes.llm.tool_calls}}"
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	args := wf.Nodes[0].GetExecuteTools()
	if args == nil {
		t.Fatal("expected ExecuteToolsArgs")
	}
	if args.ToolCalls.GetExpr() != "{{nodes.llm.tool_calls}}" {
		t.Errorf("tool_calls: got %q", args.ToolCalls.GetExpr())
	}
}

func TestNodeDispatch_Approval(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: approve
    type: approval
    args:
      title: "Approve?"
      timeout: 1h
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	args := wf.Nodes[0].GetApproval()
	if args == nil {
		t.Fatal("expected ApprovalArgs")
	}
	if args.Title.GetLiteral() != "Approve?" {
		t.Errorf("title: got %q", args.Title.GetLiteral())
	}
}

func TestNodeDispatch_Run(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: run_tests
    type: run
    command: "npm test"
    env:
      NODE_ENV: test
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	node := wf.Nodes[0]
	if node.Type != "run" {
		t.Errorf("type: got %q", node.Type)
	}
	args := node.GetRun()
	if args == nil {
		t.Fatal("expected RunArgs")
	}
	if args.Command.GetLiteral() != "npm test" {
		t.Errorf("command: got %q", args.Command.GetLiteral())
	}
	if args.Env["NODE_ENV"] != "test" {
		t.Errorf("env: got %v", args.Env)
	}
}

func TestNodeDispatch_Workflow(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: sub
    type: workflow
    ref: builtin://agent
    presets:
      default: researcher
    args:
      max_turns: 10
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	args := wf.Nodes[0].GetWorkflow()
	if args == nil {
		t.Fatal("expected SubWorkflowArgs")
	}
	if args.Ref.GetLiteral() != "builtin://agent" {
		t.Errorf("ref: got %q", args.Ref.GetLiteral())
	}
	if args.Presets["default"] != "researcher" {
		t.Errorf("presets: got %v", args.Presets)
	}
}

func TestNodeDispatch_Loop(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: agent
    type: loop
    while: outputs.stop_reason != 'end_turn'
    ref: builtin://agent
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	args := wf.Nodes[0].GetLoop()
	if args == nil {
		t.Fatal("expected LoopArgs")
	}
	if args.While.GetExpr() != "outputs.stop_reason != 'end_turn'" {
		t.Errorf("while: got %q", args.While.GetExpr())
	}
	if args.Ref.GetLiteral() != "builtin://agent" {
		t.Errorf("ref: got %q", args.Ref.GetLiteral())
	}
}

func TestNodeDispatch_Join(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: sync
    type: join
    condition: any
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	node := wf.Nodes[0]
	if node.Type != "join" {
		t.Errorf("type: got %q", node.Type)
	}
	if node.GetJoin() == nil {
		t.Fatal("expected JoinArgs")
	}
	if node.Condition.GetExpr() != "any" {
		t.Errorf("condition: got %q", node.Condition.GetExpr())
	}
}

func TestNodeDispatch_Compact(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: compact
    type: compact
    timeout: "10m"
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	node := wf.Nodes[0]
	if node.GetCompact() == nil {
		t.Fatal("expected CompactArgs")
	}
	if node.Timeout.GetLiteral() != "10m" {
		t.Errorf("timeout: got %q", node.Timeout.GetLiteral())
	}
}

func TestNodeDispatch_CreateWorktree(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: wt
    type: create_worktree
    args:
      name: feature-auth
      base_branch: main
      copy_files: [".env"]
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	args := wf.Nodes[0].GetCreateWorktree()
	if args == nil {
		t.Fatal("expected CreateWorktreeArgs")
	}
	if args.Name.GetLiteral() != "feature-auth" {
		t.Errorf("name: got %q", args.Name.GetLiteral())
	}
	if len(args.CopyFiles) != 1 || args.CopyFiles[0] != ".env" {
		t.Errorf("copy_files: got %v", args.CopyFiles)
	}
}

func TestNodeDispatch_Router(t *testing.T) {
	t.Run("full config", func(t *testing.T) {
		yaml := `
name: test
nodes:
  - id: route
    type: router
    workflows:
      - ref: builtin://researcher
        presets: [default, deep]
        description: Research tasks
      - ref: builtin://coder
        presets: [fast]
        description: Coding tasks
    system_prompt: You are a routing assistant
    model:
      tags: [flagship]
    thread:
      mode: inherit
    fallback: builtin://researcher
`
		wf, err := ParseWorkflow([]byte(yaml))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		args := wf.Nodes[0].GetRouter()
		if args == nil {
			t.Fatal("expected RouterArgs")
		}

		// workflows
		if len(args.Workflows) != 2 {
			t.Fatalf("workflows: got %d, want 2", len(args.Workflows))
		}
		w0 := args.Workflows[0]
		if w0.Ref != "builtin://researcher" {
			t.Errorf("workflows[0].ref: got %q", w0.Ref)
		}
		if len(w0.Presets) != 2 || w0.Presets[0] != "default" || w0.Presets[1] != "deep" {
			t.Errorf("workflows[0].presets: got %v", w0.Presets)
		}
		if w0.Description != "Research tasks" {
			t.Errorf("workflows[0].description: got %q", w0.Description)
		}
		w1 := args.Workflows[1]
		if w1.Ref != "builtin://coder" {
			t.Errorf("workflows[1].ref: got %q", w1.Ref)
		}
		if len(w1.Presets) != 1 || w1.Presets[0] != "fast" {
			t.Errorf("workflows[1].presets: got %v", w1.Presets)
		}

		// scalar fields
		if args.SystemPrompt.GetLiteral() != "You are a routing assistant" {
			t.Errorf("system_prompt: got %q", args.SystemPrompt.GetLiteral())
		}
		if args.Model.GetLiteral() == nil {
			t.Fatal("expected model")
		}
		if args.Thread == nil || args.Thread.Mode != "inherit" {
			t.Errorf("thread.mode: got %v", args.Thread)
		}
		if args.Fallback != "builtin://researcher" {
			t.Errorf("fallback: got %q", args.Fallback)
		}
	})

	t.Run("minimal config", func(t *testing.T) {
		yaml := `
name: test
nodes:
  - id: route
    type: router
    workflows:
      - ref: builtin://agent
`
		wf, err := ParseWorkflow([]byte(yaml))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		args := wf.Nodes[0].GetRouter()
		if args == nil {
			t.Fatal("expected RouterArgs")
		}
		if len(args.Workflows) != 1 {
			t.Fatalf("workflows: got %d, want 1", len(args.Workflows))
		}
		if args.Workflows[0].Ref != "builtin://agent" {
			t.Errorf("workflows[0].ref: got %q", args.Workflows[0].Ref)
		}
		if args.SystemPrompt != nil {
			t.Errorf("system_prompt: expected nil, got %v", args.SystemPrompt)
		}
		if args.Fallback != "" {
			t.Errorf("fallback: expected empty, got %q", args.Fallback)
		}
	})

	t.Run("multiple candidates", func(t *testing.T) {
		yaml := `
name: test
nodes:
  - id: route
    type: router
    workflows:
      - ref: builtin://researcher
        description: For research
      - ref: builtin://coder
        description: For coding
      - ref: builtin://reviewer
        description: For review
      - ref: builtin://debugger
        description: For debugging
`
		wf, err := ParseWorkflow([]byte(yaml))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		args := wf.Nodes[0].GetRouter()
		if args == nil {
			t.Fatal("expected RouterArgs")
		}
		if len(args.Workflows) != 4 {
			t.Fatalf("workflows: got %d, want 4", len(args.Workflows))
		}
		expected := []struct {
			ref  string
			desc string
		}{
			{"builtin://researcher", "For research"},
			{"builtin://coder", "For coding"},
			{"builtin://reviewer", "For review"},
			{"builtin://debugger", "For debugging"},
		}
		for i, exp := range expected {
			if args.Workflows[i].Ref != exp.ref {
				t.Errorf("workflows[%d].ref: got %q, want %q", i, args.Workflows[i].Ref, exp.ref)
			}
			if args.Workflows[i].Description != exp.desc {
				t.Errorf("workflows[%d].description: got %q, want %q", i, args.Workflows[i].Description, exp.desc)
			}
		}
	})
}

func TestNodeDispatch_RouterNodeMode(t *testing.T) {
	t.Run("node routing parses correctly", func(t *testing.T) {
		yaml := `
name: test
nodes:
  - id: classify
    type: router
    model:
      tags: [fast]
    system_prompt: "Pick which phase..."
    nodes:
      - id: brainstorm
        description: "Start from scratch"
      - id: implement
        description: "Skip to implementation"
    fallback: brainstorm
`
		wf, err := ParseWorkflow([]byte(yaml))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		args := wf.Nodes[0].GetRouter()
		if args == nil {
			t.Fatal("expected RouterArgs")
		}

		// nodes
		if len(args.Nodes) != 2 {
			t.Fatalf("nodes: got %d, want 2", len(args.Nodes))
		}
		n0 := args.Nodes[0]
		if n0.Id != "brainstorm" {
			t.Errorf("nodes[0].id: got %q, want %q", n0.Id, "brainstorm")
		}
		if n0.Description != "Start from scratch" {
			t.Errorf("nodes[0].description: got %q, want %q", n0.Description, "Start from scratch")
		}
		n1 := args.Nodes[1]
		if n1.Id != "implement" {
			t.Errorf("nodes[1].id: got %q, want %q", n1.Id, "implement")
		}
		if n1.Description != "Skip to implementation" {
			t.Errorf("nodes[1].description: got %q, want %q", n1.Description, "Skip to implementation")
		}

		// scalar fields
		if args.SystemPrompt.GetLiteral() != "Pick which phase..." {
			t.Errorf("system_prompt: got %q", args.SystemPrompt.GetLiteral())
		}
		if args.Model.GetLiteral() == nil {
			t.Fatal("expected model")
		}
		if args.Fallback != "brainstorm" {
			t.Errorf("fallback: got %q", args.Fallback)
		}
		if len(args.Workflows) != 0 {
			t.Errorf("workflows: expected empty, got %d", len(args.Workflows))
		}
	})

	t.Run("workflow routing still works", func(t *testing.T) {
		yaml := `
name: test
nodes:
  - id: route
    type: router
    workflows:
      - ref: builtin://researcher
        description: Research tasks
      - ref: builtin://coder
        description: Coding tasks
`
		wf, err := ParseWorkflow([]byte(yaml))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		args := wf.Nodes[0].GetRouter()
		if args == nil {
			t.Fatal("expected RouterArgs")
		}
		if len(args.Workflows) != 2 {
			t.Fatalf("workflows: got %d, want 2", len(args.Workflows))
		}
		if args.Workflows[0].Ref != "builtin://researcher" {
			t.Errorf("workflows[0].ref: got %q", args.Workflows[0].Ref)
		}
		if len(args.Nodes) != 0 {
			t.Errorf("nodes: expected empty, got %d", len(args.Nodes))
		}
	})

	t.Run("both fields set parses", func(t *testing.T) {
		yaml := `
name: test
nodes:
  - id: route
    type: router
    workflows:
      - ref: builtin://agent
        description: Agent workflow
    nodes:
      - id: brainstorm
        description: "Brainstorm phase"
`
		wf, err := ParseWorkflow([]byte(yaml))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		args := wf.Nodes[0].GetRouter()
		if args == nil {
			t.Fatal("expected RouterArgs")
		}
		if len(args.Workflows) != 1 {
			t.Fatalf("workflows: got %d, want 1", len(args.Workflows))
		}
		if args.Workflows[0].Ref != "builtin://agent" {
			t.Errorf("workflows[0].ref: got %q", args.Workflows[0].Ref)
		}
		if len(args.Nodes) != 1 {
			t.Fatalf("nodes: got %d, want 1", len(args.Nodes))
		}
		if args.Nodes[0].Id != "brainstorm" {
			t.Errorf("nodes[0].id: got %q", args.Nodes[0].Id)
		}
		if args.Nodes[0].Description != "Brainstorm phase" {
			t.Errorf("nodes[0].description: got %q", args.Nodes[0].Description)
		}
	})
}

// ---------------------------------------------------------------------------
// V2Input dispatch tests
// ---------------------------------------------------------------------------

func TestInputDispatch_String(t *testing.T) {
	yaml := `
name: test
inputs:
  query:
    type: string
    description: Search query
    default: "hello"
    pattern: "^[a-z]+"
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	input := wf.Inputs["query"]
	if input.Type != "string" {
		t.Errorf("type: got %q", input.Type)
	}
	cfg := input.GetStringInput()
	if cfg == nil {
		t.Fatal("expected StringInputConfig")
	}
	if cfg.Base.Description != "Search query" {
		t.Errorf("description: got %q", cfg.Base.Description)
	}
	if cfg.Default == nil || *cfg.Default != "hello" {
		t.Errorf("default: got %v", cfg.Default)
	}
	if cfg.Pattern != "^[a-z]+" {
		t.Errorf("pattern: got %q", cfg.Pattern)
	}
}

func TestInputDispatch_Number(t *testing.T) {
	yaml := `
name: test
inputs:
  temp:
    type: number
    default: 0.7
    min: 0
    max: 1
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cfg := wf.Inputs["temp"].GetNumberInput()
	if cfg == nil {
		t.Fatal("expected NumberInputConfig")
	}
	if cfg.Default == nil || *cfg.Default != 0.7 {
		t.Errorf("default: got %v", cfg.Default)
	}
	if cfg.Min == nil || *cfg.Min != 0 {
		t.Errorf("min: got %v", cfg.Min)
	}
	if cfg.Max == nil || *cfg.Max != 1 {
		t.Errorf("max: got %v", cfg.Max)
	}
}

func TestInputDispatch_Integer(t *testing.T) {
	yaml := `
name: test
inputs:
  max_turns:
    type: integer
    default: 200
    min: 1
    max: 500
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cfg := wf.Inputs["max_turns"].GetIntegerInput()
	if cfg == nil {
		t.Fatal("expected IntegerInputConfig")
	}
	if cfg.Default == nil || *cfg.Default != 200 {
		t.Errorf("default: got %v", cfg.Default)
	}
}

func TestInputDispatch_Boolean(t *testing.T) {
	yaml := `
name: test
inputs:
  verbose:
    type: boolean
    default: false
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cfg := wf.Inputs["verbose"].GetBooleanInput()
	if cfg == nil {
		t.Fatal("expected BooleanInputConfig")
	}
	if cfg.Default == nil || *cfg.Default != false {
		t.Errorf("default: got %v", cfg.Default)
	}
}

func TestInputDispatch_Enum(t *testing.T) {
	yaml := `
name: test
inputs:
  mode:
    type: enum
    enum: [fast, balanced, thorough]
    default: balanced
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cfg := wf.Inputs["mode"].GetEnumInput()
	if cfg == nil {
		t.Fatal("expected EnumInputConfig")
	}
	if len(cfg.EnumValues) != 3 {
		t.Errorf("enum: got %v", cfg.EnumValues)
	}
	if cfg.Default.GetStringValue() != "balanced" {
		t.Errorf("default: got %v", cfg.Default)
	}
}

func TestInputDispatch_Model(t *testing.T) {
	yaml := `
name: test
inputs:
  model:
    type: model
    description: LLM model
    default:
      tags: [flagship]
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cfg := wf.Inputs["model"].GetModelInput()
	if cfg == nil {
		t.Fatal("expected ModelInputConfig")
	}
	if cfg.Default == nil || len(cfg.Default.Tags) != 1 || cfg.Default.Tags[0] != "flagship" {
		t.Errorf("default tags: got %v", cfg.Default)
	}
}

func TestInputDispatch_ModelTagShorthand(t *testing.T) {
	// Test the shorthand: type: model, tags: [flagship] (tags at top level)
	yaml := `
name: test
inputs:
  model:
    type: model
    tags: [flagship]
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cfg := wf.Inputs["model"].GetModelInput()
	if cfg == nil {
		t.Fatal("expected ModelInputConfig")
	}
	if cfg.Default == nil || len(cfg.Default.Tags) != 1 || cfg.Default.Tags[0] != "flagship" {
		t.Errorf("default tags: got %v", cfg.Default)
	}
}

func TestInputDispatch_Tools(t *testing.T) {
	yaml := `
name: test
inputs:
  tools:
    type: tools
    default: ["tag:default"]
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cfg := wf.Inputs["tools"].GetToolsInput()
	if cfg == nil {
		t.Fatal("expected ToolsInputConfig")
	}
	// default is a structpb.Value list
	list := cfg.Default.GetListValue()
	if list == nil || len(list.Values) != 1 {
		t.Errorf("default: got %v", cfg.Default)
	}
}

func TestInputDispatch_Preset(t *testing.T) {
	yaml := `
name: test
inputs:
  presets:
    type: preset
    tags: [agent]
    multi: true
    default:
      - general
      - researcher
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cfg := wf.Inputs["presets"].GetPresetInput()
	if cfg == nil {
		t.Fatal("expected PresetInputConfig")
	}
	if !cfg.Multi {
		t.Error("multi: expected true")
	}
	if len(cfg.Tags) != 1 || cfg.Tags[0] != "agent" {
		t.Errorf("tags: got %v", cfg.Tags)
	}
}

// ---------------------------------------------------------------------------
// Edge tests
// ---------------------------------------------------------------------------

func TestEdge_DefaultString(t *testing.T) {
	yaml := `
name: test
edges:
  - from: step1
    default: step2
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(wf.Edges) != 1 {
		t.Fatalf("edges: got %d", len(wf.Edges))
	}
	edge := wf.Edges[0]
	if edge.From != "step1" {
		t.Errorf("from: got %q", edge.From)
	}
	if len(edge.Default) != 1 || edge.Default[0] != "step2" {
		t.Errorf("default: got %v", edge.Default)
	}
}

func TestEdge_DefaultArray(t *testing.T) {
	yaml := `
name: test
edges:
  - from: start
    default: [branch-a, branch-b]
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	edge := wf.Edges[0]
	if len(edge.Default) != 2 {
		t.Errorf("default: got %v", edge.Default)
	}
}

func TestEdgeCase_ToString(t *testing.T) {
	yaml := `
name: test
edges:
  - from: llm
    cases:
      - to: tools
        condition: nodes.llm.stop_reason == 'tool_use'
        label: tool_use
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ec := wf.Edges[0].Cases[0]
	if len(ec.To) != 1 || ec.To[0] != "tools" {
		t.Errorf("to: got %v", ec.To)
	}
	if ec.Condition != "nodes.llm.stop_reason == 'tool_use'" {
		t.Errorf("condition: got %q", ec.Condition)
	}
	if ec.Label != "tool_use" {
		t.Errorf("label: got %q", ec.Label)
	}
}

func TestEdgeCase_ToArray(t *testing.T) {
	yaml := `
name: test
edges:
  - from: start
    cases:
      - to: [a, b, c]
        condition: "true"
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ec := wf.Edges[0].Cases[0]
	if len(ec.To) != 3 {
		t.Errorf("to: got %v", ec.To)
	}
}

// ---------------------------------------------------------------------------
// Entry point tests
// ---------------------------------------------------------------------------

func TestEntry_String(t *testing.T) {
	yaml := `
name: test
entry: start
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(wf.Entry) != 1 || wf.Entry[0] != "start" {
		t.Errorf("entry: got %v", wf.Entry)
	}
}

func TestEntry_Array(t *testing.T) {
	yaml := `
name: test
entry: [node1, node2]
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(wf.Entry) != 2 {
		t.Errorf("entry: got %v", wf.Entry)
	}
}

// ---------------------------------------------------------------------------
// Thread and SaveMessage config tests
// ---------------------------------------------------------------------------

func TestNodeWithThread(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: n1
    type: workflow
    ref: builtin://agent
    thread:
      mode: inherit
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	node := wf.Nodes[0]
	thread := node.GetWorkflow().GetThread()
	if thread == nil {
		t.Fatal("expected thread config")
	}
	if thread.Mode != "inherit" {
		t.Errorf("thread.mode: got %q", thread.Mode)
	}
}

func TestNodeWithSaveMessage(t *testing.T) {
	yaml := `
name: test
nodes:
  - id: llm
    type: call_llm
    save_message:
      role: "{{output.message.role}}"
      content: "{{output.message.text}}"
      tool_calls: "{{output.tool_calls}}"
    args:
      model:
        tags: [flagship]
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	node := wf.Nodes[0]
	if node.SaveMessage == nil {
		t.Fatal("expected save_message config")
	}
	if node.SaveMessage.Role.GetExpr() != "{{output.message.role}}" {
		t.Errorf("role: got %q", node.SaveMessage.Role.GetExpr())
	}
	if node.SaveMessage.Content.GetExpr() != "{{output.message.text}}" {
		t.Errorf("content: got %q", node.SaveMessage.Content.GetExpr())
	}
}

// ---------------------------------------------------------------------------
// Round-trip tests
// ---------------------------------------------------------------------------

func TestRoundTrip_SimpleWorkflow(t *testing.T) {
	input := `
name: simple
description: A simple workflow
entry: [llm]
nodes:
  - id: llm
    type: call_llm
    args:
      model:
        tags: [flagship]
      temperature: 0.7
      tools_config:
        filter: ["tag:default"]
edges:
  - from: llm
    default: done
outputs:
  result: "{{nodes.llm.response_text}}"
`
	wf, err := ParseWorkflow([]byte(input))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Marshal back to YAML
	data, err := MarshalWorkflow(wf)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Parse again
	wf2, err := ParseWorkflow(data)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}

	// Compare proto messages
	if !proto.Equal(wf, wf2) {
		t.Errorf("round-trip mismatch:\noriginal: %v\nre-parsed: %v", wf, wf2)
	}
}

func TestRoundTrip_StructuralNodes(t *testing.T) {
	input := `
name: structural
entry: [run_cmd]
nodes:
  - id: run_cmd
    type: run
    command: "echo hello"
  - id: sub
    type: workflow
    ref: builtin://agent
  - id: sync
    type: join
    condition: all
`
	wf, err := ParseWorkflow([]byte(input))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	data, err := MarshalWorkflow(wf)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	wf2, err := ParseWorkflow(data)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}

	if !proto.Equal(wf, wf2) {
		t.Errorf("round-trip mismatch:\noriginal: %v\nre-parsed: %v", wf, wf2)
	}
}

func TestRoundTrip_EdgesStringVsArray(t *testing.T) {
	input := `
name: edges-test
edges:
  - from: a
    default: b
  - from: c
    default: [d, e]
  - from: f
    cases:
      - to: g
        condition: "true"
      - to: [h, i]
        condition: "false"
`
	wf, err := ParseWorkflow([]byte(input))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	data, err := MarshalWorkflow(wf)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	wf2, err := ParseWorkflow(data)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}

	if !proto.Equal(wf, wf2) {
		t.Errorf("round-trip mismatch")
	}
}

// ---------------------------------------------------------------------------
// Marshal specific behaviors
// ---------------------------------------------------------------------------

func TestMarshal_CelExprAsString(t *testing.T) {
	wf := &reliantv1.Workflow{
		Name:  "test",
		Entry: []string{"llm"},
		Nodes: []*reliantv1.Node{
			{
				Id:   "llm",
				Type: "call_llm",
				Args: &reliantv1.Node_CallLlm{
					CallLlm: &reliantv1.CallLLMArgs{
						Model: &reliantv1.CelModelSelector{
							Value: &reliantv1.CelModelSelector_Expr{Expr: "{{inputs.model}}"},
						},
						Temperature: &reliantv1.CelDouble{
							Value: &reliantv1.CelDouble_Expr{Expr: "{{inputs.temp}}"},
						},
					},
				},
			},
		},
	}

	data, err := MarshalWorkflow(wf)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Re-parse to verify CEL expressions survive
	wf2, err := ParseWorkflow(data)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}

	args := wf2.Nodes[0].GetCallLlm()
	if args.Model.GetExpr() != "{{inputs.model}}" {
		t.Errorf("model expr: got %q", args.Model.GetExpr())
	}
	if args.Temperature.GetExpr() != "{{inputs.temp}}" {
		t.Errorf("temperature expr: got %q", args.Temperature.GetExpr())
	}
}

func TestMarshal_EdgeDefaultSingleVsArray(t *testing.T) {
	wf := &reliantv1.Workflow{
		Name: "test",
		Edges: []*reliantv1.Edge{
			{From: "a", Default: []string{"b"}},
			{From: "c", Default: []string{"d", "e"}},
		},
	}

	data, err := MarshalWorkflow(wf)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	yamlStr := string(data)
	// Single value should marshal as string, not array
	// (We can't check exact format easily, but we can verify round-trip)

	wf2, err := ParseWorkflow(data)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	_ = yamlStr

	if len(wf2.Edges[0].Default) != 1 || wf2.Edges[0].Default[0] != "b" {
		t.Errorf("edge[0].default: got %v", wf2.Edges[0].Default)
	}
	if len(wf2.Edges[1].Default) != 2 {
		t.Errorf("edge[1].default: got %v", wf2.Edges[1].Default)
	}
}

func TestMarshal_EntryStringVsArray(t *testing.T) {
	wf := &reliantv1.Workflow{
		Name:  "test",
		Entry: []string{"start"},
	}

	data, err := MarshalWorkflow(wf)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	wf2, err := ParseWorkflow(data)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}

	if len(wf2.Entry) != 1 || wf2.Entry[0] != "start" {
		t.Errorf("entry: got %v", wf2.Entry)
	}
}

// ---------------------------------------------------------------------------
// Integration tests with builtin workflow YAML files
// ---------------------------------------------------------------------------

func builtinDir() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "builtin")
}

func TestBuiltinWorkflow_Agent(t *testing.T) {
	testBuiltinWorkflowFile(t, "agent.yaml")
}

func TestBuiltinWorkflow_StructuredAgent(t *testing.T) {
	testBuiltinWorkflowFile(t, "structured-agent.yaml")
}

func TestBuiltinWorkflow_OneRing(t *testing.T) {
	testBuiltinWorkflowFile(t, "one-ring.yaml")
}

func TestBuiltinWorkflow_ParallelCompete(t *testing.T) {
	testBuiltinWorkflowFile(t, "parallel-compete.yaml")
}

func TestBuiltinWorkflow_ParallelLoopSample(t *testing.T) {
	testBuiltinWorkflowFile(t, "parallel-loop-sample.yaml")
}

func TestBuiltinWorkflow_AuditingAgent(t *testing.T) {
	testBuiltinWorkflowFile(t, "auditing-agent.yaml")
}

func testBuiltinWorkflowFile(t *testing.T, filename string) {
	t.Helper()
	path := filepath.Join(builtinDir(), filename)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("skipping: %v", err)
		return
	}

	// Parse
	wf, err := ParseWorkflow(data)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}

	if wf.Name == "" {
		t.Errorf("%s: expected non-empty name", filename)
	}

	// Marshal back
	marshaled, err := MarshalWorkflow(wf)
	if err != nil {
		t.Fatalf("marshal %s: %v", filename, err)
	}

	// Re-parse for round-trip validation
	wf2, err := ParseWorkflow(marshaled)
	if err != nil {
		t.Fatalf("re-parse %s: %v\nYAML:\n%s", filename, err, string(marshaled))
	}

	// Proto equality check
	if !proto.Equal(wf, wf2) {
		t.Errorf("%s: round-trip mismatch", filename)
	}
}

// TestAllBuiltinWorkflows tests that all .yaml files in the builtin directory
// can be parsed and round-tripped.
func TestAllBuiltinWorkflows(t *testing.T) {
	dir := builtinDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("skipping: cannot read builtin dir: %v", err)
		return
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			testBuiltinWorkflowFile(t, entry.Name())
		})
	}
}

// ---------------------------------------------------------------------------
// Inline workflow (nested in loop/workflow nodes) test
// ---------------------------------------------------------------------------

func TestInlineWorkflow(t *testing.T) {
	yaml := `
name: outer
entry: [agent]
nodes:
  - id: agent
    type: loop
    while: "true"
    inline:
      name: inner
      entry: [llm]
      outputs:
        stop_reason: "{{nodes.llm.stop_reason}}"
      nodes:
        - id: llm
          type: call_llm
          args:
            model:
              tags: [flagship]
        - id: tools
          type: execute_tools
          args:
            tool_calls: "{{nodes.llm.tool_calls}}"
      edges:
        - from: llm
          cases:
            - to: tools
              condition: nodes.llm.stop_reason == 'tool_use'
        - from: tools
          default: llm
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	loopArgs := wf.Nodes[0].GetLoop()
	if loopArgs == nil {
		t.Fatal("expected LoopArgs")
	}
	if loopArgs.Inline == nil {
		t.Fatal("expected inline workflow")
	}
	inline := loopArgs.Inline
	if inline.Name != "inner" {
		t.Errorf("inline name: got %q", inline.Name)
	}
	if len(inline.Nodes) != 2 {
		t.Errorf("inline nodes: got %d", len(inline.Nodes))
	}
	if len(inline.Edges) != 2 {
		t.Errorf("inline edges: got %d", len(inline.Edges))
	}
	if inline.Outputs["stop_reason"] != "{{nodes.llm.stop_reason}}" {
		t.Errorf("inline outputs: got %v", inline.Outputs)
	}
}

// ---------------------------------------------------------------------------
// Workflow-level fields test
// ---------------------------------------------------------------------------

func TestWorkflowLevelFields(t *testing.T) {
	yaml := `
name: full-workflow
apiVersion: "0.0.5"
description: A complete test workflow
presets:
  tag: agent
  default: general
inputs:
  message:
    type: message
    description: User message
  model:
    type: model
    default:
      tags: [flagship]
outputs:
  result: "{{nodes.llm.response_text}}"
entry: [llm]
nodes:
  - id: llm
    type: call_llm
    args:
      model: "{{inputs.model}}"
edges:
  - from: llm
    default: done
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if wf.Name != "full-workflow" {
		t.Errorf("name: got %q", wf.Name)
	}
	if wf.ApiVersion != "0.0.5" {
		t.Errorf("apiVersion: got %q", wf.ApiVersion)
	}
	if wf.Description != "A complete test workflow" {
		t.Errorf("description: got %q", wf.Description)
	}
	if wf.Presets == nil || wf.Presets.Tag != "agent" || wf.Presets.Default != "general" {
		t.Errorf("presets: got %v", wf.Presets)
	}
	if len(wf.Inputs) != 2 {
		t.Errorf("inputs: got %d", len(wf.Inputs))
	}
	if wf.Outputs["result"] != "{{nodes.llm.response_text}}" {
		t.Errorf("outputs: got %v", wf.Outputs)
	}
}

func TestUnknownWorkflowField(t *testing.T) {
	yaml := `
name: test
apiVersion: "0.0.5"
tag: blog
entry: [llm]
nodes:
  - id: llm
    type: call_llm
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for unknown field 'tag', got nil")
	}
	if !strings.Contains(err.Error(), `unknown workflow field: "tag"`) {
		t.Errorf("unexpected error: %v", err)
	}
}
