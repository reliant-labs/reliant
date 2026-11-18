package codegen

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestScanTSServices_SampleGenDir(t *testing.T) {
	got, err := ScanTSServices("../../web-runtime/src/testdata/mockgen/gen")
	if err != nil {
		t.Fatal(err)
	}
	want := []TSService{
		{TypeName: "acme.v1.BillingService", Export: "BillingService", ImportPath: "acme/v1/billing_pb", FirstMethod: "getInvoice"},
		{TypeName: "acme.v1.TelemetryService", Export: "TelemetryService", ImportPath: "acme/v1/billing_pb", FirstMethod: "ping"},
		{TypeName: "sample.v1.ProbeService", Export: "ProbeService", ImportPath: "sample/v1/telemetry_pb", FirstMethod: "echoProbe"},
		{TypeName: "sample.v1.TelemetryService", Export: "TelemetryService", ImportPath: "sample/v1/telemetry_pb", FirstMethod: "listTraces"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ScanTSServices =\n%+v\nwant\n%+v", got, want)
	}
}

// A deprecated service carries an extra doc line between the tag and the
// export, and an aliased export name differs from the proto name.
func TestScanTSServices_DocLinesAndMissingDir(t *testing.T) {
	dir := t.TempDir()
	body := `/**
 * Old one.
 *
 * @generated from service legacy.v1.OldService
 * @deprecated
 */
export const OldService: GenService<{
  /**
   * @generated from rpc legacy.v1.OldService.Fetch
   */
  fetch: {
    methodKind: "unary";
    input: typeof FetchRequestSchema;
    output: typeof FetchResponseSchema;
  },
}> = /*@__PURE__*/
  serviceDesc(file_legacy_v1_old, 0);
`
	if err := os.MkdirAll(filepath.Join(dir, "legacy", "v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "legacy", "v1", "old_pb.ts"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// Not a protoc-gen-es module: ignored.
	if err := os.WriteFile(filepath.Join(dir, "index.ts"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ScanTSServices(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []TSService{{TypeName: "legacy.v1.OldService", Export: "OldService", ImportPath: "legacy/v1/old_pb", FirstMethod: "fetch"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}

	none, err := ScanTSServices(filepath.Join(dir, "missing"))
	if err != nil || len(none) != 0 {
		t.Errorf("missing dir: got %v, %v; want none, nil", none, err)
	}
}

func TestPlanServiceDefaults_QualifiesCollisions(t *testing.T) {
	got := PlanServiceDefaults([]TSService{
		{TypeName: "acme.v1.TelemetryService", Export: "TelemetryService"},
		{TypeName: "acme.v1.BillingService", Export: "BillingService"},
		{TypeName: "sample.v1.TelemetryService", Export: "TelemetryService"},
		{TypeName: "LooseService", Export: "LooseService"},
	})
	type nc struct{ Slug, Const string }
	var names []nc
	for _, m := range got {
		names = append(names, nc{m.Slug, m.Const})
	}
	want := []nc{
		{"acme-v1-telemetry-service", "acmeV1TelemetryServiceDefaults"},
		{"billing-service", "billingServiceDefaults"},
		{"sample-v1-telemetry-service", "sampleV1TelemetryServiceDefaults"},
		{"loose-service", "looseServiceDefaults"},
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("got %+v, want %+v", names, want)
	}
}
