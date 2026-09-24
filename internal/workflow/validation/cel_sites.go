// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"sort"
	"strings"

	"github.com/google/cel-go/cel"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// =============================================================================
// PER-SITE CEL ENVIRONMENTS
// =============================================================================
//
// Every CEL expression in a workflow is evaluated by the runtime against one
// typed context (wfcel.*Context), and that context's Namespaces() is the
// complete list of variables the expression can see. A reference outside it
// is a deterministic runtime compile error ("undeclared reference to
// 'outputs'"). So validation compiles each expression in an environment built
// from the SAME context type the runtime uses at that site — never from a
// private list. The site → context mapping:
//
//	node config, thread inject, loop items/args,
//	declared workflow outputs                    NodeResolutionContext
//	node condition, edge case condition          EdgeEvalContext
//	loop while, loop key                         LoopEvalContext
//	save_message (content, condition, …)         PostActivityContext
//
// `outputs` (the enclosing loop's previous-iteration outputs) is declared by a
// context only when its Outputs field is set, which the runtime does exactly
// at in-loop sites. Validation mirrors that by instantiating the context with
// a non-nil Outputs when the expression sits in a loop body.

// saveMessageFieldKey is the node's `save_message:` field, used as a path
// segment in findings (the field, not the save_message node type).
const saveMessageFieldKey = "save_message"

// celSite identifies which runtime context evaluates an expression.
type celSite int

const (
	siteNodeResolution celSite = iota
	siteCondition
	siteLoopWhile
	siteLoopKey
	siteSaveMessage
)

// siteNamespaces returns the namespaces the runtime context for site declares.
// inLoop reports whether the expression evaluates inside a loop iteration, so
// the enclosing loop's `outputs` is bound — the contexts declare `outputs`
// exactly when their Outputs field is non-nil, which the runtime sets at
// every in-loop site and nowhere else.
func siteNamespaces(site celSite, inLoop bool) []wfcel.CELNamespace {
	var loopOutputs map[string]interface{}
	if inLoop {
		loopOutputs = map[string]interface{}{}
	}
	var ctx wfcel.CELEvalContext
	switch site {
	case siteCondition:
		ctx = &wfcel.EdgeEvalContext{Outputs: loopOutputs}
	case siteLoopWhile:
		// Always in-loop: `while` sees the iteration that just finished.
		ctx = &wfcel.LoopEvalContext{Outputs: map[string]interface{}{}}
	case siteLoopKey:
		// A loop's `key` is evaluated per item, before that iteration runs:
		// no outputs.
		ctx = &wfcel.LoopEvalContext{}
	case siteSaveMessage:
		ctx = &wfcel.PostActivityContext{}
	default:
		ctx = &wfcel.NodeResolutionContext{Outputs: loopOutputs}
	}
	return ctx.Namespaces()
}

// iterScope describes what `iter` holds at a site.
type iterScope int

const (
	// iterNone: not inside a loop. The runtime binds {iteration: 0, index: 0}
	// (wfcel.EnsureNamespaceDefaults), so iter.item / iter.key do not exist.
	iterNone iterScope = iota
	// iterCounter: inside a sequential loop without items — iteration/index.
	iterCounter
	// iterItems: inside an items loop — iteration/index/item/key.
	iterItems
)

// loopFrame is the loop an expression is nested in.
type loopFrame struct {
	iter       iterScope
	itemFields map[string]*FieldInfo // inferred iter.item schema, when known
	parallel   bool                  // parallel iterations never see previous outputs
}

// workflowScope is one graph (root, inline sub-workflow or loop body) plus
// the loop it runs in, if any.
type workflowScope struct {
	wf        *reliantv1.Workflow
	basePath  []string
	typeCtx   *WorkflowTypeContext
	schema    *SchemaTypeChecker
	nodeIDs   []string
	loop      *loopFrame // nil at top level and in non-loop inline workflows
	envCache  map[string]*cel.Env
	envFailed bool
	// root is set for the top-level workflow (not an inline body).
	root bool
}

