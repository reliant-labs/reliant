// Copyright (c) 2025 Reliant Labs
package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Engines for code_context. Each answers the same questions for one language
// family, so adding a language is adding an engine rather than adding a branch
// to the tool.
//
// Two engines resolve REAL edges (gopls for Go, tsserver for TypeScript and
// JavaScript). Both are the toolchain a developer already has: gopls ships with
// Go tooling, and tsserver ships INSIDE every TypeScript project's
// node_modules. Neither asks the user to install something new, which is what
// makes this work on a customer's machine and not just ours.
//
// The third engine is text-based and exists so an unsupported language degrades
// to something useful instead of nothing — but it announces that it is
// approximate, because an unlabeled guess about "who calls this" is worse than
// no answer.

// languageEngine resolves a symbol's neighborhood for one language family.
type languageEngine interface {
	// ID names the engine in the response, e.g. "gopls (Go)".
	ID() string
	// Caveat is prepended to the output when the engine's answers need
	// qualification. Empty for authoritative engines.
	Caveat() string
	// Resolves reports whether this engine returns real call edges rather than
	// text matches.
	Resolves() bool
	// Resolve answers the requested relationships for a declaration.
	Resolve(ctx context.Context, root string, decl codeLocation, want string) symbolGraph

	// ResolveEdges answers ONLY "who calls this" or "what does this call".
	//
	// Traversal is the hot path — a depth-3 map issues one query per node — and
	// Resolve does extra work per node (signature lookup, implementations) that
	// a tree never renders. Measured: dropping that extra query roughly halves
	// map wall-clock, which is the difference between a usable default depth
	// and one nobody would leave on.
	ResolveEdges(ctx context.Context, root string, decl codeLocation, direction string) []codeLocation
}

// selectEngine picks the engine for a file, returning a note when the ideal
// engine was unavailable and a weaker one was substituted.
func selectEngine(root, path string) (languageEngine, string, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		if _, err := exec.LookPath("gopls"); err != nil {
			return nil, "", fmt.Errorf(
				"gopls is required to resolve Go symbols but is not on PATH.\n\n"+
					"Install it, then retry:\n"+
					"  go install golang.org/x/tools/gopls@latest\n\n"+
					"To search without it (text matches only, not resolved call edges):\n"+
					"  rg -n '%s' .", filepath.Base(path))
		}
		return goplsEngine{}, "", nil
	case ".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs":
		if _, err := exec.LookPath("node"); err != nil {
			return nil, "", fmt.Errorf(
				"node is required to run tsserver but is not on PATH.\n\n" +
					"Install Node.js, then retry.")
		}
		server := findTSServer(root, path)
		if server == "" {
			// Returning an ERROR rather than degrading is deliberate. A text
			// fallback here produced 29 plausible-looking matches for a symbol
			// whose real caller count is 1 — an answer that looks successful
			// and is silently wrong about the thing being asked. The reader
			// cannot tell the difference, so the failure has to be loud.
			//
			// This fires constantly in git worktrees: node_modules is
			// gitignored, so a branched chat NEVER has one, and the fix is a
			// single command the agent can run itself.
			return nil, "", fmt.Errorf(
				"no TypeScript install found for this file, so call edges cannot be resolved.\n\n"+
					"TypeScript ships inside each project's node_modules (there is no global\n"+
					"install). This is expected in a fresh git worktree, where node_modules is\n"+
					"gitignored and therefore absent.\n\n"+
					"Install dependencies in the frontend that owns this file, then retry:\n"+
					"  cd %s && npm install\n\n"+
					"To search without resolution (text matches only, not call edges):\n"+
					"  rg -n '<symbol>' %s",
				suggestFrontendDir(root, path), relativizePath(root, filepath.Dir(path)))
		}
		return tsserverEngine{serverPath: server}, "", nil
	default:
		// No language server for this file type. Unlike the cases above there
		// is nothing to install, so an approximate answer is the best available
		// and the caveat says so.
		return textEngine{}, "", nil
	}
}

