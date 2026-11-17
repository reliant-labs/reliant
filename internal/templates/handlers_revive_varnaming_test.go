package templates

import (
	"strings"
	"testing"
)

// CreateApiKey is the issue #797 reproduction: connect keeps the proto
// spelling CreateApiKey on the Handler interface, while revive's
// var-naming rule wants CreateAPIKey. The scaffolded stub must keep the
// interface name and suppress the lint.
func TestHandlersMethods_CreateApiKeyGetsReviveNolint(t *testing.T) {
	data := map[string]any{
		"ServicePackage": "access",
		"HandlerName":    "AccessService",
		"Module":         "example.com/app",
		"ProtoPackage":   "proto/services/access",
		"Methods": []map[string]any{
			{
				"Name":            "CreateApiKey",
				"InputType":       "CreateApiKeyRequest",
				"OutputType":      "CreateApiKeyResponse",
				"ClientStreaming": false,
				"ServerStreaming": false,
			},
			{
				"Name":            "ListOrders",
				"InputType":       "ListOrdersRequest",
				"OutputType":      "ListOrdersResponse",
				"ClientStreaming": false,
				"ServerStreaming": false,
			},
		},
	}
	got := string(renderService(t, "handlers_methods.go.tmpl", data))
	wantNolint := "//nolint:revive // var-naming: name fixed by the generated AccessServiceHandler interface (proto rpc CreateApiKey)"
	if !strings.Contains(got, wantNolint) {
		t.Fatalf("missing nolint for CreateApiKey:\n%s", got)
	}
	if strings.Count(got, "//nolint:revive // var-naming:") != 1 {
		t.Fatalf("expected exactly one revive var-naming nolint, got:\n%s", got)
	}
	if !strings.Contains(got, "func (s *Service) CreateApiKey(") {
		t.Fatalf("missing CreateApiKey method:\n%s", got)
	}
	if !strings.Contains(got, "func (s *Service) ListOrders(") {
		t.Fatalf("missing ListOrders method:\n%s", got)
	}
	// ListOrders must not pick up a nolint of its own.
	listIdx := strings.Index(got, "func (s *Service) ListOrders(")
	preceding := got[max(0, listIdx-120):listIdx]
	if strings.Contains(preceding, "nolint:revive") {
		t.Fatalf("ListOrders unexpectedly preceded by revive nolint:\n%s", preceding)
	}
}
