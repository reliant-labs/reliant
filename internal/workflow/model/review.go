package model

import (
	"fmt"
	"math"
	"slices"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

// A `type: review` node runs a reviewer agent whose verdict a server action
// may trust (redesign §5, "The author cannot approve itself"). What makes it
// trustworthy is owned by the engine, not by the workflow that declares it:
//
//   - a fresh session: every execution of the node runs the agent in a thread
//     of its own (mode new, ambient memory excluded; the runtime keys it on
//     the execution, runtime.reviewSession), so it carries nothing another
//     node, or an earlier round of this one, wrote;
//   - read-only tools: ReviewerTools, enforced at every model call in the
//     session (ClampReviewerTools), with nothing loadable and no spawn;
//   - verdict-only output: BuildReviewOutput checks the reviewer's response
//     against a fixed shape and fails the node on anything else, then records
//     the session and the role, which the model never writes.
//
// The node is not a second agent runtime. LowerReviewNodes compiles it, when a
// definition is loaded to run, into a `type: workflow` node that runs the
// engine-owned ReviewAgentRef in that locked-down shape; the inline executor
// recognises the lowered node (IsLoweredReviewNode) and does the two things a
// workflow node does not: it marks the session a reviewer's, and it turns the
// child's response into a ReviewOutput.

// Node roles: Node.role. A closed set.
const (
	RoleReviewer = "reviewer"
)

// ValidRoles lists every value Node.role accepts.
var ValidRoles = []string{RoleReviewer}

// IsValidRole reports whether s is a declarable node role.
func IsValidRole(s string) bool { return slices.Contains(ValidRoles, s) }

// ReviewAgentRef is the engine-owned workflow a review node runs. It declares
// no tools input: its tool set is fixed, and ClampReviewerTools enforces it
// whatever the definition says.
const ReviewAgentRef = "builtin://review-agent"

// ReviewResponseTool is the structured-output tool the reviewer submits its
// verdict with.
const ReviewResponseTool = "submit_verdict"

// Review verdicts, finding severities and risk tiers (ReviewOutput).
const (
	ReviewVerdictPass    = "pass"
	ReviewVerdictChanges = "changes"
	ReviewVerdictClose   = "close"

	ReviewSeverityBlocker = "blocker"
	ReviewSeverityNit     = "nit"

	ReviewRiskLow    = "low"
	ReviewRiskMedium = "medium"
	ReviewRiskHigh   = "high"
)

var (
	reviewVerdicts   = []string{ReviewVerdictPass, ReviewVerdictChanges, ReviewVerdictClose}
	reviewSeverities = []string{ReviewSeverityBlocker, ReviewSeverityNit}
	reviewRiskTiers  = []string{ReviewRiskLow, ReviewRiskMedium, ReviewRiskHigh}
)

// ReviewerTools is every tool a reviewer may be offered: reading files and
// code, reading skills, and read-only GitHub reads (each `mutates: false` in
// the GitHub manifest). No shell (search goes through it, and so can any
// write), no file writes, no git publish, no PR or comment writes, no spawn,
// no network fetch. An exact allowlist, never tags or globs: a tag gains
// members when a tool is added to it, and the reviewer's set must not.
var ReviewerTools = []string{
	"view",
	"code_context",
	"skill",
	"github__pr_get",
	"github__pr_list_files",
	"github__pr_reviews_list",
	"github__pr_review_comments_list",
	"github__issue_get",
	"github__issue_comment_list",
	"github__repo_get",
	"github__repo_get_content",
	"github__repo_get_tree",
	"github__branch_get",
	"github__code_search",
	"github__compare",
	"github__check_run_list",
	"github__commit_statuses",
}

// IsReviewerTool reports whether a reviewer may be offered the named tool.
func IsReviewerTool(name string) bool { return slices.Contains(ReviewerTools, name) }

// BuiltinPromptScheme prefixes a review prompt that names a prompt file the
// engine ships (builtin://pipeline/review.md) rather than inline text.
const BuiltinPromptScheme = "builtin://"

// IsBuiltinPromptRef reports whether a literal review prompt names a shipped
// prompt file.
func IsBuiltinPromptRef(prompt string) bool {
	return strings.HasPrefix(strings.TrimSpace(prompt), BuiltinPromptScheme)
}

// PromptResolver returns the text of a builtin prompt file, by its name
// without the scheme ("pipeline/review.md").
type PromptResolver func(name string) (string, error)

// LowerReviewNodes returns wf with every review node, at any depth (inline
// sub-workflows and loop bodies, on_failure, finally), replaced by the
// workflow node that runs it (LowerReviewNode). It also clears a role on any
// other node: a role is only ever the engine's statement, and a definition
// that reached the runtime with one elsewhere (it fails validation) must not
// pass it on. wf itself is not modified; a definition with no review node and
// no role comes back as is.
func LowerReviewNodes(wf *reliantv1.Workflow, prompts PromptResolver) (*reliantv1.Workflow, error) {
	if wf == nil || !workflowNeedsLowering(wf) {
		return wf, nil
	}
	out, ok := proto.Clone(wf).(*reliantv1.Workflow)
	if !ok {
		return nil, fmt.Errorf("clone workflow %q", wf.GetName())
	}
	if err := lowerWorkflowInPlace(out, prompts); err != nil {
		return nil, err
	}
	return out, nil
}

func workflowNeedsLowering(wf *reliantv1.Workflow) bool {
	if wf == nil {
		return false
	}
	for _, n := range wf.GetNodes() {
		if n.GetType() == NodeTypeReview || (n.GetRole() != "" && !IsLoweredReviewNode(n)) {
			return true
		}
		if workflowNeedsLowering(NodeInlineWorkflow(n)) {
			return true
		}
	}
	return workflowNeedsLowering(wf.GetOnFailure()) || workflowNeedsLowering(wf.GetFinally())
}

func lowerWorkflowInPlace(wf *reliantv1.Workflow, prompts PromptResolver) error {
	if wf == nil {
		return nil
	}
	for i, n := range wf.GetNodes() {
		switch {
		case n.GetType() == NodeTypeReview:
			lowered, err := LowerReviewNode(n, prompts)
			if err != nil {
				return err
			}
			wf.Nodes[i] = lowered
			continue
		case n.GetRole() != "" && !IsLoweredReviewNode(n):
			n.Role = ""
		}
		if err := lowerWorkflowInPlace(NodeInlineWorkflow(n), prompts); err != nil {
			return err
		}
	}
	if err := lowerWorkflowInPlace(wf.GetOnFailure(), prompts); err != nil {
		return err
	}
	return lowerWorkflowInPlace(wf.GetFinally(), prompts)
}

// LowerReviewNode compiles one review node into the `type: workflow` node
// that runs it: ReviewAgentRef, in a new thread that is never memoized and
// excludes ambient memory (the runtime gives each execution its own,
// runtime.reviewSession), with the prompt (a builtin prompt resolved to its
// text), the with data, the model and max_turns as the child's inputs. The
// node keeps its id, its role and every base field (condition, timeout,
// daemon, outcome, tolerate_failure, observe, save_message), so it routes,
// times out and is observed exactly as the review node would be.
func LowerReviewNode(n *reliantv1.Node, prompts PromptResolver) (*reliantv1.Node, error) {
	args := n.GetReview()
	if args == nil {
		return nil, fmt.Errorf("review node %q has no review args", n.GetId())
	}
	if n.GetRole() != RoleReviewer {
		return nil, fmt.Errorf("review node %q must declare role: %s", n.GetId(), RoleReviewer)
	}

	prompt, err := reviewPromptValue(n.GetId(), args.GetPrompt(), prompts)
	if err != nil {
		return nil, err
	}
	with := &structpb.Struct{Fields: map[string]*structpb.Value{}}
	for k, v := range args.GetWith() {
		with.Fields[k] = v
	}
	childArgs := map[string]*structpb.Value{
		"prompt": prompt,
		"with":   structpb.NewStructValue(with),
	}
	if m := args.GetModel(); CelModelSelectorIsSet(m) {
		v, err := modelSelectorValue(m)
		if err != nil {
			return nil, fmt.Errorf("review node %q: model: %w", n.GetId(), err)
		}
		childArgs["model"] = v
	}
	if mt := args.GetMaxTurns(); CelIntIsSet(mt) {
		if CelIntIsExpr(mt) {
			childArgs["max_turns"] = structpb.NewStringValue(CelIntExpr(mt))
		} else {
			childArgs["max_turns"] = structpb.NewNumberValue(float64(CelIntValue(mt)))
		}
	}

	lowered, ok := proto.Clone(n).(*reliantv1.Node)
	if !ok {
		return nil, fmt.Errorf("clone review node %q", n.GetId())
	}
	lowered.Type = NodeTypeWorkflow
	lowered.Role = RoleReviewer
	lowered.Args = &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
		Ref: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: ReviewAgentRef}},
		Thread: &reliantv1.ThreadConfig{
			Mode:                 ThreadModeNew,
			Memo:                 &reliantv1.CelBool{Value: &reliantv1.CelBool_Literal{Literal: false}},
			ExcludeAmbientMemory: true,
		},
		Args:    childArgs,
		Project: args.GetProject(),
	}}
	return lowered, nil
}

