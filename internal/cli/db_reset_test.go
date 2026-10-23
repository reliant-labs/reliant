// File: internal/cli/db_reset_test.go
//
// `forge db reset` — drop, recreate, migrate to head, seed. One verb.
//
// It exists because a third dogfood run hit a cycle with NO forge-supported
// escape. Seeding a dev database and then adding a foreign key is an ordinary
// act, and it produced this:
//
//	forge db migrate up    -> FK violation; migration fails part-way, DIRTY at 7
//	forge db seed reset    -> refuses: dirty; "clear it with migrate force 7"
//	forge db migrate force 6
//	forge db seed reset    -> refuses: applied 6 is BEHIND latest 00007
//	forge db migrate up    -> FK violation ... where we came in
//
// Every documented recovery path refuses, and they refuse for OPPOSITE
// reasons: seed reset wants a fully-migrated schema, migrate up wants rows
// that only seed reset can delete. The author escaped with raw psql —
// precisely the manual database surgery `forge db --help` tells you not to do.
//
// reset needs no dirty-state reasoning because it DISCARDS the state, which
// is what makes it the one command that cannot be caught in that loop.

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reset is destructive, so its wiring is part of its contract: the flags must
// be there, and --yes must exist for non-interactive use.
func TestDBResetCommand_Flags(t *testing.T) {
	cmd := newDBResetCommand()

	for _, name := range []string{"dsn", "env", "dir", "yes"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("`forge db reset` must register --%s", name)
		}
	}

	// --env defaults to dev and is not an override: the dev CHECK has no
	// escape hatch, so naming another env refuses rather than permits.
	if got := cmd.Flags().Lookup("env").DefValue; got != "dev" {
		t.Errorf("--env default = %q, want \"dev\"", got)
	}
	// --yes must default false. A destructive command that skips the prompt
	// by default has no prompt.
	if got := cmd.Flags().Lookup("yes").DefValue; got != "false" {
		t.Errorf("--yes default = %q, want \"false\" — confirmation is the default, not the opt-in", got)
	}
}

// The help has to say the command DROPS the database. Someone reaching for
// reset while wedged is not going to read the source first.
func TestDBResetCommand_HelpSaysItDrops(t *testing.T) {
	cmd := newDBResetCommand()
	help := cmd.Long + "\n" + cmd.Short

	for _, want := range []string{"DROP", "dev"} {
		if !strings.Contains(help, want) {
			t.Errorf("`forge db reset --help` must mention %q; got:\n%s", want, help)
		}
	}
}

// reset is reachable as `forge db reset` — not buried under `db seed`, where
// nobody looking for "rebuild my database" would find it.
func TestDBResetCommand_IsRegisteredUnderDB(t *testing.T) {
	found := false
	for _, sub := range newDBCmd().Commands() {
		if sub.Name() == "reset" {
			found = true
		}
	}
	if !found {
		t.Fatal("`forge db reset` is not registered under `forge db`")
	}
}

// The dev gate is the SAME fail-closed classifier seed apply/reset use, and
// there is no override flag. A destructive verb inherits the stricter of the
// two postures, never the looser.
func TestDBReset_RefusesANonDevEnv(t *testing.T) {
	dir := t.TempDir()
	writeEnvConfigK(t, dir, "prod", `environment = "production"`)
	writeEnvConfigK(t, dir, "dev", `environment = "development"`)

	if seedEnvIsDevIn(dir, "prod") {
		t.Fatal("prod classified as dev")
	}
	if !seedEnvIsDevIn(dir, "dev") {
		t.Fatal("dev not classified as dev")
	}

	err := requireDevResetTargetIn(dir, "prod")
	if err == nil {
		t.Fatal("requireDevResetTargetIn(dir, \"prod\") = nil; reset must refuse any env it cannot confirm is dev")
	}
	if !strings.Contains(err.Error(), "prod") {
		t.Errorf("refusal must name the env it rejected; got: %v", err)
	}
}

// The confirmation prompt must PRINT THE TARGET — host and database name —
// not merely an env string that was defaulted. This machine runs several
// postgres instances holding real data, which is exactly how a silent guess
// becomes a disaster.
func TestResetConfirmationPrompt_NamesHostAndDatabase(t *testing.T) {
	prompt := resetConfirmationPrompt("postgres://postgres:hunter2@localhost:5470/docvault3?sslmode=disable")

	for _, want := range []string{"localhost:5470", "docvault3", "DROP"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the confirmation must show %q so the user can see WHICH database is about to be dropped; got:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "hunter2") {
		t.Errorf("the confirmation leaked a password:\n%s", prompt)
	}
}

// Typing anything other than the database name aborts. A bare "y" is too easy
// to type reflexively for DROP DATABASE, and the whole point of printing the
// target is that the user reads it — so echoing it back is the confirmation.
func TestResetConfirmed_RequiresTheDatabaseName(t *testing.T) {
	cases := []struct {
		typed string
		want  bool
	}{
		{"docvault3", true},
		{"docvault3\n", true},
		{"  docvault3  ", true},
		{"y", false},
		{"yes", false},
		{"", false},
		{"roofers2", false},
		{"DOCVAULT3", false},
	}
	for _, tc := range cases {
		if got := resetConfirmed(tc.typed, "docvault3"); got != tc.want {
			t.Errorf("resetConfirmed(%q, \"docvault3\") = %v, want %v", tc.typed, got, tc.want)
		}
	}
}

// writeEnvConfigK writes a minimal scaffolded-shape deploy/kcl/<env>/config.k.
func writeEnvConfigK(t *testing.T, projectDir, env, body string) {
	t.Helper()
	dir := filepath.Join(projectDir, "deploy", "kcl", env)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body = "import config_gen\n\napp_config: config_gen.AppConfig = {\n    " + body + "\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "config.k"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
