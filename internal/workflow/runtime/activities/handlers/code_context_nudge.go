// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/reliant-labs/reliant/internal/llm/tools/names"
)

// Reactive nudge toward the code_context tool.
//
// WHY THIS EXISTS, AND WHY IT IS NOT A PROMPT
//
// Telling an agent to use a navigation tool does not work. Measured twice on
// this exact capability: `gopls call_hierarchy` was documented in the bash tool
// description and named explicitly in a spawn prompt, and was used 0 times in
// 166 shell calls. `code_context` was then registered as a DEFAULT tool, with a
// 40-line description, and a fresh sub-agent used it 0 times in 76 calls while
// running `rg -n 'UpdateWorkflowName'`, `rg -l 'TransitionChatOnCompletion'` and
// `rg -n 'CancelChatToolCalls'` — three searches the tool answers directly.
//
// Static instruction has now failed twice. What demonstrably DOES land is a hint
// attached to a result the agent is already reading, at the moment it acted:
// the "→ depth=3 traces these callers" line inside code_context's own output.
// So this is that same mechanism, moved to where agents actually are.
//
// The nudge appends one line to the shell tool's RESULT. It costs no turn, no
// message, and no model call. It fires only on a search that is shaped like a
// symbol lookup, and it gives up quickly rather than nagging.

// codeContextNudgeText is appended to a qualifying shell result.
//
// Wording is deliberately DIRECTIVE, not informational. The first version was a
// mild "answers this in one call" observation; measured against a real agent it
// took TWO deliveries before it switched, and it spent 47 more shell calls in
// between. The note has to read as an instruction for the next action, not as a
// fact about a tool that exists.
//
// It names the symbol just searched for and states what grep cannot do, because
// a generic rule reads as boilerplate and gets skipped.
const codeContextNudgeText = "\n\n[IMPORTANT] This grep is a symbol lookup. Run " +
	"`code_context(symbol: \"%s\")` NOW instead of grepping further — one call returns " +
	"the definition, its source, every caller and callee (3 levels deep), and the " +
	"interfaces involved.\n" +
	"Your grep CANNOT answer \"who calls this\": a call site never names its receiver's " +
	"type, so it matches every same-named method and the error compounds at each hop. " +
	"Continuing to grep costs a turn per hop for a worse answer."

const (
	// nudgeCooldownTurns suppresses repeats so the hint stays a signal. The
	// first qualifying search fires immediately — that is when redirecting is
	// cheapest, before the agent has committed to a grep-shaped plan.
	nudgeCooldownTurns = 10

	// nudgeMaxPerThread stops permanently after this many. Ignored twice means
	// it is being tuned out, and a third is pure noise in the transcript.
	nudgeMaxPerThread = 2
)

// nudgeState tracks, per thread, how often the hint has been shown.
//
// Keyed by THREAD, not chat: threads are inlined onto a single Temporal
// workflow, and a spawned sub-agent must get its own budget rather than
// inheriting an exhausted parent's. In-memory is sufficient — a worker restart
// resets the budget, whose worst case is one extra hint.
type nudgeState struct {
	mu      sync.Mutex
	shown   map[string]int // threadID -> times shown
	lastAt  map[string]int // threadID -> call index of last hint
	callSeq map[string]int // threadID -> qualifying-call counter
}

var codeContextNudges = &nudgeState{
	shown:   map[string]int{},
	lastAt:  map[string]int{},
	callSeq: map[string]int{},
}

// shouldNudge reports whether to show the hint for this thread, and records it.
func (s *nudgeState) shouldNudge(threadID string) bool {
	if threadID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.callSeq[threadID]++
	seq := s.callSeq[threadID]

	if s.shown[threadID] >= nudgeMaxPerThread {
		return false
	}
	// First qualifying call in the thread always fires.
	if s.shown[threadID] > 0 && seq-s.lastAt[threadID] < nudgeCooldownTurns {
		return false
	}
	s.shown[threadID]++
	s.lastAt[threadID] = seq
	return true
}

// resetNudgeState clears tracking. Test-only.
func resetNudgeState() {
	codeContextNudges.mu.Lock()
	defer codeContextNudges.mu.Unlock()
	codeContextNudges.shown = map[string]int{}
	codeContextNudges.lastAt = map[string]int{}
	codeContextNudges.callSeq = map[string]int{}
}

// maybeCodeContextNudge returns the hint to append to a shell result, or "".
//
// Called on the success path only: a failed command's output is about the
// failure, and a suggestion there competes with the error the agent needs to read.
func maybeCodeContextNudge(toolName, toolInput, threadID string) string {
	if !isShellToolName(toolName) {
		return ""
	}
	symbol := symbolFromShellCommand(shellCommandFromInput(toolInput))
	if symbol == "" {
		return ""
	}
	if !codeContextNudges.shouldNudge(threadID) {
		return ""
	}
	return fmt.Sprintf(codeContextNudgeText, symbol)
}

// isShellToolName covers both platform spellings of the shell tool.
func isShellToolName(name string) bool {
	return name == names.ToolBash || name == names.ToolPowerShell
}

// identifier matches a bare code identifier.
const identifier = `[A-Za-z_][A-Za-z0-9_]*`

