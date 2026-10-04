// Copyright (c) 2025 Reliant Labs

package vault_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/vault"
)

func env(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }

type warnings struct{ msgs []string }

func (w *warnings) warn(msg string, _ ...any) { w.msgs = append(w.msgs, msg) }

func TestBootHostedWithoutKeyFails(t *testing.T) {
	_, raw, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()
	dir := t.TempDir()
	_, err := vault.Boot(context.Background(), vault.BootOptions{Hosted: true, Getenv: env(nil), DataDir: dir, DB: raw})
	if err == nil {
		t.Fatal("hosted boot without key succeeded")
	}
	for _, want := range []string{"RELIANT_VAULT_KEY", "forge secret set --env"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if _, statErr := os.Stat(filepath.Join(dir, vault.KeyFileName)); statErr == nil {
		t.Error("hosted boot must never generate a key file")
	}
}

func TestBootMalformedKeyAlwaysFails(t *testing.T) {
	_, raw, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()
	for _, hosted := range []bool{true, false} {
		_, err := vault.Boot(context.Background(), vault.BootOptions{
			Hosted: hosted, Getenv: env(map[string]string{vault.EnvKey: "v1:not-base64-32-bytes"}),
			DataDir: t.TempDir(), DB: raw,
		})
		if err == nil || !strings.Contains(err.Error(), "malformed") {
			t.Errorf("hosted=%v: err = %v, want malformed-key error", hosted, err)
		}
	}
}

func TestBootSelfHostedGeneratesKeyFileAndReusesIt(t *testing.T) {
	_, raw, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	dir := t.TempDir()
	w := &warnings{}

	v, err := vault.Boot(ctx, vault.BootOptions{Getenv: env(nil), DataDir: dir, DB: raw, Warn: w.warn})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, vault.KeyFileName)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("key file not created: %v", err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Errorf("key file mode = %o, want 600", mode)
	}
	if len(w.msgs) != 1 || !strings.Contains(w.msgs[0], "back it up") {
		t.Errorf("warnings = %v, want one back-it-up warning", w.msgs)
	}

	ct, err := v.Seal(ctx, vault.UserTenant("u1"), []byte("secret"), []byte("a"))
	if err != nil {
		t.Fatal(err)
	}

	w2 := &warnings{}
	v2, err := vault.Boot(ctx, vault.BootOptions{Getenv: env(nil), DataDir: dir, DB: raw, Warn: w2.warn})
	if err != nil {
		t.Fatal(err)
	}
	if len(w2.msgs) != 0 {
		t.Errorf("second boot warned: %v", w2.msgs)
	}
	if pt, err := v2.Open(ctx, vault.UserTenant("u1"), ct, []byte("a")); err != nil || string(pt) != "secret" {
		t.Fatalf("second boot cannot open first boot's ciphertext: %q %v", pt, err)
	}
}

func TestBootSelfHostedRefusesWhenSealedDataExistsButKeyMissing(t *testing.T) {
	_, raw, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	v, err := vault.Boot(ctx, vault.BootOptions{Getenv: env(nil), DataDir: t.TempDir(), DB: raw, Warn: (&warnings{}).warn})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Seal(ctx, vault.UserTenant("u1"), []byte("x"), nil); err != nil { // creates a vault_keys row
		t.Fatal(err)
	}

	emptyDir := t.TempDir() // key file "lost"
	_, err = vault.Boot(ctx, vault.BootOptions{Getenv: env(nil), DataDir: emptyDir, DB: raw})
	if err == nil || !strings.Contains(err.Error(), "orphan") {
		t.Fatalf("err = %v, want refusal to orphan sealed data", err)
	}
	if _, statErr := os.Stat(filepath.Join(emptyDir, vault.KeyFileName)); statErr == nil {
		t.Error("a key file was generated over existing sealed data")
	}
}

func TestBootEnvKeyWinsOverFile(t *testing.T) {
	_, raw, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, vault.KeyFileName), []byte("v1:not-used\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Boot(context.Background(), vault.BootOptions{
		Getenv: env(map[string]string{vault.EnvKey: newKeyring(t, "v1")}), DataDir: dir, DB: raw,
	}); err != nil {
		t.Fatalf("env key should win over a bad file: %v", err)
	}
}
