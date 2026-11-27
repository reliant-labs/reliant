package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/scaffold"
)

// bornTasksSQL is the birth migration `forge scaffold` writes for an
// ownerless Task entity: the real renderer, so the checksum is the one a
// fresh project carries.
func bornTasksSQL(t *testing.T) string {
	t.Helper()
	born := scaffold.RenderEntityMigrationFromProto(scaffold.EntityFromProtoSpec{
		Table:     "tasks",
		MessageFQ: "services.task.v1.Task",
		ProtoPkg:  "services.task.v1",
		Fields: []codegen.SchemaFieldDef{
			{Name: "id", Kind: "string"},
			{Name: "title", Kind: "string"},
			{Name: "done", Kind: "bool"},
		},
		Timestamps: true,
	}).UpSQL
	if stamped, pristine := checksums.SQLBirthIsPristine([]byte(born)); !stamped || !pristine {
		t.Fatalf("the birth migration must carry an intact birth checksum (stamped=%v pristine=%v):\n%s", stamped, pristine, born)
	}
	return born
}

// A table still exactly as forge scaffolded it is asked, not failed: the
// finding is a warning that names the entity and keeps the whole fix.
func TestCRUDOwnership_ScaffoldedTableIsPending(t *testing.T) {
	fs := ownershipFindings(t, map[string]string{"0001_create_tasks.up.sql": bornTasksSQL(t)}, allGenerated, taskService(true))
	if len(fs) != 1 {
		t.Fatalf("findings = %+v, want one", fs)
	}
	f := fs[0]
	if !f.Pending || f.severity() != lintSevWarning {
		t.Fatalf("a freshly scaffolded table must be pending at warning; got pending=%v severity=%s", f.Pending, f.severity())
	}
	if !strings.HasPrefix(f.message(), "pending: choose an owner for Task — table tasks") ||
		!strings.Contains(f.message(), "TaskService.GetTask") || !strings.Contains(f.message(), "fails the build once the table is edited") {
		t.Errorf("message = %q, want the pending label, the entity, the rpcs and when it starts to gate", f.message())
	}
	for _, want := range []string{
		"COMMENT ON TABLE tasks IS 'forge:owner=user_id'",
		"COMMENT ON TABLE tasks IS 'forge:unscoped: <why>'",
		"'forge:public: <why>'",
	} {
		if !strings.Contains(f.fixHint(), want) {
			t.Errorf("a pending finding must still carry the concrete fix; fix hint lacks %q: %s", want, f.fixHint())
		}
	}

	// A git checkout with autocrlf still reads as scaffolded.
	crlf := strings.ReplaceAll(bornTasksSQL(t), "\n", "\r\n")
	if fs := ownershipFindings(t, map[string]string{"0001_create_tasks.up.sql": crlf}, allGenerated, taskService(true)); len(fs) != 1 || !fs[0].Pending {
		t.Errorf("a CRLF checkout of the birth must stay pending; findings = %+v", fs)
	}
}