func reviewPromptValue(nodeID string, prompt *reliantv1.CelString, prompts PromptResolver) (*structpb.Value, error) {
	if !CelStringIsSet(prompt) || strings.TrimSpace(CelStringRaw(prompt)) == "" {
		return nil, fmt.Errorf("review node %q requires a prompt", nodeID)
	}
	if CelStringIsExpr(prompt) {
		return structpb.NewStringValue(CelStringExpr(prompt)), nil
	}
	literal := CelStringValue(prompt)
	if !IsBuiltinPromptRef(literal) {
		return structpb.NewStringValue(literal), nil
	}
	name := strings.TrimPrefix(strings.TrimSpace(literal), BuiltinPromptScheme)
	if prompts == nil {
		return nil, fmt.Errorf("review node %q: prompt %s: no builtin prompts are available here", nodeID, literal)
	}
	text, err := prompts(name)
	if err != nil {
		return nil, fmt.Errorf("review node %q: prompt %s: %w", nodeID, literal, err)
	}
	return structpb.NewStringValue(text), nil
}

func modelSelectorValue(m *reliantv1.CelModelSelector) (*structpb.Value, error) {
	if CelModelSelectorIsExpr(m) {
		return structpb.NewStringValue(m.GetExpr()), nil
	}
	data, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(CelModelSelectorValue(m))
	if err != nil {
		return nil, err
	}
	s := &structpb.Struct{}
	if err := protojson.Unmarshal(data, s); err != nil {
		return nil, err
	}
	return structpb.NewStructValue(s), nil
}

