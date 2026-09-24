// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// =============================================================================
// GUARDED ACCESS ANALYSIS
// =============================================================================
//
// The runtime is strict: reading a key that is not there fails the step with
// "no such key". Most such reads are type-correct — the node exists, the field
// is declared — and fail only because, on some path, the value is ABSENT:
//
//   - a node that a router or a parallel branch never ran (nodes.<id> absent),
//   - a condition-skipped node, whose output is only the skip marker
//     (model.SkippedOutputMap / SkippedRunOutputMap),
//   - a response tool the LLM did not call (response_data.<tool> is null),
//   - a response-tool field the schema does not require (absent when omitted).
//
// This analysis finds reads of such values that no guard protects. It models
// CEL's evaluation semantics rather than pattern-matching on "a has() appears
// somewhere", because the difference is exactly the difference between a
// guard that works and one that does not:
//
//   - `a && b` / `a || b` ABSORB errors: `false && error` is false and
//     `true || error` is true, in either operand order. So in `a && b`, b is
//     protected by whatever a proves when it is true, and vice versa.
//   - `c ? t : f` evaluates only the chosen branch: t is protected by what c
//     proves when true, f by what c proves when false.
//   - Only an ERROR-FREE test guards absence: has(x.f), x.?f, 'f' in x.
//     `x.f != null` is NOT a guard for an absent f — it reads x.f first and
//     fails with "no such key". It does guard a value that is present but
//     null (a response tool that was not called), which is what it proves.
//
// Facts proven by a gating expression carry over: a save_message condition
// gates its content, and a node condition gates the node's own config,
// inject, loop `while` and save_message — none of those evaluate unless the
// condition was true.

// riskKind distinguishes a value that may be missing from one that may be null.
type riskKind int

const (
	// riskAbsent: the key at this path may not exist. Guard: has(), ?., in.
	riskAbsent riskKind = iota
	// riskNull: the key exists but may be null, so reading BENEATH it fails.
	// Guard: != null, or has() on a child (has() on a null map is false).
	riskNull
)

// accessRisk is one way a path read can fail at runtime.
type accessRisk struct {
	path       []string
	kind       riskKind
	severity   Severity
	category   Category
	message    string
	suggestion string
}

// factSet records what an expression proves about paths when it evaluates to
// a given truth value.
type factSet struct {
	present map[string]bool
	nonnull map[string]bool
}

func newFactSet() factSet {
	return factSet{present: map[string]bool{}, nonnull: map[string]bool{}}
}

func (f factSet) clone() factSet {
	out := newFactSet()
	for k := range f.present {
		out.present[k] = true
	}
	for k := range f.nonnull {
		out.nonnull[k] = true
	}
	return out
}

func (f factSet) union(o factSet) factSet {
	out := f.clone()
	for k := range o.present {
		out.present[k] = true
	}
	for k := range o.nonnull {
		out.nonnull[k] = true
	}
	return out
}

func (f factSet) intersect(o factSet) factSet {
	out := newFactSet()
	for k := range f.present {
		if o.hasPresent(k) {
			out.present[k] = true
		}
	}
	for k := range o.present {
		if f.hasPresent(k) {
			out.present[k] = true
		}
	}
	for k := range f.nonnull {
		if o.hasNonNull(k) {
			out.nonnull[k] = true
		}
	}
	for k := range o.nonnull {
		if f.hasNonNull(k) {
			out.nonnull[k] = true
		}
	}
	return out
}

// hasPresent: p is proven present, directly or because a descendant is
// (has(a.b.c) being true means a.b exists too).
func (f factSet) hasPresent(p string) bool {
	if f.present[p] || f.nonnull[p] {
		return true
	}
	for q := range f.present {
		if strings.HasPrefix(q, p+".") {
			return true
		}
	}
	return false
}

