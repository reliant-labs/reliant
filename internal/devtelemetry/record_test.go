package devtelemetry

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestReadWithoutARecord(t *testing.T) {
	rec, ok, err := Read(t.TempDir(), "dev")
	if err != nil || ok || rec != (Record{}) {
		t.Fatalf("Read of a missing record = %+v, %v, %v; want zero, false, nil", rec, ok, err)
	}
}

func TestWriteThenRead(t *testing.T) {
	dir := t.TempDir()
	want := Record{
		Env: "dev", Backend: "cloud", Reason: "dev_telemetry=auto: signed in",
		OrgID: "org-1", EnvironmentID: "env-1",
		InstanceID: "sess-1", InstanceLabel: "alice/fix-login", InstanceUser: "alice@x",
		Endpoint: "https://otlp.reliantapi.com", DecidedAt: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC),
	}
	if err := Write(dir, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := Read(dir, "dev")
	if err != nil || !ok {
		t.Fatalf("Read = %v, %v", ok, err)
	}
	if !got.DecidedAt.Equal(want.DecidedAt) {
		t.Errorf("DecidedAt = %v, want %v", got.DecidedAt, want.DecidedAt)
	}
	got.DecidedAt = want.DecidedAt
	if got != want {
		t.Errorf("Read = %+v\nwant %+v", got, want)
	}

	p := Path(dir, "dev")
	if p != filepath.Join(dir, ".forge", "obs", "backend-dev.json") {
		t.Errorf("Path = %s", p)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no POSIX permission bits: every file reads 0666.
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("record mode = %v, want 0600", fi.Mode().Perm())
	}
	if _, err := os.Stat(p + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temporary file left behind: %v", err)
	}

	// A second write replaces the first; another env's record is its own.
	want.Backend, want.Reason = "local", "dev_telemetry=local"
	if err := Write(dir, want); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := Read(dir, "dev"); got.Backend != "local" {
		t.Errorf("rewrite not read back: %+v", got)
	}
	if _, ok, _ := Read(dir, "staging"); ok {
		t.Error("staging read dev's record")
	}
}

func TestReadRefusesACorruptRecord(t *testing.T) {
	dir := t.TempDir()
	p := Path(dir, "dev")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := Read(dir, "dev"); err == nil || ok {
		t.Errorf("a corrupt record read as ok=%v err=%v", ok, err)
	}
}
