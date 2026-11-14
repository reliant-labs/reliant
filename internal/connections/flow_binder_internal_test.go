// Copyright (c) 2025 Reliant Labs

package connections

import (
	"bytes"
	"testing"
)

// A release from before org connections finishes a flow only when its
// session binding is binderHash(userBinder(user)). An org flow is bound to
// the org too, so such a release (a rollback within a flow's lifetime)
// refuses it as "started by a different account" instead of storing the
// org's credential as the admin's personal connection. A personal flow keeps
// exactly the binding it always had, so either release finishes it.
func TestFlowBinderKeepsPersonalFlowsAndFencesOrgFlows(t *testing.T) {
	legacy := binderHash(userBinder("ada"))
	if !bytes.Equal(binderHash(flowBinder("ada", "")), legacy) {
		t.Fatal("a personal flow's binding changed: a flow started by one release would not finish on the other")
	}
	org := binderHash(flowBinder("ada", "org-1"))
	if bytes.Equal(org, legacy) {
		t.Fatal("an org flow is bound like a personal one: an older release would finish it as a personal connection")
	}
	if bytes.Equal(org, binderHash(flowBinder("ada", "org-2"))) || bytes.Equal(org, binderHash(flowBinder("olivia", "org-1"))) {
		t.Fatal("an org flow's binding must name both its user and its org")
	}
}