func newWorkflowScope(wf *reliantv1.Workflow, basePath []string, typeCtx *WorkflowTypeContext, loop *loopFrame) *workflowScope {
	ids := make([]string, 0, len(wf.GetNodes()))
	for _, n := range wf.GetNodes() {
		ids = append(ids, n.GetId())
	}
	if typeCtx != nil {
		typeCtx.GuaranteedBefore = computeGuaranteedBefore(wf)
		typeCtx.GuaranteedAtCompletion = computeGuaranteedAtCompletion(wf, typeCtx.GuaranteedBefore)
		typeCtx.LoopOutputsAbsent = loop != nil && loop.parallel
		if loop != nil {
			typeCtx.InLoopBody = true
			// In a loop body `outputs` is this graph's own declared outputs
			// from the previous iteration.
			typeCtx.OutputFields = declaredOutputFieldNames(wf)
		}
	}
	return &workflowScope{
		wf: wf, basePath: basePath, typeCtx: typeCtx,
		schema: NewSchemaTypeCheckerFromProto(wf), nodeIDs: ids, loop: loop,
		envCache: map[string]*cel.Env{},
	}
}

func (s *workflowScope) inLoop() bool { return s.loop != nil }

// iterFor returns the iter binding for an expression evaluated in this
// graph. A loop's own config (items excepted) sees ITS iteration, so the
// caller passes that frame explicitly.
func (s *workflowScope) iterFor(frame *loopFrame) (iterScope, map[string]*FieldInfo) {
	if frame == nil {
		return iterNone, nil
	}
	return frame.iter, frame.itemFields
}

// env returns the validation environment for a site, cached per shape.
func (s *workflowScope) env(site celSite, inLoop bool, frame *loopFrame, outputNode *reliantv1.Node) (*cel.Env, *WorkflowTypeContext, error) {
	iter, itemFields := s.iterFor(frame)
	outputKey := ""
	if outputNode != nil {
		outputKey = outputNode.GetId()
	}
	key := fmt.Sprintf("%d/%t/%d/%p/%s", site, inLoop, iter, itemFields, outputKey)
	typeCtx := s.siteTypeContext(iter, itemFields, outputNode)
	namespaces := siteNamespaces(site, inLoop)
	if typeCtx != nil {
		typeCtx.OutputsUndeclared = !containsNamespace(namespaces, wfcel.CELOutputs)
	}
	if env, ok := s.envCache[key]; ok {
		return env, typeCtx, nil
	}
	env, err := newValidationCELEnv(namespaces, typeCtx)
	if err != nil {
		return nil, typeCtx, err
	}
	s.envCache[key] = env
	return env, typeCtx, nil
}

// siteTypeContext returns the type context for a site: the graph's, with
// iter typed for the site and `output` bound to the node being saved.
func (s *workflowScope) siteTypeContext(iter iterScope, itemFields map[string]*FieldInfo, outputNode *reliantv1.Node) *WorkflowTypeContext {
	if s.typeCtx == nil {
		return nil
	}
	c := *s.typeCtx
	c.IterScope = iter
	c.IterItemFields = itemFields
	c.CurrentNodeID = ""
	c.CurrentNodeOutputType = nil
	if outputNode != nil {
		// `output` is this node's result, typed exactly as nodes.<id> is
		// (newValidationCELEnv declares nodes_<id> the same way). For a
		// workflow/loop node that is its resolved declared outputs, so
		// `{{output.response_text}}` on a ref to builtin://structured-agent —
		// which declares only `response` and `completed` — is a compile
		// error instead of a run-time "no such key". An unresolved ref (no
		// loader) stays dyn.
		c.CurrentNodeID = outputNode.GetId()
		if !hasDynamicOutputs(&c, outputNode.GetId()) {
			c.CurrentNodeOutputType = getNodeOutputCELType(c.Registry, outputNode.GetType(), outputNode.GetId())
		}
	}
	return &c
}

// =============================================================================
// WORKFLOW WALK
// =============================================================================

