// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"

	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// A prompt cache is a prefix match: tools, then system, then history, in that
// order. A turn that writes most of a 400k prompt back to the cache instead of
// reading it changed something EARLY in that order, and the Usage line alone
// cannot say what. promptShape is a content-free fingerprint of one request,
// and promptShapeTracker compares it with the previous request of the same
// thread, so the Usage line can name the section that moved and where.
//
// Hashes only, never content: the prompt holds user code, file contents and
// memory, none of which belongs in logs.

// promptShape fingerprints one request, section by section.
type promptShape struct {
	model     string
	toolsHash uint64
	toolCount int
	system    []uint64 // one hash per system prompt, in request order
	msgs      []uint64 // one hash per history message, in request order
	kinds     []string // coarse kind per history message, parallel to msgs
}

func computePromptShape(model string, systemPrompts []string, history []message.Message, toolList []tools.Tool) promptShape {
	s := promptShape{
		model:     model,
		toolsHash: hashTools(toolList),
		toolCount: len(toolList),
		system:    make([]uint64, len(systemPrompts)),
		msgs:      make([]uint64, len(history)),
		kinds:     make([]string, len(history)),
	}
	for i, p := range systemPrompts {
		s.system[i] = hashString(p)
	}
	for i := range history {
		s.msgs[i] = hashMessage(&history[i])
		s.kinds[i] = messageKind(&history[i])
	}
	return s
}

func hashString(parts ...string) uint64 {
	h := fnv.New64a()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return h.Sum64()
}

// hashTools covers everything a provider sees of a tool: name, description and
// parameter schema, in order. Order matters — the cache breakpoint sits on the
// last tool, so a reordered array is a different prefix.
func hashTools(toolList []tools.Tool) uint64 {
	h := fnv.New64a()
	for _, t := range toolList {
		_, _ = h.Write([]byte(t.Name()))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(t.Description()))
		_, _ = h.Write([]byte{0})
		if schema := t.ParamSchema(); schema != nil {
			if raw, err := json.Marshal(schema); err == nil {
				_, _ = h.Write(raw)
			}
		}
		_, _ = h.Write([]byte{1})
	}
	return h.Sum64()
}

