// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// A Reliant server action runs on the worker, so a workflow of them neither
// needs nor wakes a machine, whether or not this build declares the name
// (an unknown one is refused at validation and at run time, never placed on
// a machine). An unresolvable integration action still counts as needing one.
func TestServerActionsNeverNeedAMachine(t *testing.T) {
	t.Parallel()
	args := func(uses string) *reliantv1.ActionArgs {
		return &reliantv1.ActionArgs{Uses: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: uses}}}
	}
	for _, uses := range []string{"reliant/pr.next@1", "reliant/nope.nope@1"} {
		if actionRequiresDaemon(args(uses)) {
			t.Errorf("preflight: %s must not require a daemon", uses)
		}
		if actionNeedsMachine(args(uses)) {
			t.Errorf("machine requirements: %s must not need a machine", uses)
		}
	}
	if !actionRequiresDaemon(args("nope/request@1")) || !actionNeedsMachine(args("nope/request@1")) {
		t.Error("an unresolvable integration action still counts as needing a machine")
	}
	if actionRequiresDaemon(args("http/request@1")) || actionNeedsMachine(args("http/request@1")) {
		t.Error("a server-placed integration action needs no machine")
	}
}
