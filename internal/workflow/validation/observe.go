package validation

import (
	"fmt"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"google.golang.org/protobuf/encoding/protojson"
)

// observeFieldKey is the `observe:` field of a node or a workflow, used as a
// path segment in findings.
const observeFieldKey = "observe"

// Every observe finding is a WARNING in category observe, whatever check
// raised it. Validation runs when a run loads its definition, and an error
// there blocks the run (runtime/load_workflow.go); a node's observability must
// never stop it from running. The lint rule observe-invalid reports the same
// findings as errors where a definition is reviewed, which is where a typo in
// observe is stopped.

// validateObserve compiles every expression of one observe block in the
// ObserveContext environment, with `output` typed as node's result. node is
// nil for a workflow's defaults, which every node of the graph merges, so
// their `output` is dyn. The block's shape is validateObserveShapes'.
func (s *workflowScope) validateObserve(o *reliantv1.NodeObserve, node *reliantv1.Node, basePath []string, scope *nodeOrderScope, result *Result) {
	found := NewResult()
	defer addObserveFindings(found, result)

	at := func(parts ...string) []string { return joinPath(basePath, parts...) }

	env, typeCtx, err := s.env(siteObserve, s.inLoop(), s.loop, node)
	if err != nil {
		s.reportEnvError(err, result)
		return
	}
	template := func(value string, path []string, eachScope bool) {
		e, tc := env, typeCtx
		if eachScope {
			if e, tc, err = s.env(siteObserveEach, s.inLoop(), s.loop, node); err != nil {
				s.reportEnvError(err, result)
				return
			}
		}
		for _, match := range extractTemplateExpressions(value) {
			if match.Expr == "" {
				continue
			}
			if referencesNodes(match.Expr) {
				rejectObserveNodesRef(path, found)
				continue
			}
			s.validateExpression(match.Expr, path, e, tc, scope, found)
		}
	}
	when := func(expr string, path []string) {
		if expr == "" || rejectTemplateDelimitersInRawCEL(expr, path, found) {
			return
		}
		if referencesNodes(expr) {
			rejectObserveNodesRef(path, found)
			return
		}
		s.validateExpression(expr, path, env, typeCtx, scope, found)
		if compiled, issues := env.Compile(expr); compiled != nil && (issues == nil || issues.Err() == nil) {
			validateConditionReturnType(compiled, expr, path, found)
		}
	}

	for _, k := range sortedStringKeys(o.GetAttributes()) {
		template(o.GetAttributes()[k], at("attributes", k), false)
	}
	for i, ev := range o.GetEvents() {
		item := fmt.Sprintf("[%d]", i)
		when(ev.GetWhen(), at("events", item, "when"))
		template(ev.GetEach(), at("events", item, "each"), false)
		for _, k := range sortedStringKeys(ev.GetAttributes()) {
			template(ev.GetAttributes()[k], at("events", item, "attributes", k), ev.GetEach() != "")
		}
	}
	for i, m := range o.GetMetrics() {
		item := fmt.Sprintf("[%d]", i)
		when(m.GetWhen(), at("metrics", item, "when"))
		template(m.GetValue(), at("metrics", item, "value"), false)
		for _, k := range sortedStringKeys(m.GetAttributes()) {
			template(m.GetAttributes()[k], at("metrics", item, "attributes", k), false)
		}
	}
}

// validateGraphObserve compiles the expressions of one graph's observe
// defaults (a node's own block is checked with the node).
func (s *workflowScope) validateGraphObserve(result *Result) {
	if defaults := s.wf.GetObserve(); defaults != nil {
		s.validateObserve(defaults, nil, joinPath(s.basePath, observeFieldKey), nil, result)
	}
}

