// Copyright (c) 2026 Reliant Labs

package messageproto

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// What a TOOL_BODY_MODE_STUB read leaves out of a transcript, and what it
// sends in its place (dev-docs/design/chat-lazy-tool-bodies.md, §2). A client
// that asks for STUB opens a chat without the bodies of its finished tool
// calls, and reads one with GetContentBlockBodies when it shows it. Nothing
// here touches the stored block or what the model sees.
//
// The two choices most likely to be revisited are the first two declarations
// below: whether thinking is elided, and which tools are always sent whole.

// stubThinking is whether a STUB read elides THINKING and REDACTED_THINKING
// content. Nothing in the web app or the TS SDK renders either (the redacted
// kind is provider ciphertext), so eliding them costs no display. Owner
// decision 3, 2026-11-08: elide.
const stubThinking = true

// alwaysFullBodyTools are the tools whose input and result a STUB read sends
// whole, whatever their size, on top of every structuredResultTools entry:
// the collapsed-fold cards, the tasks sidebar and the media rows read them
// without expanding anything, and they are small.
var alwaysFullBodyTools = map[string]bool{
	"draft_automation": true,
	"generate_image":   true,
	"generate_video":   true,
	"start_run":        true,
	"get_run":          true,
}

// StubBodyThreshold is the size a tool input or result must pass before a
// STUB read elides it. Smaller bodies cost less than the round trip that
// would fetch them.
const StubBodyThreshold = 1 << 10

// StubResultPreviewBytes is the budget of the preview an elided tool result
// is sent as. A JSON object stays valid JSON with its scalars intact, so the
// shell result keeps exit_code for the deploy card.
const StubResultPreviewBytes = 1 << 10

// stubMinStringPreview is the floor a JSON result's strings are shortened to
// inside StubResultPreviewBytes: lower than the display preview's, so a
// result with two long strings (stdout and stderr) still fits as JSON.
const stubMinStringPreview = 64

// The input skeleton's limits (see InputSkeleton).
const (
	skeletonKeepString  = 512     // a string up to this stays whole
	skeletonCutString   = 256     // a longer one keeps this much
	skeletonKeepCommand = 4 << 10 // command: deployedEnvOf reads a long chain
	skeletonCutNonJSON  = 256     // an input that is not a JSON object
	skeletonKeepArray   = 200     // elements kept of an array, each walked
	skeletonEllipsis    = "…"
)

// ToolBodyStubsOff is the TranscriptConfig.tool_body_stubs value
// (RELIANT_TRANSCRIPT_TOOL_BODY_STUBS) that throws the kill switch.
const ToolBodyStubsOff = "off"

// ResolveToolBodyMode is the mode a read is served in: STUB when the client
// asked for it and stubsSetting (TranscriptConfig.tool_body_stubs) is not
// ToolBodyStubsOff, FULL otherwise. Unset is FULL, which is what every client
// that predates the field gets, forever.
func ResolveToolBodyMode(requested reliantv1.ToolBodyMode, stubsSetting string) reliantv1.ToolBodyMode {
	if requested == reliantv1.ToolBodyMode_TOOL_BODY_MODE_STUB && stubsSetting != ToolBodyStubsOff {
		return reliantv1.ToolBodyMode_TOOL_BODY_MODE_STUB
	}
	return reliantv1.ToolBodyMode_TOOL_BODY_MODE_FULL
}

// isAlwaysFullBodyTool reports whether a tool's bodies are never elided.
// Prefixed names (mcp__server__tool) match on their last segment.
func isAlwaysFullBodyTool(toolName string) bool {
	if isStructuredResultTool(toolName) {
		return true
	}
	name := strings.ToLower(toolName)
	if i := strings.LastIndex(name, "__"); i >= 0 {
		name = name[i+2:]
	}
	return alwaysFullBodyTools[name]
}

// isTerminalToolCallStatus is a call's status as displayed (an inherited
// in-flight call is reported CANCELLED) when it is finished.
func isTerminalToolCallStatus(s *reliantv1.ToolCallStatus) bool {
	if s == nil {
		return false
	}
	switch *s {
	case reliantv1.ToolCallStatus_TOOL_CALL_STATUS_COMPLETED,
		reliantv1.ToolCallStatus_TOOL_CALL_STATUS_FAILED,
		reliantv1.ToolCallStatus_TOOL_CALL_STATUS_CANCELLED:
		return true
	default:
		return false
	}
}

// applyStub replaces the bodies a STUB read leaves out of p, the FULL wire
// form of b. It is the §2 table:
//
//   - TOOL_CALL: an input over StubBodyThreshold of a finished call whose tool
//     is not always-full becomes its skeleton. An in-flight call stays whole:
//     a pending approval renders its input, and a running call has nothing
//     to fetch yet.
//   - TOOL_RESULT: content over StubBodyThreshold of a tool that is not
//     always-full becomes a StubResultPreviewBytes preview, marked truncated
//     as any preview is, so copy and export fetch the whole of it.
//   - THINKING, REDACTED_THINKING: content is left out (stubThinking).
//
// Each elided block carries body_elided and body_size.
func applyStub(b *db.MessageContentBlock, p *reliantv1.ContentBlock, opts *Options) {
	switch b.BlockType {
	case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL:
		if b.ToolInput == nil || !isTerminalToolCallStatus(p.ToolCallStatus) {
			return
		}
		toolName := ""
		if b.ToolName != nil {
			toolName = *b.ToolName
		}
		skeleton, cut := stubInput(toolName, *b.ToolInput)
		if !cut {
			return
		}
		p.Input = &skeleton
		p.BodyElided = ptr.Of(true)
		p.BodySize = ptr.Of(int64(len(*b.ToolInput)))

	case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT:
		if b.Content == nil {
			return
		}
		preview, elided := stubResult(resultToolName(b, opts), *b.Content)
		if !elided {
			return
		}
		p.Content = &preview
		p.Truncated = ptr.Of(true)
		p.OriginalSize = ptr.Of(int64(len(*b.Content)))
		p.BodyElided = ptr.Of(true)
		p.BodySize = ptr.Of(int64(len(*b.Content)))

	case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_THINKING,
		reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_REDACTED_THINKING:
		if !stubThinking || b.Content == nil || *b.Content == "" {
			return
		}
		p.Content = nil
		p.BodyElided = ptr.Of(true)
		p.BodySize = ptr.Of(int64(len(*b.Content)))
	}
}

