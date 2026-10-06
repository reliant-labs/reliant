// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"go.temporal.io/sdk/activity"
	structpb "google.golang.org/protobuf/types/known/structpb"
)

// An action node runs ONE integration action because the graph says so. The
// action is resolved from the integration catalog by its `uses` reference and
// executed by the declarative HTTP runtime.
//
// Placement: every action the catalog can hold today is `server`/`any`, so it
// runs right here on the worker with no daemon involved — which is why
// preflight treats such a workflow as daemon-free. A `daemon`-placed action
// has no executor yet and is refused rather than silently run on the wrong
// side of the trust boundary.

// ActionActivity implements the action node type.
type ActionActivity struct {
	catalog     *catalog.Catalog
	runner      *httpaction.Runner
	credentials httpaction.CredentialSource
}

// NewActionActivity builds the activity over the embedded catalog and a runner
// whose connections go through the SSRF guard.
//
// credentials resolves a named connection for the run's owner; nil means the
// process has none, and an action that names a connection then fails with a
// FailedPrecondition instead of running unauthenticated.
func NewActionActivity(credentials httpaction.CredentialSource) *ActionActivity {
	return &ActionActivity{catalog: catalog.MustBuiltin(), runner: httpaction.NewRunner(netguard.New()), credentials: credentials}
}

// NewActionActivityWith builds the activity over a specific catalog and runner.
func NewActionActivityWith(c *catalog.Catalog, r *httpaction.Runner, credentials httpaction.CredentialSource) *ActionActivity {
	return &ActionActivity{catalog: c, runner: r, credentials: credentials}
}

func (a *ActionActivity) Name() string        { return "Action" }
func (a *ActionActivity) DisplayName() string { return "Action" }
func (a *ActionActivity) Description() string {
	return "Run an integration action (for example an HTTP request)"
}
func (a *ActionActivity) Category() schema.ActivityCategory { return schema.CategoryUtility }

// Execute resolves and runs the action. A failing action does NOT fail the
// activity: the graph decides what an error means, and returning one here
// would burn Temporal retries on something the manifest may have classified as
// permanent. The output's retryable flag carries the manifest's verdict.
func (a *ActionActivity) Execute(ctx context.Context, input ActivityInput) (*reliantv1.ActionOutput, error) {
	rtx := input.Runtime
	args := input.Node.GetAction()
	if args == nil {
		return nil, fmt.Errorf("expected action node, got %s", model.NodeType(input.Node))
	}
	uses := strings.TrimSpace(model.CelStringRaw(args.GetUses()))
	if uses == "" {
		return nil, fmt.Errorf("action node %s: uses is required", rtx.StepID)
	}
	resolved, err := a.catalog.Resolve(uses)
	if err != nil {
		return nil, fmt.Errorf("action node %s: %w", rtx.StepID, err)
	}
	if p := resolved.Spec.GetPlacement(); p != manifest.PlacementServer && p != manifest.PlacementAny {
		return nil, fmt.Errorf("action node %s: %s is %s-placed, which has no executor yet", rtx.StepID, uses, p)
	}

	params := make(map[string]any, len(args.GetWith())+1)
	for name, value := range args.GetWith() {
		params[name] = value.AsInterface()
	}
	// Only the connection REFERENCE travels in the node. The secret is resolved
	// here, on the worker, at call time, and never enters history.
	if connection := strings.TrimSpace(model.CelStringRaw(args.GetConnection())); connection != "" {
		params[httpaction.ConnectionParam] = connection
	}

	site := httpaction.CallSite{RunID: rtx.WorkflowID, NodeID: rtx.StepID, ToolCallID: invokeToolCallID(rtx.WorkflowID, rtx.StepID, rtx.LoopNodeID, rtx.LoopIteration)}
	result, runErr := a.runner.RunAuthenticated(ctx, resolved.Manifest, resolved.Spec, params, a.credentials, site)
	out := &reliantv1.ActionOutput{Uses: uses}
	if runErr != nil {
		out.Content = runErr.Error()
		out.IsError = true
		var credErr *httpaction.CredentialError
		if errors.As(runErr, &credErr) {
			out.ErrorCode = credErr.Code
			out.Content = credErr.Message
		}
	} else {
		out.ConnectionId = result.ConnectionID
		out.Content = result.Content
		out.IsError = result.IsError
		out.Retryable = result.Retryable
		out.StatusCode = int32(result.StatusCode)
		if result.Data != nil {
			if data, convErr := structpb.NewStruct(result.Data); convErr == nil {
				out.Data = data
			}
		}
	}
	activity.GetLogger(ctx).Debug("[Action] Completed", "stepID", rtx.StepID, "uses", uses,
		"isError", out.IsError, "status", out.StatusCode)
	return out, nil
}