// hasNonNull: p is proven non-null, directly or because a child is proven
// present (a null value has no children).
func (f factSet) hasNonNull(p string) bool {
	if f.nonnull[p] {
		return true
	}
	for q := range f.present {
		if strings.HasPrefix(q, p+".") {
			return true
		}
	}
	return false
}

func pathKey(segs []string) string { return strings.Join(segs, ".") }

// accessChain is a read like nodes.a.b or output.response_data['t'].f.
// optionalFrom is the index of the first segment reached through ?. (or
// [?k]); that segment and everything beneath it read as optional, so they can
// never fail on absence.
type accessChain struct {
	segs         []string
	optionalFrom int
}

// extractChain flattens a select/index chain rooted at an identifier.
func extractChain(e ast.Expr) (accessChain, bool) {
	switch e.Kind() {
	case ast.IdentKind:
		return accessChain{segs: []string{e.AsIdent()}, optionalFrom: 1 << 30}, true
	case ast.SelectKind:
		sel := e.AsSelect()
		if sel.IsTestOnly() {
			return accessChain{}, false
		}
		parent, ok := extractChain(sel.Operand())
		if !ok {
			return accessChain{}, false
		}
		parent.segs = append(append([]string{}, parent.segs...), sel.FieldName())
		return parent, true
	case ast.CallKind:
		call := e.AsCall()
		args := call.Args()
		fn := call.FunctionName()
		if (fn == operators.OptSelect || fn == operators.Index || fn == operators.OptIndex) && len(args) == 2 {
			key, ok := stringLiteral(args[1])
			if !ok {
				return accessChain{}, false
			}
			parent, ok := extractChain(args[0])
			if !ok {
				return accessChain{}, false
			}
			parent.segs = append(append([]string{}, parent.segs...), key)
			if fn != operators.Index && len(parent.segs)-1 < parent.optionalFrom {
				parent.optionalFrom = len(parent.segs) - 1
			}
			return parent, true
		}
	}
	return accessChain{}, false
}

func stringLiteral(e ast.Expr) (string, bool) {
	if e.Kind() != ast.LiteralKind {
		return "", false
	}
	s, ok := e.AsLiteral().Value().(string)
	return s, ok
}

// hasOperand returns the chain tested by a has() macro (a test-only select).
func hasOperand(e ast.Expr) (accessChain, bool) {
	if e.Kind() != ast.SelectKind || !e.AsSelect().IsTestOnly() {
		return accessChain{}, false
	}
	sel := e.AsSelect()
	parent, ok := extractChain(sel.Operand())
	if !ok {
		return accessChain{}, false
	}
	parent.segs = append(append([]string{}, parent.segs...), sel.FieldName())
	return parent, true
}

// guardWalker checks every read in one expression against the risk
// classifier, under the facts in scope at that read.
type guardWalker struct {
	classify func(segs []string) []accessRisk
	seen     map[string]bool
	findings []accessRisk
}

func (w *guardWalker) report(r accessRisk) {
	key := fmt.Sprintf("%d:%s:%s", r.kind, pathKey(r.path), r.message)
	if w.seen[key] {
		return
	}
	w.seen[key] = true
	w.findings = append(w.findings, r)
}

// require checks one read. hasMode means the read is the operand of has(),
// whose LAST segment is tested rather than read.
func (w *guardWalker) require(chain accessChain, ctx factSet, hasMode bool) {
	last := len(chain.segs) - 1
	for _, r := range w.classify(chain.segs) {
		i := len(r.path) - 1
		switch r.kind {
		case riskAbsent:
			if i >= chain.optionalFrom || (hasMode && i == last) || ctx.hasPresent(pathKey(r.path)) {
				continue
			}
			w.report(r)
		case riskNull:
			child := i + 1
			if child > last || child >= chain.optionalFrom || (hasMode && child == last) || ctx.hasNonNull(pathKey(r.path)) {
				continue
			}
			w.report(r)
		}
	}
}