// symbolSearchShapes are the search patterns that mean "find me this symbol".
//
// Derived from a real transcript rather than invented: of 21 searches a
// sub-agent ran while orienting in unfamiliar code, 16 matched one of these.
var symbolSearchShapes = []*regexp.Regexp{
	// rg 'CancelChatToolCalls'
	regexp.MustCompile(`^` + identifier + `$`),
	// rg 'workflow_name|WorkflowName'  — spellings of one concept
	regexp.MustCompile(`^` + identifier + `(\|` + identifier + `)+$`),
	// rg 'CreateWorkflow\(ctx'  — call sites
	regexp.MustCompile(`^` + identifier + `\\\(`),
	// rg 'func ValidateInputs'  — the declaration
	regexp.MustCompile(`^func\s+.*` + identifier),
}

// searchPatternRe pulls the quoted pattern out of an rg/grep invocation.
var searchPatternRe = regexp.MustCompile(`\b(?:rg|grep)\b[^;|]*?'([^']+)'|\b(?:rg|grep)\b[^;|]*?"([^"]+)"`)

// resolvableExtensions are the languages code_context can actually resolve.
// Nudging toward it for Python or Ruby would send the agent to a tool that
// degrades to the same text search it just ran.
var resolvableExtensions = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".mts": true, ".cts": true, ".mjs": true, ".cjs": true,
}

// symbolFromShellCommand returns the symbol a command is searching for, or ""
// when the command is not a symbol lookup.
//
// Deliberately conservative. A false positive trains the reader to skip the
// note, which costs more than the missed hint it was trying to buy.
func symbolFromShellCommand(command string) string {
	if command == "" {
		return ""
	}
	// A command that already scopes to a non-resolvable language is not a
	// candidate; one naming a Go/TS path is a strong signal.
	if named := namedExtensions(command); len(named) > 0 && !anyResolvable(named) {
		return ""
	}

	for _, m := range searchPatternRe.FindAllStringSubmatch(command, -1) {
		pattern := m[1]
		if pattern == "" {
			pattern = m[2]
		}
		if pattern == "" {
			continue
		}
		for _, shape := range symbolSearchShapes {
			if !shape.MatchString(pattern) {
				continue
			}
			if sym := primarySymbol(pattern); sym != "" {
				return sym
			}
		}
	}
	return ""
}

// primarySymbol reduces a matched pattern to the identifier to suggest.
func primarySymbol(pattern string) string {
	pattern = strings.TrimPrefix(pattern, "func ")
	pattern = strings.TrimSpace(pattern)
	if i := strings.Index(pattern, `\(`); i > 0 {
		pattern = pattern[:i]
	}
	// For an alternation, prefer the spelling that looks like a code
	// identifier: `workflow_name|WorkflowName` is one concept, and the Go
	// symbol is the one a language server can resolve.
	best := ""
	for _, part := range strings.Split(pattern, "|") {
		part = strings.TrimSpace(part)
		if !bareIdentifierRe.MatchString(part) {
			continue
		}
		if best == "" || identifierScore(part) > identifierScore(best) {
			best = part
		}
	}
	if !isPlausibleSymbol(best) {
		return ""
	}
	return best
}

// identifierScore prefers camelCase/PascalCase over snake_case or a lone word,
// because the former is what a Go or TypeScript declaration actually looks like.
func identifierScore(s string) int {
	score := len(s)
	if strings.ToLower(s) != s {
		score += 100 // has an interior capital: camelCase or PascalCase
	}
	return score
}

// commonWords are frequent free-text greps that happen to be valid identifiers.
// `rg -n 'TODO'` is a full-text search, and suggesting a symbol lookup for it is
// the false positive that teaches a reader to skip the note entirely.
var commonWords = map[string]bool{
	"todo": true, "fixme": true, "hack": true, "xxx": true, "note": true,
	"error": true, "errors": true, "warn": true, "warning": true, "debug": true,
	"true": true, "false": true, "nil": true, "null": true, "return": true,
	"func": true, "type": true, "struct": true, "interface": true, "import": true,
	"package": true, "const": true, "var": true, "test": true, "tests": true,
}

// isPlausibleSymbol rejects names that are not worth a language-server lookup.
func isPlausibleSymbol(s string) bool {
	if len(s) < 4 {
		return false // "id", "ok", "cfg"
	}
	if commonWords[strings.ToLower(s)] {
		return false
	}
	// A single all-lowercase word with no separator is usually prose
	// ("workflow", "approval"), not a specific symbol worth resolving.
	if strings.ToLower(s) == s && !strings.Contains(s, "_") {
		return false
	}
	return true
}

var bareIdentifierRe = regexp.MustCompile(`^` + identifier + `$`)

var extensionRe = regexp.MustCompile(`\.[A-Za-z]{1,4}\b`)

func namedExtensions(command string) []string {
	var out []string
	for _, m := range extensionRe.FindAllString(command, -1) {
		out = append(out, strings.ToLower(m))
	}
	return out
}

func anyResolvable(exts []string) bool {
	for _, e := range exts {
		if resolvableExtensions[filepath.Ext("x"+e)] || resolvableExtensions[e] {
			return true
		}
	}
	return false
}

// shellCommandFromInput pulls the command out of a shell tool's JSON arguments.
func shellCommandFromInput(toolInput string) string {
	if toolInput == "" {
		return ""
	}
	var args struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(toolInput), &args) != nil {
		return ""
	}
	return args.Command
}
