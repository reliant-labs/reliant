// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// OptionsChecker answers, for the values someone picked for an integration
// input, whether the integration can reach them. It is declared here, by its
// consumer: the options service (typed-inputs T6) is its implementation, and
// nothing in this package knows how a type's values are listed.
//
// Enabling asks it BEST EFFORT. A value it says is definitely unreachable
// refuses the enable; an error from it (the integration is down, rate
// limited, the connection could not be read) never does, because a check that
// cannot be made is not evidence about the value.
type OptionsChecker interface {
	CheckValues(ctx context.Context, check ValueCheck) (ValueCheckResult, error)
}

// ValueCheck is one input's picked values, to be checked together.
type ValueCheck struct {
	// UserID is the owner whose access is checked.
	UserID string
	// Type is the input's type ref, "github.repository".
	Type string
	// ConnectionID is the connection the check goes through; empty means the
	// owner's default for the type's integration.
	ConnectionID string
	// ActingIdentity is who the automation's writes speak as. It only feeds
	// the checker's warnings ("the bot cannot write to acme/web").
	ActingIdentity core.ActingIdentity
	// Scope is the typed values the type's options depend on, by scope key
	// (a label's repository). Nil for a type that takes none.
	Scope map[string]any
	// Values are the picked values.
	Values []string
}

// ValueCheckResult is what the checker could determine.
type ValueCheckResult struct {
	// Unreachable are the values the integration definitely cannot reach:
	// the repository does not exist, the App is not installed on it, the
	// owner cannot see it.
	Unreachable []string
	// Warnings are things that do not refuse the enable but are worth
	// knowing, in words a person can act on: the acting identity cannot write
	// to a value.
	Warnings []string
}

// checkReachability asks the options checker about row's integration-typed
// params and returns what to tell the person who enables, or a refusal for a
// definite "not reachable". It is a no-op without a checker, for a workflow
// with no integration inputs, and for a value that is not a plain string.
func (s *Service) checkReachability(
	ctx context.Context,
	userID string,
	wf *reliantv1.Workflow,
	wt *reliantv1.WorkflowTrigger,
	msg *reliantv1.EnableWorkflowRequest,
	row *core.Trigger,
) ([]string, error) {
	if s.deps.Options == nil {
		return nil, nil
	}
	mapped := wt.GetInputs()
	names := make([]string, 0, len(wf.GetInputs()))
	for name, input := range wf.GetInputs() {
		if _, fromEvent := mapped[name]; !fromEvent && model.IsIntegrationInput(input) {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	var (
		warnings []string
		refusals []string
	)
	for _, name := range names {
		input := wf.GetInputs()[name]
		values := stringValues(row.Params[name])
		if len(values) == 0 {
			continue
		}
		integration, _, _ := strings.Cut(input.GetType(), ".")
		check := ValueCheck{
			UserID:         userID,
			Type:           input.GetType(),
			ConnectionID:   s.checkConnection(integration, wt, msg, row),
			ActingIdentity: row.ActingIdentity.OrOwner(),
			Scope:          scopeValues(input, row.Params),
			Values:         values,
		}
		res, err := s.deps.Options.CheckValues(ctx, check)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf(
				"Could not check that %s can be reached (%s). It was saved anyway; if it is wrong, runs will say so.", name, err))
			continue
		}
		warnings = append(warnings, res.Warnings...)
		if len(res.Unreachable) > 0 {
			refusals = append(refusals, fmt.Sprintf("%s: %s not reachable", name, strings.Join(res.Unreachable, ", ")))
		}
	}
	if len(refusals) > 0 {
		return nil, svcerr.FailedPrecondition(fmt.Sprintf(
			"params: %s with the connected account. Choose values it can see, or reconnect with more access", strings.Join(refusals, "; ")))
	}
	return warnings, nil
}

// checkConnection is the connection a type's values are checked through: the
// one the request names for the type's integration, else the one the trigger
// itself listens through when that is the same integration.
func (s *Service) checkConnection(integration string, wt *reliantv1.WorkflowTrigger, msg *reliantv1.EnableWorkflowRequest, row *core.Trigger) string {
	if id := msg.GetConnectionIds()[integration]; id != "" {
		return id
	}
	if wt.GetIntegration().GetIntegration() == integration && row.ConnectionID != nil {
		return *row.ConnectionID
	}
	return ""
}

// stringValues reads one input's stored value as the strings picked: a single
// string, or a list of them. Anything else yields none, and so is not checked.
func stringValues(v any) []string {
	switch v := v.(type) {
	case string:
		if v != "" {
			return []string{v}
		}
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if str, ok := item.(string); ok && str != "" {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

// scopeValues is the typed values an integration input's options depend on:
// each scope key bound to the name of another input, read from params.
func scopeValues(input interface {
	GetIntegrationInput() *reliantv1.IntegrationInputConfig
}, params map[string]any) map[string]any {
	scope := input.GetIntegrationInput().GetScope()
	if len(scope) == 0 {
		return nil
	}
	out := make(map[string]any, len(scope))
	for key, inputName := range scope {
		if v, ok := params[inputName]; ok {
			out[key] = v
		}
	}
	return out
}

// ownerIdentityWarnings tells the person enabling when a declared trigger's
// filter gates on who the owner is on GitHub
// (trigger.activation.owner.github_id) and the owner has no GitHub identity
// to compare against: the activation would enable fine and then skip every
// event, including the owner's own. It is a warning, not a refusal, because
// the same filter may admit others by trusted_ids, and the identity can be
// linked later (it is read at every event, never stored on the activation).
func (s *Service) ownerIdentityWarnings(ctx context.Context, wt *reliantv1.WorkflowTrigger, row *core.Trigger) []string {
	if !triggerspec.ReadsOwnerGitHubID(wt.GetFilter()) {
		return nil
	}
	if triggers.OwnerOf(ctx, s.deps.Repo, row).GitHubID != "" {
		return nil
	}
	return []string{fmt.Sprintf(
		"Trigger %q only admits senders by your GitHub identity, and none is linked to your account yet, so "+
			"you will not match until you connect GitHub in Settings. Others can still match if you list them in trusted_ids.", wt.GetName())}
}