func (w *guardWalker) check(e ast.Expr, ctx factSet) {
	if e == nil {
		return
	}
	switch e.Kind() {
	case ast.SelectKind:
		if chain, ok := hasOperand(e); ok {
			w.require(chain, ctx, true)
			return
		}
		if chain, ok := extractChain(e); ok {
			w.require(chain, ctx, false)
			return
		}
		w.check(e.AsSelect().Operand(), ctx)
	case ast.CallKind:
		call := e.AsCall()
		args := call.Args()
		switch call.FunctionName() {
		case operators.LogicalAnd:
			if len(args) == 2 {
				w.check(args[0], ctx.union(proven(args[1], true)))
				w.check(args[1], ctx.union(proven(args[0], true)))
				return
			}
		case operators.LogicalOr:
			if len(args) == 2 {
				w.check(args[0], ctx.union(proven(args[1], false)))
				w.check(args[1], ctx.union(proven(args[0], false)))
				return
			}
		case operators.Conditional:
			if len(args) == 3 {
				w.check(args[0], ctx)
				w.check(args[1], ctx.union(proven(args[0], true)))
				w.check(args[2], ctx.union(proven(args[0], false)))
				return
			}
		case operators.OptSelect, operators.Index, operators.OptIndex:
			if chain, ok := extractChain(e); ok {
				w.require(chain, ctx, false)
				return
			}
		}
		if call.IsMemberFunction() {
			w.check(call.Target(), ctx)
		}
		for _, a := range args {
			w.check(a, ctx)
		}
	case ast.ComprehensionKind:
		comp := e.AsComprehension()
		w.check(comp.IterRange(), ctx)
		w.check(comp.AccuInit(), ctx)
		w.check(comp.LoopCondition(), ctx)
		w.check(comp.LoopStep(), ctx)
		w.check(comp.Result(), ctx)
	case ast.ListKind:
		for _, el := range e.AsList().Elements() {
			w.check(el, ctx)
		}
	case ast.MapKind:
		for _, entry := range e.AsMap().Entries() {
			me := entry.AsMapEntry()
			w.check(me.Key(), ctx)
			w.check(me.Value(), ctx)
		}
	case ast.StructKind:
		for _, f := range e.AsStruct().Fields() {
			w.check(f.AsStructField().Value(), ctx)
		}
	}
}

// proven returns the facts that hold whenever e evaluates to polarity.
func proven(e ast.Expr, polarity bool) factSet {
	out := provenDirect(e, polarity)
	// Loop outputs are all-or-nothing: any presence proof for one
	// outputs.<name>, or proof that this is not the first iteration, proves
	// them all (see validateLoopOutputsAccess).
	populated := string(wfcel.CELOutputs) + "." + loopOutputsSentinel
	for p := range out.present {
		if strings.HasPrefix(p, string(wfcel.CELOutputs)+".") {
			out.present[populated] = true
			break
		}
	}
	if provesNotFirstIteration(e, polarity) {
		out.present[populated] = true
	}
	return out
}

// provesNotFirstIteration reports whether e evaluating to polarity proves
// iter.iteration (or iter.index) is not 0.
func provesNotFirstIteration(e ast.Expr, polarity bool) bool {
	if e == nil || e.Kind() != ast.CallKind {
		return false
	}
	call := e.AsCall()
	args := call.Args()
	if call.FunctionName() == operators.LogicalNot && len(args) == 1 {
		return provesNotFirstIteration(args[0], !polarity)
	}
	if len(args) != 2 {
		return false
	}
	lhs, ok := extractChain(args[0])
	if !ok || len(lhs.segs) != 2 || lhs.segs[0] != string(wfcel.CELIter) || (lhs.segs[1] != "iteration" && lhs.segs[1] != "index") {
		return false
	}
	if args[1].Kind() != ast.LiteralKind {
		return false
	}
	n, ok := args[1].AsLiteral().Value().(int64)
	if !ok {
		return false
	}
	switch call.FunctionName() {
	case operators.Greater: // iter > n, n >= 0
		return polarity && n >= 0
	case operators.GreaterEquals: // iter >= n, n >= 1
		return polarity && n >= 1
	case operators.NotEquals: // iter != 0
		return polarity && n == 0
	case operators.Equals: // !(iter == 0)
		return !polarity && n == 0
	case operators.LessEquals: // !(iter <= 0)
		return !polarity && n == 0
	case operators.Less: // !(iter < 1)
		return !polarity && n == 1
	}
	return false
}

