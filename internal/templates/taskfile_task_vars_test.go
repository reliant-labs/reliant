package templates

import (
	"bytes"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// THE BUG THIS CATCHES: db-snapshot-restore read "${DUMP:-…}" from the
// shell, but its documented usage, `task db-snapshot-restore DUMP=x`, sets a
// Task var, and Task never puts a var into the shell's environment. So the
// task restored snapshot.dump.gz whatever DUMP named. A knob a task documents
// as NAME= reaches its shell only through the task's env: block.
func TestServiceTaskfileHandsTaskVarsToTheShell(t *testing.T) {
	rendered := string(renderProject(t, "Taskfile.yml.tmpl", projectData()))
	for _, want := range []string{
		`RESTORE_DUMP: "{{.DUMP}}"`,
		`DUMP="${RESTORE_DUMP:-snapshot.dump.gz}"`,
		`RESTORE_DB: "{{.SNAPSHOT_DB}}"`,
		`PSQL_DB: "{{.POSTGRES_DB}}"`,
		`-d "${PSQL_DB:-`,
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the service Taskfile lacks %q", want)
		}
	}
	for _, stale := range []string{`"${DUMP:-`, `"${SNAPSHOT_DB:-`, `"${POSTGRES_DB:-`, `"${POSTGRES_USER:-`} {
		if strings.Contains(rendered, stale) {
			t.Errorf("the service Taskfile still reads %s… from the shell, where a var given to task never arrives", stale)
		}
	}
}

// TestServiceTaskfileSnapshotRestoreHonoursDUMP runs the rendered Taskfile
// with the real `task` binary and fake Postgres tools: DUMP= given to task
// wins, an exported DUMP still works, and with neither the default dump is
// the one looked for. A dump gunzip cannot read, or a pg_restore that fails,
// fails the task and never prints "Restore complete".
func TestServiceTaskfileSnapshotRestoreHonoursDUMP(t *testing.T) {
	if testing.Short() {
		t.Skip("execs the task binary; runs in task test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the fake docker is a POSIX shell script")
	}
	taskBin, err := exec.LookPath("task")
	if err != nil {
		t.Skip("task is not installed")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), renderProject(t, "Taskfile.yml.tmpl", projectData()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	fake := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fake("docker", `echo "docker $*"`)
	fake("createdb", "exit 0")
	fake("dropdb", "exit 0")
	// pg_restore drains stdin, then exits with PG_RESTORE_EXIT (default 0).
	fake("pg_restore", `cat >/dev/null; echo "pg_restore ran"; exit "${PG_RESTORE_EXIT:-0}"`)
	run := func(env []string, args ...string) string {
		t.Helper()
		// --silent: Task would otherwise echo each script, whose text
		// contains every message the cases look for.
		cmd := exec.Command(taskBin, append([]string{"--silent"}, args...)...)
		cmd.Dir = dir
		// Drop what the host may export, so each case sets exactly what it names.
		var base []string
		for _, kv := range os.Environ() {
			switch strings.SplitN(kv, "=", 2)[0] {
			case "DUMP", "SNAPSHOT_DB", "POSTGRES_USER", "POSTGRES_DB", "PATH":
				continue
			}
			base = append(base, kv)
		}
		cmd.Env = append(append(base, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH")), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return string(out) + "\n[task failed]"
		}
		return string(out)
	}
	write := func(name string, data []byte) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte("PGDMP"))
	_ = zw.Close()
	goodGz := write("good.dump.gz", gz.Bytes())
	corruptGz := write("corrupt.dump.gz", []byte("not gzip at all"))
	plain := write("plain.dump", []byte("PGDMP"))

	missing := func(name string) string { return "Dump file '" + filepath.Join(dir, name) + "' not found" }
	for _, tc := range []struct {
		name string
		env  []string
		args []string
		want string
	}{
		{"var", nil, []string{"db-snapshot-restore", "DUMP=" + filepath.Join(dir, "var.dump")}, missing("var.dump")},
		{"env", []string{"DUMP=" + filepath.Join(dir, "env.dump")}, []string{"db-snapshot-restore"}, missing("env.dump")},
		{"var beats env", []string{"DUMP=" + filepath.Join(dir, "env.dump")}, []string{"db-snapshot-restore", "DUMP=" + filepath.Join(dir, "var.dump")}, missing("var.dump")},
		{"default", nil, []string{"db-snapshot-restore"}, "Dump file 'snapshot.dump.gz' not found"},
		{"restore succeeds", nil, []string{"db-snapshot-restore", "DUMP=" + goodGz}, "==> Restore complete"},
		{"corrupt gzip fails", nil, []string{"db-snapshot-restore", "DUMP=" + corruptGz}, "[task failed]"},
		{"pg_restore failure fails", []string{"PG_RESTORE_EXIT=1"}, []string{"db-snapshot-restore", "DUMP=" + plain}, "[task failed]"},
		{"psql vars", nil, []string{"dev-psql", "POSTGRES_USER=alice", "POSTGRES_DB=shop"}, "docker compose exec postgres psql -U alice -d shop"},
		{"psql defaults", nil, []string{"dev-psql"}, "docker compose exec postgres psql -U postgres -d demo"},
	} {
		out := run(tc.env, tc.args...)
		if !strings.Contains(out, tc.want) {
			t.Errorf("%s: task %v printed\n%s\nwant it to contain %q", tc.name, tc.args, out, tc.want)
		}
		if strings.Contains(out, "[task failed]") && strings.Contains(out, "Restore complete.") {
			t.Errorf("%s: task %v failed yet printed \"Restore complete\":\n%s", tc.name, tc.args, out)
		}
	}
}
