package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A view opted in with `forge:entity` is a read-only entity (reliant P5b,
// F12.7): chats_with_activity is the one definition of a chat's activity,
// and its row type was hand-written because forge projected BASE TABLEs
// only. A view that did not opt in stays out.
func TestStepInternalDBORM_ProjectsOptedInViewsReadOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("applies the fixture's migrations to a shadow postgres; runs in task test")
	}
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.22\n",
		"db/migrations/00001_init.up.sql": `CREATE TABLE chats (id TEXT PRIMARY KEY, title TEXT NOT NULL, unread BIGINT NOT NULL DEFAULT 0);
CREATE VIEW chats_with_activity AS
  SELECT c.*, (c.unread > 0) AS has_unread FROM chats c;
COMMENT ON VIEW chats_with_activity IS 'the sidebar row — forge:entity';
CREATE VIEW chat_titles AS SELECT title FROM chats;
CREATE MATERIALIZED VIEW chat_counts AS SELECT count(*) AS n FROM chats;
COMMENT ON MATERIALIZED VIEW chat_counts IS 'forge:entity';
`,
	}
	for rel, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := &pipelineContext{ProjectDir: dir, ModulePath: "example.com/app"}
	var stepErr error
	out := captureStdout(t, func() { stepErr = stepInternalDBORM(ctx) })
	if stepErr != nil || ctx.ORMFailure != nil {
		t.Fatalf("stepInternalDBORM: %v / %v\n%s", stepErr, ctx.ORMFailure, out)
	}

	view, err := os.ReadFile(filepath.Join(dir, "internal", "db", "chats_with_activity_orm_gen.go"))
	if err != nil {
		t.Fatalf("the opted-in view was not projected: %v\n%s", err, out)
	}
	src := string(view)
	for _, want := range []string{
		"type ChatsWithActivity struct",
		`bun:"table:chats_with_activity,alias:chats_with_activity"`,
		"HasUnread *bool",
		"func GetChatsWithActivityByID(ctx context.Context, db orm.Context, id string) (*ChatsWithActivity, error)",
		"func ListChatsWithActivity(",
		"func CountChatsWithActivity(",
	} {
		if !strings.Contains(strings.Join(strings.Fields(src), " "), strings.Join(strings.Fields(want), " ")) {
			t.Errorf("view entity lacks %q:\n%s", want, src)
		}
	}
	for _, write := range []string{"func CreateChatsWithActivity", "func UpdateChatsWithActivity", "func DeleteChatsWithActivity"} {
		if strings.Contains(src, write) {
			t.Errorf("a view entity must not write: found %s", write)
		}
	}

	counts, err := os.ReadFile(filepath.Join(dir, "internal", "db", "chat_count_orm_gen.go"))
	if err != nil {
		t.Fatalf("the opted-in materialized view was not projected: %v", err)
	}
	if strings.Contains(string(counts), "ByID") {
		t.Errorf("a keyless view has no Get:\n%s", counts)
	}
	if _, err := os.Stat(filepath.Join(dir, "internal", "db", "chat_title_orm_gen.go")); err == nil {
		t.Error("a view that did not opt in was projected")
	}
}