// provenDirect is proven() without the loop-outputs closure.
func provenDirect(e ast.Expr, polarity bool) factSet {
	out := newFactSet()
	if e == nil {
		return out
	}
	if chain, ok := hasOperand(e); ok {
		if polarity {
			out.present[pathKey(chain.segs)] = true
		}
		return out
	}
	if e.Kind() != ast.CallKind {
		return out
	}
	call := e.AsCall()
	args := call.Args()
	switch call.FunctionName() {
	case operators.LogicalNot:
		if len(args) == 1 {
			return proven(args[0], !polarity)
		}
	case operators.LogicalAnd:
		if len(args) == 2 {
			if polarity {
				return proven(args[0], true).union(proven(args[1], true))
			}
			return proven(args[0], false).intersect(proven(args[1], false))
		}
	case operators.LogicalOr:
		if len(args) == 2 {
			if polarity {
				return proven(args[0], true).intersect(proven(args[1], true))
			}
			return proven(args[0], false).union(proven(args[1], false))
		}
	case operators.Conditional:
		if len(args) == 3 {
			whenTrue := proven(args[0], true).union(proven(args[1], polarity))
			whenFalse := proven(args[0], false).union(proven(args[2], polarity))
			return whenTrue.intersect(whenFalse)
		}
	case operators.NotEquals, operators.Equals:
		// x != null proves x non-null when true; x == null proves it when false.
		if len(args) == 2 && polarity == (call.FunctionName() == operators.NotEquals) {
			for _, pair := range [][2]ast.Expr{{args[0], args[1]}, {args[1], args[0]}} {
				if isNullLiteral(pair[1]) {
					if chain, ok := extractChain(pair[0]); ok {
						out.nonnull[pathKey(chain.segs)] = true
					}
				}
			}
		}
	case operators.In:
		if len(args) == 2 && polarity {
			if key, ok := stringLiteral(args[0]); ok {
				if chain, ok := extractChain(args[1]); ok {
					out.present[pathKey(append(append([]string{}, chain.segs...), key))] = true
				}
			}
		}
	case "hasValue":
		// x.?f.hasValue() is has(x.f) spelled with optionals.
		if polarity && call.IsMemberFunction() {
			if chain, ok := extractChain(call.Target()); ok {
				out.present[pathKey(chain.segs)] = true
			}
		}
	}
	return out
}

// guardParseEnv parses an expression in its original nodes.X.f form, with
// every namespace dyn, so the chains the walker sees are the ones the author
// wrote (the typed compile pass rewrites nodes.X to nodes_X).
func guardParseEnv() (*cel.Env, error) {
	opts := []cel.EnvOption{wfcel.StdLib(), cel.OptionalTypes()}
	for _, ns := range wfcel.AllNamespaces() {
		opts = append(opts, cel.Variable(string(ns), cel.DynType))
	}
	opts = append(opts, wfcel.CustomFunctions()...)
	return cel.NewCustomEnv(opts...)
}

// analyzeGuardedAccess parses expr and returns every unguarded risky read.
func analyzeGuardedAccess(expr string, gate factSet, classify func([]string) []accessRisk) []accessRisk {
	env, err := guardParseEnv()
	if err != nil {
		return nil
	}
	parsed, issues := env.Parse(expr)
	if issues != nil && issues.Err() != nil {
		return nil // reported by the compile pass
	}
	w := &guardWalker{classify: classify, seen: map[string]bool{}}
	w.check(parsed.NativeRep().Expr(), gate)
	return w.findings
}

