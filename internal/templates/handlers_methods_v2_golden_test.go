package templates

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestGoldenHandlersMethodsConnectV2 snapshots the method fragment forge
// appends to a handler file in a connect-go v2 project — one rpc of each
// shape. Beside handlers_methods.go.golden (the v1 render, unchanged): v2
// handlers take and return bare messages, and every streaming shape takes
// the per-rpc <Service><Method>ServerStream protoc-gen-connect-go v2
// declares. The whole v2 service surface (files, imports, a build against
// real v2 stubs) is pinned in internal/codegen's connect_v2_service goldens.
//
// `go test ./internal/templates/ -run TestGoldenHandlersMethodsConnectV2 -update`
func TestGoldenHandlersMethodsConnectV2(t *testing.T) {
	method := func(name string, client, server bool) map[string]any {
		return map[string]any{
			"Name": name, "InputType": name + "Request", "OutputType": name + "Response",
			"ClientStreaming": client, "ServerStreaming": server, "AuthRequired": true,
		}
	}
	data := map[string]any{
		"ServiceName":         "api",
		"ServicePackage":      "api",
		"Module":              "github.com/example/demo",
		"ProtoImportPath":     "proto/services/api",
		"ProtoPackage":        "proto/services/api",
		"ProtoConnectPackage": "apiv1connect",
		"HandlerName":         "ApiService",
		"ProtoFileSymbol":     "File_services_api_v1_api_proto",
		"ConnectV2":           true,
		"Methods": []map[string]any{
			method("Echo", false, false),
			method("Tail", false, true),
			method("Upload", true, false),
			method("Chat", true, true),
		},
	}
	got := renderService(t, "handlers_methods.go.tmpl", data)
	path := filepath.Join("testdata", "golden", "handlers_methods_v2.go.golden")
	if *updateGolden {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run with -update to create it)", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("handlers_methods.go.tmpl (connect-go v2) differs from %s; run with -update if intended\n--- got ---\n%s", path, got)
	}
}