// validateWorkflowScope validates every CEL expression in one graph, then
// recurses into inline sub-workflows and loop bodies.
func validateWorkflowScope(s *workflowScope, result *Result) {
	if s.typeCtx == nil {
		return
	}
	for i, node := range s.wf.GetNodes() {
		nodePath := append(append([]string{}, s.basePath...), "nodes", fmt.Sprintf("[%d](%s)", i, node.GetId()))
		s.validateNode(node, nodePath, result)
	}

	for i, edge := range s.wf.GetEdges() {
		for j, c := range edge.GetCases() {
			if c.GetCondition() == "" {
				continue
			}
			path := append(append([]string{}, s.basePath...), "edges", fmt.Sprintf("[%d]", i), "cases", fmt.Sprintf("[%d]", j), "condition")
			// Edge conditions evaluate after the source node completes.
			scope := &nodeOrderScope{nodeID: edge.GetFrom(), afterNode: true}
			s.validateRawCondition(c.GetCondition(), path, scope, result)
		}
	}

	validateSwitchCaseConditions(s.wf, s.basePath, result)
	validateAllJoinReachability(s.wf, s.basePath, result)
	s.validateDeclaredOutputs(result)
}

// validateNode validates one node's condition, config, inject, loop fields
// and save_message, then its inline children.
func (s *workflowScope) validateNode(node *reliantv1.Node, nodePath []string, result *Result) {
	nodeID := node.GetId()
	condition := ""
	if node.GetType() != model.NodeTypeJoin {
		condition = model.ConditionExpr(node)
	}

	// Everything else on the node evaluates only if the condition was true,
	// so what the condition proves protects them.
	before := (&nodeOrderScope{nodeID: nodeID}).withGate(condition)
	after := (&nodeOrderScope{nodeID: nodeID, afterNode: true, outputNode: nodeID}).withGate(condition)

	if condition != "" {
		s.validateRawCondition(condition, append(append([]string{}, nodePath...), "condition"), &nodeOrderScope{nodeID: nodeID}, result)
	}

	if sm := node.GetSaveMessage(); sm != nil {
		s.validateSaveMessage(node, sm, append(append([]string{}, nodePath...), saveMessageFieldKey), after, result)
	}

	if thread := model.NodeThreadConfig(node); thread != nil {
		if inject := thread.GetInject(); inject != nil {
			frame := s.loop
			if node.GetLoop() != nil {
				frame = loopFrameFor(node, s)
			}
			for _, f := range []struct {
				name  string
				value *reliantv1.CelString
			}{{"role", inject.GetRole()}, {"content", inject.GetContent()}, {"display_style", inject.GetDisplayStyle()}} {
				if model.CelStringIsSet(f.value) {
					s.validateTemplate(celString(f.value), append(append([]string{}, nodePath...), "thread", "inject", f.name), siteNodeResolution, frame, before, nil, result)
				}
			}
		}
	}

	s.validateNodeFields(node, nodePath, before, result)
}

// loopFrameFor returns the frame a loop node's iterations run in.
func loopFrameFor(node *reliantv1.Node, s *workflowScope) *loopFrame {
	args := node.GetLoop()
	frame := &loopFrame{iter: iterCounter, parallel: model.CelBoolValue(args.GetParallel()) || model.CelBoolIsExpr(args.GetParallel())}
	if model.CelStringIsSet(args.GetItems()) {
		frame.iter = iterItems
		frame.itemFields = inferLoopItemFields(args, s.wf, s.typeCtx)
	}
	return frame
}