// provenByCondition returns the facts a raw-CEL condition proves when true.
func provenByCondition(condition string) factSet {
	if strings.TrimSpace(condition) == "" {
		return newFactSet()
	}
	env, err := guardParseEnv()
	if err != nil {
		return newFactSet()
	}
	parsed, issues := env.Parse(condition)
	if issues != nil && issues.Err() != nil {
		return newFactSet()
	}
	return proven(parsed.NativeRep().Expr(), true)
}

// =============================================================================
// RISK CLASSIFICATION
// =============================================================================

// nodeOrderScope describes where an expression evaluates, for ordering and
// guard analysis. See node_order.go.
//
//   - nodeID/afterNode: the node the expression belongs to, and whether it
//     evaluates after that node completes (save_message, outbound edges) or
//     before it starts (config, inject, node condition, while).
//   - completion: a declared workflow output, evaluated when the graph ends.
//   - gate: facts proven by a condition that must have been true for this
//     expression to evaluate at all.
//   - outputNode: the node whose result is bound to `output` (save_message).
type nodeOrderScope struct {
	nodeID     string
	afterNode  bool
	completion bool
	gate       factSet
	outputNode string
	// loopOutputsBound: outputs.* holds a completed iteration's outputs
	// whenever this expression evaluates (a loop's `while`).
	loopOutputsBound bool
}

func (s *nodeOrderScope) gateFacts() factSet {
	if s == nil || s.gate.present == nil {
		return newFactSet()
	}
	return s.gate
}

// withGate returns a copy of the scope that also carries the facts proven by
// a gating condition.
func (s nodeOrderScope) withGate(condition string) *nodeOrderScope {
	s.gate = s.gateFacts().union(provenByCondition(condition))
	return &s
}

// skipOutputFields is exactly what a condition-skipped node publishes
// (engine.go skipNodeIfConditionFalse): the skip marker, plus zeroed run
// fields for run nodes. Derived from the runtime's own maps.
func skipOutputFields(nodeType string) map[string]bool {
	var skipped map[string]interface{}
	if nodeType == model.NodeTypeRun {
		skipped = model.SkippedRunOutputMap()
	} else {
		skipped = model.SkippedOutputMap()
	}
	out := make(map[string]bool, len(skipped))
	for k := range skipped {
		out[k] = true
	}
	return out
}

// guaranteedForScope returns the nodes guaranteed to have produced an output
// when an expression in this scope evaluates, and whether ordering applies.
func guaranteedForScope(scope *nodeOrderScope, typeCtx *WorkflowTypeContext) (map[string]bool, bool) {
	if scope == nil || typeCtx == nil {
		return nil, false
	}
	if scope.completion {
		if typeCtx.GuaranteedAtCompletion == nil {
			return nil, false
		}
		return typeCtx.GuaranteedAtCompletion, true
	}
	if scope.nodeID == "" || typeCtx.GuaranteedBefore == nil {
		return nil, false
	}
	g, ok := typeCtx.GuaranteedBefore[scope.nodeID]
	return g, ok
}

// classifyAccess returns every way a read of segs can fail on some path.
func classifyAccess(segs []string, scope *nodeOrderScope, typeCtx *WorkflowTypeContext) []accessRisk {
	if typeCtx == nil || len(segs) < 2 {
		return nil
	}
	var risks []accessRisk
	switch segs[0] {
	case string(wfcel.CELNodes):
		nodeID := segs[1]
		nodeType, known := typeCtx.NodeTypes[nodeID]
		if !known {
			return nil
		}
		if r, ok := orderingRisk(nodeID, scope, typeCtx); ok {
			risks = append(risks, r)
		}
		if len(segs) >= 3 {
			if condition, conditional := typeCtx.ConditionalNodes[nodeID]; conditional {
				if r, ok := skippedFieldRisk(nodeID, nodeType, segs[2], condition); ok {
					risks = append(risks, r)
				}
			}
			if segs[2] == "response_data" {
				risks = append(risks, responseToolRisks(nodeID, segs[:3], segs[3:], typeCtx)...)
			}
		}
	case string(wfcel.CELOutput):
		if scope != nil && scope.outputNode != "" && segs[1] == "response_data" {
			risks = append(risks, responseToolRisks(scope.outputNode, segs[:2], segs[2:], typeCtx)...)
		}
	}
	return risks
}

