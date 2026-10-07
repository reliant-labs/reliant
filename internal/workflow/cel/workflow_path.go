package wfcel

import (
	"errors"

	"github.com/google/cel-go/cel"
	celast "github.com/google/cel-go/common/ast"

	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// ErrWorkflowPathUnset is returned when an expression reads workflow.path in a
// run that has no project directory.
var ErrWorkflowPathUnset = errors.New("workflow.path is not set: this run has no project directory, " +
	"so there is no path to render. Test for it with has(workflow.path) where a run without one is expected")

// CheckWorkflowPathReference fails an evaluation that READS workflow.path when
// the activation's workflow.path is empty.
//
// workflow.path is the directory the run operates on. Every other workflow
// field can legitimately be empty; this one never can in a run that has a
// directory, and rendering it as "" in a run that does not is how builtin
// parallel-compete turned `rsync --delete <winner>/ "{{workflow.path}}/"` into
// a sync onto "/" (2026-10-06). So reading it then is an error — in a shell
// command, a sub-workflow's project.path, or a message telling the user where
// to `cd` — exactly as reading an absent map key is.
//
// "Reads" is decided by evaluation, not by the text: the expression is
// re-evaluated with state tracking and fails only if a workflow.path access
// actually produced a value. So has(workflow.path) (a test, not a read) and
// `has(workflow.path) ? workflow.path : 'none'` stay legal, the same way
// has()-guarded map access does. The extra evaluation happens only in the rare
// case the path is empty AND the expression mentions it.
func CheckWorkflowPathReference(env *cel.Env, checked *cel.Ast, activation map[string]interface{}) error {
	if checked == nil {
		return nil
	}
	if wc, ok := activation[string(CELWorkflow)].(*model.WorkflowContext); ok && wc != nil && wc.Path != "" {
		return nil
	}
	var reads []int64
	celast.PreOrderVisit(checked.NativeRep().Expr(), celast.NewExprVisitor(func(e celast.Expr) {
		if e.Kind() != celast.SelectKind {
			return
		}
		sel := e.AsSelect()
		if sel.IsTestOnly() || sel.FieldName() != "path" {
			return
		}
		if operand := sel.Operand(); operand.Kind() == celast.IdentKind && operand.AsIdent() == string(CELWorkflow) {
			reads = append(reads, e.ID())
		}
	}))
	if len(reads) == 0 {
		return nil
	}
	prg, err := env.Program(checked, cel.EvalOptions(cel.OptTrackState))
	if err != nil {
		return err
	}
	_, details, _ := prg.Eval(activation)
	if details == nil || details.State() == nil {
		// No way to tell which branches ran: refuse rather than guess.
		return ErrWorkflowPathUnset
	}
	for _, id := range reads {
		if _, evaluated := details.State().Value(id); evaluated {
			return ErrWorkflowPathUnset
		}
	}
	return nil
}
