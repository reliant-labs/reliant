// Copyright (c) 2025 Reliant Labs
package model

import (
	"fmt"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// MaxWorkflowTimeout is the longest run timeout a workflow may declare
// (Workflow.timeout). A week is past any real release soak, and short enough
// that a typo cannot declare a run unbounded in all but name.
const MaxWorkflowTimeout = 7 * 24 * time.Hour

// WorkflowTimeout reads a workflow's run timeout (Workflow.timeout). set is
// false when it declares none. A declared timeout that is not a literal Go
// duration in (0, MaxWorkflowTimeout] is an error: validation refuses it at
// load, and the runtime runs such a workflow unbounded rather than failing a
// run on a value an older validator let through.
func WorkflowTimeout(wf *reliantv1.Workflow) (timeout time.Duration, literal string, set bool, err error) {
	cs := wf.GetTimeout()
	if !CelStringIsSet(cs) {
		return 0, "", false, nil
	}
	if CelStringIsExpr(cs) {
		return 0, "", true, fmt.Errorf("a workflow's timeout must be a literal duration such as 3h, not an expression")
	}
	literal = strings.TrimSpace(CelStringValue(cs))
	timeout, err = time.ParseDuration(literal)
	if err != nil {
		return 0, literal, true, fmt.Errorf("invalid duration %q: %v", literal, err)
	}
	if timeout <= 0 {
		return 0, literal, true, fmt.Errorf("a workflow's timeout must be more than zero, not %q", literal)
	}
	if timeout > MaxWorkflowTimeout {
		return 0, literal, true, fmt.Errorf("a workflow's timeout may be at most %s, not %q", MaxWorkflowTimeout, literal)
	}
	return timeout, literal, true, nil
}
