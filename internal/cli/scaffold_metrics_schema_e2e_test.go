//go:build e2e

package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/observe/metrics"
)

// TestE2EScaffoldedBinaryPrintsMetricsSchema is the end-to-end proof of the
// declared-metrics registry (owner decision 3, contract K3):
//
//  1. a fresh project is born with `<bin> metrics schema`, the owned
//     internal/metrics/metrics.go declaring one example metric, and a CRUD
//     Create shim that records it;
//  2. the built binary prints a forge.metrics/v1 document that carries the
//     built-in metrics AND the project's own declaration, with the project's
//     service name;
//  3. `forge obs metrics declared --binary` reads that same document back.
func TestE2EScaffoldedBinaryPrintsMetricsSchema(t *testing.T) {
	t.Parallel()
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "msch", "--mod", "example.com/msch", "--service", "api")
	projectDir := filepath.Join(dir, "msch")
	addCorpusForgePkgReplace(t, projectDir)
	runCmd(t, projectDir, forgeBin, "generate")

	assertPathExistsE2E(t, filepath.Join(projectDir, "internal", "metrics", "metrics.go"))
	assertPathExistsE2E(t, filepath.Join(projectDir, "cmd", "msch", "cmd", "metrics.go"))

	bin := filepath.Join(t.TempDir(), "msch")
	runCmd(t, projectDir, "go", "build", "-o", bin, "./cmd/msch")

	out := runCmdStdout(t, projectDir, bin, "metrics", "schema", "--json")
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("`metrics schema --json` is not JSON: %v\n%s", err, out)
	}
	schema, err := metrics.ParseSchema([]byte(out))
	if err != nil {
		t.Fatalf("parse schema: %v\n%s", err, out)
	}
	if schema.Service != "msch" {
		t.Errorf("service = %q, want msch", schema.Service)
	}
	have := map[string]metrics.Spec{}
	for _, m := range schema.Metrics {
		have[m.Name] = m
	}
	for _, want := range []string{"rpc.server.calls", "rpc.server.duration", "go.memory.used", "go.sql.query_timing", "records_created"} {
		if _, ok := have[want]; !ok {
			t.Errorf("schema does not list %q; has %d metrics", want, len(schema.Metrics))
		}
	}
	if rc := have["records_created"]; rc.Unit != "{record}" || rc.Type != metrics.Counter || len(rc.Attributes) != 2 {
		t.Errorf("records_created = %+v", rc)
	}

	table := runCmdStdout(t, projectDir, bin, "metrics", "schema")
	if !strings.Contains(table, "records_created") || !strings.Contains(table, "NAME") {
		t.Errorf("table output does not list records_created:\n%s", table)
	}

	observed := runCmdStdout(t, projectDir, forgeBin, "obs", "metrics", "declared", "--binary", bin, "--json")
	if _, err := metrics.ParseSchema([]byte(observed)); err != nil {
		t.Fatalf("`forge obs metrics declared --binary` did not return a schema: %v\n%s", err, observed)
	}
}
