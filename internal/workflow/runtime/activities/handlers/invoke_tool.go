// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"go.temporal.io/sdk/activity"
	structpb "google.golang.org/protobuf/types/known/structpb"
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
func NewInvokeToolActivity(repo db.Repository, toolExecutor toolexec.ToolExecutor) *InvokeToolActivity {
	return &InvokeToolActivity{
		executeTools: NewExecuteToolsActivity(repo, toolExecutor),
	}
}

// Name returns the activity name for registration.
func (a *InvokeToolActivity) Name() string { return "InvokeTool" }

// DisplayName returns the human-readable name for UI.
func (a *InvokeToolActivity) DisplayName() string { return "Invoke Tool" }

// Description returns what the activity does.
func (a *InvokeToolActivity) Description() string {
	return "Invoke a single tool directly from the graph"
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
	if !tools.IsNodeExposedTool(toolName) {
		return nil, fmt.Errorf(
			"invoke_tool node %s: tool %q is not exposed as a node; exposed tools: %v",
			rtx.StepID, toolName, nodeExposedToolNames())
	}

	toolInput, err := toolInputFromParams(args.GetParams())
	if err != nil {
		return nil, fmt.Errorf("invoke_tool node %s: %w", rtx.StepID, err)
	}

	activityInfo := activity.GetInfo(ctx)

	result := a.executeTools.executeSingleTool(
		ctx,
		rtx.ChatID,
		rtx.Thread,
		toolName,
		toolInput,
		invokeToolCallID(rtx.WorkflowID, rtx.StepID, rtx.LoopNodeID, rtx.LoopIteration),
		activityInfo.ActivityID,
		activityInfo.WorkflowExecution.RunID,
		int(activityInfo.Attempt),
		rtx.ProjectPath,
		rtx.DaemonSelector,
	)

	output := &reliantv1.InvokeToolOutput{
		Tool:          toolName,
		Content:       result.Content,
		IsError:       result.IsError,
		AttachmentIds: result.AttachmentIDs,
		Data:          structuredToolData(result.Metadata),
	}

	activity.GetLogger(ctx).Info("[InvokeTool] Completed",
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
