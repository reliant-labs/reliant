// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/integrations/writeledger"
	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/stepsecrets"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"github.com/reliant-labs/reliant/pkg/hostedhttp"
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
	runner      writeledger.Runner
	credentials httpaction.CredentialSource
	hosted      HostedActionExecutor
	secrets     SecretResolver
}

// WithSecretResolver lets an action's inputs use the workflow's declared
// secrets ({{ secrets.NAME }}). A nil resolver makes such an action fail rather
// than send the placeholder.
func (a *ActionActivity) WithSecretResolver(r SecretResolver) *ActionActivity {
	clone := *a
	clone.secrets = r
	return &clone
}

// HostedActionExecutor is used only by fixed action nodes, never by the
// generic integration-tool credential source. It issues a durable one-use
// ticket from trusted activity context before dispatching to the controller.
type HostedActionExecutor interface {
	ExecuteAction(ctx context.Context, runID, nodeID, grantID string, req hostedhttp.Request) (*hostedhttp.Result, error)
}

// WithHostedExecutor enables explicitly granted hosted HTTP actions. A nil
// executor leaves them fail-closed; it never falls back to an unauthenticated
// request or saved connection.
func (a *ActionActivity) WithHostedExecutor(executor HostedActionExecutor) *ActionActivity {
	clone := *a
	clone.hosted = executor
	return &clone
}

// WithLedger records every mutating call this activity makes in the write
// ledger. A nil ledger changes nothing.
func (a *ActionActivity) WithLedger(rec writeledger.Service) *ActionActivity {
	clone := *a
	if rec != nil {
		clone.runner = rec.Wrap(a.runner)
	}
	return &clone
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

	if args.GetHostedUse() != nil {
		grant := strings.TrimSpace(model.CelStringRaw(args.GetHostedUse()))
		return a.executeHosted(ctx, uses, grant, params, rtx), nil
	}

	// Declared secrets are resolved here, inside the activity: the node reached
	// it with markers, never values. They are substituted into the request and
	// every byte the step returns is masked.
	secrets, failure := a.resolveActionSecrets(ctx, input, params)
	if failure != nil {
		return failure, nil
	}
	if len(secrets.values) > 0 {
		params = secrets.values.SubstituteAny(params).(map[string]any)
	}

	site := httpaction.CallSite{
		RunID: rtx.WorkflowID, NodeID: rtx.StepID, ToolCallID: invokeToolCallID(rtx.WorkflowID, rtx.StepID, rtx.LoopNodeID, rtx.LoopIteration),
		ActingIdentity: rtx.ActingIdentity, Automation: rtx.Automation,
	}
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
		out.Content = secrets.masker.Mask(out.Content)
	} else {
		out.ConnectionId = result.ConnectionID
		out.Content = secrets.masker.Mask(result.Content)
		out.IsError = result.IsError
		out.Retryable = result.Retryable
		out.StatusCode = int32(result.StatusCode)
		if result.Data != nil {
			if data, convErr := structpb.NewStruct(result.Data); convErr == nil {
				out.Data = secrets.masker.MaskStruct(data)
			}
		}
	}
	activity.GetLogger(ctx).Debug("[Action] Completed", "stepID", rtx.StepID, "uses", uses,
		"isError", out.IsError, "status", out.StatusCode)
	return out, nil
}

// resolveActionSecrets resolves the secrets an action's inputs name. The output
// is nil-safe: with no secrets in play the masker masks nothing.
func (a *ActionActivity) resolveActionSecrets(ctx context.Context, input ActivityInput, params map[string]any) (*stepSecrets, *reliantv1.ActionOutput) {
	rtx := input.Runtime
	names := namesInAny(params)
	if len(names) == 0 {
		return &stepSecrets{values: stepsecrets.Values{}, masker: stepsecrets.NewMasker()}, nil
	}
	uses := strings.TrimSpace(model.CelStringRaw(input.Node.GetAction().GetUses()))
	fail := func(code, message string) *reliantv1.ActionOutput { return failedAction(uses, code, message) }
	want, err := nodeSecretRefs(input.Node, names)
	if err != nil {
		return nil, fail("invalid_argument", err.Error())
	}
	if rtx.WorkflowID == "" || rtx.StepID == "" {
		return nil, fail("failed_precondition", "secrets require a fixed workflow step")
	}
	got, err := resolveStepSecrets(ctx, a.secrets, rtx.WorkflowID, rtx.StepID, want)
	if err != nil {
		message, denied := secretFailure(err)
		code := "failed_precondition"
		if denied {
			code = "permission_denied"
		}
		return nil, fail(code, message)
	}
	return got, nil
}

func failedAction(uses, code, message string) *reliantv1.ActionOutput {
	return &reliantv1.ActionOutput{Uses: uses, IsError: true, ErrorCode: code, Content: message}
}

// namesInAny returns the distinct secret markers anywhere in decoded JSON.
func namesInAny(in any) []string {
	set := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			for _, n := range stepsecrets.Names(x) {
				set[n] = true
			}
		case map[string]any:
			for k, item := range x {
				walk(k)
				walk(item)
			}
		case []any:
			for _, item := range x {
				walk(item)
			}
		}
	}
	walk(in)
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
