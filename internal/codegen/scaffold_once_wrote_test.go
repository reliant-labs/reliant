package codegen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// `forge generate` prints "✅ Scaffolded internal/app/login_broker.go" (and
// the auth.go line) from these functions' bool, and agents take a ✅ line as
// proof the file exists. It once printed unconditionally while the generator
// declined, so a project with no login_broker.go before or after the run was
// told one had been written. The bool must be true exactly when this call
// wrote the file.

func TestGenerateLoginBroker_ReportsWhetherItWrote(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "internal", "app", "login_broker.go")

	wrote, err := GenerateLoginBroker("example.com/app", "app", dir)
	if err != nil {
		t.Fatalf("GenerateLoginBroker: %v", err)
	}
	if !wrote {
		t.Fatal("first run on a fresh project reported wrote=false")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("reported wrote=true but the file is not there: %v", err)
	}

	// Present: scaffold-once declines.
	if wrote, err := GenerateLoginBroker("example.com/app", "app", dir); err != nil || wrote {
		t.Fatalf("second run over an existing file: wrote=%v err=%v, want false, nil", wrote, err)
	}

	// Deleted on purpose: the ledger keeps it gone, and nothing claims otherwise.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if wrote, err := GenerateLoginBroker("example.com/app", "app", dir); err != nil || wrote {
		t.Fatalf("run after a deliberate deletion: wrote=%v err=%v, want false, nil", wrote, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a deliberately deleted login_broker.go was resurrected (stat err %v)", err)
	}
}

// A config that declares none of the broker's settings: nothing to scaffold,
// so nothing is reported written.
func TestGenerateLoginBroker_NoBrokerConfigReportsNotWritten(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/app\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// An UNSTAMPED descriptor (no proto tree to hash, so the staleness
	// guard does not apply) whose root config has no broker fields.
	desc := ForgeDescriptor{Configs: []ConfigMessage{{
		Name:   "AppConfig",
		Fields: []ConfigField{{Name: "port", GoName: "Port", GoType: "int32", ProtoType: "int32"}},
	}}}
	b, err := json.Marshal(desc)
	if err != nil {
		t.Fatal(err)
	}
	descDir := filepath.Join(dir, "gen", "forge_descriptor")
	if err := os.MkdirAll(descDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(descDir, "fixture.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, declared := configDeclaring(dir, brokerConfigFields); declared {
		t.Fatal("fixture is wrong: its config declares the broker fields, so this test would not cover the no-config decline")
	}

	wrote, err := GenerateLoginBroker("example.com/app", "app", dir)
	if err != nil {
		t.Fatalf("GenerateLoginBroker: %v", err)
	}
	if wrote {
		t.Fatal("reported wrote=true for a project whose config declares no broker settings")
	}
	if _, err := os.Stat(filepath.Join(dir, "internal", "app", "login_broker.go")); !os.IsNotExist(err) {
		t.Fatalf("login_broker.go was written for a project with no broker config (stat err %v)", err)
	}
}

func TestGenerateAuthSetup_ReportsWhetherItWrote(t *testing.T) {
	dir := t.TempDir()
	wrote, err := GenerateAuthSetup("example.com/app", dir)
	if err != nil {
		t.Fatalf("GenerateAuthSetup: %v", err)
	}
	if !wrote {
		t.Fatal("first run on a fresh project reported wrote=false")
	}
	if _, err := os.Stat(filepath.Join(dir, "internal", "app", "auth.go")); err != nil {
		t.Fatalf("reported wrote=true but the file is not there: %v", err)
	}
	if wrote, err := GenerateAuthSetup("example.com/app", dir); err != nil || wrote {
		t.Fatalf("second run over an existing file: wrote=%v err=%v, want false, nil", wrote, err)
	}
}