func orderingRisk(refID string, scope *nodeOrderScope, typeCtx *WorkflowTypeContext) (accessRisk, bool) {
	guaranteed, applies := guaranteedForScope(scope, typeCtx)
	if !applies || guaranteed[refID] || (!scope.completion && refID == scope.nodeID) {
		return accessRisk{}, false
	}
	path := []string{string(wfcel.CELNodes), refID}
	if !scope.completion {
		if refG, ok := typeCtx.GuaranteedBefore[refID]; ok && refG[scope.nodeID] {
			return accessRisk{
				path: path, kind: riskAbsent, severity: SeverityError, category: CategoryNodeOrdering,
				message: fmt.Sprintf(
					"references 'nodes.%s', but node '%s' always executes AFTER node '%s' — nodes.%s can never be populated when this expression is evaluated",
					refID, refID, scope.nodeID, refID),
				suggestion: fmt.Sprintf("reference a node that runs before '%s', or restructure the edges", scope.nodeID),
			}, true
		}
	}
	where := fmt.Sprintf("before node '%s'", scope.nodeID)
	if scope.completion {
		where = "when the workflow completes"
	}
	return accessRisk{
		path: path, kind: riskAbsent, severity: SeverityError, category: CategoryNodeOrdering,
		message: fmt.Sprintf(
			"references 'nodes.%s', but node '%s' is not guaranteed to have executed %s (it is not on every path from the workflow entry — e.g. a router dispatch, a parallel branch or a conditional edge can skip it); at runtime this fails with \"no such key: %s\" when the node has not run",
			refID, refID, where, refID),
		suggestion: fmt.Sprintf(
			"guard the access: has(nodes.%s) && has(nodes.%s.<field>) ? nodes.%s.<field> : <fallback>, or restructure edges so '%s' always runs first",
			refID, refID, refID, refID),
	}, true
}

func skippedFieldRisk(nodeID, nodeType, field, condition string) (accessRisk, bool) {
	supplied := skipOutputFields(nodeType)
	if field == model.SkippedOutputField {
		return accessRisk{}, false
	}
	path := []string{string(wfcel.CELNodes), nodeID, field}
	if supplied[field] {
		// Present either way, so the read cannot fail — but on a skip it is a
		// stand-in zero ("did not run" reads the same as "ran and returned 0").
		// A warning, silenced by the same guards as the error case.
		return accessRisk{
			path: path, kind: riskAbsent, severity: SeverityWarning, category: CategoryConditionalAccess,
			message: fmt.Sprintf(
				"node '%s' has a condition and may be skipped (condition: %s); when skipped, nodes.%s.%s is the zero value from the skip output, indistinguishable from a real result",
				nodeID, condition, nodeID, field),
			suggestion: fmt.Sprintf("check the same condition as '%s' before trusting nodes.%s.%s", nodeID, nodeID, field),
		}, true
	}
	return accessRisk{
		path: path, kind: riskAbsent, severity: SeverityError, category: CategoryConditionalAccess,
		message: fmt.Sprintf(
			"node '%s' has a condition and may be skipped (condition: %s); a skipped node's output has no '%s', so this read fails with \"no such key: %s\"",
			nodeID, condition, field, field),
		suggestion: fmt.Sprintf("guard the access: has(nodes.%s.%s) ? nodes.%s.%s : <fallback> (or nodes.%s.?%s)", nodeID, field, nodeID, field, nodeID, field),
	}, true
}