// validateObserveShapes checks every observe block's shape, in every graph of
// wf (inline bodies and the on_failure and finally handlers included): the
// rules the YAML parser enforces, for a definition built as a proto (the
// builder), plus what only the whole graph shows: the distinct metric names it
// declares, and each node's block once the graph's defaults are merged into
// it, which must fit the request it rides in. It needs no types, so it runs
// even when the graph's structure has errors.
func validateObserveShapes(wf *reliantv1.Workflow, result *Result) {
	found := NewResult()
	defer addObserveFindings(found, result)
	walkObserveGraphs(wf, nil, func(g *reliantv1.Workflow, base []string) {
		defaults := g.GetObserve()
		names := map[string]bool{}
		check := func(o *reliantv1.NodeObserve, path []string) {
			for _, p := range model.ObserveProblems(o) {
				found.AddError(CategoryObserve, joinPath(path, p.Path...), "", p.Message)
			}
			for _, m := range o.GetMetrics() {
				names[m.GetName()] = true
			}
		}
		check(defaults, joinPath(base, observeFieldKey))
		for i, node := range g.GetNodes() {
			path := joinPath(base, "nodes", nodePathSegment(node, i), observeFieldKey)
			check(node.GetObserve(), path)
			merged := model.MergeObserve(defaults, node.GetObserve())
			if merged == nil {
				continue
			}
			if data, err := protojson.Marshal(merged); err == nil && len(data) > model.ObserveMaxRequestBytes {
				found.AddErrorWithSuggestion(CategoryObserve, path, "",
					fmt.Sprintf("this node's observe block, merged with the workflow's, is %d bytes; at most %d: the runtime will not record it", len(data), model.ObserveMaxRequestBytes),
					"record less, or move a long expression into a compute node's output and read that")
			}
		}
		if len(names) > model.ObserveMaxMetricsPerWorkflow {
			found.AddError(CategoryObserve, joinPath(base, observeFieldKey), "",
				fmt.Sprintf("the graph declares %d distinct metrics; at most %d", len(names), model.ObserveMaxMetricsPerWorkflow))
		}
	})
}

// walkObserveGraphs calls fn for wf and every graph nested in it: workflow
// and loop nodes' inline bodies, and the on_failure and finally handlers.
// Each is a graph of its own for observe: its defaults apply to its nodes.
func walkObserveGraphs(wf *reliantv1.Workflow, base []string, fn func(g *reliantv1.Workflow, base []string)) {
	if wf == nil {
		return
	}
	fn(wf, base)
	for i, node := range wf.GetNodes() {
		path := joinPath(base, "nodes", nodePathSegment(node, i), "inline")
		walkObserveGraphs(node.GetWorkflow().GetInline(), path, fn)
		walkObserveGraphs(node.GetLoop().GetInline(), path, fn)
	}
	walkObserveGraphs(wf.GetOnFailure(), joinPath(base, "on_failure"), fn)
	walkObserveGraphs(wf.GetFinally(), joinPath(base, "finally"), fn)
}

func rejectObserveNodesRef(path []string, result *Result) {
	result.Add(&Error{
		Severity: SeverityError,
		Category: CategoryObserve,
		Path:     append([]string{}, path...),
		Message: "observe references `nodes`, which is not in its scope: a node records what its own result says, " +
			"so observe reads `output` (this node's result), `inputs`, `trigger`, `workflow`, `iter`, `node` and, under each:, `item`",
		Suggestion: "expose the value as one of this node's outputs (a compute node's outputs:) and read output.<name>",
	})
}

// addObserveFindings adds what an observe check found to result as warnings in
// category observe, whatever check found them.
func addObserveFindings(found, result *Result) {
	for _, e := range found.errors {
		e.Severity = SeverityWarning
		e.Category = CategoryObserve
		result.Add(e)
	}
}

// ObserveFindings returns the observe findings of a validation result: what
// the lint rule observe-invalid reports.
func ObserveFindings(r *Result) []*Error {
	if r == nil {
		return nil
	}
	var out []*Error
	for _, e := range r.errors {
		if e.Category == CategoryObserve {
			out = append(out, e)
		}
	}
	return out
}