// validateNodeFields validates the node-type-specific template fields.
func (s *workflowScope) validateNodeFields(node *reliantv1.Node, basePath []string, scope *nodeOrderScope, result *Result) {
	tmpl := func(raw string, fieldPath []string, frame *loopFrame) {
		if raw != "" && containsTemplate(raw) {
			s.validateTemplate(raw, fieldPath, siteNodeResolution, frame, scope, nil, result)
		}
	}
	at := func(parts ...string) []string { return append(append([]string{}, basePath...), parts...) }

	switch {
	case node.GetCallLlm() != nil:
		args := node.GetCallLlm()
		tmpl(celString(args.GetSystemPrompt()), at("system_prompt"), s.loop)
		tmpl(celString(args.GetThinkingLevel()), at("thinking_level"), s.loop)
		for i, msg := range args.GetMessages() {
			tmpl(msg.GetContent(), at("messages", fmt.Sprintf("[%d]", i), "content"), s.loop)
			tmpl(msg.GetRole(), at("messages", fmt.Sprintf("[%d]", i), "role"), s.loop)
		}

	case node.GetExecuteTools() != nil:
		tmpl(celString(node.GetExecuteTools().GetToolCalls()), at("tool_calls"), s.loop)

	case node.GetRun() != nil:
		args := node.GetRun()
		tmpl(celString(args.GetCommand()), at("command"), s.loop)
		tmpl(celString(args.GetWorkDir()), at("work_dir"), s.loop)

	case node.GetRouter() != nil:
		tmpl(celString(node.GetRouter().GetSystemPrompt()), at("system_prompt"), s.loop)

	case node.GetWorkflow() != nil:
		args := node.GetWorkflow()
		tmpl(celString(args.GetRef()), at("ref"), s.loop)
		for _, key := range sortedValueKeys(args.GetArgs()) {
			tmpl(args.GetArgs()[key].GetStringValue(), at("args", key), s.loop)
		}
		if inline := args.GetInline(); inline != nil {
			// An inline sub-workflow runs in the same loop iteration (if any)
			// as its parent node, with its own node namespace.
			child := newWorkflowScope(inline, at("inline"), inlineTypeContext(inline), s.loop)
			validateWorkflowScope(child, result)
		}

	case node.GetLoop() != nil:
		args := node.GetLoop()
		frame := loopFrameFor(node, s)
		tmpl(celString(args.GetRef()), at("ref"), s.loop)
		// items is evaluated once, before any iteration: the ENCLOSING
		// loop's iter (none at top level), parent-scope nodes.
		tmpl(celString(args.GetItems()), at("items"), s.loop)
		// key is evaluated per item (LoopEvalContext, this loop's iter);
		// args per iteration (node resolution, this loop's iter).
		if k := args.GetKey(); k != "" && containsTemplate(k) {
			s.validateTemplate(k, at("key"), siteLoopKey, frame, scope, nil, result)
		}
		for _, key := range sortedValueKeys(args.GetArgs()) {
			tmpl(args.GetArgs()[key].GetStringValue(), at("args", key), frame)
		}
		if model.DirectCelIsSet(args.GetWhile()) {
			whilePath := at("while")
			expr := model.DirectCelExpr(args.GetWhile())
			if !rejectTemplateDelimitersInRawCEL(expr, whilePath, result) {
				validateLoopWhileCondition(args, whilePath, result)
				s.validateWhile(expr, whilePath, node, frame, scope, result)
			}
		}
		if inline := args.GetInline(); inline != nil {
			child := newWorkflowScope(inline, at("inline"), inlineTypeContext(inline), frame)
			validateWorkflowScope(child, result)
		}

	case node.GetSaveMessageNode() != nil:
		args := node.GetSaveMessageNode()
		for _, f := range []struct {
			name  string
			value *reliantv1.CelString
		}{
			{"role", args.GetRole()}, {"content", args.GetContent()}, {"tool_calls", args.GetToolCalls()},
			{"tool_results", args.GetToolResults()}, {"attachments", args.GetAttachments()}, {"display_style", args.GetDisplayStyle()},
		} {
			tmpl(celString(f.value), at(f.name), s.loop)
		}
	}
}

// inlineTypeContext builds the type context for an inline graph. Inputs are
// lenient: the parent supplies them dynamically via args.
func inlineTypeContext(wf *reliantv1.Workflow) *WorkflowTypeContext {
	typeCtx := BuildWorkflowTypeContext(wf, nil)
	if typeCtx != nil {
		typeCtx.LenientInputs = true
	}
	return typeCtx
}

// =============================================================================
// EXPRESSION CHECKS
// =============================================================================

// validateTemplate validates every {{ }} expression in a template field.
func (s *workflowScope) validateTemplate(input string, path []string, site celSite, frame *loopFrame, scope *nodeOrderScope, outputNode *reliantv1.Node, result *Result) {
	env, typeCtx, err := s.env(site, s.inLoop(), frame, outputNode)
	if err != nil {
		s.reportEnvError(err, result)
		return
	}
	for _, match := range extractTemplateExpressions(input) {
		if match.expr == "" {
			continue
		}
		s.validateExpression(match.expr, path, env, typeCtx, scope, result)
	}
}

