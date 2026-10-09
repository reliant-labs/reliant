// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"fmt"
	"strings"
	"testing"

	"github.com/invopop/jsonschema"

	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/rctx"
)

func textMsg(role message.MessageRole, text string) message.Message {
	return message.Message{Role: role, Parts: []message.ContentPart{message.TextContent{Text: text}}}
}

func baseHistory() []message.Message {
	return []message.Message{
		textMsg(message.System, "# User defined rules, memories, and context\n\nglobal"),
		textMsg(message.System, "<system-memory repo=forge>\nforge notes\n</system-memory>"),
		textMsg(message.User, "hello"),
		textMsg(message.Assistant, "hi"),
	}
}

func shapeOf(system []string, history []message.Message) promptShape {
	return computePromptShape("claude-opus", system, history, nil)
}

// History that only grew is the healthy case: nothing diverges.
func TestDiffPromptShape_GrownHistoryHasNoDivergence(t *testing.T) {
	prev := shapeOf([]string{"sys"}, baseHistory())
	grown := append(baseHistory(), textMsg(message.User, "next"))
	d := diffPromptShape(prev, shapeOf([]string{"sys"}, grown))

	if !d.Known || d.ModelChanged || d.ToolsChanged || len(d.SystemChanged) != 0 {
		t.Fatalf("unexpected change flagged: %+v", d)
	}
	if d.DivergedAt != -1 || d.SharedPrefix != 4 {
		t.Fatalf("grown history: DivergedAt=%d SharedPrefix=%d, want -1 and 4", d.DivergedAt, d.SharedPrefix)
	}
}

// A changed project memory sits at the front of history. The diff must point at
// index 0 and name it, not merely say "history changed".
func TestDiffPromptShape_MemoryBlockChangeIsNamed(t *testing.T) {
	prev := shapeOf([]string{"sys"}, baseHistory())
	h := baseHistory()
	h[0] = textMsg(message.System, "# User defined rules, memories, and context\n\nglobal CHANGED")
	d := diffPromptShape(prev, shapeOf([]string{"sys"}, h))

	if d.DivergedAt != 0 || d.SharedPrefix != 0 || d.DivergedKind != "memory" {
		t.Fatalf("got DivergedAt=%d SharedPrefix=%d Kind=%q, want 0/0/memory", d.DivergedAt, d.SharedPrefix, d.DivergedKind)
	}
}

func TestDiffPromptShape_RepoMemoryChangeIsNamed(t *testing.T) {
	prev := shapeOf([]string{"sys"}, baseHistory())
	h := baseHistory()
	h[1] = textMsg(message.System, "<system-memory repo=forge>\nforge notes v2\n</system-memory>")
	d := diffPromptShape(prev, shapeOf([]string{"sys"}, h))

	if d.DivergedAt != 1 || d.SharedPrefix != 1 || d.DivergedKind != "repo-memory" {
		t.Fatalf("got DivergedAt=%d SharedPrefix=%d Kind=%q, want 1/1/repo-memory", d.DivergedAt, d.SharedPrefix, d.DivergedKind)
	}
}

// Editing a message deep in the conversation (compaction, a rewritten tool
// result) diverges late: the shared prefix is long, which is the cheap kind.
func TestDiffPromptShape_LateEditKeepsLongSharedPrefix(t *testing.T) {
	prev := shapeOf([]string{"sys"}, baseHistory())
	h := baseHistory()
	h[3] = textMsg(message.Assistant, "hi, rewritten")
	d := diffPromptShape(prev, shapeOf([]string{"sys"}, h))

	if d.DivergedAt != 3 || d.SharedPrefix != 3 || d.DivergedKind != string(message.Assistant) {
		t.Fatalf("got DivergedAt=%d SharedPrefix=%d Kind=%q", d.DivergedAt, d.SharedPrefix, d.DivergedKind)
	}
}

func TestDiffPromptShape_SystemAndModelChanges(t *testing.T) {
	prev := shapeOf([]string{"a", "b"}, baseHistory())
	cur := computePromptShape("claude-sonnet", []string{"a", "B"}, baseHistory(), nil)
	d := diffPromptShape(prev, cur)

	if !d.ModelChanged {
		t.Error("model change not flagged")
	}
	if fmt.Sprint(d.SystemChanged) != "[1]" {
		t.Errorf("SystemChanged = %v, want [1]", d.SystemChanged)
	}
}

func TestDiffPromptShape_SystemPromptAppearingIsAChange(t *testing.T) {
	prev := shapeOf([]string{"a"}, baseHistory())
	d := diffPromptShape(prev, shapeOf([]string{"a", "extra"}, baseHistory()))
	if fmt.Sprint(d.SystemChanged) != "[1]" {
		t.Errorf("SystemChanged = %v, want [1]", d.SystemChanged)
	}
}

// fakeShapeTool is a tools.Tool that carries only what a provider sees of one:
// a name, a description and a parameter schema.
type fakeShapeTool struct {
	name, desc string
	schema     *jsonschema.Schema
}

func (f fakeShapeTool) Name() string                    { return f.name }
func (f fakeShapeTool) Description() string             { return f.desc }
func (f fakeShapeTool) ParamSchema() *jsonschema.Schema { return f.schema }
func (f fakeShapeTool) RequiresPermission(*rctx.ToolContext, tools.ToolCall) (bool, error) {
	return false, nil
}
func (f fakeShapeTool) Run(*rctx.ToolContext, tools.ToolCall) (tools.ToolResponse, error) {
	return tools.ToolResponse{}, nil
}

func schemaWith(prop string) *jsonschema.Schema {
	s := &jsonschema.Schema{Type: "object", Properties: jsonschema.NewProperties()}
	s.Properties.Set(prop, &jsonschema.Schema{Type: "string"})
	return s
}

