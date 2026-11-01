package architecture

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ─── projects.path never reaches a daemon ──────────────────────────────────
//
// projects.path is where a project was CREATED, on whichever machine created
// it. A project made on the owner's Mac carries /Users/<them>/…; the same
// project on their cloud machine lives somewhere else. Sending projects.path
// to a daemon names a directory on one machine and asks another about it —
// the forge console reported "forge project dir does not exist:
// /Users/seanteeling/src/reliant-labs/control-plane" from a cloud machine
// (PR #2016), and git status, MCP config, repo discovery, scenario and
// workflow saves did the same.
//
// The per-machine truth is project_daemons, and internal/projectcheckout is
// the way to use it: Resolve returns the machine AND the project's path on
// that machine from one row, and Target.Send / Target.Pinned send only to
// that machine.
//
// This guard fails any read of a project's .Path — `project.Path`,
// `proj.Path`, `x.project.Path` — in a function that is not on the reviewed
// list below, and fails a listed function whose count changed, so the list
// can only shrink by a deliberate edit. The match is syntactic (the receiver's
// name), which is what keeps it cheap and cache-sound; a read through another
// name is for review to catch.

// projectPathReason is why a function may still read projects.path.
type projectPathReason string

const (
	// The projects row itself: written, displayed, or returned as data.
	reasonProjectRow projectPathReason = "the projects row as data, never sent to a daemon"
	// Project creation: the path the caller named, on the machine creating it.
	reasonCreate projectPathReason = "creating the project at the path the caller named, on the machine creating it"
	// The connect-time reconcile asks the CONNECTING machine whether it has
	// a checkout at the creation path, and records a yes as that machine's
	// own row (with that path) in project_daemons — discovery, not dispatch.
	reasonProbe projectPathReason = "probing the connecting machine for a checkout at the creation path, recorded as its own project_daemons row"
	// project-dir-heal recreates a plain folder on the machine that
	// connected, only when no other machine holds it.
	reasonHeal projectPathReason = "recreating a plain folder on the machine that connected, when no other machine holds it"
	// Text in a message; nothing is sent.
	reasonMessage projectPathReason = "text in a message"
	// TRACKED DEBT, not a precedent: the chat runtime, workspace placement
	// and the Files tab still root a machine's MAIN checkout at
	// projects.path. They pick the machine by the chat or workspace (not by
	// default resolution), so they break only when that machine's checkout
	// is at a different path. Moving them to the machine's own project_daemons
	// row is the follow-up; do not add new entries under this reason.
	reasonRuntimeDebt projectPathReason = "TRACKED: runtime/workspace main checkout still rooted at projects.path (follow-up)"
)

// projectPathAllowed is every function that may read a project's .Path, how
// many times, and why. Keyed "<file>:<func>", file relative to the repo root.
var projectPathAllowed = map[string]struct {
	count  int
	reason projectPathReason
}{
	"internal/db/repository_impl.go:CreateProject":                                     {1, reasonProjectRow},
	"internal/handlers/project/project.go:GetProjectMetadata":                          {1, reasonProjectRow},
	"internal/handlers/project/rpc_create_project.go:CreateProject":                    {3, reasonCreate},
	"internal/handlers/project/rpc_project_daemons.go:ensureProjectDirsOnDaemon":       {5, reasonHeal},
	"internal/handlers/tools_daemon/tools_daemon_reconcile.go:reconcileProjectDaemons": {4, reasonProbe},
	"internal/repo/adopt.go:NoReposMessage":                                            {1, reasonMessage},

	"internal/filepreview/access.go:ResolveBasePath":                                                {1, reasonRuntimeDebt},
	"internal/handlers/tools_daemon/tools_daemon_connect.go:listAllProjectPaths":                    {2, reasonRuntimeDebt},
	"internal/handlers/worktree/rpc_create_worktree.go:CreateWorktree":                              {1, reasonRuntimeDebt},
	"internal/handlers/worktree/rpc_discover_worktrees.go:ImportWorktree":                           {2, reasonRuntimeDebt},
	"internal/handlers/worktree/rpc_discover_worktrees.go:projectRepoRefs":                          {1, reasonRuntimeDebt},
	"internal/handlers/worktree/rpc_list_worktrees.go:ListWorktrees":                                {1, reasonRuntimeDebt},
	"internal/handlers/worktree/rpc_worktree_lifecycle.go:ArchiveWorktree":                          {1, reasonRuntimeDebt},
	"internal/handlers/worktree/rpc_worktree_lifecycle.go:DeleteWorktree":                           {4, reasonRuntimeDebt},
	"internal/handlers/worktree/rpc_worktree_lifecycle.go:RecreateWorktree":                         {2, reasonRuntimeDebt},
	"internal/handlers/worktree/rpc_worktree_lifecycle.go:repairActiveWorktree":                     {2, reasonRuntimeDebt},
	"internal/handlers/worktree/worktree.go:repoDiscoveryError":                                     {2, reasonRuntimeDebt},
	"internal/launch/session.go:GetEffectiveCheckout":                                               {1, reasonRuntimeDebt},
	"internal/llm/tools/metadata_writer.go:Execute":                                                 {2, reasonRuntimeDebt},
	"internal/llm/tools/project_analyzer.go:Execute":                                                {2, reasonRuntimeDebt},
	"internal/llm/tools/worktree_tool.go:handleDelete":                                              {1, reasonRuntimeDebt},
	"internal/rctx/context.go:WorkingDir":                                                           {1, reasonRuntimeDebt},
	"internal/repo/adopt.go:AdoptFromDaemon":                                                        {4, reasonRuntimeDebt},
	"internal/workflow/runtime/activities/handlers/call_llm.go:streamLLMResponse":                   {1, reasonRuntimeDebt},
	"internal/workflow/runtime/activities/handlers/execute_tools.go:buildToolRequest":               {1, reasonRuntimeDebt},
	"internal/workflow/runtime/activities/handlers/execute_tools.go:loadWorktreeInfo":               {2, reasonRuntimeDebt},
	"internal/workflow/runtime/activities/handlers/execute_tools_workspace.go:describeFallback":     {4, reasonRuntimeDebt},
	"internal/workflow/runtime/activities/handlers/execute_tools_workspace.go:ensureBatchWorkspace": {4, reasonRuntimeDebt},
	"internal/workflow/runtime/activities/handlers/execute_tools_workspace.go:workspaceNoticeText":  {6, reasonRuntimeDebt},
	"internal/workflow/runtime/activities/handlers/load_workflow.go:resolveWorkflowContext":         {1, reasonRuntimeDebt},
	"internal/workflow/runtime/activities/handlers/run_executor.go:resolveRunExecutorContext":       {2, reasonRuntimeDebt},
	"internal/workflow/runtime/activities/handlers/worktree.go:Execute":                             {2, reasonRuntimeDebt},
	"internal/workspacecreate/workspacecreate.go:Finish":                                            {5, reasonRuntimeDebt},
}

