// Copyright (c) 2025 Reliant Labs

package messageproto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ToolResultPreviewBytes caps a tool_result block's content on the
// client-facing read paths. A tool's output can be megabytes (a file read, a
// test log, a grep dump) and the transcript of a chat sent all of it on open,
// for every call in the window, so opening a chat with large outputs was
// dominated by bytes nobody had asked to see yet. Content over the cap is
// sent as a preview, marked truncated, with its full size; GetToolResult
// returns the rest when the user asks for it.
//
// This is a DISPLAY cap. It is applied while converting stored rows to the
// wire for a client, never to the stored block and never on the path that
// builds the model's context (that path reads db rows through the threads
// and workflow packages, not through this package).
const ToolResultPreviewBytes = 8 << 10

// previewTailShare is the part of the preview kept from the END of the
// output. Head AND tail, not head alone: the conclusion of most long outputs
// is at the bottom — a test run's summary and failures, a build's error, a
// command's exit status — and a head-only preview would show the user
// everything except the line they opened it for.
const previewTailShare = 4 // 1/4 of the budget is tail, 3/4 head

// lineSnapWindow is how far a cut may move to land on a line boundary. The
// renderers that parse output (grep's file list, glob's paths, websearch's
// result headers) work line by line, and a line cut in half reads as a bogus
// entry.
const lineSnapWindow = 512

// minStringPreview is the smallest a JSON string field is shortened to before
// a JSON preview is abandoned for a plain-text one.
const minStringPreview = 256

