// Copyright (c) 2025 Reliant Labs

package messageproto

import (
	"bytes"
	"encoding/json"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// A VIEWER reads a chat its owner shared with their org (control-plane
// docs/design/org-sharing.md §4, decision D2): the transcript and every tool
// call's input, but no tool's result. A result embeds what the tool read off
// the owner's machine (file contents, command output, secrets in either), so
// it is left out entirely, not previewed, and GetContentBlockBodies and
// GetToolResult refuse a viewer, so it cannot be fetched either.
//
// The results of the built-in tools the client parses to build its own
// state (plans, tasks, questions; viewerKeptResultTools) are the
// conversation's, not the machine's, and stay whole.

// viewerKeptResultTools are the built-in tools whose results a viewer reads
// whole, matched by EXACT name. It is a disclosure decision, so it is not
// structuredResultTools (a rendering decision, which matches MCP tools by
// their last segment): an MCP tool is always named mcp__<server>__<tool>
// (tools.mcpToolAdapter), so no MCP server can produce one of these names,
// and mcp__anything__get_plan is a third party's output and is elided. A
// content block carries no tool source, so the exact registered name is the
// identity. TestViewerKeptResultToolsAreBuiltIn holds every entry to a
// structured-result tool.
var viewerKeptResultTools = map[string]bool{
	"create_plan":     true,
	"update_plan":     true,
	"get_plan":        true,
	"add_task":        true,
	"update_task":     true,
	"create_subtask":  true,
	"list_tasks":      true,
	"ask_user":        true,
	"request_machine": true,
}

// viewerKeepsResult reports whether a viewer reads the result of the tool
// named name (exactly, case and all) whole.
func viewerKeepsResult(name string) bool { return viewerKeptResultTools[name] }

// viewerKeepsResultOf decides for one tool_result block, by every name it is
// known by: the result block's own tool_name and, when its call is in view,
// the call's. Each known name must be a kept built-in; a block that names
// none is elided.
func viewerKeepsResultOf(resultName, callName string, callKnown bool) bool {
	if resultName == "" && !callKnown {
		return false
	}
	if resultName != "" && !viewerKeepsResult(resultName) {
		return false
	}
	return !callKnown || viewerKeepsResult(callName)
}

// ElideForViewer leaves every tool result out of msgs, in place: content
// removed, body_elided set and body_size the stored length. Applied after
// the transcript is assembled (in whatever tool body mode), on every
// transcript read a viewer makes.
func ElideForViewer(msgs []*reliantv1.Message) {
	names := map[string]string{}
	for _, m := range msgs {
		for _, b := range m.GetContentBlocks() {
			if b.GetType() == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL && b.GetToolCallId() != "" {
				names[b.GetToolCallId()] = b.GetToolName()
			}
		}
	}
	for _, m := range msgs {
		for _, b := range m.GetContentBlocks() {
			elideBlockForViewer(b, names)
		}
	}
}

// ElideBlocksForViewer is ElideForViewer over loose blocks (a block-body read).
func ElideBlocksForViewer(blocks []*reliantv1.ContentBlock) {
	names := map[string]string{}
	for _, b := range blocks {
		if b.GetType() == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL && b.GetToolCallId() != "" {
			names[b.GetToolCallId()] = b.GetToolName()
		}
	}
	for _, b := range blocks {
		elideBlockForViewer(b, names)
	}
}

func elideBlockForViewer(b *reliantv1.ContentBlock, names map[string]string) {
	if b == nil || b.GetType() != reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT {
		return
	}
	callName, known := names[b.GetToolCallId()]
	if viewerKeepsResultOf(b.GetToolName(), callName, known) || b.Content == nil {
		return
	}
	size := int64(len(b.GetContent()))
	if b.OriginalSize != nil {
		size = b.GetOriginalSize()
	}
	b.Content = nil
	b.Truncated = nil
	b.OriginalSize = nil
	b.BodyElided = ptr.Of(true)
	b.BodySize = ptr.Of(size)
}

// ViewerMessageUpdate is ElideForViewer for a message update's JSON payload
// (a CHAT_UPDATE_TYPE_MESSAGE row, possibly wrapped in {"message": ...}): the
// form replays and live stream frames carry. A tool result's content is
// removed and body_elided/body_size set. It fails closed: a payload that
// carries content blocks but cannot be decoded is replaced by an empty
// object, never sent as it is.
func ViewerMessageUpdate(raw []byte) []byte {
	if !bytes.Contains(raw, []byte(`"content_blocks"`)) {
		return raw
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var payload map[string]any
	if err := dec.Decode(&payload); err != nil {
		return []byte(`{}`)
	}
	msg := payload
	if wrapped, ok := payload["message"].(map[string]any); ok {
		msg = wrapped
	}
	blocks, _ := msg["content_blocks"].([]any)
	names := map[string]string{}
	for _, b := range blocks {
		block, ok := b.(map[string]any)
		if !ok || !isBlockType(block["type"], reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL) {
			continue
		}
		id, _ := block["tool_call_id"].(string)
		name, _ := block["tool_name"].(string)
		if id != "" {
			names[id] = name
		}
	}
	changed := false
	for _, b := range blocks {
		block, ok := b.(map[string]any)
		if !ok || !isToolResultBlockType(block["type"]) {
			continue
		}
		content, ok := block["content"].(string)
		if !ok {
			continue
		}
		resultName, _ := block["tool_name"].(string)
		id, _ := block["tool_call_id"].(string)
		callName, known := names[id]
		if viewerKeepsResultOf(resultName, callName, known) {
			continue
		}
		size := len(content)
		if n, ok := block["original_size"].(json.Number); ok {
			if v, err := n.Int64(); err == nil {
				size = int(v)
			}
		}
		delete(block, "content")
		delete(block, "truncated")
		delete(block, "original_size")
		block["body_elided"] = true
		block["body_size"] = size
		changed = true
	}
	if !changed {
		return raw
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return []byte(`{}`)
	}
	return out
}

// ViewerChatUpdate is what a viewer is sent of one chat update's data (a
// replay, a snapshot's other updates, a live frame), by the update's type.
// Every type is classified in viewerChatUpdates; one that is not (a type
// added later) is sent as {}, so a new payload reaches a viewer only once
// someone decided it may (TestEveryChatUpdateTypeIsClassifiedForViewers).
//
// A payload of a redacted type that is not a JSON object (null, an array, a
// malformed body) is sent as {}: there is nothing to keep of it.
func ViewerChatUpdate(t reliantv1.ChatUpdateType, data string) string {
	switch viewerChatUpdates[t] {
	case viewerPass:
		return data
	case viewerMessage:
		return string(ViewerMessageUpdate([]byte(data)))
	case viewerKeepOnly:
		payload := decodeObject(data)
		if payload == nil {
			return `{}`
		}
		for k := range payload {
			if !runOutputViewerKeys[k] {
				delete(payload, k)
			}
		}
		payload["body_elided"] = true
		return marshalOrEmpty(payload)
	case viewerDropOutputs:
		// An object, whose nested values are walked; a bare string or array
		// at the top is a value with no keys to judge it by.
		payload := decodeObject(data)
		if payload == nil {
			return `{}`
		}
		dropKeys(payload, nodeOutputKeys)
		return marshalOrEmpty(payload)
	case viewerError:
		payload := decodeObject(data)
		if payload == nil {
			return `{}`
		}
		delete(payload, "error_message")
		delete(payload, "error_summary")
		payload["body_elided"] = true
		return marshalOrEmpty(payload)
	case viewerQuestion:
		payload := decodeObject(data)
		if payload == nil {
			return `{}`
		}
		if id, _ := payload["tool_call_id"].(string); id == "" {
			delete(payload, "metadata")
			delete(payload, "response_data")
		}
		return marshalOrEmpty(payload)
	default:
		return `{}`
	}
}

// viewerRule is how a chat update of one type is shaped for a viewer.
type viewerRule uint8

const (
	// viewerWithhold: the type is not classified; sent as {}.
	viewerWithhold viewerRule = iota
	// viewerPass: the payload carries nothing a tool read, a command printed
	// or the engine resolved, and is sent as it is.
	viewerPass
	// viewerMessage: ViewerMessageUpdate (tool results out).
	viewerMessage
	// viewerKeepOnly: only the step's identity, its command and how it
	// ended (runOutputViewerKeys); stdout, stderr, output, working directory
	// and log file are left out.
	viewerKeepOnly
	// viewerDropOutputs: without what a node produced or why it failed
	// (nodeOutputKeys), at any depth.
	viewerDropOutputs
	// viewerError: an error's raw text (error_message, error_summary) is the
	// failure as it happened, which embeds what a command or tool printed;
	// a viewer is told that it failed, where and when.
	viewerError
	// viewerQuestion: a workflow step's question (no tool_call_id) is a
	// resolved prompt, which can carry an earlier result: sent without its
	// metadata. An agent's ask_user is the model's own words and stays.
	viewerQuestion
)

// viewerChatUpdates classifies every ChatUpdateType for viewers.
var viewerChatUpdates = map[reliantv1.ChatUpdateType]viewerRule{
	reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE:            viewerMessage,
	reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_RUN_OUTPUT:         viewerKeepOnly,
	reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_EXECUTION_LOG:      viewerKeepOnly,
	reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_NODE_EXECUTION:     viewerDropOutputs,
	reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_WORKFLOW_EXECUTION: viewerDropOutputs,
	reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_ERROR:              viewerError,
	reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_QUESTION:           viewerQuestion,

	// id, type, status, title (the tool call's own summary, whose input
	// the viewer reads), denial reason, timestamps, workflow and thread.
	reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_APPROVAL: viewerPass,
	// id, title, status, origin, parent, router decision (workflow, preset).
	reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_THREAD: viewerPass,
	// tool call id, name, status, timestamps, node, child workflow.
	reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_TOOL_CALL: viewerPass,
	// workflow id, name, status, outcome, parent.
	reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_WORKFLOW_STATUS: viewerPass,
	// chat id, title, state, workflow name.
	reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_CHAT: viewerPass,
	// what to re-read, and its scope.
	reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_REFETCH: viewerPass,
	// the model's own streamed output (text, thinking, a tool call's input
	// as it is written), as the viewer's transcript keeps it.
	reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_STREAMING_DELTA: viewerPass,
	// message id, thread, reason, last stream sequence.
	reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_STREAM_FINALIZED: viewerPass,
	// thread and the drained messages' ids.
	reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_AGENT_MESSAGES_DRAINED: viewerPass,

	// No production writer; withheld until one is classified.
	reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_UNSPECIFIED:      viewerWithhold,
	reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_INFO:             viewerWithhold,
	reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_WARNING:          viewerWithhold,
	reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_SKILL_INVOCATION: viewerWithhold,
}

// decodeObject is data as a JSON object, nil when it is not one (null, an
// array, a scalar, malformed).
func decodeObject(data string) map[string]any {
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return nil
	}
	return payload
}

// runOutputViewerKeys is what a viewer keeps of a run step's output update.
var runOutputViewerKeys = map[string]bool{
	"update_type": true, "id": true, "step_id": true, "command": true, "exit_code": true,
	"interrupted": true, "duration": true, "workflow_id": true, "unique_activity_id": true, "timestamp": true,
}

// nodeOutputKeys are a run's or node's resolved values: what it produced, why
// it failed, and what it was given (params and inputs are resolved from
// earlier steps' results).
var nodeOutputKeys = map[string]bool{
	"output": true, "outputs": true, "output_json": true, "result": true,
	"error_message": true, "error": true, "metadata": true, "stdout": true, "stderr": true,
	"params": true, "inputs": true, "input": true, "input_json": true, "variables": true,
}

func dropKeys(v any, keys map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if keys[k] {
				delete(x, k)
				continue
			}
			dropKeys(child, keys)
		}
	case []any:
		for _, child := range x {
			dropKeys(child, keys)
		}
	}
}

func marshalOrEmpty(v any) string {
	out, err := json.Marshal(v)
	if err != nil {
		return `{}`
	}
	return string(out)
}

// ElideWorkflowStatesForViewer leaves every resolved value out of a chat
// snapshot's live workflow states, in place: each run's params (a child's are
// resolved from earlier steps' results) and outputs, and each node's error
// and metadata. What ran, its status and its timing stay.
func ElideWorkflowStatesForViewer(states []*reliantv1.WorkflowExecutionState) {
	for _, w := range states {
		if w == nil {
			continue
		}
		w.Params = nil
		w.Outputs = nil
		for _, n := range w.GetNodes() {
			n.ErrorMessage = nil
			n.Metadata = nil
		}
		ElideWorkflowStatesForViewer(w.GetChildren())
	}
}
