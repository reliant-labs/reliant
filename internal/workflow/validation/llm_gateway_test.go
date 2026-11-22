// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

func llmGatewayFindings(errs []*Error) []*Error {
	var out []*Error
	for _, e := range errs {
		if e.Field == "llm_gateway" || (e.Field == "env" && strings.Contains(e.Message, "llm_gateway")) {
			out = append(out, e)
		}
	}
	return out
}

func runNodeWithGateway(gw *reliantv1.LLMGatewayStep, env map[string]string) *reliantv1.Workflow {
	return &reliantv1.Workflow{
		Name:  "gw",
		Entry: []string{"bench"},
		Nodes: []*reliantv1.Node{{
			Id:   "bench",
			Type: "run",
			Args: &reliantv1.Node_Run{Run: &reliantv1.RunArgs{
				Command:    &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "claude -p x"}},
				Env:        env,
				LlmGateway: gw,
			}},
		}},
	}
}

// A declaration Reliant cannot mint for is an error when saved AND when run:
// the step would be refused every time.
func TestRunLLMGatewayValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		gw   *reliantv1.LLMGatewayStep
		env  map[string]string
		want string
	}{
		{name: "minimal", gw: &reliantv1.LLMGatewayStep{Cli: "claude-code"}},
		{name: "full", gw: &reliantv1.LLMGatewayStep{Cli: "claude-code", BudgetUsd: 50, Models: []string{"claude-sonnet-4-6", "claude-haiku-4-5"}},
			env: map[string]string{"CI": "1"}},
		{name: "no cli", gw: &reliantv1.LLMGatewayStep{}, want: "needs a cli"},
		{name: "unknown cli", gw: &reliantv1.LLMGatewayStep{Cli: "gemini"}, want: `"gemini"`},
		{name: "codex not yet", gw: &reliantv1.LLMGatewayStep{Cli: "codex"}, want: `"codex"`},
		{name: "budget over the ceiling", gw: &reliantv1.LLMGatewayStep{Cli: "claude-code", BudgetUsd: 51}, want: "budget_usd"},
		{name: "negative budget", gw: &reliantv1.LLMGatewayStep{Cli: "claude-code", BudgetUsd: -1}, want: "budget_usd"},
		{name: "empty model", gw: &reliantv1.LLMGatewayStep{Cli: "claude-code", Models: []string{""}}, want: "empty"},
		{name: "duplicate model", gw: &reliantv1.LLMGatewayStep{Cli: "claude-code", Models: []string{"m", "m"}}, want: "twice"},
		{name: "env redirects the gateway", gw: &reliantv1.LLMGatewayStep{Cli: "claude-code"},
			env: map[string]string{"ANTHROPIC_BASE_URL": "https://elsewhere"}, want: "ANTHROPIC_BASE_URL"},
		{name: "env sets an unset name", gw: &reliantv1.LLMGatewayStep{Cli: "claude-code"},
			env: map[string]string{"ANTHROPIC_API_KEY": "x", "anthropic_auth_token": "y"}, want: "ANTHROPIC_API_KEY, anthropic_auth_token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wf := runNodeWithGateway(tc.gw, tc.env)
			for _, opts := range []*ValidationOptions{{Authoring: true}, {}} {
				got := llmGatewayFindings(StaticAnalysisWithOptions(wf, opts).Errors())
				if tc.want == "" {
					if len(got) != 0 {
						t.Fatalf("authoring=%v: want no findings, got %+v", opts.Authoring, got)
					}
					continue
				}
				if len(got) != 1 || !strings.Contains(got[0].Message, tc.want) {
					t.Fatalf("authoring=%v: want one error naming %q, got %+v", opts.Authoring, tc.want, got)
				}
			}
		})
	}
}

// The YAML spelling reaches the proto field, and a node without it is
// unchanged.
func TestRunLLMGatewayParsesFromYAML(t *testing.T) {
	t.Parallel()
	wf, err := wfyaml.ParseWorkflow([]byte(`
name: gw
entry: [bench]
nodes:
- id: bench
  type: run
  timeout: 20m
  args:
    command: claude -p "fix the failing test"
    credentials: none
    llm_gateway:
      cli: claude-code
      budget_usd: 2
      models: [claude-sonnet-4-6, claude-haiku-4-5]
`))
	if err != nil {
		t.Fatal(err)
	}
	gw := wf.GetNodes()[0].GetRun().GetLlmGateway()
	if gw.GetCli() != "claude-code" || gw.GetBudgetUsd() != 2 || strings.Join(gw.GetModels(), ",") != "claude-sonnet-4-6,claude-haiku-4-5" {
		t.Fatalf("parsed %+v", gw)
	}
	if errs := llmGatewayFindings(StaticAnalysisWithOptions(wf, &ValidationOptions{Authoring: true}).Errors()); len(errs) != 0 {
		t.Fatalf("valid declaration: %+v", errs)
	}
}