// IsLoweredReviewNode reports whether n is a review node as LowerReviewNode
// compiled it: a workflow node with role: reviewer that runs ReviewAgentRef in
// a new thread. Nothing else gets the reviewer session, and only this shape
// keeps its role through LowerReviewNodes.
func IsLoweredReviewNode(n *reliantv1.Node) bool {
	if n == nil || n.GetType() != NodeTypeWorkflow || n.GetRole() != RoleReviewer {
		return false
	}
	args := n.GetWorkflow()
	return args != nil && args.GetInline() == nil &&
		CelStringValue(args.GetRef()) == ReviewAgentRef &&
		args.GetThread().GetMode() == ThreadModeNew
}

// ClampReviewerTools narrows an evaluated call_llm node to what a reviewer
// may be offered: the preloaded tools it names that are in ReviewerTools,
// nothing loadable, no spawn, and parameter bindings only for tools it keeps.
// The engine applies it to every call_llm in a reviewer session, so the
// allowlist holds whatever the definition it runs declares. The response tool
// is not a registry tool and is untouched. A node with no tools_config is
// offered no tools and is left alone.
func ClampReviewerTools(evaluated *reliantv1.Node) {
	tc := evaluated.GetCallLlm().GetToolsConfig()
	if tc == nil {
		return
	}
	var kept []string
	for _, name := range CelStringListValue(tc.GetPreloadedTools()) {
		if IsReviewerTool(name) && !slices.Contains(kept, name) {
			kept = append(kept, name)
		}
	}
	empty := func() *reliantv1.CelStringList {
		return &reliantv1.CelStringList{Value: &reliantv1.CelStringList_Literal{Literal: &reliantv1.StringList{}}}
	}
	tc.PreloadedTools = &reliantv1.CelStringList{Value: &reliantv1.CelStringList_Literal{Literal: &reliantv1.StringList{Values: kept}}}
	tc.LoadableTools = empty()
	tc.Spawn = empty()
	for name := range tc.GetTools() {
		if !slices.Contains(kept, name) {
			delete(tc.Tools, name)
		}
	}
}

