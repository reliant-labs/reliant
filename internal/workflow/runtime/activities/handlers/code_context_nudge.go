// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
	"sync"

	"mvdan.cc/sh/v3/syntax"

	"github.com/reliant-labs/reliant/internal/llm/tools/names"
)

// Reactive nudge toward the code_context tool.
//
// WHY THIS EXISTS, AND WHY IT IS NOT A PROMPT
//
// Telling an agent to use a navigation tool does not work. Measured twice on
// this exact capability: `gopls call_hierarchy` was documented in the shell tool
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
// toolOutput is the result the command produced. It is evidence, not
// decoration: for a search that names no file type, the files its hits came
// from are the only honest answer to "did this search Go or TypeScript?".
//
// Called on the success path only: a failed command's output is about the
// failure, and a suggestion there competes with the error the agent needs to read.
func maybeCodeContextNudge(toolName, toolInput, toolOutput, threadID string) string {
	if !isShellToolName(toolName) {
		return ""
	}
	symbol := symbolFromShellCommand(shellCommandFromInput(toolInput), shellStdout(toolOutput))
	if symbol == "" {
		return ""
	}
	if !codeContextNudges.shouldNudge(threadID) {
		return ""
	}
	return fmt.Sprintf(codeContextNudgeText, symbol)
}

// isShellToolName reports whether name is the shell tool.
func isShellToolName(name string) bool {
	return name == names.ToolShell
}

// symbolFromShellCommand returns the symbol a command looks up, or "" when it
// is not a lookup code_context can answer. output is the command's stdout.
//
// Deliberately conservative. A false positive trains the reader to skip the
// note, which costs more than the missed hint it was trying to buy. One search
// in the command must pass EVERY clause below, and each clause is there
// because a real command fired without it:
//
//  1. The pattern is ONE bare identifier with a capital in it. Alternations,
//     regex metacharacters, `func X` declarations and repeated -e are not
//     lookups of one symbol — `rg -v 'page_size|page_token|order_by'` fired
//     with "page_token". An all-lowercase identifier is a lone word or
//     snake_case, which in Go and TS is a column, proto field or JSON key.
//  2. It is not inverted. `rg -v X` lists the lines that do NOT mention X.
//  3. It searches files, not piped input. `... | rg Foo` filters another
//     command's output; there is nothing for a language server to resolve.
//  4. What it searches is Go/TS/JS. Paths and -t/-g/--include scopes decide
//     when they name file types, and any other file type vetoes — the measured
//     case targeted `proto/services/*/v1/*.proto` through a loop variable.
//     A search that names no file type at all (`rg Foo .`, `rg Foo proto/`)
//     qualifies only when its hits are in Go/TS/JS files: a repo-wide grep
//     whose matches are all `.proto` is not something code_context resolves.
//
// The command is parsed as shell, not pattern-matched as text: quoting, pipes,
// loops and command substitution decide which word is the pattern and which
// are paths, and a regex over the raw command cannot tell them apart.
func symbolFromShellCommand(command, output string) string {
	if strings.TrimSpace(command) == "" {
		return ""
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return "" // not shell we can read; say nothing rather than guess
	}
	for _, search := range searchInvocations(file) {
		if sym := search.lookupSymbol(output); sym != "" {
			return sym
		}
	}
	return ""
}

// searchInvocation is one rg / grep / git grep call, as the nudge reads it.
type searchInvocation struct {
	patterns   []string // every pattern searched for: each -e, else the first operand
	opaque     bool     // a pattern that is not a literal word, or patterns read from a file
	inverted   bool     // -v / --invert-match
	readsStdin bool     // filters piped input instead of searching files
	types      []string // positive -t/--type scopes (rg type names)
	globs      []string // positive -g/--glob/--include scopes
	targets    []string // path operands, loop variables expanded
}

// lookupSymbol applies the rule documented on symbolFromShellCommand.
func (s searchInvocation) lookupSymbol(output string) string {
	if s.opaque || s.inverted || s.readsStdin || len(s.patterns) != 1 {
		return ""
	}
	sym := s.patterns[0]
	if !bareIdentifierRe.MatchString(sym) || !isPlausibleSymbol(sym) {
		return ""
	}
	switch s.targetLanguage() {
	case targetResolvable:
		return sym
	case targetUnscoped:
		if outputNamesResolvableSource(output) {
			return sym
		}
	}
	return ""
}

// targetLanguage is what a search's paths and scopes say about the files it reads.
type targetLanguage int

const (
	targetUnscoped   targetLanguage = iota // names no file type
	targetResolvable                       // names only Go/TS/JS files
	targetOther                            // names some other file type
)

