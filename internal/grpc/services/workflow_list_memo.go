// Copyright (c) 2025 Reliant Labs
package services

import (
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/workflowcatalog"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
)

// parseBuiltinWorkflowFile is parseWorkflowYAML for an embedded builtin
// workflow file, from the process cache.
func parseBuiltinWorkflowFile(name string, data []byte) (*reliantv1.Workflow, error) {
	return workflowcatalog.CachedBuiltinParse("file:"+name, func() (*reliantv1.Workflow, error) {
		return parseWorkflowYAML(data)
	})
}

// resolvingToSelf is workflowsource.DraftLoader's rule for the workflow being
// validated: a ref to itself resolves to the definition in hand, so a draft
// that spawns itself validates. Every other ref goes to load, which a listing
// shares across all the workflows it validates.
func resolvingToSelf(self *reliantv1.Workflow, load func(ref string) (*reliantv1.Workflow, error)) func(ref string) (*reliantv1.Workflow, error) {
	selfKey := ""
	if self != nil && self.GetName() != "" {
		selfKey = workflowref.RefKey(self.GetName())
	}
	return func(ref string) (*reliantv1.Workflow, error) {
		if selfKey != "" && workflowref.RefKey(ref) == selfKey {
			return self, nil
		}
		return load(ref)
	}
}

// memoizePreflight answers each distinct tool question once. Machine analysis
// asks the same ones of every workflow (the builtin agent's default tool
// filter appears in most of them), and each answer rebuilds the tool
// registry.
func memoizePreflight(cfg *v2.PreflightConfig) *v2.PreflightConfig {
	if cfg == nil {
		return nil
	}
	memo := *cfg
	if cfg.ExpandToolFilter != nil {
		expanded := map[string][]string{}
		memo.ExpandToolFilter = func(filter []string) []string {
			key := strings.Join(filter, "\x00")
			out, ok := expanded[key]
			if !ok {
				out = cfg.ExpandToolFilter(filter)
				expanded[key] = out
			}
			return append([]string(nil), out...)
		}
	}
	memo.IsDaemonTool = memoizeToolCheck(cfg.IsDaemonTool)
	memo.NeedsMachine = memoizeToolCheck(cfg.NeedsMachine)
	return &memo
}

func memoizeToolCheck(check v2.DaemonToolChecker) v2.DaemonToolChecker {
	if check == nil {
		return nil
	}
	answers := map[string]bool{}
	return func(name string) bool {
		answer, ok := answers[name]
		if !ok {
			answer = check(name)
			answers[name] = answer
		}
		return answer
	}
}
