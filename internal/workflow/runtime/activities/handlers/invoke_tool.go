// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	structpb "google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	cfgpkg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	activitytypes "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// An invoke_tool node runs ONE tool because the graph says so, rather than
// because a model decided to call it. Everything else about the execution is
// deliberately identical to a tool call made through execute_tools: the same
// executor, the same idempotency rules, the same durable tool_call rows, the
// same daemon routing. That is why this activity delegates to
// ExecuteToolsActivity.executeSingleTool instead of reimplementing any of it —
// a second execution path would drift, and the first thing to drift would be
// the retry semantics, which are the subtle part.
//
// What is genuinely new here is the two ends: parameters arrive as a map from
// the node's args rather than as a model-emitted JSON blob, and the result is
// returned as the node's OWN output rather than keyed by tool name under an
// execute_tools node's response_data.

// InvokeToolActivity implements the invoke_tool node type.
type InvokeToolActivity struct {
	// executeTools carries the whole tool-execution path. Held as a concrete
	// type rather than an interface because there is exactly one
	// implementation and no substitution point — an interface here would be
	// indirection, not abstraction.
	executeTools *ExecuteToolsActivity
}

// NewInvokeToolActivity creates a new InvokeToolActivity.
// Deprecated: use NewInvokeToolActivityWithDeps.
func NewInvokeToolActivity(storeOrDeps any, legacy ...any) *InvokeToolActivity {
	return &InvokeToolActivity{
		executeTools: NewExecuteToolsActivity(storeOrDeps, legacy...),
	}
}

// NewInvokeToolActivityWithDeps creates an invoke activity with explicit dependencies.
func NewInvokeToolActivityWithDeps(deps ExecuteToolsDeps) *InvokeToolActivity {
	return &InvokeToolActivity{executeTools: NewExecuteToolsActivityWithDeps(deps)}
}

// WithUpdates gives the tool-execution path what tells the owner's clients a
// workspace changed; see ExecuteToolsActivity.WithUpdates.
func (a *InvokeToolActivity) WithUpdates(u UserRefetcher) *InvokeToolActivity {
	a.executeTools.WithUpdates(u)
	return a
}

// WithToolCallResults gives the tool-execution path the durable-result reader;
// see ExecuteToolsActivity.WithToolCallResults.
func (a *InvokeToolActivity) WithToolCallResults(r ToolCallResults) *InvokeToolActivity {
	a.executeTools.WithToolCallResults(r)
	return a
}

// WithRunFunding gives the tool-execution path the run's funding reader; see
// ExecuteToolsActivity.WithRunFunding.
func (a *InvokeToolActivity) WithRunFunding(s RunFundingStore) *InvokeToolActivity {
	a.executeTools.WithRunFunding(s)
	return a
}

// WithWorktreeReviver gives the tool-execution path the archived-worktree
// reviver; see ExecuteToolsActivity.WithWorktreeReviver.
func (a *InvokeToolActivity) WithWorktreeReviver(r WorktreeReviver) *InvokeToolActivity {
	a.executeTools.WithWorktreeReviver(r)
	return a
}

// WithConfigProvider gives the tool-execution path the worker's project
// config provider; see ExecuteToolsActivity.WithConfigProvider.
func (a *InvokeToolActivity) WithConfigProvider(provider cfgpkg.ConfigProvider) *InvokeToolActivity {
	a.executeTools.WithConfigProvider(provider)
	return a
}

// WithDaemonRouter gives the tool-execution path the router selectors
// resolve through; see ExecuteToolsActivity.WithDaemonRouter.
func (a *InvokeToolActivity) WithDaemonRouter(router toolexec.DaemonRouter) *InvokeToolActivity {
	a.executeTools.WithDaemonRouter(router)
	return a
}

// WithChatProjects gives the tool-execution path the reader of a chat's
// attached projects; see ExecuteToolsActivity.WithChatProjects.
func (a *InvokeToolActivity) WithChatProjects(l ChatProjectLister) *InvokeToolActivity {
	a.executeTools.WithChatProjects(l)
	return a
}

// WithToolUsage gives the tool-execution path the tool usage ledger; see
// ExecuteToolsActivity.WithToolUsage.
func (a *InvokeToolActivity) WithToolUsage(r ToolUsageRecorder) *InvokeToolActivity {
	a.executeTools.WithToolUsage(r)
	return a
}

// Name returns the activity name for registration.
func (a *InvokeToolActivity) Name() string { return "InvokeTool" }

// DisplayName returns the human-readable name for UI.
func (a *InvokeToolActivity) DisplayName() string { return "Run Tool" }

// Description returns what the activity does.
func (a *InvokeToolActivity) Description() string {
	return "Run one tool you pick, with parameters you set; no LLM involved"
}

// Category returns the activity category for UI grouping.
func (a *InvokeToolActivity) Category() schema.ActivityCategory {
	return schema.CategoryAgentic
}

