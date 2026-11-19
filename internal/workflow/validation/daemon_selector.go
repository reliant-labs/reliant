// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// validateDaemonSelectors checks the shape of every literal `daemon:`
// selector: the workflow's own and every node's, inline bodies and the
// on_failure/finally handlers included.
//
// `{pool: <name>}` places the run (or the node) once on a member of the
// owner's machine pool, so it chooses the machine itself: it cannot also
// name one (id, name), filter by kind (type) or by labels. A labels-only
// selector places the same way over the machines whose durable labels
// contain every pair, so an empty key matches nothing anyone can set.
//
// Whether the named pool exists is the server's question (it needs the
// owner); a CEL selector (`daemon: "{{ ... }}"`) names nothing until the run
// resolves it, so neither is checked here.
func validateDaemonSelectors(wf *reliantv1.Workflow, result *Result) {
	validateDaemonSelector(wf.GetDaemon(), nil, result)

	walkBody := func(w *reliantv1.Workflow, base []string) {
		walkWorkflowNodes(w, base, func(node *reliantv1.Node, nodePath []string) {
			validateDaemonSelector(node.GetDaemon(), nodePath, result)
		})
	}
	walkBody(wf, []string{wf.GetName()})
	if h := wf.GetOnFailure(); h != nil {
		walkBody(h, []string{wf.GetName(), "on_failure"})
	}
	if h := wf.GetFinally(); h != nil {
		walkBody(h, []string{wf.GetName(), "finally"})
	}
}

func validateDaemonSelector(sel *reliantv1.CelDaemonSelector, path []string, result *Result) {
	ds := sel.GetLiteral()
	if ds == nil {
		return
	}
	if ds.GetPool() != "" {
		if strings.TrimSpace(ds.GetPool()) == "" {
			result.AddErrorWithSuggestion(CategoryStructure, path, "daemon.pool",
				"pool is blank",
				"name one of your machine pools: daemon: {pool: gate}")
		}
		var also []string
		for _, f := range []struct {
			name string
			set  bool
		}{
			{"id", ds.GetId() != ""},
			{"name", ds.GetName() != ""},
			{"type", ds.GetType() != ""},
			{"labels", len(ds.GetLabels()) > 0},
		} {
			if f.set {
				also = append(also, f.name)
			}
		}
		if len(also) > 0 {
			result.AddErrorWithSuggestion(CategoryStructure, path, "daemon.pool",
				fmt.Sprintf("pool cannot be combined with %s: the pool chooses the machine itself", strings.Join(also, ", ")),
				fmt.Sprintf("use daemon: {pool: %s} alone, or drop pool and select the machine by %s", ds.GetPool(), strings.Join(also, ", ")))
		}
		return
	}
	labelsOnly := ds.GetId() == "" && ds.GetName() == "" && ds.GetType() == ""
	if !labelsOnly {
		return
	}
	for k := range ds.GetLabels() {
		if strings.TrimSpace(k) == "" {
			result.AddErrorWithSuggestion(CategoryStructure, path, "daemon.labels",
				"a label key is blank, so no machine can carry it",
				"name the label, e.g. daemon: {labels: {role: gate}}")
			return
		}
	}
}