// structuredResultTools are the tools whose result content the client parses
// as a whole and acts on, rather than showing it: the plan and task tools
// (the tasks sidebar reads their JSON or key/value output to build the plan),
// and the questions to the user (rendered as their own cards). A preview of
// these would not render broken; it would silently build the wrong state.
// Their output is small, so sending it whole costs little.
var structuredResultTools = map[string]bool{
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

// isStructuredResultTool reports whether a tool's result must be sent whole.
// MCP-prefixed names (mcp__server__tool) are matched by their last segment,
// the way the client's renderers match them.
func isStructuredResultTool(toolName string) bool {
	name := strings.ToLower(toolName)
	if strings.HasPrefix(name, "mcp__") {
		if i := strings.LastIndex(name, "__"); i >= 0 {
			name = name[i+2:]
		}
	}
	return structuredResultTools[name]
}

// PreviewToolResult returns what a client is sent for a tool_result block's
// content: the content itself when it fits ToolResultPreviewBytes or the tool
// is one whose result the client parses (see structuredResultTools), and a
// preview otherwise. truncated reports which.
//
// A JSON object (the shell tool's {stdout, stderr, exit_code}, for one) is
// previewed as the same object with its long strings shortened, so the
// renderer that parses it still can. Anything else is previewed as text: the
// head and the tail, joined by a line naming how much was left out.
func PreviewToolResult(toolName, content string) (preview string, truncated bool) {
	if isStructuredResultTool(toolName) {
		return content, false
	}
	return previewContent(content, ToolResultPreviewBytes, minStringPreview)
}

// previewContent shortens content to about budget bytes, keeping a JSON
// object valid JSON with none of its strings cut below minString (see
// previewJSONObject). truncated reports whether it shortened anything.
func previewContent(content string, budget, minString int) (preview string, truncated bool) {
	if len(content) <= budget {
		return content, false
	}
	if p, ok := previewJSONObject(content, budget, minString); ok {
		return p, true
	}
	return previewText(content, budget), true
}

// previewText shortens s to about budget bytes: a head, an omission marker,
// and a tail, each cut on a UTF-8 boundary (and on a line boundary when one is
// near).
func previewText(s string, budget int) string {
	if len(s) <= budget {
		return s
	}
	// Reserve room for the widest marker this cut can produce ("1023 bytes"
	// is wider than the "N KB" a larger omission prints), so the result
	// fits budget.
	room := budget - len(omissionMarker(len(s))) - len(" bytes")
	if room < 2 {
		return cutHead(s, budget)
	}
	tailLen := room / previewTailShare
	headLen := room - tailLen

	head := snapHeadToLine(cutHead(s, headLen))
	tail := snapTailToLine(cutTail(s, tailLen))
	return head + omissionMarker(len(s)-len(head)-len(tail)) + tail
}

// omissionMarker is the line that stands in for what a preview left out. It
// deliberately contains no '.' or '/': the glob renderer treats a line with
// either as a file path.
func omissionMarker(omitted int) string {
	return fmt.Sprintf("\n… %s omitted …\n", humanBytes(omitted))
}

func humanBytes(n int) string {
	if n < 1<<10 {
		return fmt.Sprintf("%d bytes", n)
	}
	return fmt.Sprintf("%d KB", (n+(1<<10)-1)>>10)
}

// cutHead returns the longest prefix of s of at most n bytes that ends on a
// rune boundary.
func cutHead(s string, n int) string {
	if n >= len(s) {
		return s
	}
	if n <= 0 {
		return ""
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// cutTail returns the longest suffix of s of at most n bytes that starts on a
// rune boundary.
func cutTail(s string, n int) string {
	if n >= len(s) {
		return s
	}
	if n <= 0 {
		return ""
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// snapHeadToLine drops a trailing partial line when a line break is close to
// the end.
func snapHeadToLine(head string) string {
	from := max(len(head)-lineSnapWindow, 0)
	if i := strings.LastIndexByte(head[from:], '\n'); i >= 0 {
		return head[:from+i]
	}
	return head
}

// snapTailToLine drops a leading partial line when a line break is close to
// the start.
func snapTailToLine(tail string) string {
	to := min(lineSnapWindow, len(tail))
	if i := strings.IndexByte(tail[:to], '\n'); i >= 0 {
		return tail[i+1:]
	}
	return tail
}

// previewJSONObject shortens the strings inside a JSON object until it
// marshals to at most budget bytes, keeping it valid JSON. It reports false
// when s is not a JSON object, or when even its strings at minString leave it
// over budget (a huge array of small values) — the caller then previews it as
// text.
func previewJSONObject(s string, budget, minString int) (string, bool) {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "{") {
		return "", false
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber() // keep integers exact
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil || dec.More() {
		return "", false
	}

	longest := longestString(doc)
	if longest <= minString {
		return "", false
	}
	encode := func(limit int) (string, bool) {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(shortenStrings(doc, limit)); err != nil {
			return "", false
		}
		return strings.TrimSuffix(buf.String(), "\n"), true
	}

	// The marshaled size only grows with the per-string limit, so the
	// largest limit that fits is found by bisection.
	lo, hi := minString, longest
	best, ok := encode(lo)
	if !ok || len(best) > budget {
		return "", false
	}
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		out, ok := encode(mid)
		if ok && len(out) <= budget {
			lo, best = mid, out
		} else {
			hi = mid - 1
		}
	}
	return best, true
}

func longestString(v any) int {
	switch t := v.(type) {
	case string:
		return len(t)
	case map[string]any:
		n := 0
		for _, e := range t {
			n = max(n, longestString(e))
		}
		return n
	case []any:
		n := 0
		for _, e := range t {
			n = max(n, longestString(e))
		}
		return n
	default:
		return 0
	}
}

// shortenStrings returns a copy of v with every string over limit bytes
// replaced by its text preview.
func shortenStrings(v any, limit int) any {
	switch t := v.(type) {
	case string:
		if len(t) <= limit {
			return t
		}
		return previewText(t, limit)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = shortenStrings(e, limit)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = shortenStrings(e, limit)
		}
		return out
	default:
		return v
	}
}

// PreviewMessageUpdate applies PreviewToolResult to a live message update:
// the JSON payload a CHAT_UPDATE_TYPE_MESSAGE row carries (its
// content_blocks, possibly wrapped in {"message": ...}). Each tool_result
// block over the cap gets the same preview, truncated and original_size the
// transcript read paths give it. The stored update row is not touched; this
// runs when it is sent to a client.
//
// Under TOOL_BODY_MODE_STUB (a replayed update, never a live one) a result
// gets the stub preview and thinking is elided, as on the transcript reads
// (see stubResult, stubThinking), but a tool call's input stays whole: the
// update carries the call's status from when it was written, so it cannot
// tell a finished call from a running one.
//
// raw is returned as it is when nothing in it needs a preview, and when it is
// not a payload this understands.
func PreviewMessageUpdate(raw []byte, mode reliantv1.ToolBodyMode) []byte {
	stub := mode == reliantv1.ToolBodyMode_TOOL_BODY_MODE_STUB
	if !bytes.Contains(raw, []byte(`"content_blocks"`)) {
		return raw
	}
	if !stub && len(raw) <= ToolResultPreviewBytes {
		return raw // no block in it can be over the cap
	}
	if stub && len(raw) <= StubBodyThreshold && !mayHoldThinking(raw) {
		return raw // nothing in it can be elided; skip the decode
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // re-marshaling must not turn seq and ids into floats
	var payload map[string]any
	if err := dec.Decode(&payload); err != nil {
		return raw
	}
	msg := payload
	if wrapped, ok := payload["message"].(map[string]any); ok {
		msg = wrapped
	}
	blocks, _ := msg["content_blocks"].([]any)

	changed := false
	for _, b := range blocks {
		block, ok := b.(map[string]any)
		if !ok {
			continue
		}
		content, ok := block["content"].(string)
		if !ok {
			continue
		}
		if stub && isThinkingBlockType(block["type"]) {
			if stubThinking && content != "" {
				delete(block, "content")
				block["body_elided"] = true
				block["body_size"] = len(content)
				changed = true
			}
			continue
		}
		if !isToolResultBlockType(block["type"]) {
			continue
		}
		toolName, _ := block["tool_name"].(string)
		if stub {
			if preview, elided := stubResult(toolName, content); elided {
				block["content"] = preview
				block["truncated"] = true
				block["original_size"] = len(content)
				block["body_elided"] = true
				block["body_size"] = len(content)
				changed = true
			}
			continue
		}
		if preview, truncated := PreviewToolResult(toolName, content); truncated {
			block["content"] = preview
			block["truncated"] = true
			block["original_size"] = len(content)
			changed = true
		}
	}
	if !changed {
		return raw
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return raw
	}
	return out
}

func isToolResultBlockType(v any) bool {
	return isBlockType(v, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT)
}

// mayHoldThinking is a cheap byte test for a thinking block in a live
// update's JSON, which spells a block type as its number. A false positive
// costs only a decode.
func mayHoldThinking(raw []byte) bool {
	for _, t := range []reliantv1.ContentBlockType{
		reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_THINKING,
		reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_REDACTED_THINKING,
	} {
		if bytes.Contains(raw, []byte(`"type":`+strconv.Itoa(int(t)))) ||
			bytes.Contains(raw, []byte(`"type":"`+t.String()+`"`)) {
			return true
		}
	}
	return false
}

func isThinkingBlockType(v any) bool {
	return isBlockType(v, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_THINKING) ||
		isBlockType(v, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_REDACTED_THINKING)
}

// isBlockType reports whether a live update's block "type" is want, spelled
// as its number (what db.ContentBlockPayloadsWithToolCalls writes) or its
// name.
func isBlockType(v any, want reliantv1.ContentBlockType) bool {
	switch t := v.(type) {
	case json.Number:
		return string(t) == strconv.Itoa(int(want))
	case string:
		return t == want.String()
	default:
		return false
	}
}