// validateExpression runs every per-expression check on one raw CEL string.
func (s *workflowScope) validateExpression(expr string, path []string, env *cel.Env, typeCtx *WorkflowTypeContext, scope *nodeOrderScope, result *Result) {
	validateInputPropertyAccess(expr, path, typeCtx, result)
	validateResponseDataAccessFromExpr(expr, path, typeCtx, result)
	if scope != nil && scope.outputNode != "" {
		// `output` in a save_message is this node's result: check
		// output.response_data.<tool>.<field> exactly like the
		// nodes.<id>.response_data form.
		validateResponseDataAccessFromExpr(aliasOutputAsNode(expr, scope.outputNode), path, typeCtx, result)
	}
	validateCELExpressionWithCompilationAndSchema(expr, rewriteNodesAccess(expr, s.nodeIDs), path, env, s.schema, typeCtx, result)
	validateGuardedAccess(expr, path, scope, typeCtx, result)
	validateLoopOutputsAccess(expr, path, scope, typeCtx, result)
}

// validateRawCondition validates a node or edge condition: raw CEL, the
// EdgeEvalContext environment, bool result.
func (s *workflowScope) validateRawCondition(expr string, path []string, scope *nodeOrderScope, result *Result) {
	if rejectTemplateDelimitersInRawCEL(expr, path, result) {
		return
	}
	env, typeCtx, err := s.env(siteCondition, s.inLoop(), s.loop, nil)
	if err != nil {
		s.reportEnvError(err, result)
		return
	}
	s.validateExpression(expr, path, env, typeCtx, scope, result)
	if compiled, issues := env.Compile(rewriteNodesAccess(expr, s.nodeIDs)); compiled != nil && (issues == nil || issues.Err() == nil) {
		validateConditionReturnType(compiled, expr, path, result)
	}
}

// validateWhile compiles a loop's `while` in the LoopEvalContext
// environment. It evaluates between iterations, in the PARENT graph: nodes
// are the parent's (never the body's), iter and outputs are this loop's.
func (s *workflowScope) validateWhile(expr string, path []string, loopNode *reliantv1.Node, frame *loopFrame, scope *nodeOrderScope, result *Result) {
	env, typeCtx, err := s.env(siteLoopWhile, true, frame, nil)
	if err != nil {
		s.reportEnvError(err, result)
		return
	}
	if inline := loopNode.GetLoop().GetInline(); inline != nil && typeCtx != nil {
		// outputs.* in `while` is this loop's declared outputs.
		c := *typeCtx
		c.OutputFields = declaredOutputFieldNames(inline)
		typeCtx = &c
		if env, err = newValidationCELEnv(siteNamespaces(siteLoopWhile, true), typeCtx); err != nil {
			s.reportEnvError(err, result)
			return
		}
		rejectBodyNodeRefs(expr, path, inline, s.nodeIDs, result)
	}
	// Evaluated after an iteration completes, so outputs.* is always set:
	// the loop-outputs absence rule does not apply here.
	whileScope := *scope
	whileScope.loopOutputsBound = true
	s.validateExpression(expr, path, env, typeCtx, &whileScope, result)
	if compiled, issues := env.Compile(rewriteNodesAccess(expr, s.nodeIDs)); compiled != nil && (issues == nil || issues.Err() == nil) {
		validateConditionReturnType(compiled, expr, path, result)
	}
}

