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
// can only shrink by a deliberate edit. A read is found by what the value is
// (projectTypeIndex): a parameter or variable declared as a db.Project, the
// result of any function or method that returns one, an element of a slice of
// them, or a field of that type, as well as by the receiver's name.
// TestProjectPathSQLIsReviewed holds SQL text to the same rule.
//
// The chat runtime is not on the list. A chat's directory is resolved at
// dispatch on the machine that runs each call (projectcheckout.On), the
// daemon-side tool context carries that machine's checkout (rctx.Project,
// not a db.Project), and a main worktree row is read as "the main checkout on
// this machine", never as a path.

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
)

// projectPathAllowed is every function that may read a project's .Path, how
// many times, and why. Keyed "<file>:<func>", file relative to the repo root.
var projectPathAllowed = map[string]struct {
	count  int
	reason projectPathReason
}{
	"internal/db/repository_impl.go:CreateProject":                                     {1, reasonProjectRow},
	"internal/handlers/project/project.go:GetProjectMetadata":                          {1, reasonProjectRow},
	"internal/handlers/project/project.go:projectToProto":                              {1, reasonProjectRow},
	"internal/handlers/project/rpc_create_project.go:CreateProject":                    {3, reasonCreate},
	"internal/handlers/project/rpc_project_daemons.go:ensureProjectDirsOnDaemon":       {5, reasonHeal},
	"internal/handlers/tools_daemon/tools_daemon_reconcile.go:reconcileProjectDaemons": {4, reasonProbe},
	// The main worktree row mirrors projects.path for display; every runtime
	// reader treats an is_main row as the main checkout ON ITS OWN MACHINE.
	"internal/handlers/worktree/rpc_list_worktrees.go:ListWorktrees": {1, reasonProjectRow},
}

func TestProjectPathNeverReachesADaemon(t *testing.T) {
	t.Parallel()

	repo := root(t)
	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "testdata": true, "vendor": true,
		"tmp": true, "data": true, "dist": true, ".reliant": true,
		"gen": true, "web": true, "electron": true, "proto-vendor": true,
	}
	type parsed struct {
		file *ast.File
		rel  string
	}
	var files []parsed
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
			files = append(files, parsed{file: file, rel: filepath.ToSlash(rel)})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// Every file declares what a value is (the generated ORM's GetProject
	// among them); every file but the ORM's is judged by it. The ORM projects
	// every column, Path included.
	ix := newProjectTypeIndex()
	for _, f := range files {
		ix.indexProjectTypes(f.file, f.rel)
	}
	reads := map[string]int{}
	for _, f := range files {
		if !strings.HasPrefix(f.rel, "internal/store/") {
			ix.projectPathReads(f.file, f.rel, reads)
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