func TestHashTools_OrderDescriptionAndSchemaMatter(t *testing.T) {
	shell := fakeShapeTool{name: "shell", desc: "runs commands", schema: schemaWith("command")}
	view := fakeShapeTool{name: "view", desc: "reads files", schema: schemaWith("path")}

	base := hashTools([]tools.Tool{shell, view})
	if base != hashTools([]tools.Tool{shell, view}) {
		t.Fatal("hash is not deterministic")
	}
	if base == hashTools([]tools.Tool{view, shell}) {
		t.Error("tool order must change the hash: the cache breakpoint sits on the last tool")
	}
	reworded := fakeShapeTool{name: "view", desc: "reads files!", schema: schemaWith("path")}
	if base == hashTools([]tools.Tool{shell, reworded}) {
		t.Error("a changed description must change the hash")
	}
	reshaped := fakeShapeTool{name: "view", desc: "reads files", schema: schemaWith("file_path")}
	if base == hashTools([]tools.Tool{shell, reshaped}) {
		t.Error("a changed parameter schema must change the hash")
	}
	if hashTools(nil) == base {
		t.Error("an empty tool set must differ from a populated one")
	}
}

func TestDiffPromptShape_ToolSetChangeIsFlagged(t *testing.T) {
	a := fakeShapeTool{name: "shell", desc: "x", schema: schemaWith("c")}
	b := fakeShapeTool{name: "find_replace", desc: "y", schema: schemaWith("p")}
	prev := computePromptShape("m", []string{"s"}, baseHistory(), []tools.Tool{a})
	cur := computePromptShape("m", []string{"s"}, baseHistory(), []tools.Tool{a, b})
	d := diffPromptShape(prev, cur)
	if !d.ToolsChanged || cur.toolCount != 2 {
		t.Fatalf("ToolsChanged=%v toolCount=%d", d.ToolsChanged, cur.toolCount)
	}
	if d.DivergedAt != -1 || len(d.SystemChanged) != 0 {
		t.Fatalf("only the tools moved: %+v", d)
	}
}

func TestMessageHash_CoversToolCallsAndResults(t *testing.T) {
	call := func(input string) message.Message {
		return message.Message{Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "t1", Name: "shell", Input: input},
		}}
	}
	res := func(content string) message.Message {
		return message.Message{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "t1", Name: "shell", Content: content},
		}}
	}
	if hashMessage(msgPtr(call("ls"))) == hashMessage(msgPtr(call("pwd"))) {
		t.Error("tool call input must affect the hash")
	}
	if hashMessage(msgPtr(res("a"))) == hashMessage(msgPtr(res("b"))) {
		t.Error("tool result content must affect the hash")
	}
}

func msgPtr(m message.Message) *message.Message { return &m }

func TestPromptShapeTracker_FirstSightIsUnknownThenDiffs(t *testing.T) {
	tr := newPromptShapeTracker(4)
	first := tr.observe("chat|thread", shapeOf([]string{"s"}, baseHistory()))
	if first.Known {
		t.Fatal("first observation must be unknown: there is nothing to diff against")
	}
	second := tr.observe("chat|thread", shapeOf([]string{"s"}, append(baseHistory(), textMsg(message.User, "x"))))
	if !second.Known || second.DivergedAt != -1 {
		t.Fatalf("second observation: %+v", second)
	}
}

func TestPromptShapeTracker_IsBoundedAndPerThread(t *testing.T) {
	tr := newPromptShapeTracker(2)
	tr.observe("a", shapeOf(nil, baseHistory()))
	tr.observe("b", shapeOf(nil, baseHistory()))
	tr.observe("c", shapeOf(nil, baseHistory())) // evicts "a"

	if tr.observe("a", shapeOf(nil, baseHistory())).Known {
		t.Error("evicted thread should read as unknown")
	}
	if len(tr.last) > 2 {
		t.Errorf("tracker holds %d entries, limit is 2", len(tr.last))
	}
	if !tr.observe("c", shapeOf(nil, baseHistory())).Known {
		t.Error("a thread still tracked should diff")
	}
}

// The log must carry hashes and structure, never prompt content.
// kv renders slog-style alternating key/values as "k=v k=v" so assertions read
// the way the log line does.
func kv(fields []any) string {
	var b strings.Builder
	for i := 0; i+1 < len(fields); i += 2 {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%v=%v", fields[i], fields[i+1])
	}
	return b.String()
}

func TestPromptShape_LogFieldsHoldNoContent(t *testing.T) {
	secret := "SUPER-SECRET-FILE-CONTENTS"
	h := append(baseHistory(), textMsg(message.User, secret))
	shape := computePromptShape("m", []string{"system " + secret}, h, nil)
	prev := computePromptShape("m", []string{"system " + secret}, baseHistory(), nil)

	out := kv(shape.logFields(diffPromptShape(prev, shape)))
	if strings.Contains(out, secret) || strings.Contains(out, "forge notes") {
		t.Fatalf("log fields leak prompt content: %s", out)
	}
	for _, want := range []string{"toolsHash", "systemHashes", "historyMsgs", "divergedAt", "sharedPrefixMsgs"} {
		if !strings.Contains(out, want) {
			t.Errorf("log fields missing %q: %s", want, out)
		}
	}
}

func TestPromptShape_UnknownDiffOmitsComparisonFields(t *testing.T) {
	shape := shapeOf([]string{"s"}, baseHistory())
	out := kv(shape.logFields(shapeDiff{}))
	if strings.Contains(out, "divergedAt") || !strings.Contains(out, "shapeKnown=false") {
		t.Fatalf("unknown diff should report only shapeKnown=false and the hashes: %s", out)
	}
}