func (s searchInvocation) targetLanguage() targetLanguage {
	var resolvable, other bool
	note := func(ok bool) {
		if ok {
			resolvable = true
		} else {
			other = true
		}
	}
	for _, t := range s.types {
		note(resolvableRipgrepTypes[strings.ToLower(t)])
	}
	for _, p := range append(append([]string(nil), s.globs...), s.targets...) {
		for _, ext := range pathExtensions(p) {
			note(resolvableExtensions["."+ext])
		}
	}
	switch {
	case other:
		return targetOther
	case resolvable:
		return targetResolvable
	}
	return targetUnscoped
}

// resolvableExtensions are the languages code_context can actually resolve.
// Nudging toward it for Python or Ruby would send the agent to a tool that
// degrades to the same text search it just ran.
var resolvableExtensions = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".mts": true, ".cts": true, ".mjs": true, ".cjs": true,
}

// resolvableRipgrepTypes are rg's built-in type names covering those files
// (`rg --type-list`).
var resolvableRipgrepTypes = map[string]bool{
	"go": true, "ts": true, "typescript": true, "js": true,
}

// pathExtensions returns the lower-cased file extensions a path or glob names:
// "go" for internal/x.go or *.go, "ts","tsx" for *.{ts,tsx}, nothing for a
// directory, a dotfile or a wildcard extension.
func pathExtensions(p string) []string {
	base := path.Base(strings.TrimSuffix(p, "/"))
	dot := strings.LastIndexByte(base, '.')
	if dot <= 0 || dot == len(base)-1 {
		return nil
	}
	ext := strings.ToLower(base[dot+1:])
	if strings.HasPrefix(ext, "{") && strings.HasSuffix(ext, "}") {
		return strings.Split(strings.Trim(ext, "{}"), ",")
	}
	if strings.ContainsAny(ext, "*?[") {
		return nil
	}
	return []string{ext}
}

// outputScanLines bounds how much of a result is read for file evidence.
const outputScanLines = 200

// outputNamesResolvableSource reports whether a search's printed hits include a
// Go/TS/JS file. rg and grep print `path:line:text`, `path:text`, or a bare path
// under -l; a line whose leading field has whitespace in it is match text, not a
// path, and is skipped.
func outputNamesResolvableSource(stdout string) bool {
	for i, line := range strings.SplitN(stdout, "\n", outputScanLines+1) {
		if i == outputScanLines {
			break
		}
		field := line
		if colon := strings.IndexByte(line, ':'); colon >= 0 {
			field = line[:colon]
		}
		if field == "" || strings.ContainsAny(field, " \t") {
			continue
		}
		if resolvableExtensions[strings.ToLower(path.Ext(field))] {
			return true
		}
	}
	return false
}

// searchInvocations finds every rg / grep / git grep call in a parsed command.
func searchInvocations(file *syntax.File) []searchInvocation {
	// First pass: what each for-loop variable iterates over, and which calls
	// read a pipe. Both are facts about the surrounding shell that a call's
	// own words do not carry.
	loopVars := map[string][]string{}
	piped := map[*syntax.CallExpr]bool{}
	syntax.Walk(file, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.ForClause:
			if iter, ok := n.Loop.(*syntax.WordIter); ok && iter.Name != nil {
				for _, item := range iter.Items {
					loopVars[iter.Name.Value] = append(loopVars[iter.Name.Value], wordValues(item, nil)...)
				}
			}
		case *syntax.BinaryCmd:
			if n.Op == syntax.Pipe || n.Op == syntax.PipeAll {
				syntax.Walk(n.Y, func(inner syntax.Node) bool {
					if call, ok := inner.(*syntax.CallExpr); ok {
						piped[call] = true
					}
					return true
				})
			}
		}
		return true
	})

	var out []searchInvocation
	syntax.Walk(file, func(node syntax.Node) bool {
		if call, ok := node.(*syntax.CallExpr); ok {
			if search, ok := parseSearch(call, loopVars, piped[call]); ok {
				out = append(out, search)
			}
		}
		return true
	})
	return out
}

// searchFlags describes how one search tool spells its options.
type searchFlags struct {
	shortValue         string          // short flags that consume a value
	longValue          map[string]bool // long flags (sans --) that consume a value
	recursiveByDefault bool            // searches the working directory when given no path
	neverStdin         bool            // never reads piped input (git grep)
}