// responseToolRisks classifies reads beneath <prefix> = …response_data of an
// execute_tools node: the tool segment is null when the LLM did not call the
// tool (the runtime pre-creates every expected tool key as null), and a
// field the schema does not require is absent when the LLM omitted it.
func responseToolRisks(execNodeID string, prefix, rest []string, typeCtx *WorkflowTypeContext) []accessRisk {
	if typeCtx.ResponseTools == nil || len(rest) == 0 {
		return nil
	}
	schema, ok := typeCtx.ResponseTools.AvailableTools[execNodeID][rest[0]]
	if !ok {
		return nil // unknown tool: reported by validateResponseDataAccessWithContext
	}
	tool := rest[0]
	toolPath := append(append([]string{}, prefix...), tool)
	display := pathKey(toolPath)
	risks := []accessRisk{{
		path: toolPath, kind: riskNull, severity: SeverityError, category: CategoryCELSemantic,
		message: fmt.Sprintf(
			"%s is null when the LLM did not call the '%s' response tool, so reading beneath it fails with \"no such key\"",
			display, tool),
		suggestion: fmt.Sprintf("guard the access: has(%s.<field>) ? %s.<field> : <fallback>, or %s != null && …", display, display, display),
	}}
	if len(rest) >= 2 {
		field := rest[1]
		if _, declared := schema.Fields[field]; declared && !schema.Required[field] {
			severity := SeverityError
			why := fmt.Sprintf("the '%s' schema does not list it in `required`", tool)
			if !schema.HasRequired {
				// A schema with no `required` array at all is usually loosely
				// authored rather than deliberately all-optional.
				severity = SeverityWarning
				why = fmt.Sprintf("the '%s' schema declares no `required` fields", tool)
			}
			fieldPath := append(append([]string{}, toolPath...), field)
			risks = append(risks, accessRisk{
				path: fieldPath, kind: riskAbsent, severity: severity, category: CategoryCELSemantic,
				message: fmt.Sprintf(
					"response-tool field '%s' is optional (%s): it is absent when the LLM omits it, so this read fails with \"no such key: %s\"",
					field, why, field),
				suggestion: fmt.Sprintf("guard the access: has(%s) ? %s : <fallback>, or add '%s' to the schema's `required`", pathKey(fieldPath), pathKey(fieldPath), field),
			})
		}
	}
	return risks
}

// validateGuardedAccess reports every unguarded risky read in one CEL
// expression (the ORIGINAL nodes.X.f form, without {{ }}).
func validateGuardedAccess(expr string, path []string, scope *nodeOrderScope, typeCtx *WorkflowTypeContext, result *Result) {
	if typeCtx == nil || strings.TrimSpace(expr) == "" {
		return
	}
	if scope != nil && scope.completion && rescuedByTypedZero(expr, typeCtx) {
		return
	}
	findings := analyzeGuardedAccess(expr, scope.gateFacts(), func(segs []string) []accessRisk {
		return classifyAccess(segs, scope, typeCtx)
	})
	sort.SliceStable(findings, func(i, j int) bool { return pathKey(findings[i].path) < pathKey(findings[j].path) })
	for _, f := range findings {
		result.Add(&Error{
			Severity:   f.severity,
			Category:   f.category,
			Path:       append([]string{}, path...),
			Message:    f.message,
			Suggestion: f.suggestion,
		})
	}
}

// =============================================================================
// TYPED-ZERO RESCUE (declared outputs)
// =============================================================================

// rescuedByTypedZero mirrors the runtime's substituteTypedZero
// (runtime/loop_output_schema.go): a declared output that is NOTHING BUT a
// bare `nodes.<id>.<field>` reference to a declared activity node, whose
// field is a list or map on the node's output type, resolves to the empty
// container when the node did not produce it. Nested paths never qualify —
// the runtime zero for a sub-message is nil, which it will not traverse.
//
// The runtime cannot be imported here (it imports this package), so the rule
// is restated from the proto descriptors, and
// scenario/runner.TestTypedZeroRescueParity pins the two against each other
// for every node type the registry knows.
func rescuedByTypedZero(expr string, typeCtx *WorkflowTypeContext) bool {
	nodeID, field, ok := parseBareNodeField(expr)
	if !ok {
		return false
	}
	nodeType, known := typeCtx.NodeTypes[nodeID]
	if !known {
		return false
	}
	return TypedZeroRescuable(nodeType, field)
}