// suggestFrontendDir names the directory to run `npm install` in: the nearest
// ancestor of the file holding a package.json, falling back to the file's own
// directory. Naming the wrong directory would send the agent to install
// dependencies where they do not belong.
func suggestFrontendDir(root, path string) string {
	dir := filepath.Dir(path)
	for {
		if _, err := os.Stat(filepath.Join(dir, "package.json")); err == nil {
			return relativizePath(root, dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir || !strings.HasPrefix(dir, root) {
			return relativizePath(root, filepath.Dir(path))
		}
		dir = parent
	}
}

// --- Go: gopls ---

type goplsEngine struct{}

func (goplsEngine) ID() string     { return "gopls (Go) — authoritative" }
func (goplsEngine) Caveat() string { return "" }
func (goplsEngine) Resolves() bool { return true }

func (e goplsEngine) Resolve(ctx context.Context, root string, decl codeLocation, want string) symbolGraph {
	pos := fmt.Sprintf("%s:%d:%d", decl.Path, decl.Line, decl.Col)
	var graph symbolGraph

	// gopls definition also yields the real signature, which beats the source
	// line the declaration regex matched.
	if outStr, err := runTool(ctx, root, "gopls", "definition", pos); err == nil {
		for _, line := range strings.Split(outStr, "\n") {
			if m := goplsDefinitionRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
				graph.Detail = strings.TrimSpace(m[4])
				graph.DefinitionName = goplsSymbolName(graph.Detail)
				break
			}
		}
	}

	if want == "all" || want == "callers" || want == "callees" {
		graph.Callers, graph.Callees = goplsCallHierarchy(ctx, root, pos)
	}
	if want == "all" || want == "implementations" {
		graph.Implementations = goplsImplementation(ctx, root, pos)
	}
	return graph
}

func (e goplsEngine) ResolveEdges(ctx context.Context, root string, decl codeLocation, direction string) []codeLocation {
	pos := fmt.Sprintf("%s:%d:%d", decl.Path, decl.Line, decl.Col)
	callers, callees := goplsCallHierarchy(ctx, root, pos)
	if direction == "callees" {
		return callees
	}
	return callers
}

// goplsDefinitionRe parses `path:line:col-col: defined here as <detail>`.
var goplsDefinitionRe = regexp.MustCompile(`^(.+?):(\d+):(\d+)(?:-\d+)?:\s*(?:defined here as\s*)?(.*)$`)

// goplsSymbolNameRe pulls the identifier out of a Go declaration, skipping an
// optional method receiver: "func (s *Svc) Resolve(...)" -> "Resolve".
var goplsSymbolNameRe = regexp.MustCompile(`^func\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)|^(?:type|var|const)\s+([A-Za-z_]\w*)`)

func goplsSymbolName(detail string) string {
	m := goplsSymbolNameRe.FindStringSubmatch(strings.TrimSpace(detail))
	if m == nil {
		return ""
	}
	if m[1] != "" {
		return m[1]
	}
	return m[2]
}

// goplsCallerRe parses:
//
//	caller[0]: ranges 166:9-21 in /abs/file.go from/to function Fetch in /abs/factory.go:165:24-29
//
// Capture groups: 1 kind, 2 call-site line, 3 call-site file, 4 enclosing
// symbol, 5 declaration file, 6 declaration line, 7 declaration column. The
// declaration position (5,6,7) is what makes the node re-queryable.
var goplsCallerRe = regexp.MustCompile(`^(caller|callee)\[\d+\]:\s*ranges\s+(\d+):[\d-]+(?:,\s*[\d:-]+)*\s+in\s+(.+?)\s+from/to\s+(.+?)\s+in\s+(.+?):(\d+):(\d+)`)

func goplsCallHierarchy(ctx context.Context, root, pos string) (callers, callees []codeLocation) {
	outStr, err := runTool(ctx, root, "gopls", "call_hierarchy", pos)
	if err != nil && strings.TrimSpace(outStr) == "" {
		return nil, nil
	}
	for _, line := range strings.Split(outStr, "\n") {
		m := goplsCallerRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		if m[1] == "caller" {
			// A caller line carries both positions: the call site (m[2] in
			// m[3]) and the declaration of the enclosing function
			// (m[5]:m[6]:declCol). Keep both — the first is what a reader wants
			// to see, the second is the only one gopls can be re-queried at.
			ln, _ := strconv.Atoi(m[2])
			declLine, _ := strconv.Atoi(m[6])
			declCol, _ := strconv.Atoi(m[7])
			callers = append(callers, codeLocation{
				Path:      m[3],
				Line:      ln,
				Enclosing: strings.TrimSpace(m[4]),
				Decl:      &codePosition{Path: m[5], Line: declLine, Col: declCol},
			})
			continue
		}
		// For a callee the interesting site IS the declaration of the thing
		// being called, so display and traversal coincide.
		defLine, _ := strconv.Atoi(m[6])
		defCol, _ := strconv.Atoi(m[7])
		callees = append(callees, codeLocation{
			Path: m[5], Line: defLine, Col: defCol, Enclosing: strings.TrimSpace(m[4]),
		})
	}
	return callers, callees
}

// goplsLocationRe parses a bare `path:line:col-col` location line.
var goplsLocationRe = regexp.MustCompile(`^(.+?):(\d+):(\d+)(?:-\d+)?$`)

func goplsImplementation(ctx context.Context, root, pos string) []codeLocation {
	outStr, err := runTool(ctx, root, "gopls", "implementation", pos)
	if err != nil && strings.TrimSpace(outStr) == "" {
		return nil
	}
	var locs []codeLocation
	for _, line := range strings.Split(outStr, "\n") {
		if m := goplsLocationRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			ln, _ := strconv.Atoi(m[2])
			locs = append(locs, codeLocation{Path: m[1], Line: ln})
		}
	}
	return locs
}