func flagSet(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

var (
	ripgrepFlags = searchFlags{
		shortValue: "ABCdEefgjMmrtT",
		longValue: flagSet("after-context", "before-context", "context", "max-depth", "maxdepth",
			"encoding", "regexp", "file", "glob", "iglob", "threads", "max-columns", "max-count",
			"replace", "type", "type-not", "type-add", "type-clear", "sort", "sortr", "color", "colors",
			"context-separator", "field-context-separator", "field-match-separator", "path-separator",
			"max-filesize", "pre", "pre-glob", "dfa-size-limit", "regex-size-limit", "engine",
			"ignore-file", "hyperlink-format", "generate"),
		recursiveByDefault: true,
	}
	grepFlags = searchFlags{
		shortValue: "ABCDdefm",
		longValue: flagSet("after-context", "before-context", "context", "devices", "directories",
			"regexp", "file", "max-count", "include", "exclude", "exclude-dir", "exclude-from",
			"label", "binary-files", "group-separator"),
	}
	gitGrepFlags = searchFlags{
		shortValue:         "ABCefm",
		longValue:          flagSet("after-context", "before-context", "context", "regexp", "file", "max-count", "max-depth", "threads"),
		recursiveByDefault: true,
		neverStdin:         true,
	}
)

// parseSearch reads one call as a search, or reports ok=false when the call is
// not rg / grep / git grep (after stripping wrappers like env, timeout, xargs).
func parseSearch(call *syntax.CallExpr, loopVars map[string][]string, piped bool) (searchInvocation, bool) {
	args, viaXargs := stripCommandWrappers(call.Args)
	if len(args) == 0 {
		return searchInvocation{}, false
	}
	name, _ := literalWord(args[0])
	var flags searchFlags
	switch path.Base(name) {
	case "rg":
		flags = ripgrepFlags
	case "grep", "egrep", "fgrep":
		flags = grepFlags
	case "git":
		if len(args) < 2 {
			return searchInvocation{}, false
		}
		if sub, _ := literalWord(args[1]); sub != "grep" {
			return searchInvocation{}, false
		}
		flags = gitGrepFlags
		args = args[1:]
	default:
		return searchInvocation{}, false
	}

	var (
		search    searchInvocation
		operands  []*syntax.Word
		recursive = flags.recursiveByDefault
		listFiles bool
		endOfOpts bool
	)
	addPattern := func(value string, word *syntax.Word) {
		if word != nil {
			if lit, ok := literalWord(word); ok {
				value = lit
			} else {
				search.opaque = true
			}
		}
		search.patterns = append(search.patterns, value)
	}
	apply := func(flag, value string, word *syntax.Word) {
		switch flag {
		case "e", "regexp":
			addPattern(value, word)
		case "f", "file":
			search.opaque = true
		case "v", "invert-match":
			search.inverted = true
		case "t", "type":
			search.types = append(search.types, value)
		case "g", "glob", "iglob", "include":
			if !strings.HasPrefix(value, "!") {
				search.globs = append(search.globs, value)
			}
		case "r", "R", "recursive", "dereference-recursive":
			recursive = true
		case "files", "type-list":
			listFiles = true
		}
	}

	for i := 1; i < len(args); i++ {
		lit, isLit := literalWord(args[i])
		if endOfOpts || !isLit || len(lit) < 2 || lit[0] != '-' {
			operands = append(operands, args[i])
			continue
		}
		if lit == "--" {
			endOfOpts = true
			continue
		}
		if strings.HasPrefix(lit, "--") {
			flag, value, hasValue := strings.Cut(lit[2:], "=")
			var word *syntax.Word
			if flags.longValue[flag] && !hasValue && i+1 < len(args) {
				i++
				word = args[i]
				value, _ = literalWord(word)
			}
			apply(flag, value, word)
			continue
		}
		bundle := lit[1:]
		for j := 0; j < len(bundle); j++ {
			flag := bundle[j : j+1]
			if strings.IndexByte(flags.shortValue, bundle[j]) < 0 {
				apply(flag, "", nil)
				continue
			}
			// The first value-taking flag in a bundle takes the rest of the
			// token (-B5, -tgo), or the next word when nothing is left.
			value := bundle[j+1:]
			var word *syntax.Word
			if value == "" && i+1 < len(args) {
				i++
				word = args[i]
				value, _ = literalWord(word)
			}
			apply(flag, value, word)
			break
		}
	}

	if listFiles {
		return searchInvocation{}, false // lists files; there is no pattern
	}
	paths := operands
	if len(search.patterns) == 0 {
		if len(operands) == 0 {
			return searchInvocation{}, false
		}
		addPattern("", operands[0])
		paths = operands[1:]
	}
	for _, p := range paths {
		search.targets = append(search.targets, wordValues(p, loopVars)...)
	}
	if len(paths) == 0 && !viaXargs && !flags.neverStdin {
		// rg reads a pipe it is given; grep without -r reads stdin always.
		search.readsStdin = piped || !recursive
	}
	return search, true
}

// stripCommandWrappers drops commands that run another command, returning the
// wrapped argv and whether xargs supplies its trailing arguments.
func stripCommandWrappers(args []*syntax.Word) ([]*syntax.Word, bool) {
	viaXargs := false
	for len(args) > 0 {
		name, _ := literalWord(args[0])
		switch path.Base(name) {
		case "env", "command", "builtin", "exec", "nice", "nohup", "sudo":
			args = args[1:]
			// env FOO=bar, nice -n 5: skip options and assignments.
			for len(args) > 0 {
				lit, _ := literalWord(args[0])
				if !strings.HasPrefix(lit, "-") && !strings.Contains(lit, "=") {
					break
				}
				args = args[1:]
			}
		case "timeout":
			args = args[1:]
			for len(args) > 0 {
				lit, _ := literalWord(args[0])
				args = args[1:]
				if !strings.HasPrefix(lit, "-") {
					break // the duration
				}
			}
		case "xargs":
			viaXargs = true
			args = args[1:]
			for len(args) > 0 {
				lit, _ := literalWord(args[0])
				if !strings.HasPrefix(lit, "-") {
					break
				}
				args = args[1:]
				if len(lit) == 2 && strings.ContainsRune("nIPLsdEa", rune(lit[1])) && len(args) > 0 {
					args = args[1:] // -n 1, -I {}, -P 4 ...
				}
			}
		default:
			return args, viaXargs
		}
	}
	return args, viaXargs
}

// literalWord returns a word's value when it is fully literal — plain,
// single-quoted or double-quoted text with no expansion in it.
func literalWord(w *syntax.Word) (string, bool) {
	if w == nil {
		return "", false
	}
	return literalParts(w.Parts)
}

func literalParts(parts []syntax.WordPart) (string, bool) {
	var b strings.Builder
	for _, part := range parts {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(p.Value)
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			s, ok := literalParts(p.Parts)
			if !ok {
				return "", false
			}
			b.WriteString(s)
		default:
			return "", false
		}
	}
	return b.String(), true
}