// Execute runs the named tool and returns its result as this node's output.
func (a *InvokeToolActivity) Execute(ctx context.Context, input ActivityInput) (*reliantv1.InvokeToolOutput, error) {
	rtx := input.Runtime
	args := input.Node.GetInvokeTool()
	if args == nil {
		return nil, fmt.Errorf("expected invoke_tool node, got %s", model.NodeType(input.Node))
	}

	toolName := strings.TrimSpace(model.CelStringRaw(args.GetTool()))
	if toolName == "" {
		return nil, fmt.Errorf("invoke_tool node %s: tool is required", rtx.StepID)
	}

	// Opt-in is enforced here as well as at validation time. Validation
	// catches the authoring mistake; this catches a tool whose exposure was
	// withdrawn after a workflow was written, and refuses rather than running
	// something the palette no longer offers.
	//
	// The refusal is the definition's, so it is final: non-retryable, and a
	// step config failure, which ends the run instead of pausing it. As a
	// plain error it was retried, then paused the run, and an unattended run
	// self-resumed into it for 33 minutes before giving up (prod 23295ad3).
	// Benign: the workflow is the user's, not a platform fault.
	if !tools.IsNodeExposedTool(toolName) {
		return nil, temporal.NewApplicationErrorWithOptions(fmt.Sprintf(
			"invoke_tool node %s: tool %q is not exposed as a node; exposed tools: %v",
			rtx.StepID, toolName, nodeExposedToolNames()), activitytypes.StepConfigFailedErrType,
			temporal.ApplicationErrorOptions{NonRetryable: true, Category: temporal.ApplicationErrorCategoryBenign})
	}

	toolInput, err := toolInputFromParams(args.GetParams())
	if err != nil {
		return nil, fmt.Errorf("invoke_tool node %s: %w", rtx.StepID, err)
	}

	activityInfo := activity.GetInfo(ctx)
	toolCallID := invokeToolCallID(rtx.WorkflowID, rtx.StepID, rtx.LoopNodeID, rtx.LoopIteration)

	// The call's span (execute_tool), under this node's: no agent turn asked
	// for it, the graph did. Its duration and outcome are also its row in the
	// tool usage ledger (tool_usage_record.go).
	meter := newToolUsageMeter(a.executeTools.usage, a.executeTools.repo, rtx.ChatID, rtx.Thread)
	call := meteredToolCall{name: toolName, callID: toolCallID, location: toolLocation(toolName)}
	result := meter.run(ctx, call, func(callCtx context.Context) message.ToolResult {
		// No capability set: the node's author wrote every parameter, and no
		// model was offered a schema with some of them bound away.
		return a.executeTools.executeSingleTool(
			callCtx,
			nil,
			rtx.ChatID,
			rtx.Thread,
			toolName,
			toolInput,
			toolCallID,
			activityInfo.ActivityID,
			activityInfo.WorkflowExecution.RunID,
			int(activityInfo.Attempt),
			rtx.ProjectPath,
			rtx.DaemonSelector,
		)
	})

	output := &reliantv1.InvokeToolOutput{
		Tool:          toolName,
		Content:       result.Content,
		IsError:       result.IsError,
		AttachmentIds: result.AttachmentIDs,
		Data:          structuredToolData(result.Metadata),
	}

	activity.GetLogger(ctx).Debug("[InvokeTool] Completed",
		"stepID", rtx.StepID,
		"tool", toolName,
		"isError", result.IsError,
		"hasData", output.Data != nil)

	// A failing tool does NOT fail the activity. The graph decides what a tool
	// error means — a router can branch on is_error, a retry edge can loop —
	// and returning an error here would instead burn Temporal retries on
	// something that is not a transient fault.
	return output, nil
}

// toolInputFromParams renders the node's params map as the JSON object the
// tool executor expects, which is the same encoding a model-emitted tool call
// arrives in. Going through JSON rather than a bespoke path is what makes an
// invoked tool indistinguishable from a called one downstream.
func toolInputFromParams(params map[string]*structpb.Value) (string, error) {
	if len(params) == 0 {
		return "{}", nil
	}
	fields := make(map[string]interface{}, len(params))
	for name, value := range params {
		fields[name] = value.AsInterface()
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return "", fmt.Errorf("encoding params: %w", err)
	}
	return string(encoded), nil
}

// structuredToolData parses a tool's metadata string back into a Struct.
//
// This is the same read execute_tools performs to build response_data, and it
// works for the same reason: WithResponseMetadata JSON-encodes the tool's typed
// output into that string, and the string survives byte-for-byte through the
// executor, the daemon wire and the activity boundary. Parsing it here makes
// the fields reachable as nodes.<id>.data.<field>.
//
// Metadata that is absent or not a JSON object yields nil. A tool that never
// calls WithResponseMetadata simply has no structured output, which is not an
// error — content is still returned.
func structuredToolData(metadata string) *structpb.Struct {
	if strings.TrimSpace(metadata) == "" {
		return nil
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(metadata), &parsed); err != nil {
		return nil
	}
	data, err := structpb.NewStruct(parsed)
	if err != nil {
		return nil
	}
	return data
}

// invokeToolCallID derives the tool_call_id from the node's position in the
// run rather than minting a fresh one.
//
// The id IS the idempotency key: checkPriorTerminalResult refuses to re-run a
// call that already reached a terminal status under the same id. A random id
// per attempt would defeat that and let an interrupted-then-resumed workflow
// generate the image twice.
func invokeToolCallID(workflowID, stepID, loopNodeID string, loopIteration int) string {
	id := workflowID + ":" + stepID
	if loopNodeID != "" {
		id = fmt.Sprintf("%s:%s#%d", id, loopNodeID, loopIteration)
	}
	return id
}

// nodeExposedToolNames lists the opted-in tools, for error messages that tell
// an author what they could have written instead.
func nodeExposedToolNames() []string {
	exposures := tools.NodeExposedTools()
	names := make([]string, 0, len(exposures))
	for _, exposure := range exposures {
		names = append(names, exposure.Tool)
	}
	sort.Strings(names)
	return names
}