// Every way a person edits the table ends its scaffolded state, and the
// rule gates as it did before the exception existed.
func TestCRUDOwnership_EditedTableGates(t *testing.T) {
	born := bornTasksSQL(t)
	for name, migrations := range map[string]map[string]string{
		"birth edited in place": {
			"0001_create_tasks.up.sql": strings.Replace(born, "    done BOOLEAN", "    notes TEXT NOT NULL DEFAULT '',\n    done BOOLEAN", 1),
		},
		"birth comment reworded": {
			"0001_create_tasks.up.sql": strings.Replace(born, "-- Ownership: not declared.", "-- Ownership: TBD.", 1),
		},
		"column added later": {
			"0001_create_tasks.up.sql": born,
			"0002_add_notes.up.sql":    "ALTER TABLE tasks ADD COLUMN notes TEXT NOT NULL DEFAULT '';\n",
		},
		"index added later": {
			"0001_create_tasks.up.sql": born,
			"0002_index.up.sql":        "CREATE INDEX CONCURRENTLY IF NOT EXISTS tasks_title_idx ON tasks (title);\n",
		},
		"column commented later": {
			"0001_create_tasks.up.sql": born,
			"0002_comment.up.sql":      "COMMENT ON COLUMN tasks.title IS 'what the task is';\n",
		},
		"table commented later": {
			"0001_create_tasks.up.sql": born,
			"0002_comment.up.sql":      "COMMENT ON TABLE tasks IS 'the to-do list';\n",
		},
		"table renamed later": {
			"0001_create_todos.up.sql": checksums.StampSQLBirth(strings.ReplaceAll(stripBirthChecksum(born), "tasks", "todos")),
			"0002_rename.up.sql":       "ALTER TABLE todos RENAME TO tasks;\n",
		},
		"born before the checksum": {
			"0001_create_tasks.up.sql": stripBirthChecksum(born),
		},
		"hand-written": {
			"0001_create_tasks.up.sql": ownershipBirthSQL,
		},
	} {
		t.Run(name, func(t *testing.T) {
			fs := ownershipFindings(t, migrations, allGenerated, taskService(true))
			if len(fs) != 1 || fs[0].Table != "tasks" {
				t.Fatalf("findings = %+v, want one on tasks", fs)
			}
			if fs[0].Pending || fs[0].severity() != lintSevError || strings.HasPrefix(fs[0].message(), "pending:") {
				t.Fatalf("an edited (or never-scaffolded) table must gate; got pending=%v severity=%s message=%q",
					fs[0].Pending, fs[0].severity(), fs[0].message())
			}
		})
	}
}

// Every spelling of a later statement that changes the table ends its
// scaffolded state: a COMMENT ON whatever its value (IS NULL included), on
// the table, a column or an object of it, and a schema-qualified, quoted,
// IF EXISTS or ONLY name. A statement on another table leaves it pending.
func TestCRUDOwnership_EveryEditSpellingGates(t *testing.T) {
	born := bornTasksSQL(t)
	for _, stmt := range []string{
		"COMMENT ON TABLE tasks IS NULL;",
		"comment on table tasks is null;",
		"COMMENT ON COLUMN tasks.title IS NULL;",
		"COMMENT ON TABLE public.tasks IS NULL;",
		`COMMENT ON TABLE "tasks" IS NULL;`,
		`COMMENT ON TABLE "public"."tasks" IS 'x';`,
		`COMMENT ON COLUMN public."tasks"."title" IS NULL;`,
		"COMMENT ON TABLE tasks IS E'the to-do\\'s list';",
		"COMMENT ON TABLE tasks IS $$the to-do list$$;",
		"COMMENT ON COLUMN tasks.title IS $body$what it is$body$;",
		"COMMENT ON CONSTRAINT tasks_pkey ON tasks IS NULL;",
		"COMMENT ON CONSTRAINT tasks_pkey ON public.tasks IS 'the key';",
		"COMMENT ON TRIGGER tasks_touch ON tasks IS NULL;",
		"COMMENT ON POLICY tasks_owner ON tasks IS NULL;",
		"COMMENT ON RULE tasks_rule ON tasks IS NULL;",
		"ALTER TABLE public.tasks ADD COLUMN notes TEXT;",
		`ALTER TABLE "tasks" ADD COLUMN notes TEXT;`,
		"ALTER TABLE IF EXISTS tasks ADD COLUMN notes TEXT;",
		"ALTER TABLE ONLY tasks ADD COLUMN notes TEXT;",
		"ALTER TABLE IF EXISTS ONLY public.tasks ENABLE ROW LEVEL SECURITY;",
		"CREATE UNIQUE INDEX tasks_title_key ON public.tasks (title);",
		"CREATE INDEX tasks_title_idx ON ONLY \"tasks\" (title);",
		"CREATE POLICY tasks_owner ON tasks USING (true);",
		"CREATE TRIGGER tasks_touch BEFORE UPDATE ON public.tasks FOR EACH ROW EXECUTE FUNCTION touch();",
		"CREATE OR REPLACE RULE tasks_rule AS ON INSERT TO tasks DO NOTHING;",
		"DROP TRIGGER IF EXISTS tasks_touch ON tasks;",
		"ALTER POLICY tasks_owner ON public.tasks USING (false);",
		"DROP RULE tasks_rule ON tasks;",
	} {
		t.Run(stmt, func(t *testing.T) {
			fs := ownershipFindings(t, map[string]string{
				"0001_create_tasks.up.sql": born,
				"0002_edit.up.sql":         stmt + "\n",
			}, allGenerated, taskService(true))
			if len(fs) != 1 || fs[0].Table != "tasks" {
				t.Fatalf("findings = %+v, want one on tasks", fs)
			}
			if fs[0].Pending || fs[0].severity() != lintSevError {
				t.Fatalf("%q edits tasks, so it must gate; got pending=%v severity=%s", stmt, fs[0].Pending, fs[0].severity())
			}
		})
	}

	for _, stmt := range []string{
		"COMMENT ON TABLE projects IS NULL;",
		"COMMENT ON COLUMN public.projects.title IS NULL;",
		"ALTER TABLE IF EXISTS ONLY public.projects ADD COLUMN notes TEXT;",
		"CREATE RULE projects_rule AS ON INSERT TO projects DO NOTHING;",
		"COMMENT ON INDEX tasks_title_idx IS NULL;", // names no table: not read
	} {
		t.Run("other: "+stmt, func(t *testing.T) {
			fs := ownershipFindings(t, map[string]string{
				"0001_create_tasks.up.sql": born,
				"0002_other.up.sql":        stmt + "\n",
			}, allGenerated, taskService(true))
			if len(fs) != 1 || !fs[0].Pending {
				t.Fatalf("%q does not edit tasks, so it must stay pending; findings = %+v", stmt, fs)
			}
		})
	}
}

