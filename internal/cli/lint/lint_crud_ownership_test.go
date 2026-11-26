package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/scaffold"
)

const ownershipBirthSQL = `-- forge:backward-compatible — create table tasks (a comment, not a statement)
CREATE TABLE tasks (
    id      TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    title   TEXT NOT NULL
);
`

// taskService declares the CRUD quintet over tasks; auth decides whether
// each rpc is authenticated.
func taskService(auth bool) codegen.ServiceDef {
	var ms []codegen.Method
	for _, n := range []string{"CreateTask", "GetTask", "ListTasks", "UpdateTask", "DeleteTask"} {
		ms = append(ms, codegen.Method{Name: n, AuthRequired: auth})
	}
	return codegen.ServiceDef{Name: "TaskService", Methods: ms}
}

func allGenerated(codegen.ServiceDef, codegen.Method) bool { return true }

// ownershipFindings replays migrations (file name → SQL) and judges them
// against services, every op generated unless generated says otherwise.
func ownershipFindings(t *testing.T, migrations map[string]string, generated func(codegen.ServiceDef, codegen.Method) bool, services ...codegen.ServiceDef) []crudOwnershipFinding {
	t.Helper()
	root := writeGrantsProject(t, migrations)
	schema, err := ownershipSchemaFromMigrations(root, filepath.Join(root, "db", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	return crudOwnershipFindingsFor(schema, services, generated)
}

func TestCRUDOwnership_Answers(t *testing.T) {
	for name, comment := range map[string]string{
		"table-form owner":      `COMMENT ON TABLE tasks IS 'forge:owner=user_id';`,
		"table-form path owner": `COMMENT ON TABLE tasks IS 'forge:owner=user:user_id,org:org_id';`,
		"column-form owner":     `COMMENT ON COLUMN tasks.user_id IS 'forge:owner';`,
		"public with a reason":  `COMMENT ON TABLE tasks IS 'forge:public: the pricing page lists every task';`,
		"unscoped with reason":  `COMMENT ON TABLE tasks IS 'forge:unscoped: admin service only';`,
		// generate reports a spec that does not resolve; lint does not restate it.
		"unresolvable owner": `COMMENT ON TABLE tasks IS 'forge:owner=nope_id';`,
	} {
		t.Run(name, func(t *testing.T) {
			fs := ownershipFindings(t, map[string]string{
				"0001_birth.up.sql": ownershipBirthSQL,
				"0002_owner.up.sql": comment + "\n",
			}, allGenerated, taskService(true))
			if len(fs) != 0 {
				t.Fatalf("findings = %+v, want none", fs)
			}
		})
	}
}

func TestCRUDOwnership_PlainAuthenticatedCRUDIsAFinding(t *testing.T) {
	fs := ownershipFindings(t, map[string]string{"0001_birth.up.sql": ownershipBirthSQL}, allGenerated, taskService(true))
	if len(fs) != 1 {
		t.Fatalf("findings = %+v, want one", fs)
	}
	f := fs[0]
	if f.Table != "tasks" || f.File != "db/migrations/0001_birth.up.sql" || f.Line != 2 {
		t.Errorf("finding at %s:%d for %s, want db/migrations/0001_birth.up.sql:2 for tasks", f.File, f.Line, f.Table)
	}
	if got, want := strings.Join(f.RPCs, ","), "TaskService.CreateTask,TaskService.DeleteTask,TaskService.GetTask,TaskService.ListTasks,TaskService.UpdateTask"; got != want {
		t.Errorf("rpcs = %s, want %s", got, want)
	}
	for _, want := range []string{
		"COMMENT ON TABLE tasks IS 'forge:owner=user_id'",
		"COMMENT ON TABLE tasks IS 'forge:unscoped: <why>'",
		"'forge:public: <why>'",
	} {
		if !strings.Contains(f.fixHint(), want) {
			t.Errorf("fix hint lacks %q: %s", want, f.fixHint())
		}
	}
	if !strings.Contains(f.message(), "TaskService.GetTask") {
		t.Errorf("message does not name the rpcs: %s", f.message())
	}
}

// Forge's own birth migration does not answer the rule for its author: a
// table `forge scaffold` births with no owner field is a finding until the
// author declares an owner or an exemption, and one born with a
// `// forge:owner` field answers it with the column form.
func TestCRUDOwnership_ScaffoldedBirthIsAskedNotExempted(t *testing.T) {
	spec := func(owner bool) scaffold.EntityFromProtoSpec {
		return scaffold.EntityFromProtoSpec{
			Table:     "tasks",
			MessageFQ: "services.task.v1.Task",
			ProtoPkg:  "services.task.v1",
			Fields: []codegen.SchemaFieldDef{
				{Name: "id", Kind: "string"},
				{Name: "user_id", Kind: "string", Owner: owner},
				{Name: "title", Kind: "string"},
			},
			Timestamps: true,
		}
	}
	born := scaffold.RenderEntityMigrationFromProto(spec(false)).UpSQL
	fs := ownershipFindings(t, map[string]string{"0001_birth.up.sql": born}, allGenerated, taskService(true))
	if len(fs) != 1 || fs[0].Table != "tasks" || fs[0].Marker != "" {
		t.Fatalf("an ownerless birth must be asked whose rows it holds; findings = %+v\n%s", fs, born)
	}

	// The author answers in a new migration, as the scaffold note says.
	fs = ownershipFindings(t, map[string]string{
		"0001_birth.up.sql": born,
		"0002_scope.up.sql": "COMMENT ON TABLE tasks IS 'forge:unscoped: admin service only';\n",
	}, allGenerated, taskService(true))
	if len(fs) != 0 {
		t.Fatalf("an author-declared exemption must answer the rule; findings = %+v", fs)
	}

	owned := scaffold.RenderEntityMigrationFromProto(spec(true)).UpSQL
	if fs := ownershipFindings(t, map[string]string{"0001_birth.up.sql": owned}, allGenerated, taskService(true)); len(fs) != 0 {
		t.Fatalf("a birth with a forge:owner field answers the rule; findings = %+v\n%s", fs, owned)
	}
}

func TestCRUDOwnership_ExemptionWithoutAReasonIsAFinding(t *testing.T) {
	fs := ownershipFindings(t, map[string]string{
		"0001_birth.up.sql": ownershipBirthSQL,
		"0002_scope.up.sql": "\n\nCOMMENT ON TABLE tasks IS 'forge:unscoped';\n",
	}, allGenerated, taskService(true))
	if len(fs) != 1 || fs[0].Marker != "forge:unscoped" {
		t.Fatalf("findings = %+v, want one forge:unscoped finding", fs)
	}
	if fs[0].File != "db/migrations/0002_scope.up.sql" || fs[0].Line != 3 {
		t.Errorf("located at %s:%d, want the COMMENT ON TABLE", fs[0].File, fs[0].Line)
	}
	if !strings.Contains(fs[0].fixHint(), "'forge:unscoped: <why>'") {
		t.Errorf("fix hint: %s", fs[0].fixHint())
	}
}

func TestCRUDOwnership_NotJudged(t *testing.T) {
	t.Run("only auth_required:false rpcs", func(t *testing.T) {
		if fs := ownershipFindings(t, map[string]string{"0001_birth.up.sql": ownershipBirthSQL}, allGenerated, taskService(false)); len(fs) != 0 {
			t.Fatalf("findings = %+v, want none", fs)
		}
	})
	t.Run("no generated op (hand-written handlers)", func(t *testing.T) {
		none := func(codegen.ServiceDef, codegen.Method) bool { return false }
		if fs := ownershipFindings(t, map[string]string{"0001_birth.up.sql": ownershipBirthSQL}, none, taskService(true)); len(fs) != 0 {
			t.Fatalf("findings = %+v, want none", fs)
		}
	})
	t.Run("a dropped table", func(t *testing.T) {
		fs := ownershipFindings(t, map[string]string{
			"0001_birth.up.sql": ownershipBirthSQL,
			"0002_drop.up.sql":  "DROP TABLE tasks;\n",
		}, allGenerated, taskService(true))
		if len(fs) != 0 {
			t.Fatalf("findings = %+v, want none", fs)
		}
	})
}

// COMMENT ON replaces the whole comment: a later one without the owner
// withdraws it.
func TestCRUDOwnership_LastCommentWins(t *testing.T) {
	fs := ownershipFindings(t, map[string]string{
		"0001_birth.up.sql": ownershipBirthSQL + "COMMENT ON TABLE tasks IS 'forge:owner=user_id';\n",
		"0002_note.up.sql":  "COMMENT ON TABLE tasks IS 'Tasks a user wrote';\n",
	}, allGenerated, taskService(true))
	if len(fs) != 1 || fs[0].Comment != "Tasks a user wrote" {
		t.Fatalf("findings = %+v, want one carrying the current comment", fs)
	}
	if !strings.Contains(fs[0].fixHint(), `keep its text: it is "Tasks a user wrote"`) {
		t.Errorf("fix hint does not say to keep the comment: %s", fs[0].fixHint())
	}
}

// The provenance read: crud<Method>Op() methods in the service's ops file.
func TestCRUDOwnership_GeneratedOpsAreReadFromTheOpsFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "handlers", "task")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package task\n\nfunc (s *Service) crudGetTaskOp() crud.GetOp[pb.GetTaskRequest, pb.GetTaskResponse, *db.Task] {\n\treturn nil\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "handlers_crud_ops_gen.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	ops := generatedCRUDOps(root, []codegen.ServiceDef{taskService(true)})
	if !ops["TaskService"]["GetTask"] || ops["TaskService"]["ListTasks"] {
		t.Fatalf("ops = %v, want GetTask only", ops)
	}
}

// Warn-first: a project pinned to the release that ships the rule sees a
// warning carrying the gate date; the next release gates. The rule missed tag
// v1.10.0 (#2461 merged after it), so the release that ships it is v1.11.0,
// or a v1.10.x patch cut before it (forge#2638).
func TestCRUDOwnership_RollsOutWarnFirst(t *testing.T) {
	f := lintJSONFinding{File: "db/migrations/0001_birth.up.sql", Line: 2, Severity: lintSevError, Rule: ruleCRUDTableUnowned, Message: "m"}
	for _, pin := range []string{"v1.10.0", "v1.10.1", "v1.11.0", "v1.11.3"} {
		got, gated := gradeProjectLane(pinnedCfg(pin, nil), []lintJSONFinding{f}, true)
		if len(got) != 1 || got[0].Severity != lintSevWarning || gated || got[0].Grace == nil ||
			got[0].Grace.IntroducedIn != "v1.11.0" || got[0].Grace.GatesFrom != "v1.12.0" {
			t.Fatalf("pinned %s: %+v gated=%v, want a warning new in v1.11.0, gating from v1.12.0", pin, got, gated)
		}
	}
	got, gated := gradeProjectLane(pinnedCfg("v1.12.0", nil), []lintJSONFinding{f}, true)
	if len(got) != 1 || got[0].Severity != lintSevError || !gated {
		t.Fatalf("pinned v1.12.0: %+v gated=%v, want an error", got, gated)
	}
}

// End to end over the real readers: the descriptor's auth_required, the ops
// file's provenance, the migrations' comments.
func TestCRUDOwnership_CollectsFromTheProject(t *testing.T) {
	root := writeGrantsProject(t, map[string]string{"0001_birth.up.sql": ownershipBirthSQL})
	desc := `{"Services":[{"Name":"TaskService","Package":"services.task.v1","GoPackage":"example.com/app/gen/services/task/v1",` +
		`"ProtoFile":"proto/services/task/v1/task.proto","Methods":[` +
		`{"Name":"GetTask","InputType":"GetTaskRequest","OutputType":"GetTaskResponse","AuthRequired":true},` +
		`{"Name":"ListTasks","InputType":"ListTasksRequest","OutputType":"ListTasksResponse","AuthRequired":false}]}]}`
	for path, body := range map[string]string{
		"gen/forge_descriptor/fixture.json": desc,
		"go.mod":                            "module example.com/app\n\ngo 1.24\n",
		"internal/handlers/task/handlers_crud_ops_gen.go": "package task\n\n" +
			"func (s *Service) crudGetTaskOp() crud.GetOp[pb.GetTaskRequest, pb.GetTaskResponse, *db.Task] {\n\treturn nil\n}\n\n" +
			"func (s *Service) crudListTasksOp() crud.ListOp[pb.ListTasksRequest, pb.ListTasksResponse, *db.Task] {\n\treturn nil\n}\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fs, err := collectCRUDOwnershipFindings(root, "db/migrations")
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 || strings.Join(fs[0].RPCs, ",") != "TaskService.GetTask" {
		t.Fatalf("findings = %+v, want tasks served by the authenticated GetTask only", fs)
	}
}