// validateSaveMessage validates a node's save_message in the
// PostActivityContext environment, with `output` typed as this node's result.
func (s *workflowScope) validateSaveMessage(node *reliantv1.Node, sm *reliantv1.SaveMessageConfig, basePath []string, scope *nodeOrderScope, result *Result) {
	env, typeCtx, err := s.env(siteSaveMessage, s.inLoop(), s.loop, node)
	if err != nil {
		s.reportEnvError(err, result)
		return
	}
	fields := map[string]string{
		"role":          model.CelStringRaw(sm.GetRole()),
		"content":       model.CelStringRaw(sm.GetContent()),
		"tool_calls":    model.CelStringRaw(sm.GetToolCalls()),
		"tool_results":  model.CelStringRaw(sm.GetToolResults()),
		"attachments":   model.CelStringRaw(sm.GetAttachments()),
		"display_style": model.CelStringRaw(sm.GetDisplayStyle()),
	}
	condition := model.DirectCelExpr(sm.GetCondition())
	rejectSaveMessageNodesRefs(fields, condition, basePath, result)

	if condition != "" {
		condPath := append(append([]string{}, basePath...), "condition")
		if !rejectTemplateDelimitersInRawCEL(condition, condPath, result) && !referencesNodes(condition) {
			s.validateExpression(condition, condPath, env, typeCtx, scope, result)
			if compiled, issues := env.Compile(condition); compiled != nil && (issues == nil || issues.Err() == nil) {
				validateConditionReturnType(compiled, condition, condPath, result)
			}
		}
	}

	// The condition gates every field.
	fieldScope := scope.withGate(condition)
	for _, fieldName := range []string{"role", "content", "tool_calls", "tool_results", "attachments", "display_style"} {
		value := fields[fieldName]
		if value == "" {
			continue
		}
		fieldPath := append(append([]string{}, basePath...), fieldName)
		if containsTemplate(value) {
			for _, match := range extractTemplateExpressions(value) {
				if match.expr == "" || referencesNodes(match.expr) {
					continue // reported by rejectSaveMessageNodesRefs
				}
				s.validateExpression(match.expr, fieldPath, env, typeCtx, fieldScope, result)
			}
			validateSaveMessageFieldReturnType(fieldName, value, fieldPath, env, s.nodeIDs, result)
		} else if isSaveMessageListField(fieldName) {
			result.Add(&Error{
				Severity: SeverityError,
				Category: CategoryCELSemantic,
				Path:     fieldPath,
				Message:  fmt.Sprintf("save_message field '%s' expects a list type and must use a CEL expression (e.g., {{output.%s}}), not static text", fieldName, fieldName),
			})
		}
	}
}

// validateDeclaredOutputs validates the graph's `outputs:` block. Outputs
// evaluate when the graph completes, in the NodeResolutionContext
// environment, and their inferred types feed parent-side typing.
func (s *workflowScope) validateDeclaredOutputs(result *Result) {
	outputs := s.wf.GetOutputs()
	if len(outputs) == 0 {
		return
	}
	env, typeCtx, err := s.env(siteNodeResolution, s.inLoop(), s.loop, nil)
	if err != nil {
		s.reportEnvError(err, result)
		return
	}
	rewritten := make(map[string]string, len(outputs))
	for _, name := range sortedStringKeys(outputs) {
		raw := outputs[name]
		expr := unwrapTemplate(raw)
		path := append(append([]string{}, s.basePath...), "outputs", name)
		scope := &nodeOrderScope{completion: true}
		validateInputPropertyAccess(raw, path, typeCtx, result)
		validateResponseDataAccessFromExpr(raw, path, typeCtx, result)
		rewritten[name] = rewriteNodesAccess(expr, s.nodeIDs)
		validateCELExpressionWithCompilationAndSchema(raw, rewritten[name], path, env, s.schema, typeCtx, result)
		if rescuedByTypedZero(raw, typeCtx) {
			// The runtime substitutes an empty container here; see
			// rescuedByTypedZero. Nothing can fail.
		} else {
			validateGuardedAccess(expr, path, scope, typeCtx, result)
			validateLoopOutputsAccess(expr, path, scope, typeCtx, result)
		}
		if compiled, issues := env.Compile(rewritten[name]); compiled != nil && (issues == nil || issues.Err() == nil) {
			validateOutputNotAlwaysNull(compiled, raw, path, result)
		}
	}

	inferred, inferErrors := inferOutputTypes(rewritten, env)
	for _, name := range sortedErrKeys(inferErrors) {
		if msg := inferErrors[name].Error(); strings.Contains(msg, "found no matching overload") {
			result.Add(&Error{
				Severity: SeverityError,
				Category: CategoryCELSemantic,
				Path:     append(append([]string{}, s.basePath...), "outputs", name),
				Message:  msg,
			})
		}
	}
	// Root workflow only, as before: an inline body's outputs are typed at
	// the parent's read site instead.
	for _, name := range sortedFieldKeys(inferred) {
		if fi := inferred[name]; s.root && fi != nil && fi.IsDynamic {
			result.Add(&Error{
				Severity:   SeverityWarning,
				Category:   CategoryCELSemantic,
				Path:       append(append([]string{}, s.basePath...), "outputs", name),
				Message:    "output expression has dynamic type (dyn) - type cannot be validated at compile time",
				Suggestion: "ensure the expression references a known field or type to enable type validation",
			})
		}
	}
	if len(inferred) > 0 {
		s.typeCtx.OutputFields = inferred
	}
}