// COMMENT ON replaces the comment whatever the literal's spelling, and IS
// NULL removes it: an owner set by a dollar quote answers the rule, and a
// later IS NULL takes the answer away again.
func TestCRUDOwnership_CommentValueSpellings(t *testing.T) {
	born := bornTasksSQL(t)
	for _, stmt := range []string{
		"COMMENT ON TABLE tasks IS $$forge:owner=user_id$$;",
		"COMMENT ON TABLE tasks IS $o$forge:owner=user_id$o$;",
		"COMMENT ON TABLE tasks IS E'forge:owner=user_id';",
		"COMMENT ON TABLE public.\"tasks\" IS 'forge:owner=user_id';",
	} {
		if fs := ownershipFindings(t, map[string]string{
			"0001_create_tasks.up.sql": born,
			"0002_owner.up.sql":        stmt + "\n",
		}, allGenerated, taskService(true)); len(fs) != 0 {
			t.Errorf("%q declares an owner; findings = %+v, want none", stmt, fs)
		}
	}

	fs := ownershipFindings(t, map[string]string{
		"0001_create_tasks.up.sql": born,
		"0002_owner.up.sql":        "COMMENT ON TABLE tasks IS 'forge:owner=user_id';\n",
		"0003_unset.up.sql":        "COMMENT ON TABLE tasks IS NULL;\n",
	}, allGenerated, taskService(true))
	if len(fs) != 1 || fs[0].Pending || fs[0].Comment != "" || fs[0].severity() != lintSevError {
		t.Fatalf("IS NULL removes the owner, so the edited table gates again; findings = %+v", fs)
	}
}

// A later birth that back-fills a foreign key onto the table is forge's
// write, not a person's: the table stays pending. An edited later birth is a
// person's write.
func TestCRUDOwnership_LaterBirthBackfillKeepsPending(t *testing.T) {
	backfill := checksums.StampSQLBirth("-- Born from proto message services.task.v1.Project (`forge scaffold entity --from-proto`).\n" +
		"CREATE TABLE projects (\n    id TEXT PRIMARY KEY CHECK (id <> '')\n);\n" +
		"ALTER TABLE tasks ADD CONSTRAINT tasks_project_id_fkey FOREIGN KEY (project_id) REFERENCES projects (id);\n")
	migrations := map[string]string{
		"0001_create_tasks.up.sql":    bornTasksSQL(t),
		"0002_create_projects.up.sql": backfill,
	}
	fs := ownershipFindings(t, migrations, allGenerated, taskService(true))
	if len(fs) != 1 || !fs[0].Pending {
		t.Fatalf("a pristine later birth's FK back-fill must leave tasks pending; findings = %+v", fs)
	}

	migrations["0002_create_projects.up.sql"] = strings.Replace(backfill, "CHECK (id <> '')", "", 1)
	fs = ownershipFindings(t, migrations, allGenerated, taskService(true))
	if len(fs) != 1 || fs[0].Pending {
		t.Fatalf("an edited later birth that alters tasks is a person's edit; findings = %+v", fs)
	}
}

