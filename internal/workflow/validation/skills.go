// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"sort"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"google.golang.org/protobuf/types/known/structpb"
)

// SkillResolver reports the canonical path a workflow-declared skill name
// resolves to in the catalog the workflow will actually run against, and
// whether it resolves at all. It is the validation-time twin of the runtime
// lookup the skill tool performs, and both must be backed by
// skillscore.ResolveSkillPathIndex or the guard is worthless.
//
// Names is the full candidate set, used only to render "did you mean".
type SkillResolver struct {
	Resolve func(path string) bool
	Names   []string
}

// skillsFieldName is the name a preloaded-skill list carries at every hop.
//
// It is not a convention this check invents: `skills` is the proto field name
// on CallLLMArgs — the terminal consumer that seeds the list into the agent's
// context — and every forwarding hop (a workflow node's `args.skills`, the
// child's `inputs.skills`) is named to match it so the value can be threaded
// through unchanged. A hop that renamed the field would break the forwarding,
// not just this check.
const skillsFieldName = "skills"

// validateSkillReferences checks every STATICALLY KNOWN skill name a workflow
// declares against the resolver, so a charter naming a skill nothing resolves
// fails validation instead of failing silently at runtime.
//
// The run time cannot recover this, which is the reason this layer exists. A
// miss against a warm catalog is permanent — no retry conjures a skill that is
// not there — so the preloader (buildSeededSkillMessages) warns, tells the
// model in the seed which skills never arrived, and carries on. A charter that
// asks for "forge/service-layer" when the catalog surfaces it at
// "service-layer" therefore runs without the guidance it was written to depend
// on, on every single run. Only this layer turns that into a failure the author
// can fix, and only because it runs before the run does.
//
// Only literals are checked. A templated value ("{{inputs.skills}}") names
// nothing until the run binds it, so it is skipped rather than guessed at.
//
// The check is skipped entirely when no resolver is supplied — the same
// optional-capability shape as WorkflowLoader. The guard against that
// silently becoming a no-op is TestBuiltinWorkflowSkillsResolve, which
// supplies the real catalog and fails when the candidate set is empty.
func validateSkillReferences(wf *reliantv1.Workflow, opts *ValidationOptions, result *Result) {
	if opts == nil || opts.SkillResolver == nil || opts.SkillResolver.Resolve == nil {
		return
	}

	// A `skills` input's default names skills just as literally as a node arg,
	// and gets used whenever a caller does not override it.
	if input, ok := wf.GetInputs()[skillsFieldName]; ok {
		for _, s := range literalSkillsFromValue(input.GetArrayInput().GetDefault()) {
			checkSkillName(opts.SkillResolver, s, []string{"inputs", skillsFieldName}, "default", result)
		}
	}

	for i, node := range wf.GetNodes() {
		id := nodePathSegment(node, i)

		// call_llm.skills — the terminal consumer.
		if llm := node.GetCallLlm(); llm != nil {
			for _, s := range model.CelStringListValue(llm.GetSkills()) {
				checkSkillName(opts.SkillResolver, s, []string{"nodes", id, model.NodeTypeCallLLM}, skillsFieldName, result)
			}
		}

		// workflow/loop node args.skills — a forwarding hop. This is where a
		// charter spells the names it wants a phase to run with. Ordered, not
		// a map range, so error output is stable run to run.
		for _, carrier := range []struct {
			kind string
			args map[string]*structpb.Value
		}{
			{model.NodeTypeWorkflow, node.GetWorkflow().GetArgs()},
			{model.NodeTypeLoop, node.GetLoop().GetArgs()},
		} {
			for _, s := range literalSkillsFromValue(carrier.args[skillsFieldName]) {
				checkSkillName(opts.SkillResolver, s, []string{"nodes", id, carrier.kind, "args"}, skillsFieldName, result)
			}
		}
	}
}

// nodePathSegment identifies a node in an error path, falling back to the
// index when a node has no id (structural validation reports that separately).
func nodePathSegment(node *reliantv1.Node, i int) string {
	if id := node.GetId(); id != "" {
		return id
	}
	return fmt.Sprintf("[%d]", i)
}

// literalSkillsFromValue extracts the string entries of a skills list.
//
// A non-list value is never checked: `skills: "{{inputs.skills}}"` is the
// forwarding spelling every wrapper workflow uses, and it names nothing until
// the run binds it. Within a list, entries are returned as-is and filtered by
// checkSkillName, so a list mixing a template with literals still gets its
// literals checked rather than being abandoned wholesale.
func literalSkillsFromValue(v *structpb.Value) []string {
	list := v.GetListValue()
	if list == nil {
		return nil
	}
	out := make([]string, 0, len(list.GetValues()))
	for _, item := range list.GetValues() {
		out = append(out, item.GetStringValue())
	}
	return out
}

// checkSkillName records an error for a name the catalog cannot resolve,
// with a "did you mean" built from the candidate set.
func checkSkillName(r *SkillResolver, name string, path []string, field string, result *Result) {
	if strings.TrimSpace(name) == "" || strings.Contains(name, "{{") || r.Resolve(name) {
		return
	}
	msg := fmt.Sprintf("skill %q does not resolve to any available skill; it will be silently skipped at runtime and the guidance will never reach the agent", name)
	if s := suggestSkillName(r.Names, name); s != "" {
		result.AddErrorWithSuggestion(CategorySkillRef, path, field, msg, fmt.Sprintf("did you mean %q?", s))
		return
	}
	result.AddError(CategorySkillRef, path, field, msg)
}

// suggestSkillName finds the likeliest intended skill for an unresolvable
// name by matching the last path component. This catches the exact drift that
// motivated the check: a skill forge surfaces at its bare path ("service-layer")
// spelled with the synthetic namespace it does not carry ("forge/service-layer").
// Returns "" when the match is not unique.
func suggestSkillName(names []string, want string) string {
	leaf := want
	if i := strings.LastIndex(leaf, "/"); i >= 0 {
		leaf = leaf[i+1:]
	}
	leaf = strings.ToLower(leaf)

	var hits []string
	for _, n := range names {
		candidate := n
		if i := strings.LastIndex(candidate, "/"); i >= 0 {
			candidate = candidate[i+1:]
		}
		if strings.EqualFold(candidate, leaf) {
			hits = append(hits, n)
		}
	}
	if len(hits) != 1 {
		return ""
	}
	sort.Strings(hits)
	return hits[0]
}