// BuildReviewOutput turns the reviewer's response tool payload into a review
// node's output (ReviewOutput): the verdict, findings, risk tier and summary,
// checked against a fixed shape, plus the session it ran in and the role,
// which come from the engine and never from the model. A missing response (the
// reviewer stopped without submitting one) or any deviation from the shape is
// an error, and fails the node.
//
// The shape: verdict is pass, changes or close; findings is a list (possibly
// empty) of {id, severity, path?, line?, body}, ids unique and non-empty,
// severity blocker or nit, line a positive integer; risk_tier is optional, low,
// medium or high; summary is non-empty. No other key is accepted at either
// level. A pass carries no blocker, and changes carries at least one: a
// verdict that contradicts its own findings is not one a server can act on.
func BuildReviewOutput(response interface{}, sessionID string) (map[string]interface{}, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("the reviewer's session is unknown")
	}
	resp, ok := response.(map[string]interface{})
	if !ok || resp == nil {
		return nil, fmt.Errorf("the reviewer did not submit a verdict (%s was never called)", ReviewResponseTool)
	}
	for key := range resp {
		switch key {
		case "verdict", "findings", "risk_tier", "summary":
		default:
			return nil, fmt.Errorf("verdict has unknown field %q", key)
		}
	}
	verdict, err := enumField(resp, "verdict", reviewVerdicts, true)
	if err != nil {
		return nil, err
	}
	risk, err := enumField(resp, "risk_tier", reviewRiskTiers, false)
	if err != nil {
		return nil, err
	}
	summary, ok := resp["summary"].(string)
	if !ok || strings.TrimSpace(summary) == "" {
		return nil, fmt.Errorf("summary is required and must be a non-empty string")
	}
	rawFindings, present := resp["findings"]
	if !present || rawFindings == nil {
		return nil, fmt.Errorf("findings is required (an empty list when there are none)")
	}
	list, ok := rawFindings.([]interface{})
	if !ok {
		return nil, fmt.Errorf("findings must be a list, got %T", rawFindings)
	}
	findings := make([]interface{}, 0, len(list))
	ids := map[string]bool{}
	blockers := 0
	for i, raw := range list {
		f, err := reviewFinding(i, raw)
		if err != nil {
			return nil, err
		}
		id := f["id"].(string)
		if ids[id] {
			return nil, fmt.Errorf("findings[%d]: id %q is not unique", i, id)
		}
		ids[id] = true
		if f["severity"] == ReviewSeverityBlocker {
			blockers++
		}
		findings = append(findings, f)
	}
	switch {
	case verdict == ReviewVerdictPass && blockers > 0:
		return nil, fmt.Errorf("a pass verdict cannot carry a blocker (%d found)", blockers)
	case verdict == ReviewVerdictChanges && blockers == 0:
		return nil, fmt.Errorf("a changes verdict must name at least one blocker")
	}
	return map[string]interface{}{
		"verdict":    verdict,
		"findings":   findings,
		"risk_tier":  risk,
		"summary":    summary,
		"session_id": sessionID,
		"role":       RoleReviewer,
	}, nil
}

func enumField(m map[string]interface{}, key string, allowed []string, required bool) (string, error) {
	raw, present := m[key]
	if !present || raw == nil {
		if required {
			return "", fmt.Errorf("%s is required (one of %s)", key, strings.Join(allowed, ", "))
		}
		return "", nil
	}
	s, ok := raw.(string)
	if !ok || !slices.Contains(allowed, s) {
		return "", fmt.Errorf("%s must be one of %s, got %v", key, strings.Join(allowed, ", "), raw)
	}
	return s, nil
}

func reviewFinding(i int, raw interface{}) (map[string]interface{}, error) {
	f, ok := raw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("findings[%d] must be an object, got %T", i, raw)
	}
	for key := range f {
		switch key {
		case "id", "severity", "path", "line", "body":
		default:
			return nil, fmt.Errorf("findings[%d] has unknown field %q", i, key)
		}
	}
	id, ok := f["id"].(string)
	if !ok || strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("findings[%d].id is required and must be a non-empty string", i)
	}
	severity, err := enumField(f, "severity", reviewSeverities, true)
	if err != nil {
		return nil, fmt.Errorf("findings[%d]: %w", i, err)
	}
	body, ok := f["body"].(string)
	if !ok || strings.TrimSpace(body) == "" {
		return nil, fmt.Errorf("findings[%d].body is required and must be a non-empty string", i)
	}
	path := ""
	if p, present := f["path"]; present && p != nil {
		if path, ok = p.(string); !ok {
			return nil, fmt.Errorf("findings[%d].path must be a string, got %T", i, p)
		}
	}
	line := int64(0)
	if l, present := f["line"]; present && l != nil {
		n, ok := wholeNumber(l)
		if !ok || n < 1 {
			return nil, fmt.Errorf("findings[%d].line must be a positive integer, got %v", i, l)
		}
		line = n
	}
	return map[string]interface{}{
		"id":       id,
		"severity": severity,
		"path":     path,
		"line":     line,
		"body":     body,
	}, nil
}

func wholeNumber(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		if n != math.Trunc(n) || n > math.MaxInt32 || n < math.MinInt32 {
			return 0, false
		}
		return int64(n), true
	default:
		return 0, false
	}
}
