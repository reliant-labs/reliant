package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ─── chat_updates has exactly the writers chat_update_heads knows ──────────
//
// chat_update_heads holds each chat's newest snapshot rows of the
// chat_updates log, and the snapshot read serves them instead of the log
// once a chat's heads_through_seq vouches for them
// (dev-docs/design/chat-update-heads.md). That is correct only while every
// change to the log also reaches the heads table:
//
//   - an INSERT goes through AppendChatUpdate or SaveMessageAtomic, which
//     apply the row's head in the inserting transaction (ApplyChatUpdateHead);
//   - a row is never UPDATEd in place, which would leave its head stale;
//   - a DELETE removes the heads it leaves behind with it (accountpurge
//     deletes chat_update_heads beside chat_updates).
//
// A new writer — the generated ORM's Update/Delete, the generated store,
// another raw INSERT/UPDATE/DELETE — would serve stale or orphaned heads
// without any test of the heads table noticing. This guard makes adding one
// a decision: maintain chat_update_heads in it, then list it here.

// chatUpdatesWriteSQL is a statement that writes chat_updates.
var chatUpdatesWriteSQL = regexp.MustCompile(
	`(?is)\b(?:insert\s+into|update|delete\s+from|truncate(?:\s+table)?)\s+(?:only\s+)?(?:public\.)?"?chat_updates"?(?:[^a-z0-9_]|$)`)

// chatUpdatesWriters are the files allowed to write chat_updates, and what
// they write it with.
var chatUpdatesWriters = map[string][]string{
	// AppendChatUpdate: inserts the row, then ApplyChatUpdateHead.
	// CompactChatUpdatesSQL (ledger retention): deletes only rows a newer row
	// of the same (type, entity) or node supersedes, never the chat's newest,
	// so every row a head points at stays (ledger_retention_heads_test.go).
	"internal/store/chat_update_repo_ext.go": {"CreateChatUpdate", "DELETE FROM chat_updates"},
	// SaveMessageAtomic: inserts the row, then ApplyChatUpdateHead.
	"internal/store/save_message_atomic.go": {"CreateChatUpdate"},
	// The account purge deletes the user's chat_update_heads beside it.
	"internal/accountpurge/accountpurge.go": {"DELETE FROM chat_updates"},
}

// chatUpdateMutators are the generated functions and store methods that
// write a chat_updates row: package-level in internal/store, and methods of
// its generated ChatUpdateStore. ChatUpdates() hands out that store.
var chatUpdateMutators = map[string]bool{
	"CreateChatUpdate":       true,
	"UpdateChatUpdate":       true,
	"UpdateChatUpdateMasked": true,
	"DeleteChatUpdate":       true,
	"ChatUpdates":            true,
}

const storeImportPath = "github.com/reliant-labs/reliant/internal/store"

func TestChatUpdatesWriteSQL(t *testing.T) {
	t.Parallel()
	flagged := []string{
		"INSERT INTO chat_updates (id) VALUES ($1)",
		"insert into public.chat_updates (id) values ($1)",
		"UPDATE chat_updates SET data = $1 WHERE id = $2",
		"DELETE FROM chat_updates WHERE chat_id = $1",
		`DELETE FROM "chat_updates" WHERE chat_id = $1`,
		"TRUNCATE chat_updates",
		"TRUNCATE TABLE ONLY chat_updates",
	}
	for _, sql := range flagged {
		if !chatUpdatesWriteSQL.MatchString(sql) {
			t.Errorf("chatUpdatesWriteSQL misses %q", sql)
		}
	}
	allowed := []string{
		"SELECT data FROM chat_updates WHERE chat_id = $1",
		"INSERT INTO chat_update_heads (chat_id) VALUES ($1)",
		"DELETE FROM chat_update_heads WHERE chat_id = $1",
		"UPDATE update_stream_counters SET heads_through_seq = $1",
		"ON CONFLICT (chat_id, head_key) DO UPDATE SET data = EXCLUDED.data",
	}
	for _, sql := range allowed {
		if chatUpdatesWriteSQL.MatchString(sql) {
			t.Errorf("chatUpdatesWriteSQL flags %q, which does not write chat_updates", sql)
		}
	}
}

func TestChatUpdatesHaveOnlyTheKnownWriters(t *testing.T) {
	t.Parallel()

	repo := root(t)
	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "testdata": true, "vendor": true,
		"tmp": true, "data": true, "dist": true, ".reliant": true,
		"gen": true, "web": true, "electron": true, "proto-vendor": true,
	}
	found := map[string]map[string]bool{}
	note := func(rel, what string) {
		if found[rel] == nil {
			found[rel] = map[string]bool{}
		}
		found[rel][what] = true
	}
	fset := token.NewFileSet()
	err := filepath.WalkDir(repo, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != repo && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		// Generated files define the mutators; a call to one is in an owned file.
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "_gen.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(repo, path)
		rel = filepath.ToSlash(rel)
		inStore := file.Name.Name == "store" && filepath.ToSlash(filepath.Dir(rel)) == "internal/store"
		storeName := ""
		for _, imp := range file.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == storeImportPath {
				storeName = "store"
				if imp.Name != nil {
					storeName = imp.Name.Name
				}
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				switch fn := n.Fun.(type) {
				case *ast.Ident:
					// A package-level generated function, called from inside internal/store.
					if inStore && chatUpdateMutators[fn.Name] {
						note(rel, fn.Name)
					}
				case *ast.SelectorExpr:
					name := fn.Sel.Name
					if !chatUpdateMutators[name] {
						return true
					}
					if x, ok := fn.X.(*ast.Ident); ok && storeName != "" && x.Name == storeName {
						note(rel, name) // store.CreateChatUpdate(...) and friends
						return true
					}
					// db.Repo.CreateChatUpdate goes through AppendChatUpdate; every
					// other mutator name is the generated store's alone.
					if name != "CreateChatUpdate" {
						note(rel, name)
					}
				}
			case *ast.BasicLit:
				if lit, ok := stringLit(n); ok {
					for _, m := range chatUpdatesWriteSQL.FindAllString(lit, -1) {
						verb := strings.Fields(strings.ToUpper(m))
						note(rel, strings.Join(verb[:len(verb)-1], " ")+" chat_updates")
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", repo, err)
	}

	for rel, whats := range found {
		allowed := map[string]bool{}
		for _, w := range chatUpdatesWriters[rel] {
			allowed[w] = true
		}
		var extra []string
		for w := range whats {
			if !allowed[w] {
				extra = append(extra, w)
			}
		}
		sort.Strings(extra)
		for _, w := range extra {
			t.Errorf("%s writes chat_updates (%s), which chat_update_heads does not know about. A served head "+
				"must equal the log's newest row (dev-docs/design/chat-update-heads.md): insert through "+
				"store.AppendChatUpdate, never update a row in place, and delete the chat's chat_update_heads "+
				"beside its chat_updates. Then list the file in chatUpdatesWriters.", rel, w)
		}
	}
	// A listed writer that no longer writes is a stale entry: drop it.
	for rel, whats := range chatUpdatesWriters {
		for _, w := range whats {
			if !found[rel][w] {
				t.Errorf("chatUpdatesWriters lists %s writing chat_updates with %s, but it no longer does: remove the entry", rel, w)
			}
		}
	}
}