// --- TypeScript / JavaScript: tsserver ---

// findTSServer locates the TypeScript install that OWNS this file, by walking
// up from the file to the filesystem root.
//
// Walking up (rather than checking the repo root) is what makes this work in a
// monorepo: web/ and electron/ each carry their own TypeScript, and a file in
// web/ must be resolved by web/'s copy so it sees that project's tsconfig and
// module resolution. There is no global tsserver to fall back on — TypeScript
// is always a project dependency.
func findTSServer(root, path string) string {
	dir := filepath.Dir(path)
	for {
		candidate := filepath.Join(dir, "node_modules", "typescript", "lib", "tsserver.js")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

type tsserverEngine struct {
	serverPath string
}

func (tsserverEngine) ID() string     { return "tsserver (TypeScript/JavaScript) — authoritative" }
func (tsserverEngine) Caveat() string { return "" }
func (tsserverEngine) Resolves() bool { return true }

func (e tsserverEngine) Resolve(ctx context.Context, root string, decl codeLocation, want string) symbolGraph {
	session, err := startTSSession(ctx, e.serverPath, decl.Path)
	if err != nil {
		return symbolGraph{}
	}
	defer session.Close()

	var graph symbolGraph

	// quickinfo yields the real signature, which beats the matched source line.
	if body, ok := session.request("quickinfo", tsFileArgs(decl)); ok {
		var info struct {
			DisplayString string `json:"displayString"`
		}
		if json.Unmarshal(body, &info) == nil {
			graph.Detail = info.DisplayString
		}
	}

	// prepareCallHierarchy reports the declaration's full extent, which lets a
	// source preview stop exactly at the end of the function.
	if body, ok := session.request("prepareCallHierarchy", tsFileArgs(decl)); ok {
		var item tsCallHierarchyItem
		if json.Unmarshal(body, &item) == nil && item.Name != "" {
			graph.DefinitionName = item.Name
			graph.DefinitionEndLine = item.Span.End.Line
		} else {
			// tsserver returns either an item or a one-element array.
			var items []tsCallHierarchyItem
			if json.Unmarshal(body, &items) == nil && len(items) > 0 {
				graph.DefinitionName = items[0].Name
				graph.DefinitionEndLine = items[0].Span.End.Line
			}
		}
	}

	if want == "all" || want == "callers" {
		if body, ok := session.request("provideCallHierarchyIncomingCalls", tsFileArgs(decl)); ok {
			var incoming []struct {
				From tsCallHierarchyItem `json:"from"`
			}
			if json.Unmarshal(body, &incoming) == nil {
				for _, c := range incoming {
					graph.Callers = append(graph.Callers, c.From.toLocation())
				}
			}
		}
	}
	if want == "all" || want == "callees" {
		if body, ok := session.request("provideCallHierarchyOutgoingCalls", tsFileArgs(decl)); ok {
			var outgoing []struct {
				To tsCallHierarchyItem `json:"to"`
			}
			if json.Unmarshal(body, &outgoing) == nil {
				for _, c := range outgoing {
					graph.Callees = append(graph.Callees, c.To.toLocation())
				}
			}
		}
	}
	if want == "all" || want == "implementations" {
		if body, ok := session.request("implementation", tsFileArgs(decl)); ok {
			var impls []struct {
				File  string    `json:"file"`
				Start tsPointer `json:"start"`
			}
			if json.Unmarshal(body, &impls) == nil {
				for _, i := range impls {
					graph.Implementations = append(graph.Implementations,
						codeLocation{Path: i.File, Line: i.Start.Line, Col: i.Start.Offset})
				}
			}
		}
	}
	return graph
}

// ResolveEdges reuses one tsserver process for a single node's edges. tsserver
// starts in ~0.3s and answers subsequent queries in milliseconds, so a session
// per node is affordable in a way a gopls process per node is not.
func (e tsserverEngine) ResolveEdges(ctx context.Context, root string, decl codeLocation, direction string) []codeLocation {
	session, err := startTSSession(ctx, e.serverPath, decl.Path)
	if err != nil {
		return nil
	}
	defer session.Close()

	command := "provideCallHierarchyIncomingCalls"
	if direction == "callees" {
		command = "provideCallHierarchyOutgoingCalls"
	}
	body, ok := session.request(command, tsFileArgs(decl))
	if !ok {
		return nil
	}

	var edges []struct {
		From tsCallHierarchyItem `json:"from"`
		To   tsCallHierarchyItem `json:"to"`
	}
	if json.Unmarshal(body, &edges) != nil {
		return nil
	}
	out := make([]codeLocation, 0, len(edges))
	for _, edge := range edges {
		item := edge.From
		if direction == "callees" {
			item = edge.To
		}
		if item.Name == "" {
			continue
		}
		out = append(out, item.toLocation())
	}
	return out
}

type tsPointer struct {
	Line   int `json:"line"`
	Offset int `json:"offset"`
}

type tsCallHierarchyItem struct {
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	File          string `json:"file"`
	Span          tsSpan `json:"span"`
	SelectionSpan tsSpan `json:"selectionSpan"`
}

// tsSpan is the {start,end} shape tsserver uses throughout its protocol.
type tsSpan struct {
	Start tsPointer `json:"start"`
	End   tsPointer `json:"end"`
}

// toLocation converts a tsserver call-hierarchy item. tsserver reports the
// DECLARATION of the related function (not the call site), so display and
// traversal coincide and no separate Decl is needed.
func (i tsCallHierarchyItem) toLocation() codeLocation {
	return codeLocation{
		Path:      i.File,
		Line:      i.SelectionSpan.Start.Line,
		Col:       i.SelectionSpan.Start.Offset,
		Enclosing: i.Name,
		// tsserver reports the whole declaration extent, so a source preview
		// can stop exactly at the end of the function rather than guessing.
		EndLine: i.Span.End.Line,
	}
}

func tsFileArgs(decl codeLocation) map[string]any {
	return map[string]any{"file": decl.Path, "line": decl.Line, "offset": decl.Col}
}

// tsSession is a short-lived tsserver process driven over its stdio protocol.
//
// tsserver speaks line-delimited JSON requests and Content-Length-framed
// responses. Opening ONE file is enough: tsserver loads that file's whole
// tsconfig project, so callers in files we never mentioned still resolve.
type tsSession struct {
	cmd     *exec.Cmd
	stdin   *os.File
	stdout  *bufio.Reader
	seq     int
	closers []func()
}

func startTSSession(ctx context.Context, serverPath, file string) (*tsSession, error) {
	cmd := exec.CommandContext(ctx, "node", serverPath,
		"--disableAutomaticTypingAcquisition", "--suppressDiagnosticEvents")
	cmd.Dir = filepath.Dir(serverPath)

	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, err
	}
	cmd.Stdin = inR
	cmd.Stdout = outW
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		return nil, err
	}
	// The child owns its ends now; closing ours makes EOF propagate correctly.
	inR.Close()
	outW.Close()

	s := &tsSession{cmd: cmd, stdin: inW, stdout: bufio.NewReaderSize(outR, 1<<20)}
	s.closers = append(s.closers, func() { inW.Close() }, func() { outR.Close() })

	// Opening the declaring file is what loads the project.
	s.send("open", map[string]any{"file": file})
	return s, nil
}

