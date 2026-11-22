package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// A hosted env's records are its control plane's, and `forge env status` says
// so: it reports no local sessions, which once sent its records to the
// machine ledger and labelled the control plane's floors "machine ledger".
func TestHostedEnvRecordsComeFromTheControlPlane(t *testing.T) {
	testkit.Slow(t, "execs git")
	dir := newLedgerTestProject(t, "tl29-hosted")
	t.Setenv("FORGE_TEST_CP_TOKEN", "rlat_test")
	entities := &KCLEntities{
		ControlPlane: &ControlPlaneEntity{
			Type: "control_plane", Endpoint: "http://127.0.0.1:1/", TokenEnv: "FORGE_TEST_CP_TOKEN",
		},
		Workloads: []WorkloadEntity{hostedWL("api")},
	}
	if _, ok := envReportsSessions(entities); ok {
		t.Fatal("precondition: a hosted env reports no local sessions")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	records, _ := readEnvRecordsFor(ctx, dir, "prod", entities, time.Now())
	if records.Source != "control plane" {
		t.Fatalf("records source = %q, want the control plane", records.Source)
	}
}

// An env whose workloads the control plane applies has no break-glass, and
// `forge env status` does not advertise one.
func TestOfflineCacheReportNamesARefusal(t *testing.T) {
	var out bytes.Buffer
	writeOfflineCache(&out, "prod", &offlineCacheReport{Warm: true, Refused: "--offline-ledger cannot deploy prod: it binds hosted services to https://cp.example, which the control plane publishes and converges"})
	if strings.Contains(out.String(), "break-glass: forge env deploy") || !strings.Contains(out.String(), "cannot deploy prod") {
		t.Fatalf("a refused env's offline block:\n%s", out.String())
	}
}
