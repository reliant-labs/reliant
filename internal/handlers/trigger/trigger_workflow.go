// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
)

// leftEnabledError is a write that failed after it took effect, and whose
// revert failed too: the trigger is running, whatever the write's error says.
type leftEnabledError struct {
	cause, revert error
	// settings is an update whose previous settings could not be put back,
	// rather than an enable that could not be switched back off.
	settings bool
}

func (e *leftEnabledError) Error() string { return e.cause.Error() }
func (e *leftEnabledError) Unwrap() error { return e.cause }

func (e *leftEnabledError) message() string {
	if e.settings {
		return "Saving failed (" + connectMessage(e.cause) + ") and this trigger's previous settings could not be put back: " + connectMessage(e.revert) + " It is enabled; check its settings, or disable it from Automations if it should not run."
	}
	return "Enabling failed (" + connectMessage(e.cause) + ") but the trigger is enabled and could not be switched back off: " + connectMessage(e.revert) + " Disable it from Automations if it should not run."
}

// activationPrefix is the "<prefix> · <trigger>" prefix the new activations are
// named by: the request's (or the workflow ref), made free in this project.
// A name taken by an automation of ANOTHER workflow — a user workflow of the
// same title as a built-in, or a leftover of a workflow that was deleted —
// never fails the enable. The prefix is qualified by this workflow's source
// ("(built-in)" / "(yours)") first, then numbered, so one enable names all of
// its activations consistently. A name the caller chose for one trigger
// (override.Name) is theirs and is not rewritten.
func (s *Service) activationPrefix(
	msg *reliantv1.EnableWorkflowRequest,
	declared []*reliantv1.WorkflowTrigger,
	activations map[string]*core.Trigger,
	overrides map[string]*reliantv1.WorkflowTriggerOverride,
	taken map[string]bool,
) string {
	base := strings.TrimSpace(msg.NamePrefix)
	if base == "" {
		base = msg.Workflow
	}
	var created []string
	for _, wt := range declared {
		if activations[wt.GetName()] != nil {
			continue
		}
		if o := overrides[wt.GetName()]; o != nil && o.Name != nil {
			continue
		}
		created = append(created, wt.GetName())
	}
	free := func(prefix string) bool {
		for _, name := range created {
			if taken[prefix+" · "+name] {
				return false
			}
		}
		return true
	}
	if free(base) {
		return base
	}
	qualifier := "yours"
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(msg.Workflow)), "builtin://") {
		qualifier = "built-in"
	}
	if candidate := base + " (" + qualifier + ")"; free(candidate) {
		return candidate
	}
	for n := 2; ; n++ {
		if candidate := fmt.Sprintf("%s (%d)", base, n); free(candidate) {
			return candidate
		}
	}
}

func sameWorkflowRef(a, b string) bool {
	key := func(ref string) string {
		ref = strings.TrimSpace(ref)
		if strings.HasPrefix(ref, "builtin://") {
			return strings.ToLower(ref)
		}
		return launch.NormalizeWorkflowSlug(ref)
	}
	ka, kb := key(a), key(b)
	return ka != "" && ka == kb
}

// connectMessage is the text of a handler's error as a person reads it: what
// the svcerr interceptor would send, so an unclassified error's own text stays
// withheld here too.
func connectMessage(err error) string {
	return svcerr.ToConnect(err).Message()
}

// RetireDeletedTriggers stops what outlives the rows of triggers already
// deleted: their Temporal Schedules and the automation access nothing else
// holds any more. It is best effort, because the rows are the truth: a
// schedule left behind finds no row to fire and SyncAll collects it.
func (s *Service) RetireDeletedTriggers(ctx context.Context, deleted []*core.Trigger) {
	type holder struct{ user, daemon string }
	released := map[holder]bool{}
	for _, t := range deleted {
		if s.deps.Syncer != nil && syncs(t.Kind) {
			if err := s.deps.Syncer.Delete(ctx, t.ID); err != nil {
				logging.WarnContext(ctx, "could not delete the schedule of a deleted trigger; the next sync collects it",
					"trigger_id", t.ID, "error", err)
			}
		}
		if s.managed(t) {
			s.retireAlerting(ctx, t.ID)
		}
		h := holder{t.UserID, t.DaemonID}
		if t.DaemonID != "" && !released[h] {
			released[h] = true
			s.grants.releaseIfUnused(ctx, t.UserID, t.DaemonID)
		}
	}
}
