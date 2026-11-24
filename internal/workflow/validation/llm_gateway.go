// Copyright (c) 2025 Reliant Labs
package validation

import (
	"sort"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llmgatewaystep"
)

// validateRunLLMGateway checks a run node's `llm_gateway`: a CLI Reliant can
// set up, a budget within bounds, distinct non-empty models, and a node `env:`
// that touches none of the variables the CLI's step key sets or removes (an
// author's ANTHROPIC_BASE_URL would send the key somewhere else; the machine
// applies the step's variables last regardless). Every value is plain, so all
// of it is known when the workflow is saved, and an error here is an error at
// run time too: a step that would be refused never starts.
func validateRunLLMGateway(args *reliantv1.RunArgs, nodePath []string, result *Result) {
	gw := args.GetLlmGateway()
	if gw == nil {
		return
	}
	if err := llmgatewaystep.Validate(gw.GetCli(), gw.GetBudgetUsd(), gw.GetModels()); err != nil {
		result.AddErrorWithSuggestion(CategoryStructure, nodePath, "llm_gateway", err.Error(),
			"llm_gateway: {cli: claude-code, budget_usd: 2}")
		return
	}
	cli, _ := llmgatewaystep.Lookup(gw.GetCli())
	reserved := cli.Names()
	var clashes []string
	for name := range args.GetEnv() {
		for _, r := range reserved {
			if strings.EqualFold(name, r) {
				clashes = append(clashes, name)
				break
			}
		}
	}
	if len(clashes) > 0 {
		sort.Strings(clashes)
		result.AddErrorWithSuggestion(CategoryStructure, nodePath, "env",
			"env sets "+strings.Join(clashes, ", ")+", which llm_gateway sets or removes for "+cli.Name+
				": the step key's variables are Reliant's to set",
			"remove "+strings.Join(clashes, ", ")+" from env; choose models with llm_gateway.models")
	}
}