// stubInput is what a STUB read sends for a finished call's input, and
// whether that is an elision. A skeleton that cuts nothing is not one: the
// input is then sent whole, with nothing to fetch.
func stubInput(toolName, input string) (string, bool) {
	if len(input) <= StubBodyThreshold || isAlwaysFullBodyTool(toolName) {
		return input, false
	}
	return InputSkeleton(input)
}

// stubResult is what a STUB read sends for a tool result's content, and
// whether that is an elision.
func stubResult(toolName, content string) (string, bool) {
	if len(content) <= StubBodyThreshold || isAlwaysFullBodyTool(toolName) {
		return content, false
	}
	return previewContent(content, StubResultPreviewBytes, stubMinStringPreview)
}

// InputSkeleton is what a STUB read sends for an elided tool input: enough
// for the client's formatters (formatToolParams, toolActionVerb,
// deployOfShellCall, requestMachineReason) to label the call, never enough
// to render it. cut reports whether anything was left out; when it is false
// the skeleton is the input itself.
//
//   - Not a JSON object: the head, cut to skeletonCutNonJSON on a rune
//     boundary.
//   - An object, walked recursively: numbers, booleans and null stay; a
//     string up to skeletonKeepString stays whole, a longer one keeps its
//     first skeletonCutString bytes and an ellipsis; command keeps up to
//     skeletonKeepCommand; patch_text keeps exactly its *** header lines
//     (everything formatPatchParams reads); message keeps its first line;
//     arrays keep their first skeletonKeepArray elements.
//
// testdata/skeleton_corpus.json pins the result for a real-shaped input of
// each tool, and the web's formatter test reads the same file.
func InputSkeleton(input string) (skeleton string, cut bool) {
	trimmed := strings.TrimSpace(input)
	if strings.HasPrefix(trimmed, "{") {
		dec := json.NewDecoder(strings.NewReader(trimmed))
		dec.UseNumber() // keep integers exact
		var doc map[string]any
		if err := dec.Decode(&doc); err == nil && !dec.More() {
			w := skeletonWalker{}
			out := w.value("", doc)
			if !w.cut {
				return input, false
			}
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false)
			if err := enc.Encode(out); err == nil {
				return strings.TrimSuffix(buf.String(), "\n"), true
			}
		}
	}
	if len(input) <= skeletonCutNonJSON {
		return input, false
	}
	return cutHead(input, skeletonCutNonJSON), true
}

type skeletonWalker struct {
	cut bool
}

// value is v's skeleton. key is the object key v sits under (an array's
// elements inherit their array's), which decides how a string is cut.
func (w *skeletonWalker) value(key string, v any) any {
	switch t := v.(type) {
	case string:
		return w.str(key, t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = w.value(k, e)
		}
		return out
	case []any:
		n := len(t)
		if n > skeletonKeepArray {
			n = skeletonKeepArray
			w.cut = true
		}
		out := make([]any, n)
		for i := range n {
			out[i] = w.value(key, t[i])
		}
		return out
	default:
		return v
	}
}

func (w *skeletonWalker) str(key, s string) string {
	switch key {
	case "command":
		if len(s) <= skeletonKeepCommand {
			return s
		}
		w.cut = true
		return cutHead(s, skeletonKeepCommand) + skeletonEllipsis
	case "patch_text":
		if len(s) <= skeletonKeepString {
			return s
		}
		w.cut = true
		return patchHeaders(s)
	case "message":
		if len(s) <= skeletonKeepString {
			return s
		}
		w.cut = true
		head := cutHead(s, skeletonCutString)
		if line, _, _ := strings.Cut(s, "\n"); len(line) > len(head) {
			head = cutHead(line, skeletonKeepString)
		}
		return head + skeletonEllipsis
	default:
		if len(s) <= skeletonKeepString {
			return s
		}
		w.cut = true
		return cutHead(s, skeletonCutString) + skeletonEllipsis
	}
}

// patchHeaderLine is a patch_text line formatPatchParams reads: the envelope
// and each file operation.
var patchHeaderLine = regexp.MustCompile(`^\*\*\* (Begin Patch|End Patch|Update File:|Add File:|Delete File:|Move to:)`)

// patchHeaders keeps only patch's header lines, in order.
func patchHeaders(patch string) string {
	var kept []string
	for line := range strings.SplitSeq(patch, "\n") {
		if patchHeaderLine.MatchString(strings.TrimRight(line, "\r")) {
			kept = append(kept, strings.TrimRight(line, "\r"))
		}
	}
	return strings.Join(kept, "\n")
}
