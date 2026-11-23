// Copyright (c) 2025 Reliant Labs
package db

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// orgSensitiveStoreWrites are the store writes whose correctness depends on
// an org decision: they write a row's org (a new row's, or a NULL one's
// stamp), move a row between projects, or overwrite or delete a row an org
// decides access to. Each runs under its owner's org lock
// (store.LockUserOrgs, internal/store/org_lock.go), taken first in its
// transaction.
var orgSensitiveStoreWrites = map[string]bool{
	"InsertTrigger":     true,
	"ReplaceTrigger":    true,
	"MoveChatToProject": true,
	"MoveProjectOrg":    true,
}

// draftStoreWrites are the workflow_drafts writes, now made only by
// internal/workflowdrafts (through its Drafts dependency). Each of the
// package's methods that makes one takes its owner's org lock first: through
// Locks.LockUserOrgs itself, or through a helper of the package that does.
var draftStoreWrites = map[string]bool{
	"InsertWorkflowDraft":        true,
	"UpsertWorkflowDraftBySlug":  true,
	"RemoveWorkflowDraft":        true,
	"ReplaceWorkflowDraft":       true,
	"SetWorkflowDraftDefinition": true,
	"SetWorkflowDraftStatus":     true,
	"SetWorkflowDraftHidden":     true,
	"SetWorkflowDraftForkedFrom": true,
}

// orgWritingDecls are the internal/store declarations whose SQL writes an
// org_id. A new one fails TestOrgSensitiveWritesTakeTheOrgLock until it is
// listed here, and its write in orgSensitiveStoreWrites (or it is the
// backfill, which takes the lock itself).
var orgWritingDecls = map[string]string{
	"ReplaceTrigger":       "stamps a NULL org on a moved trigger (orgSensitiveStoreWrites)",
	"moveChatToProjectSQL": "stamps the moved chat's org (MoveChatToProject, orgSensitiveStoreWrites)",
	"orgBackfillSteps":     "the backfill: assignUnassignedOrg takes the org lock for every batch",
	// A project move's statement set (MoveProjectOrg, orgSensitiveStoreWrites).
	"moveProjectOrgProjectSQL":     "restamps the moved project's org (MoveProjectOrg, orgSensitiveStoreWrites)",
	"moveProjectOrgSharedChatsSQL": "restamps the moved project's shared chats (MoveProjectOrg, orgSensitiveStoreWrites)",
	"moveProjectOrgChatsSQL":       "restamps the moved project's chats (MoveProjectOrg, orgSensitiveStoreWrites)",
	"moveProjectOrgRunsSQL":        "restamps the moved chats' runs (MoveProjectOrg, orgSensitiveStoreWrites)",
	"moveProjectOrgTriggerSQL":     "restamps a moved trigger (MoveProjectOrg, orgSensitiveStoreWrites)",
	// Provenance, not placement: the org of the run a thread's call was paid in. Nothing is
	// scoped by it; a project move leaves it naming the old org, which only makes it not count.
	"PutThreadFunding": "records the org a thread's call was paid in (thread_funding); no org lock: it scopes nothing",
	// A machine's org, mirrored from control-plane at its connect. The org
	// lock guards a user's rows against the lazy backfill, which never
	// stamps daemons: nothing races it.
	"UpsertDaemonRegistration": "mirrors a machine's org (daemons.org_id) from control-plane at connect; no org lock: the backfill never stamps daemons",
}

var writesOrgID = regexp.MustCompile(`(?i)\bSET\s+org_id\b|\borg_id\s*=\s*COALESCE\(`)

type draftMethod struct {
	pos            string
	writes, helper map[string]bool
	locks          bool
}

// takesLock reports whether m takes the org lock itself or through a helper
// method of the package, however many calls deep.
func takesLock(m *draftMethod, methods map[string]*draftMethod, seen map[string]bool) bool {
	if m == nil {
		return false
	}
	if m.locks {
		return true
	}
	for h := range m.helper {
		if seen[h] {
			continue
		}
		seen[h] = true
		if takesLock(methods[h], methods, seen) {
			return true
		}
	}
	return false
}

// TestWorkflowDraftWritesTakeTheOrgLock is the same rule for the one package
// that writes workflow_drafts: a method that calls a draft write takes the
// owner's org lock in its own body or through a helper that does, and every
// draft write is made by some method (so a rename cannot leave the list stale).
func TestWorkflowDraftWritesTakeTheOrgLock(t *testing.T) {
	dir := filepath.Join("..", "workflowdrafts")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	methods := map[string]*draftMethod{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_gen.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Recv == nil {
				continue
			}
			m := &draftMethod{pos: fset.Position(fn.Pos()).String(), writes: map[string]bool{}, helper: map[string]bool{}}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch x := sel.X.(type) {
				case *ast.Ident:
					if x.Name == "s" {
						m.helper[sel.Sel.Name] = true
					}
				case *ast.SelectorExpr:
					if x.Sel.Name == "Locks" && sel.Sel.Name == "LockUserOrgs" {
						m.locks = true
					}
					if x.Sel.Name == "Drafts" && draftStoreWrites[sel.Sel.Name] {
						m.writes[sel.Sel.Name] = true
					}
				}
				return true
			})
			methods[fn.Name.Name] = m
		}
	}
	used := map[string]bool{}
	for name, m := range methods {
		for write := range m.writes {
			used[write] = true
			if !takesLock(methods[name], methods, map[string]bool{}) {
				t.Errorf("%s: (*service).%s calls Drafts.%s without its owner's org lock: take Locks.LockUserOrgs first in the transaction", m.pos, name, write)
			}
		}
	}
	for write := range draftStoreWrites {
		if !used[write] {
			t.Errorf("draftStoreWrites[%q] is called by no workflowdrafts method any more: remove it", write)
		}
	}
}