// TypedZeroRescuable reports whether the runtime substitutes an empty
// container for an absent `nodes.<id>.<field>` of this node type in a
// declared output. Exported for the runtime parity test.
func TypedZeroRescuable(nodeType, field string) bool {
	if !typedZeroActivityNode(nodeType) {
		return false
	}
	md := registryOutputDescriptor(sharedRegistry, nodeType)
	if md == nil {
		return false
	}
	fd := md.Fields().ByName(protoreflect.Name(field))
	if fd == nil {
		return false
	}
	return fd.IsList() || fd.IsMap()
}

// typedZeroActivityNode mirrors runtime.nodeActivityName: activity nodes plus
// run (which has a registered output type). Structural nodes have none.
func typedZeroActivityNode(nodeType string) bool {
	return nodeType == model.NodeTypeRun || model.IsActivityNode(nodeType)
}

// bareNodeFieldPattern is runtime.bareNodePath restricted to ONE field: the
// runtime accepts deeper paths, but only a top-level field can have a
// non-nil container zero (see TypedZeroRescuable).
var bareNodeFieldPattern = regexp.MustCompile(`^nodes\.([A-Za-z_][A-Za-z0-9_]*)\.([A-Za-z_][A-Za-z0-9_]*)$`)

// parseBareNodeField accepts exactly `{{nodes.<id>.<field>}}` (or unwrapped).
func parseBareNodeField(expr string) (string, string, bool) {
	trimmed := strings.TrimSpace(expr)
	if strings.HasPrefix(trimmed, "{{") && strings.HasSuffix(trimmed, "}}") && len(trimmed) > 4 {
		trimmed = strings.TrimSpace(trimmed[2 : len(trimmed)-2])
	}
	m := bareNodeFieldPattern.FindStringSubmatch(trimmed)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// =============================================================================
// COMPLETION ORDERING
// =============================================================================

// computeGuaranteedAtCompletion returns the nodes guaranteed to have produced
// an output when the workflow ends, which is when declared outputs evaluate.
//
// A run ends at some path end: a node with no outbound edges, or a node whose
// outbound edges are all conditional with no default (no case may match). A
// node is guaranteed at completion when it is guaranteed at EVERY possible
// path end — whichever end the run reached, it ran.
func computeGuaranteedAtCompletion(wf *reliantv1.Workflow, guaranteedBefore map[string]map[string]bool) map[string]bool {
	if wf == nil || guaranteedBefore == nil {
		return nil
	}
	always := map[string]bool{}
	hasOut := map[string]bool{}
	for _, edge := range wf.GetEdges() {
		if len(edge.GetDefault()) > 0 {
			always[edge.GetFrom()] = true
		}
		for _, c := range edge.GetCases() {
			if c.GetCondition() == "" && len(c.GetTo()) > 0 {
				always[edge.GetFrom()] = true
			}
		}
		hasOut[edge.GetFrom()] = true
	}
	for _, node := range wf.GetNodes() {
		if node.GetType() == model.NodeTypeRouter && len(node.GetRouter().GetNodes()) > 0 {
			always[node.GetId()] = true
			hasOut[node.GetId()] = true
		}
	}
	var result map[string]bool
	for _, node := range wf.GetNodes() {
		id := node.GetId()
		if hasOut[id] && always[id] {
			continue // not a path end
		}
		reached := map[string]bool{id: true}
		for k := range guaranteedBefore[id] {
			reached[k] = true
		}
		if result == nil {
			result = reached
			continue
		}
		for k := range result {
			if !reached[k] {
				delete(result, k)
			}
		}
	}
	if result == nil {
		result = map[string]bool{}
	}
	return result
}