func hashMessage(m *message.Message) uint64 {
	h := fnv.New64a()
	w := func(s string) {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	w(string(m.Role))
	for _, part := range m.Parts {
		switch p := part.(type) {
		case message.TextContent:
			w("text")
			w(p.Text)
			w(p.Phase)
		case message.ReasoningContent:
			w("reasoning")
			w(p.Thinking)
			w(p.Signature)
		case message.ImageURLContent:
			w("image")
			w(p.URL)
			w(p.Detail)
		case message.BinaryContent:
			w("binary")
			w(p.Path)
			w(p.MIMEType)
			w(fmt.Sprint(len(p.Data)))
		case message.ToolCall:
			w("toolcall")
			w(p.ID)
			w(p.Name)
			w(p.Input)
		case message.ToolResult:
			w("toolresult")
			w(p.ToolCallID)
			w(p.Name)
			w(p.Content)
			w(fmt.Sprint(p.IsError, len(p.BinaryParts)))
		default:
			w(fmt.Sprintf("%T", part))
		}
	}
	return h.Sum64()
}

// messageKind names what a history message IS, coarsely, so a divergence can be
// reported as "the project memory changed" rather than "message 0 changed".
// The prefixes are the ones the prompt builder writes; anything else reports
// its role.
func messageKind(m *message.Message) string {
	var text string
	for _, part := range m.Parts {
		if tc, ok := part.(message.TextContent); ok {
			text = tc.Text
			break
		}
	}
	switch {
	case strings.HasPrefix(text, "# User defined rules"):
		return "memory"
	case strings.HasPrefix(text, "<system-memory"):
		return "repo-memory"
	case strings.HasPrefix(text, "<preloaded-skills>"):
		return "skills-seed"
	}
	return string(m.Role)
}

// shapeDiff is how a request differs from the previous one on the same thread.
type shapeDiff struct {
	// Known is false for the first request seen by this process (a new thread,
	// or the activity ran on another replica). Everything below is then zero.
	Known bool

	ModelChanged  bool
	ToolsChanged  bool
	SystemChanged []int // indexes of system prompts whose text differs

	// SharedPrefix is how many leading history messages are identical to the
	// previous request's. History that merely grew has SharedPrefix equal to the
	// previous length and DivergedAt -1 — the healthy case.
	SharedPrefix int
	// DivergedAt is the first history index whose content differs, or -1 if the
	// previous history is a prefix of this one.
	DivergedAt int
	// DivergedKind is what the message at DivergedAt was in the PREVIOUS
	// request ("memory", "skills-seed", "user", ...).
	DivergedKind string
}

func diffPromptShape(prev, cur promptShape) shapeDiff {
	d := shapeDiff{Known: true, DivergedAt: -1}
	d.ModelChanged = prev.model != cur.model
	d.ToolsChanged = prev.toolsHash != cur.toolsHash
	n := len(prev.system)
	if len(cur.system) > n {
		n = len(cur.system)
	}
	for i := 0; i < n; i++ {
		if i >= len(prev.system) || i >= len(cur.system) || prev.system[i] != cur.system[i] {
			d.SystemChanged = append(d.SystemChanged, i)
		}
	}
	shared := 0
	for shared < len(prev.msgs) && shared < len(cur.msgs) && prev.msgs[shared] == cur.msgs[shared] {
		shared++
	}
	d.SharedPrefix = shared
	if shared < len(prev.msgs) {
		d.DivergedAt = shared
		d.DivergedKind = prev.kinds[shared]
	}
	return d
}

// logFields renders the shape and its diff as slog key/values for the Usage
// line. The hashes let two lines be compared by eye; the diff answers the
// question directly.
func (s promptShape) logFields(d shapeDiff) []any {
	systemHashes := make([]string, len(s.system))
	for i, h := range s.system {
		systemHashes[i] = fmt.Sprintf("%08x", uint32(h))
	}
	fields := []any{
		"toolsHash", fmt.Sprintf("%08x", uint32(s.toolsHash)),
		"toolCount", s.toolCount,
		"systemHashes", strings.Join(systemHashes, ","),
		"historyMsgs", len(s.msgs),
		"shapeKnown", d.Known,
	}
	if !d.Known {
		return fields
	}
	fields = append(fields,
		"modelChanged", d.ModelChanged,
		"toolsChanged", d.ToolsChanged,
		"systemChanged", fmt.Sprint(d.SystemChanged),
		"sharedPrefixMsgs", d.SharedPrefix,
		"divergedAt", d.DivergedAt,
		"divergedKind", d.DivergedKind,
	)
	return fields
}

// promptShapeTracker remembers the last shape per thread, in this process only.
// It is a diagnostic: when the next request lands on a different replica the
// diff is simply unknown, which the log says. Bounded so a long-lived worker
// cannot grow without limit.
type promptShapeTracker struct {
	mu    sync.Mutex
	limit int
	last  map[string]promptShape
	order []string
}

func newPromptShapeTracker(limit int) *promptShapeTracker {
	return &promptShapeTracker{limit: limit, last: make(map[string]promptShape, limit)}
}

// observe records cur as the latest shape for key and returns how it differs
// from the one before it.
func (t *promptShapeTracker) observe(key string, cur promptShape) shapeDiff {
	t.mu.Lock()
	defer t.mu.Unlock()
	prev, ok := t.last[key]
	if !ok {
		if len(t.order) >= t.limit {
			oldest := t.order[0]
			t.order = t.order[1:]
			delete(t.last, oldest)
		}
		t.order = append(t.order, key)
	}
	t.last[key] = cur
	if !ok {
		return shapeDiff{}
	}
	return diffPromptShape(prev, cur)
}

// callLLMShapes is shared by every CallLLMActivity in the process.
var callLLMShapes = newPromptShapeTracker(512)