func (s *tsSession) send(command string, args map[string]any) {
	s.seq++
	payload, err := json.Marshal(map[string]any{
		"seq": s.seq, "type": "request", "command": command, "arguments": args,
	})
	if err != nil {
		return
	}
	fmt.Fprintf(s.stdin, "%s\n", payload)
}

// request issues a command and returns the body of its response. Events and
// responses to other commands are skipped, so a slow project load cannot
// desynchronize the reader.
func (s *tsSession) request(command string, args map[string]any) (json.RawMessage, bool) {
	s.send(command, args)
	want := s.seq
	for {
		msg, err := s.readMessage()
		if err != nil {
			return nil, false
		}
		var envelope struct {
			Type       string          `json:"type"`
			Command    string          `json:"command"`
			RequestSeq int             `json:"request_seq"`
			Success    bool            `json:"success"`
			Body       json.RawMessage `json:"body"`
		}
		if json.Unmarshal(msg, &envelope) != nil {
			continue
		}
		if envelope.Type != "response" || envelope.RequestSeq != want {
			continue
		}
		if !envelope.Success {
			return nil, false
		}
		return envelope.Body, true
	}
}

// readMessage reads one Content-Length-framed message.
func (s *tsSession) readMessage() ([]byte, error) {
	length := 0
	for {
		line, err := s.stdout.ReadString('\n')
		if err != nil {
			return nil, err
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if length > 0 {
				break
			}
			continue
		}
		if rest, ok := strings.CutPrefix(trimmed, "Content-Length:"); ok {
			length, _ = strconv.Atoi(strings.TrimSpace(rest))
		}
	}
	buf := make([]byte, length)
	if _, err := readFull(s.stdout, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func readFull(r *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func (s *tsSession) Close() {
	for _, c := range s.closers {
		c()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
	}
}

// --- Everything else: text ---

type textEngine struct{}

func (textEngine) ID() string { return "ripgrep (text) — approximate" }

func (textEngine) Caveat() string {
	return "No language server for this file type, so the entries below are TEXT MATCHES\n" +
		"rather than resolved call edges: they may include same-named symbols on\n" +
		"unrelated types, and cannot see calls made through dynamic dispatch.\n"
}

func (textEngine) Resolves() bool { return false }

// ResolveEdges returns nothing: a text engine cannot distinguish a call from a
// mention, and a call map built from mentions would be confidently wrong.
// depth>1 is refused for these languages rather than faked.
func (textEngine) ResolveEdges(context.Context, string, codeLocation, string) []codeLocation {
	return nil
}

func (textEngine) Resolve(ctx context.Context, root string, decl codeLocation, _ string) symbolGraph {
	symbol := symbolAt(decl)
	if symbol == "" {
		return symbolGraph{}
	}
	outStr, err := runTool(ctx, root, "rg",
		"--line-number", "--column", "--no-heading", "--color", "never",
		"--max-count", "200",
		"--glob", "!node_modules", "--glob", "!.git", "--glob", "!vendor", "--glob", "!dist",
		"-w", "-e", regexp.QuoteMeta(symbol), ".")
	if err != nil && strings.TrimSpace(outStr) == "" {
		return symbolGraph{}
	}

	var graph symbolGraph
	for _, line := range strings.Split(outStr, "\n") {
		loc, ok := parseRipgrepLine(root, line)
		if !ok {
			continue
		}
		if loc.Path == decl.Path && loc.Line == decl.Line {
			continue
		}
		graph.References = append(graph.References, loc)
	}
	return graph
}

// tsIdentifierRe extracts the identifier starting at a declaration's column.
var tsIdentifierRe = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*`)

// symbolAt recovers the identifier from the declaration line, so the text
// engine searches for the symbol rather than the whole matched line.
func symbolAt(decl codeLocation) string {
	if decl.Col <= 0 || decl.Col > len(decl.Detail) {
		return ""
	}
	return tsIdentifierRe.FindString(decl.Detail[decl.Col-1:])
}