// projectReceiver reports whether a selector's receiver names a project:
// `project`, `proj`, `prj`, or a field of that name (`tec.project`).
func projectReceiver(x ast.Expr) bool {
	var name string
	switch x := x.(type) {
	case *ast.Ident:
		name = x.Name
	case *ast.SelectorExpr:
		name = x.Sel.Name
	}
	switch strings.ToLower(name) {
	case "project", "proj", "prj":
		return true
	}
	return false
}

// projectPathReads counts the reads of a project's .Path in each function of
// file, keyed "<rel>:<func>".
func projectPathReads(file *ast.File, rel string, into map[string]int) {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Path" && projectReceiver(sel.X) {
				into[rel+":"+fn.Name.Name]++
			}
			return true
		})
	}
}

func TestProjectPathReads(t *testing.T) {
	src := `package x
func a(project *P) { send(project.Path); _ = t.project.Path; _ = proj.Path }
func b(p *P, wt *W) { _ = p.Path; _ = wt.Path; _ = project.Name }
`
	file, err := parser.ParseFile(token.NewFileSet(), "x.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	projectPathReads(file, "x.go", got)
	if len(got) != 1 || got["x.go:a"] != 3 {
		t.Fatalf("reads = %v, want x.go:a three times and nothing in b", got)
	}
}

func TestProjectPathNeverReachesADaemon(t *testing.T) {
	t.Parallel()

	repo := root(t)
	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "testdata": true, "vendor": true,
		"tmp": true, "data": true, "dist": true, ".reliant": true,
		"gen": true, "web": true, "electron": true, "proto-vendor": true,
		// The generated ORM projects every column, Path included.
		"store": true,
	}
	reads := map[string]int{}
	fset := token.NewFileSet()
	for _, top := range []string{"internal", "cmd", "pkg"} {
		dir := filepath.Join(repo, top)
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if path == dir {
					return filepath.SkipDir
				}
				return err
			}
			if d.IsDir() {
				if skipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(repo, path)
			if err != nil {
				return err
			}
			projectPathReads(file, filepath.ToSlash(rel), reads)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	var problems []string
	for key, n := range reads {
		allowed, ok := projectPathAllowed[key]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("%s reads a project's .Path %d time(s). projects.path is the CREATING machine's path: "+
				"resolve the machine and its own path with projectcheckout.Resolve and send with Target.Send / Target.Pinned", key, n))
		case n != allowed.count:
			problems = append(problems, fmt.Sprintf("%s reads a project's .Path %d time(s), the list allows %d (%s): "+
				"a new read uses projectcheckout; a removed one lowers the count", key, n, allowed.count, allowed.reason))
		}
	}
	for key, allowed := range projectPathAllowed {
		if _, ok := reads[key]; !ok {
			problems = append(problems, fmt.Sprintf("%s is listed (%d, %s) but no longer reads a project's .Path: remove the entry", key, allowed.count, allowed.reason))
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}
