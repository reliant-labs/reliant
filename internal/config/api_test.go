package config

import "testing"

// TestAPIConfigRoundTrip verifies the new api: block parses correctly
// under LoadProject and that an unknown sub-key (e.g. typo) is reported
// with a suggestion. This is a load-bearing sanity check: forge.yaml
// validation rides on the reflect walk picking up the APIConfig fields.
func TestAPIConfigRoundTrip(t *testing.T) {
	data := []byte(`name: test
module_path: github.com/foo/bar
version: 0.1.0
api:
  openapi: true
  rest: false
`)
	cfg, err := LoadProject(data, "t.yaml")
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if !cfg.API.OpenAPI {
		t.Errorf("api.openapi = false, want true")
	}
	if cfg.API.REST {
		t.Errorf("api.rest = true, want false")
	}
}

// TestAPIConfigRejectsUnknownKey ensures a typo in an api: sub-field
// is caught by the unknown-keys walker so users get an actionable
// validation error rather than a silently-ignored setting.
func TestAPIConfigRejectsUnknownKey(t *testing.T) {
	data := []byte(`name: test
module_path: github.com/foo/bar
version: 0.1.0
api:
  openpi: true
`)
	_, err := LoadProject(data, "t.yaml")
	if err == nil {
		t.Fatal("expected validation error for unknown key 'openpi', got nil")
	}
	if !contains(err.Error(), "openpi") {
		t.Errorf("error should mention the unknown key 'openpi': %v", err)
	}
}

// api.proto_package is the override for the scaffolded service's proto
// package. Both forms — a fixed package and a {service} pattern — must load,
// and a value that is not a proto package must fail at load time, naming the
// key, rather than surface later as a buf parse error in a file forge wrote.
func TestAPIConfigProtoPackage(t *testing.T) {
	for _, ok := range []string{"controlplane.v1", "acme.{service}.v1", "{service}.v1"} {
		data := []byte("name: test\nmodule_path: github.com/foo/bar\napi:\n  proto_package: \"" + ok + "\"\n")
		cfg, err := LoadProject(data, "t.yaml")
		if err != nil {
			t.Fatalf("api.proto_package %q should load: %v", ok, err)
		}
		if cfg.API.ProtoPackage != ok {
			t.Errorf("api.proto_package = %q, want %q", cfg.API.ProtoPackage, ok)
		}
	}
	for _, bad := range []string{"control-plane.v1", "acme..v1", "acme.{svc}.v1", ".v1"} {
		data := []byte("name: test\nmodule_path: github.com/foo/bar\napi:\n  proto_package: \"" + bad + "\"\n")
		_, err := LoadProject(data, "t.yaml")
		if err == nil {
			t.Errorf("api.proto_package %q should be rejected", bad)
			continue
		}
		if !contains(err.Error(), "api.proto_package") {
			t.Errorf("error for %q should name the key: %v", bad, err)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
