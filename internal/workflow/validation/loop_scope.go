// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"regexp"
	"sort"

	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"google.golang.org/protobuf/types/known/structpb"
)

// =============================================================================
// LOOP-SCOPED NAMESPACES: outputs and iter
// =============================================================================

// validateLoopOutputsAccess reports reads of the enclosing loop's `outputs`
// that can hit a missing key.
//
// Inside a loop body, `outputs` is the PREVIOUS iteration's declared outputs.
// On iteration 0 there is no previous iteration, so the runtime binds an
// EMPTY map; in a parallel loop every iteration sees that empty map. A bare
// outputs.<name> therefore fails with "no such key" on the first pass.
//
// `outputs` is all-or-nothing: empty on iteration 0, the full declared set on
// every later iteration of a sequential loop. So ANY proof that one output is
// present — has(outputs.x), outputs.?x, 'x' in outputs — or that this is not
// the first iteration (iter.iteration > 0, or the false branch of
// iter.iteration == 0) protects every outputs.<name> read. The proof is
// recorded under loopOutputsSentinel by proven().
//
// A loop's own `while` is exempt: it evaluates after an iteration completes,
// so outputs.* is always the full declared set there.
func validateLoopOutputsAccess(expr string, path []string, scope *nodeOrderScope, typeCtx *WorkflowTypeContext, result *Result) {
	if typeCtx == nil || !typeCtx.InLoopBody || (scope != nil && scope.loopOutputsBound) {
		return
	}
	parallel := typeCtx.LoopOutputsAbsent
	findings := analyzeGuardedAccess(expr, scope.gateFacts(), func(segs []string) []accessRisk {
		if len(segs) < 2 || segs[0] != string(wfcel.CELOutputs) {
			return nil
		}
		name := segs[1]
		if parallel {
			return []accessRisk{{
				path: segs[:2], kind: riskAbsent, severity: SeverityError, category: CategoryCELSemantic,
				message:    fmt.Sprintf("outputs.%s is never set in a parallel loop body (iterations do not see each other), so this read always fails with \"no such key: %s\"", name, name),
				suggestion: "read the value from nodes, inputs or iter.item instead",
			}}
		}
		return []accessRisk{{
			// The risk is "outputs is not populated yet", which any sibling
			// presence proof discharges — hence the sentinel path.
			path: []string{string(wfcel.CELOutputs), loopOutputsSentinel}, kind: riskAbsent, severity: SeverityError, category: CategoryCELSemantic,
			message:    fmt.Sprintf("outputs.%s is the previous loop iteration's output; on the first iteration `outputs` is empty, so this read fails with \"no such key: %s\"", name, name),
			suggestion: fmt.Sprintf("guard it: has(outputs.%s) ? outputs.%s : <fallback>, or iter.iteration > 0 && …", name, name),
		}}
	})
	for _, f := range findings {
		result.Add(&Error{
			Severity: f.severity, Category: f.category, Path: append([]string{}, path...),
			Message: f.message, Suggestion: f.suggestion,
		})
	}
}

// loopOutputsSentinel is a synthetic path segment meaning "the loop's
// outputs are populated". It cannot collide with a real field name.
const loopOutputsSentinel = "\x00populated"

// iterFieldNames is the iter shape per scope (see wfcel's iter activation:
// iteration/index are always bound; item/key only in items loops).
func iterFieldNames(scope iterScope) []string {
	if scope == iterItems {
		return []string{"iteration", "index", "item", "key"}
	}
	return []string{"iteration", "index"}
}

func sortStrings(keys []string) { sort.Strings(keys) }

func sortedValueKeys(m map[string]*structpb.Value) []string {
	keys := make([]string, 0, len(m))
	for k, v := range m {
		if v != nil {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// outputIdentPattern matches the `output` namespace followed by a field
// select, not as a suffix of a longer identifier or a select.
var outputIdentPattern = regexp.MustCompile(`(^|[^A-Za-z0-9_.])output\.`)
