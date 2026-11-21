package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/bugreport"
	"github.com/reliant-labs/forge/pkg/observe/cliev"
)

func TestWantsMachineOutput(t *testing.T) {
	for args, want := range map[string]bool{
		"env deploy prod --json": true, "obs check prod -q": true, "env deploy prod": false, "telemetry status": false,
	} {
		if got := wantsMachineOutput(strings.Fields(args)); got != want {
			t.Errorf("wantsMachineOutput(%q) = %v, want %v", args, got, want)
		}
	}
}

func TestTelemetryAdoptsTheBugReportInstallID(t *testing.T) {
	dir := t.TempDir()
	if got := bugReportInstallID(dir); got != "" {
		t.Errorf("no bug report state: got %q", got)
	}
	doc := `{"install_id": "11111111-2222-4333-8444-555555555555"}`
	if err := os.WriteFile(filepath.Join(dir, bugreport.StateFile), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := bugReportInstallID(dir); got != "11111111-2222-4333-8444-555555555555" {
		t.Errorf("bugReportInstallID = %q", got)
	}
}

func TestTelemetryCommandRecordsTheChoice(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{}
	cfg := cliev.Config{App: "forge", Endpoint: "http://127.0.0.1:1/v1/logs", ConfigDir: dir, Stderr: &bytes.Buffer{},
		Getenv: func(k string) string { return env[k] }}
	cliev.Init(cfg)
	t.Cleanup(func() { cliev.Init(cliev.Config{App: "forge", ConfigDir: t.TempDir(), Stderr: &bytes.Buffer{}}) })

	run := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		cmd := newTelemetryCmd()
		cmd.SetOut(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("telemetry %v: %v", args, err)
		}
		return out.String()
	}
	if out := run("status"); !strings.Contains(out, "telemetry: on") || !strings.Contains(out, "install id: ") {
		t.Errorf("status = %q", out)
	}
	if out := run("off"); !strings.Contains(out, "telemetry: off (opted_out)") {
		t.Errorf("off = %q", out)
	}
	if st := cliev.New(cfg).Status(); st.Enabled {
		t.Errorf("off must be remembered: %+v", st)
	}
	if out := run("on"); !strings.Contains(out, "telemetry: on") {
		t.Errorf("on = %q", out)
	}
}
