package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/secrets"
)

// covASecretProject is an empty forge project directory the secret verbs run
// in; the env's render is whatever entities say.
func covASecretProject(t *testing.T, entities *KCLEntities) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: shop\nmodule_path: example.com/shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	withSecretEntities(t, entities)
	return dir
}

func covAFileSecrets(path string) *KCLEntities {
	return &KCLEntities{SecretProvider: &SecretProviderEntity{Type: "file", Path: path}}
}

func TestFileSecretStorePath(t *testing.T) {
	dir := covASecretProject(t, nil)
	abs := filepath.Join(t.TempDir(), "forge", "p.yaml")
	tests := []struct {
		name     string
		entities *KCLEntities
		want     string
		wantErr  string
	}{
		{"no provider names the line to add", &KCLEntities{}, "", "env \"prod\" declares no secret_provider"},
		{"a provider that is not a file", &KCLEntities{SecretProvider: &SecretProviderEntity{Type: "hosted"}}, "", `declares a "hosted" secret_provider, not FileSecrets`},
		{"the default path is secrets/<env>.yaml", &KCLEntities{SecretProvider: &SecretProviderEntity{Type: "file"}}, filepath.Join(dir, "secrets", "prod.yaml"), ""},
		{"a relative path is anchored in the project", covAFileSecrets("vault/p.yaml"), filepath.Join(dir, "vault", "p.yaml"), ""},
		{"an absolute path is kept", covAFileSecrets(abs), abs, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fileSecretStorePath("prod", tc.entities)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q err %v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestSecretStorePathWrapsARenderFailure(t *testing.T) {
	covASecretProject(t, nil)
	prev := renderEntitiesForSecrets
	renderEntitiesForSecrets = func(context.Context, string) (*KCLEntities, error) { return nil, errors.New("kcl exploded") }
	t.Cleanup(func() { renderEntitiesForSecrets = prev })
	if _, _, err := secretStorePath(context.Background(), "prod"); err == nil || !strings.Contains(err.Error(), "render KCL: kcl exploded") {
		t.Fatalf("err = %v", err)
	}
	if _, err := hostedEntitiesFor(context.Background(), "prod"); err == nil || !strings.Contains(err.Error(), "render KCL: kcl exploded") {
		t.Fatalf("a render failure must not fall through to the file store: %v", err)
	}
}

func TestHostedEntitiesFor(t *testing.T) {
	covASecretProject(t, covAFileSecrets(""))
	if e, err := hostedEntitiesFor(context.Background(), "prod"); err != nil || e != nil {
		t.Errorf("a file env is not hosted: e=%v err=%v", e, err)
	}
	covASecretProject(t, &KCLEntities{SecretProvider: &SecretProviderEntity{Type: "hosted"}})
	if e, err := hostedEntitiesFor(context.Background(), "prod"); err != nil || e == nil {
		t.Errorf("a hosted env returns its entities: e=%v err=%v", e, err)
	}
}

func TestReadSecretValue(t *testing.T) {
	f := filepath.Join(t.TempDir(), "v")
	if err := os.WriteFile(f, []byte("from-file\r\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := readSecretValue(f, strings.NewReader("ignored")); err != nil || got != "from-file" {
		t.Errorf("file: %q %v: trailing newlines are trimmed and the file wins over stdin", got, err)
	}
	if got, err := readSecretValue("", strings.NewReader("  spaced \n")); err != nil || got != "  spaced " {
		t.Errorf("stdin: %q %v: only line endings are trimmed, never the value's own spaces", got, err)
	}
	if _, err := readSecretValue(filepath.Join(t.TempDir(), "absent"), nil); err == nil || !strings.Contains(err.Error(), "read --from-file") {
		t.Errorf("err = %v", err)
	}
	if _, err := readSecretValue("", covAErrReader{}); err == nil || !strings.Contains(err.Error(), "read value from stdin") {
		t.Errorf("err = %v", err)
	}
}

type covAErrReader struct{}

func (covAErrReader) Read([]byte) (int, error) { return 0, errors.New("pipe closed") }

func TestRunSecretSetWritesTheFileStore(t *testing.T) {
	dir := covASecretProject(t, covAFileSecrets("secrets/prod.yaml"))
	var out strings.Builder
	if err := runSecretSet(context.Background(), "prod", "API_KEY", "", strings.NewReader("s3cr3t\n"), &out); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(dir, "secrets", "prod.yaml")
	vals, err := secrets.ReadSecretFile(store)
	if err != nil || vals["API_KEY"] != "s3cr3t" {
		t.Fatalf("store = %v err %v", vals, err)
	}
	if !strings.Contains(out.String(), "set API_KEY (6 bytes) in "+store) || strings.Contains(out.String(), "s3cr3t") {
		t.Errorf("output must report size, never the value: %q", out.String())
	}

	// A second key is added without losing the first.
	if err := runSecretSet(context.Background(), "prod", "OTHER", "", strings.NewReader("x"), &out); err != nil {
		t.Fatal(err)
	}
	if vals, _ := secrets.ReadSecretFile(store); vals["API_KEY"] != "s3cr3t" || vals["OTHER"] != "x" {
		t.Errorf("store = %v", vals)
	}
}

func TestRunSecretSetRefusals(t *testing.T) {
	ctx := context.Background()
	t.Run("an empty value is refused with the way to pipe one in", func(t *testing.T) {
		dir := covASecretProject(t, covAFileSecrets(""))
		err := runSecretSet(ctx, "prod", "API_KEY", "", strings.NewReader("\n"), &strings.Builder{})
		if err == nil || !strings.Contains(err.Error(), "refusing to write an empty value for API_KEY") || !strings.Contains(err.Error(), "forge secret set --env prod API_KEY") {
			t.Fatalf("err = %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "secrets", "prod.yaml")); statErr == nil {
			t.Error("nothing may be written for an empty value")
		}
	})
	t.Run("a name that is no env var", func(t *testing.T) {
		covASecretProject(t, covAFileSecrets(""))
		err := runSecretSet(ctx, "prod", "bad-name", "", strings.NewReader("v"), &strings.Builder{})
		if err == nil || !strings.Contains(err.Error(), `"bad-name" is not a valid env-var name`) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a bad name is still named when the render fails", func(t *testing.T) {
		covASecretProject(t, nil)
		prev := renderEntitiesForSecrets
		renderEntitiesForSecrets = func(context.Context, string) (*KCLEntities, error) { return nil, errors.New("no kcl") }
		t.Cleanup(func() { renderEntitiesForSecrets = prev })
		if err := runSecretSet(ctx, "prod", "bad-name", "", strings.NewReader("v"), &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "not a valid env-var name") {
			t.Fatalf("err = %v", err)
		}
		if err := runSecretSet(ctx, "prod", "GOOD", "", strings.NewReader("v"), &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "render KCL: no kcl") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a missing --from-file", func(t *testing.T) {
		covASecretProject(t, covAFileSecrets(""))
		err := runSecretSet(ctx, "prod", "API_KEY", filepath.Join(t.TempDir(), "nope"), nil, &strings.Builder{})
		if err == nil || !strings.Contains(err.Error(), "read --from-file") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an env with no secret_provider", func(t *testing.T) {
		covASecretProject(t, &KCLEntities{})
		err := runSecretSet(ctx, "prod", "API_KEY", "", strings.NewReader("v"), &strings.Builder{})
		if err == nil || !strings.Contains(err.Error(), "declares no secret_provider") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a corrupt store is not overwritten", func(t *testing.T) {
		dir := covASecretProject(t, covAFileSecrets(""))
		store := filepath.Join(dir, "secrets", "prod.yaml")
		if err := os.MkdirAll(filepath.Dir(store), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store, []byte("{ not: [yaml"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := runSecretSet(ctx, "prod", "API_KEY", "", strings.NewReader("v"), &strings.Builder{}); err == nil {
			t.Fatal("want an error")
		}
		if b, _ := os.ReadFile(store); string(b) != "{ not: [yaml" {
			t.Errorf("the unreadable store was rewritten: %q", b)
		}
	})
}

func TestRunSecretUnset(t *testing.T) {
	ctx := context.Background()
	dir := covASecretProject(t, covAFileSecrets(""))
	store := filepath.Join(dir, "secrets", "prod.yaml")
	if err := secrets.WriteSecretFile(store, map[string]string{"A": "1", "B": "2"}); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := runSecretUnset(ctx, "prod", "A", &out); err != nil {
		t.Fatal(err)
	}
	if vals, _ := secrets.ReadSecretFile(store); len(vals) != 1 || vals["B"] != "2" {
		t.Errorf("store = %v", vals)
	}
	if out.String() != "unset A in "+store+"\n" {
		t.Errorf("out = %q", out.String())
	}
	if err := runSecretUnset(ctx, "prod", "A", &out); err == nil || !strings.Contains(err.Error(), "A is not set in "+store) {
		t.Errorf("unsetting an absent key: %v", err)
	}

	covASecretProject(t, &KCLEntities{})
	if err := runSecretUnset(ctx, "prod", "A", &out); err == nil || !strings.Contains(err.Error(), "declares no secret_provider") {
		t.Errorf("err = %v", err)
	}
	prev := renderEntitiesForSecrets
	renderEntitiesForSecrets = func(context.Context, string) (*KCLEntities, error) { return nil, errors.New("no kcl") }
	t.Cleanup(func() { renderEntitiesForSecrets = prev })
	if err := runSecretUnset(ctx, "prod", "A", &out); err == nil || !strings.Contains(err.Error(), "render KCL: no kcl") {
		t.Errorf("err = %v", err)
	}
}

func TestRunSecretMigrate(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T, env, render string) string {
		t.Helper()
		dir := covASecretProject(t, nil)
		t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, render))
		_ = env
		return dir
	}
	write := func(t *testing.T, path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("dry run lists the keys and writes nothing", func(t *testing.T) {
		dir := setup(t, "prod", `{}`)
		write(t, filepath.Join(dir, ".env.prod"), "B=2\nA=1\n")
		var out strings.Builder
		if err := runSecretMigrate(ctx, "prod", true, &out); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{".env.prod  ->  " + filepath.Join(dir, "secrets", "prod.yaml"), "  A\n  B\n", "dry-run: nothing written (2 key(s) would move)"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("output lacks %q:\n%s", want, out.String())
			}
		}
		if strings.Index(out.String(), "  A\n") > strings.Index(out.String(), "  B\n") {
			t.Error("keys are listed sorted")
		}
		if _, err := os.Stat(filepath.Join(dir, "secrets", "prod.yaml")); err == nil {
			t.Error("a dry run wrote the store")
		}
		if _, err := os.Stat(filepath.Join(dir, ".env.prod")); err != nil {
			t.Error("a dry run removed the legacy file")
		}
	})

	t.Run("moves the keys, merges into an existing store and removes the dotenv", func(t *testing.T) {
		dir := setup(t, "prod", `{"output":{"secret_provider":{"type":"file","path":"vault/prod.yaml"}}}`)
		write(t, filepath.Join(dir, ".env"), "NEW=n\nSHARED=fromenv\n")
		if err := os.MkdirAll(filepath.Join(dir, "vault"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := secrets.WriteSecretFile(filepath.Join(dir, "vault", "prod.yaml"), map[string]string{"OLD": "o", "SHARED": "fromstore"}); err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		if err := runSecretMigrate(ctx, "prod", false, &out); err != nil {
			t.Fatal(err)
		}
		vals, err := secrets.ReadSecretFile(filepath.Join(dir, "vault", "prod.yaml"))
		if err != nil || vals["OLD"] != "o" || vals["NEW"] != "n" || vals["SHARED"] != "fromenv" {
			t.Fatalf("store = %v err %v: a re-run is additive, the dotenv's value wins", vals, err)
		}
		if _, err := os.Stat(filepath.Join(dir, ".env")); err == nil {
			t.Error("the legacy dotenv must be removed once its secrets moved")
		}
		for _, want := range []string{"moved 2 secret(s); removed", "deploy/kcl/prod/main.k", `secret_provider = forge.FileSecrets {path = "vault/prod.yaml"}`} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("output lacks %q:\n%s", want, out.String())
			}
		}
	})

	t.Run("the most specific dotenv wins", func(t *testing.T) {
		dir := setup(t, "prod", `{}`)
		write(t, filepath.Join(dir, ".env"), "GENERIC=1\n")
		write(t, filepath.Join(dir, ".env.prod.secrets"), "SPECIFIC=1\n")
		var out strings.Builder
		if err := runSecretMigrate(ctx, "prod", true, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "SPECIFIC") || strings.Contains(out.String(), "GENERIC") {
			t.Errorf("output:\n%s", out.String())
		}
	})

	t.Run("no legacy file", func(t *testing.T) {
		setup(t, "prod", `{}`)
		err := runSecretMigrate(ctx, "prod", false, &strings.Builder{})
		if err == nil || !strings.Contains(err.Error(), `no legacy .env file found for env "prod" (looked for .env.prod.secrets, .env.prod, .env)`) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a key that is no env-var name stops the migration before anything moves", func(t *testing.T) {
		dir := setup(t, "prod", `{}`)
		write(t, filepath.Join(dir, ".env"), "GOOD=1\nBAD-KEY=2\n")
		err := runSecretMigrate(ctx, "prod", false, &strings.Builder{})
		if err == nil || !strings.Contains(err.Error(), "cannot migrate: 1 key(s) are not valid env-var names: BAD-KEY") {
			t.Fatalf("err = %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(dir, ".env")); statErr != nil {
			t.Error("the dotenv was removed despite the refusal")
		}
		if _, statErr := os.Stat(filepath.Join(dir, "secrets", "prod.yaml")); statErr == nil {
			t.Error("a store was written despite the refusal")
		}
	})
}

func TestSecretProviderPathForEnv(t *testing.T) {
	for name, tc := range map[string]struct{ render, want string }{
		"a file provider":       {`{"output":{"secret_provider":{"type":"file","path":"a/b.yaml"}}}`, "a/b.yaml"},
		"another kind of store": {`{"output":{"secret_provider":{"type":"hosted","path":"x"}}}`, ""},
		"no provider":           {`{}`, ""},
		"a render that fails":   {`not json`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			covASecretProject(t, nil)
			t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, tc.render))
			if got := secretProviderPathForEnv(context.Background(), "prod"); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDeclaredSecretNamesAndGeneratable(t *testing.T) {
	if got := declaredSecretNames(nil); len(got) != 0 {
		t.Errorf("nil entities declare nothing: %v", got)
	}
	if got := generatableSecretNames(nil); len(got) != 0 {
		t.Errorf("nil entities declare nothing: %v", got)
	}
	if !containsString([]string{"a", "b"}, "b") || containsString([]string{"a"}, "z") || containsString(nil, "a") {
		t.Error("containsString")
	}
}
