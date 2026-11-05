package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateHooks_ValidLoads(t *testing.T) {
	in := validBaseYAML + `generate:
  hooks:
    - name: sqlc
      command: [sqlc, generate]
      writes: [internal/db/postgres/generated]
    - name: docs
      command: [sh, -c, "make docs"]
      dir: tools
`
	cfg, err := LoadProject([]byte(in), "forge.yaml")
	if err != nil {
		t.Fatalf("expected clean load, got: %v", err)
	}
	hooks := cfg.Generate.Hooks
	if len(hooks) != 2 || hooks[0].Name != "sqlc" || hooks[1].Dir != "tools" {
		t.Fatalf("hooks did not decode in order: %+v", hooks)
	}
	if got := strings.Join(hooks[0].Command, " "); got != "sqlc generate" {
		t.Errorf("command = %q, want %q", got, "sqlc generate")
	}
}

func TestGenerateHooks_Invalid(t *testing.T) {
	cases := []struct {
		name  string
		hooks string
		want  []string
	}{
		{
			name:  "missing name",
			hooks: "    - command: [sqlc, generate]\n",
			want:  []string{"generate.hooks[0] has no name"},
		},
		{
			name:  "duplicate name",
			hooks: "    - name: a\n      command: [x]\n    - name: a\n      command: [y]\n",
			want:  []string{`hook name "a" is declared twice`},
		},
		{
			name:  "missing command",
			hooks: "    - name: a\n",
			want:  []string{"generate.hooks[0] has no command"},
		},
		{
			name:  "dir escapes the project",
			hooks: "    - name: a\n      command: [x]\n      dir: ../elsewhere\n",
			want:  []string{`generate.hooks[0].dir "../elsewhere" is not inside the project`},
		},
		{
			name:  "absolute write",
			hooks: "    - name: a\n      command: [x]\n      writes: [/etc]\n",
			want:  []string{`generate.hooks[0].writes[0] "/etc" is not a path inside the project`},
		},
		{
			// Capturing the whole project would journal every file.
			name:  "project root as a write",
			hooks: "    - name: a\n      command: [x]\n      writes: [.]\n",
			want:  []string{`generate.hooks[0].writes[0] "." is not a path inside the project`},
		},
		{
			name:  "unknown hook key",
			hooks: "    - name: a\n      command: [x]\n      cmd: [y]\n",
			want:  []string{"unknown key", "generate.hooks[0].cmd"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validBaseYAML + "generate:\n  hooks:\n" + tc.hooks
			_, err := LoadProject([]byte(in), "forge.yaml")
			ve := requireValidationError(t, err)
			if !containsAll(ve.Error(), tc.want...) {
				t.Errorf("error missing %q:\n%s", tc.want, ve.Error())
			}
		})
	}
}

// A root sqlc.yaml that no hook runs gets a non-gating migration hint: forge
// used to run it implicitly, and without the hint its output would go stale
// silently. A hook that runs sqlc silences it.
func TestGenerateHooks_SqlcHint(t *testing.T) {
	load := func(t *testing.T, forgeYAML string) string {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "forge.yaml")
		if err := os.WriteFile(path, []byte(forgeYAML), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "sqlc.yaml"), []byte("version: \"2\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var sink strings.Builder
		prev := SetConfigWarningSink(&sink)
		defer SetConfigWarningSink(prev)
		if _, err := LoadProject([]byte(forgeYAML), path); err != nil {
			t.Fatalf("the hint must not gate the load: %v", err)
		}
		return sink.String()
	}

	t.Run("no hook warns", func(t *testing.T) {
		got := load(t, validBaseYAML)
		if !containsAll(got, "sqlc.yaml is no longer run by `forge generate`", "command: [sqlc, generate]") {
			t.Errorf("want the sqlc migration hint, got:\n%s", got)
		}
	})
	t.Run("a sqlc hook silences it", func(t *testing.T) {
		got := load(t, validBaseYAML+"generate:\n  hooks:\n    - name: q\n      command: [sqlc, generate]\n")
		if strings.Contains(got, "sqlc") {
			t.Errorf("a hook running sqlc must silence the hint, got:\n%s", got)
		}
	})
}
