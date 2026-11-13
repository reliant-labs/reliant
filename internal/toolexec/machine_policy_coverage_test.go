// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// commandSites is every place in this module that builds or sends a command
// for a machine to run, and how an org's forced `credentials: none`
// (applyMachinePolicy) reaches it. Each key is "<file>:<func>:<kind>"; kind
// is what the site does:
//
//	request   builds a daemon.RunCommandRequest
//	run       calls RunCommand or StartBackground on a daemon client
//	dispatch  names a raw daemon exec command ("exec.run", "exec.bg_start")
//	toolexec  builds a ToolExecutionRequest, the wire form of a tool call
//
// TestEveryCommandBuildingSiteIsCovered fails on a site missing here: route
// it through the daemon client a tool receives (rctx.ToolContext.Daemon,
// wrapped by daemon.ForceCredentialsNone when the run's org forces it) or
// through RemoteExecutor.ExecuteTool, then say so below. A site a person
// drives, not an agent, says why it is not an org run's command.
var commandSites = map[string]string{
	// The shell tool: its input is rewritten (credentials, env) before
	// dispatch, and on the machine it runs over the wrapped client.
	"internal/llm/tools/shell.go:execute:request": "shell tool input, rewritten by applyMachinePolicy",
	"internal/llm/tools/shell.go:execute:run":     "rctx.Daemon, wrapped by daemon.ForceCredentialsNone under the stamp",
	// prove_test builds its command on the machine: refused while the rule is
	// enforced (reachStampOnly), forced by a machine that reads the stamp.
	"internal/llm/tools/prove_test_tool.go:Execute:request": "prove_test, reachStampOnly",
	"internal/llm/tools/prove_test_tool.go:Execute:run":     "tc.Daemon, wrapped under the stamp",
	// The wrapper itself.
	"internal/daemon/credentials_none_client.go:RunCommand:run":      "the wrapper",
	"internal/daemon/credentials_none_client.go:StartBackground:run": "the wrapper",
	// The clients: what every caller above reaches.
	"internal/daemon/remote.go:RunCommand:dispatch":      "RemoteClient: reached only through a tool's (wrapped) client",
	"internal/daemon/remote.go:StartBackground:dispatch": "RemoteClient: reached only through a tool's (wrapped) client",
	// The worker's tool transport: every ToolRequest passes ExecuteTool
	// (applyMachinePolicy) before it is placed.
	"internal/toolexec/remote_executor.go:executeOnDaemon:toolexec":         "after applyMachinePolicy in ExecuteTool",
	"internal/toolexec/tool_result_redelivery.go:followToolResult:toolexec": "carries only a request id: waits for a result an earlier, policed dispatch started",
	// The Commands panel: a command a person starts from the app, not an
	// agent's; org policy does not govern a person at the keyboard (§4.6).
	"internal/handlers/package_commands/package_commands_processes.go:RunCommand:request":  "person-driven Commands panel",
	"internal/handlers/package_commands/package_commands_processes.go:RunCommand:dispatch": "person-driven Commands panel",
	// The machine's side: it receives and classifies commands, it builds none.
	"internal/toolexec/daemonruntime/cmd_exec.go:init:dispatch":                         "the machine's handler registration",
	"internal/toolexec/daemonruntime/worktree_activity.go:selfAdmittedCommand:dispatch": "classifies a received command",
	"internal/toolexec/daemonruntime/worktree_use.go:usesPathsCommand:dispatch":         "classifies a received command",
	"internal/daemonpolicy/policy.go:isExecCommand:dispatch":                            "classifies a received command",
}

// TestEveryCommandBuildingSiteIsCovered is the bypass detector of the
// machines policy: it parses every non-test Go file of the module and fails
// on a site that builds or sends a machine command without being listed in
// commandSites, and on a listed site that no longer exists.
func TestEveryCommandBuildingSiteIsCovered(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch {
			case rel == "gen", rel == "sdk", rel == "frontends", rel == "vendor", rel == "electron",
				strings.HasPrefix(d.Name(), "."), d.Name() == "node_modules", d.Name() == "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, site := range commandSitesIn(t, rel, src) {
			found[site] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var unlisted, gone []string
	for site := range found {
		if _, ok := commandSites[site]; !ok {
			unlisted = append(unlisted, site)
		}
	}
	for site := range commandSites {
		if !found[site] {
			gone = append(gone, site)
		}
	}
	sort.Strings(unlisted)
	sort.Strings(gone)
	if len(unlisted) > 0 {
		t.Errorf("a machine command is built or sent where an org's forced `credentials: none` may not reach it.\n"+
			"Route it through a tool's daemon client or RemoteExecutor.ExecuteTool, then list it in commandSites:\n  %s",
			strings.Join(unlisted, "\n  "))
	}
	if len(gone) > 0 {
		t.Errorf("commandSites lists sites that no longer exist; remove them:\n  %s", strings.Join(gone, "\n  "))
	}
}

// commandSitesIn are the command sites of one file, keyed as commandSites.
func commandSitesIn(t *testing.T, rel string, src []byte) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	var sites []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			kind := ""
			switch n := n.(type) {
			case *ast.CompositeLit:
				switch typeName(n.Type) {
				case "RunCommandRequest":
					kind = "request"
				case "ToolExecutionRequest":
					kind = "toolexec"
				}
			case *ast.CallExpr:
				if sel, ok := n.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "RunCommand" || sel.Sel.Name == "StartBackground") {
					kind = "run"
				}
			case *ast.BasicLit:
				if n.Kind == token.STRING {
					if s, err := strconv.Unquote(n.Value); err == nil && (s == "exec.run" || s == "exec.bg_start") {
						kind = "dispatch"
					}
				}
			}
			if kind != "" {
				sites = append(sites, rel+":"+fn.Name.Name+":"+kind)
			}
			return true
		})
	}
	return sites
}

// typeName is the bare name of a composite literal's type: RunCommandRequest
// for daemon.RunCommandRequest or RunCommandRequest.
func typeName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	}
	return ""
}