// TestOrgSensitiveWritesTakeTheOrgLock holds the backfill-vs-write race
// closed for every org-sensitive write, not path by path:
//   - every *Repo method that calls an orgSensitiveStoreWrites function takes
//     its owner's org lock in the same body (store.LockUserOrgs, or a Repo
//     helper that does);
//   - every internal/store declaration whose SQL writes org_id is listed in
//     orgWritingDecls, so a new org writer is classified before it lands.
//
// It reads every file itself (os.ReadFile), so Go's test cache sees each one.
func TestOrgSensitiveWritesTakeTheOrgLock(t *testing.T) {
	// The Repo methods: lockers call store.LockUserOrgs directly; a method
	// may also call a locker.
	type method struct {
		pos           string
		calls, lockrs map[string]bool
	}
	methods := map[string]*method{}
	lockers := map[string]bool{}
	parse := func(dir string, visit func(path string, src []byte, file *ast.File, fset *token.FileSet)) {
		t.Helper()
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path != dir {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			visit(path, src, file, fset)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	parse(".", func(_ string, _ []byte, file *ast.File, fset *token.FileSet) {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Recv == nil {
				continue
			}
			m := &method{pos: fset.Position(fn.Pos()).String(), calls: map[string]bool{}, lockrs: map[string]bool{}}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				recv, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				switch {
				case recv.Name == "store" && sel.Sel.Name == "LockUserOrgs":
					lockers[fn.Name.Name] = true
				case recv.Name == "store" && orgSensitiveStoreWrites[sel.Sel.Name]:
					m.calls[sel.Sel.Name] = true
				case recv.Name == "r":
					m.lockrs[sel.Sel.Name] = true
				}
				return true
			})
			methods[fn.Name.Name] = m
		}
	})
	var unlocked []string
	used := map[string]bool{}
	// A store function that wraps an org-sensitive write in its own
	// transaction (store.CommitProjectOrgMove) is held to the same rule: it
	// calls LockUserOrgs in the same body, as the Repo methods above do.
	parse(filepath.Join("..", "store"), func(path string, _ []byte, file *ast.File, fset *token.FileSet) {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Recv != nil || orgSensitiveStoreWrites[fn.Name.Name] {
				continue
			}
			var writes []string
			locks := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if id, ok := call.Fun.(*ast.Ident); ok {
						switch {
						case id.Name == "LockUserOrgs":
							locks = true
						case orgSensitiveStoreWrites[id.Name]:
							writes = append(writes, id.Name)
						}
					}
				}
				return true
			})
			for _, w := range writes {
				used[w] = true
				if !locks {
					unlocked = append(unlocked, fset.Position(fn.Pos()).String()+": store."+fn.Name.Name+" calls "+w)
				}
			}
		}
	})
	for name, m := range methods {
		for write := range m.calls {
			used[write] = true
			locked := lockers[name]
			for helper := range m.lockrs {
				locked = locked || lockers[helper]
			}
			if !locked {
				unlocked = append(unlocked, m.pos+": (*Repo)."+name+" calls store."+write)
			}
		}
	}
	sort.Strings(unlocked)
	for _, u := range unlocked {
		t.Errorf("%s without its owner's org lock: take store.LockUserOrgs first in the transaction (internal/store/org_lock.go)", u)
	}
	for write := range orgSensitiveStoreWrites {
		if !used[write] {
			t.Errorf("orgSensitiveStoreWrites[%q] is called by no Repo method any more: remove it", write)
		}
	}

	// The org writers in internal/store.
	seen := map[string]bool{}
	parse(filepath.Join("..", "store"), func(path string, src []byte, file *ast.File, fset *token.FileSet) {
		for _, decl := range file.Decls {
			var names []string
			switch d := decl.(type) {
			case *ast.FuncDecl:
				names = []string{d.Name.Name}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						for _, n := range vs.Names {
							names = append(names, n.Name)
						}
					}
				}
			}
			text := string(src[fset.Position(decl.Pos()).Offset:fset.Position(decl.End()).Offset])
			if len(names) == 0 || !writesOrgID.MatchString(text) {
				continue
			}
			for _, name := range names {
				seen[name] = true
				if _, ok := orgWritingDecls[name]; !ok {
					t.Errorf("%s: %s writes org_id but is not in orgWritingDecls: decide how it takes the org lock and list it", path, name)
				}
			}
		}
	})
	for name := range orgWritingDecls {
		if !seen[name] {
			t.Errorf("orgWritingDecls[%q] writes no org_id any more: remove it", name)
		}
	}
}