// A malformed declaration is never held at warning: an exemption with no
// reason is an error on a table forge just scaffolded too.
func TestCRUDOwnership_MalformedDeclarationOnScaffoldedTableGates(t *testing.T) {
	fs := ownershipFindings(t, map[string]string{
		"0001_create_tasks.up.sql": bornTasksSQL(t),
		"0002_scope.up.sql":        "COMMENT ON TABLE tasks IS 'forge:public';\n",
	}, allGenerated, taskService(true))
	if len(fs) != 1 || fs[0].Marker == "" || fs[0].Pending || fs[0].severity() != lintSevError {
		t.Fatalf("a reasonless exemption must stay an error; findings = %+v", fs)
	}
}

// The lane's verdict follows the findings' severity: a project whose only
// finding is a pending one passes, at every pin, and a raised lint.rules
// entry still makes it gate.
func TestCRUDOwnership_PendingFindingDoesNotGateTheLane(t *testing.T) {
	root := writeGrantsProject(t, map[string]string{"0001_create_tasks.up.sql": bornTasksSQL(t)})
	desc := `{"Services":[{"Name":"TaskService","Package":"services.task.v1","GoPackage":"example.com/app/gen/services/task/v1",` +
		`"ProtoFile":"proto/services/task/v1/task.proto","Methods":[` +
		`{"Name":"GetTask","InputType":"GetTaskRequest","OutputType":"GetTaskResponse","AuthRequired":true}]}]}`
	for path, body := range map[string]string{
		"gen/forge_descriptor/fixture.json": desc,
		"go.mod":                            "module example.com/app\n\ngo 1.24\n",
		"internal/handlers/task/handlers_crud_ops_gen.go": "package task\n\n" +
			"func (s *Service) crudGetTaskOp() crud.GetOp[pb.GetTaskRequest, pb.GetTaskResponse, *db.Task] {\n\treturn nil\n}\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.ProjectConfig{}
	fs, gated, err := lintStepNamed(t, "crud-ownership lint").collect(&lintRunCtx{cwd: root, cfg: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 || fs[0].Severity != lintSevWarning || !strings.HasPrefix(fs[0].Message, "pending: choose an owner for Task") || gated {
		t.Fatalf("got %+v gated=%v, want one ungated pending warning", fs, gated)
	}
	for _, pin := range []string{"v1.11.0", "v1.12.0", "v1.99.0"} {
		if got, gated := gradeProjectLane(pinnedCfg(pin, nil), fs, false); gated || got[0].Severity != lintSevWarning {
			t.Errorf("pinned %s: %+v gated=%v, want the pending warning left ungated", pin, got, gated)
		}
	}
	if got, gated := gradeProjectLane(pinnedCfg("v1.12.0", map[string]string{ruleCRUDTableUnowned: "error"}), fs, false); !gated || got[0].Severity != lintSevError {
		t.Errorf("lint.rules {%s: error} must raise the pending warning to a gate; got %+v gated=%v", ruleCRUDTableUnowned, got, gated)
	}
}

// stripBirthChecksum removes the birth-checksum line: a birth written by a
// forge that did not stamp one.
func stripBirthChecksum(sql string) string {
	var out []string
	for _, line := range strings.SplitAfter(sql, "\n") {
		if !strings.HasPrefix(line, "-- Birth checksum: ") {
			out = append(out, line)
		}
	}
	return strings.Join(out, "")
}