// maxWordValues caps loop-variable expansion; a path operand naming more
// alternatives than this carries no more evidence, only more work.
const maxWordValues = 32

// wordValues returns the values a word can take: its literal text, with each
// `$var` bound by an enclosing for-loop replaced by that loop's items. Any
// other expansion contributes nothing, so `$dir/*.go` still reads as *.go.
func wordValues(w *syntax.Word, loopVars map[string][]string) []string {
	return partValues(w.Parts, loopVars)
}

func partValues(parts []syntax.WordPart, loopVars map[string][]string) []string {
	values := []string{""}
	for _, part := range parts {
		var alternatives []string
		switch p := part.(type) {
		case *syntax.Lit:
			alternatives = []string{p.Value}
		case *syntax.SglQuoted:
			alternatives = []string{p.Value}
		case *syntax.DblQuoted:
			alternatives = partValues(p.Parts, loopVars)
		case *syntax.ParamExp:
			if p.Param != nil {
				alternatives = loopVars[p.Param.Value]
			}
		}
		if len(alternatives) == 0 {
			alternatives = []string{""}
		}
		var next []string
		for _, v := range values {
			for _, alt := range alternatives {
				if len(next) < maxWordValues {
					next = append(next, v+alt)
				}
			}
		}
		values = next
	}
	return values
}

// identifier matches a bare code identifier.
const identifier = `[A-Za-z_][A-Za-z0-9_]*`

var bareIdentifierRe = regexp.MustCompile(`^` + identifier + `$`)

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
	// No capital means a lone word ("workflow", "approval") or snake_case. In
	// Go and TypeScript snake_case is a SQL column, proto field, JSON key or
	// env var — `page_token` in the measured false positive — not a symbol a
	// language server resolves.
	if strings.ToLower(s) == s {
		return false
	}
	return true
}

// shellStdout extracts stdout from a shell tool result. The shell tool returns
// a JSON document; anything else is treated as the output itself.
func shellStdout(toolOutput string) string {
	var out struct {
		Stdout *string `json:"stdout"`
	}
	if json.Unmarshal([]byte(toolOutput), &out) == nil && out.Stdout != nil {
		return *out.Stdout
	}
	return toolOutput
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