func (s *workflowScope) reportEnvError(err error, result *Result) {
	if s.envFailed {
		return
	}
	s.envFailed = true
	result.Add(&Error{
		Severity: SeverityError,
		Category: CategoryCELSemantic,
		Path:     append([]string{}, s.basePath...),
		Message:  fmt.Sprintf("failed to create CEL environment: %v", err),
	})
}

// =============================================================================
// SMALL HELPERS
// =============================================================================

// aliasOutputAsNode rewrites `output.` to `nodes.<id>.` so save_message reads
// of this node's result reuse the nodes.<id>.… response_data checks.
func aliasOutputAsNode(expr, nodeID string) string {
	return outputIdentPattern.ReplaceAllString(expr, "${1}nodes."+nodeID+".")
}

func referencesNodes(expr string) bool {
	refs, err := wfcel.ReferencesOf([]string{expr}, wfcel.CELNodes)
	return err == nil && refs.Referenced
}

func unwrapTemplate(raw string) string {
	expr := strings.TrimSpace(raw)
	if strings.HasPrefix(expr, "{{") && strings.HasSuffix(expr, "}}") {
		expr = strings.TrimSpace(expr[2 : len(expr)-2])
	}
	return expr
}

// declaredOutputFieldNames types a loop body's declared outputs as dyn by
// name, so `outputs.<typo>` in `while` is caught.
func declaredOutputFieldNames(inline *reliantv1.Workflow) map[string]*FieldInfo {
	out := make(map[string]*FieldInfo, len(inline.GetOutputs()))
	for name := range inline.GetOutputs() {
		out[name] = &FieldInfo{Name: name, IsDynamic: true}
	}
	return out
}

// rejectBodyNodeRefs reports `while` reads of the loop BODY's nodes. `while`
// evaluates in the parent graph, whose nodes map never contains body nodes —
// body results reach `while` only through the body's declared outputs.
func rejectBodyNodeRefs(expr string, path []string, inline *reliantv1.Workflow, parentIDs []string, result *Result) {
	parent := make(map[string]bool, len(parentIDs))
	for _, id := range parentIDs {
		parent[id] = true
	}
	for _, m := range nodeFieldRegex.FindAllStringSubmatch(expr, -1) {
		id := m[1]
		if parent[id] {
			continue
		}
		for _, n := range inline.GetNodes() {
			if n.GetId() == id {
				result.Add(&Error{
					Severity:   SeverityError,
					Category:   CategoryCELSemantic,
					Path:       append([]string{}, path...),
					Message:    fmt.Sprintf("`while` reads nodes.%s, a node of the loop body; `while` evaluates in the parent graph and sees only the parent's nodes", id),
					Suggestion: fmt.Sprintf("expose the value as a declared output of the loop body and read outputs.<name> instead of nodes.%s", id),
				})
				return
			}
		}
	}
}

func sortedStringKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

func sortedErrKeys(m map[string]error) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

func sortedFieldKeys(m map[string]*FieldInfo) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func containsNamespace(list []wfcel.CELNamespace, ns wfcel.CELNamespace) bool {
	for _, n := range list {
		if n == ns {
			return true
		}
	}
	return false
}
