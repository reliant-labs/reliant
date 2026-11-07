package forgeconv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The field every project scaffolded before forge 0d94e103 carries, verbatim
// in shape: control-plane's prod reported reliant-api-server,
// reliant-temporal-worker and daemon-gateway all as service "unknown"
// through it.
const legacyServiceNameConfig = `syntax = "proto3";

package config.v1;

import "forge/v1/forge.proto";

message AppConfig {
  string otlp_endpoint = 21 [(forge.v1.config) = {
    env_var: "OTEL_EXPORTER_OTLP_ENDPOINT"
    flag: "otlp-endpoint"
    description: "OTLP/gRPC collector endpoint (e.g. http://localhost:4317)."
  }];

  string service_name = 22 [(forge.v1.config) = {
    env_var: "OTEL_SERVICE_NAME"
    flag: "service-name"
    default_value: "unknown"
  }];
}
`

func TestConfigServiceName_FlagsTheLegacyField(t *testing.T) {
	t.Parallel()
	got := findingsForRule(lintProtoFile("proto/config/v1/config.proto", legacyServiceNameConfig, LintOptions{}), ruleConfigServiceName)
	if len(got) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(got), got)
	}
	f := got[0]
	if f.Severity != SeverityError {
		t.Errorf("severity = %s, want error", f.Severity)
	}
	if f.Line != 14 {
		t.Errorf("line = %d, want 14 (the field declaration, not the annotation)", f.Line)
	}
	if !strings.Contains(f.Message, `"service_name"`) || !strings.Contains(f.Remediation, "reserved") {
		t.Errorf("finding must name the field and say to reserve it: %+v", f)
	}
}

func TestConfigServiceName_IgnoresReservedAndCommentedOut(t *testing.T) {
	t.Parallel()
	fixed := `syntax = "proto3";

package config.v1;

message AppConfig {
  // string service_name = 22 [(forge.v1.config) = { env_var: "OTEL_SERVICE_NAME" }];
  reserved 22;
  reserved "service_name";
  // OTEL_SERVICE_NAME is set per workload by forge.render.
  string otlp_endpoint = 21 [(forge.v1.config) = { env_var: "OTEL_EXPORTER_OTLP_ENDPOINT" }];
}
`
	if got := findingsForRule(lintProtoFile("proto/config/v1/config.proto", fixed, LintOptions{}), ruleConfigServiceName); len(got) != 0 {
		t.Fatalf("want no findings, got %+v", got)
	}
}

// Suppressible like every other rule, through the tree walk.
func TestConfigServiceName_Suppressible(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "proto", "config", "v1", "config.proto")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	suppressed := strings.Replace(legacyServiceNameConfig, "  string service_name = 22",
		"  // forge:lint-disable-next-line forgeconv-config-service-name: single-workload project\n  string service_name = 22", 1)
	if err := os.WriteFile(path, []byte(suppressed), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := LintProtoTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := findingsForRule(res.Findings, ruleConfigServiceName); len(got) != 0 {
		t.Fatalf("suppressed field still reported: %s", res.FormatText())
	}
}
