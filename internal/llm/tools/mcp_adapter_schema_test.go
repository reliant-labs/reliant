package tools

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/mcp"
)

func TestNormalizeSchemaType_ArrayPrefersConcreteNonNull(t *testing.T) {
	typeValue := []interface{}{"object", "null"}
	got := normalizeSchemaType(typeValue)
	if got != "object" {
		t.Fatalf("expected object, got %#v", got)
	}
}

func TestNormalizeSchemaForInvopop_NormalizesNestedTypeArrays(t *testing.T) {
	input := map[string]interface{}{
		"type": []interface{}{"object", "null"},
		"properties": map[string]interface{}{
			"payload": map[string]interface{}{
				"type": []interface{}{"array", "null"},
				"items": map[string]interface{}{
					"type": []interface{}{"string", "null"},
				},
			},
		},
	}

	normalized, ok := normalizeSchemaForInvopop(input).(map[string]interface{})
	if !ok {
		t.Fatalf("expected map output")
	}

	if normalized["type"] != "object" {
		t.Fatalf("expected root type object, got %#v", normalized["type"])
	}

	props, ok := normalized["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected properties map")
	}
	payload, ok := props["payload"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected payload map")
	}
	if payload["type"] != "array" {
		t.Fatalf("expected payload type array, got %#v", payload["type"])
	}

	items, ok := payload["items"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected items map")
	}
	if items["type"] != "string" {
		t.Fatalf("expected items type string, got %#v", items["type"])
	}
}

func TestExtractSchemaHints(t *testing.T) {
	input := map[string]interface{}{
		"properties": map[string]interface{}{
			"zeta":  map[string]interface{}{"type": "string"},
			"alpha": map[string]interface{}{"type": "number"},
		},
		"required": []interface{}{"zeta", "alpha"},
	}

	properties, required := extractSchemaHints(input)
	if len(properties) != 2 || properties[0] != "alpha" || properties[1] != "zeta" {
		t.Fatalf("unexpected properties: %#v", properties)
	}
	if len(required) != 2 || required[0] != "alpha" || required[1] != "zeta" {
		t.Fatalf("unexpected required: %#v", required)
	}
}

func TestMCPToolAdapterName_UsesLogicalServerName(t *testing.T) {
	adapter, err := NewProjectMCPToolAdapter(
		"/tmp/project",
		"chrome-devtools",
		mcp.Tool{Name: "fill_form"},
	)
	if err != nil {
		t.Fatalf("expected adapter construction to succeed: %v", err)
	}

	if got, want := adapter.Name(), "mcp__chrome-devtools__fill_form"; got != want {
		t.Fatalf("unexpected tool name: got %q want %q", got, want)
	}
}

func TestNewProjectMCPToolAdapter_RejectsScopedServerIdentifier(t *testing.T) {
	_, err := NewProjectMCPToolAdapter(
		"/tmp/project",
		"/tmp/project::chrome-devtools",
		mcp.Tool{Name: "fill_form"},
	)
	if err == nil {
		t.Fatal("expected scoped server identifier to be rejected")
	}
}

func TestValidateMCPLogicalServerName(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{name: "plain logical name", in: "chrome-devtools", wantErr: false},
		{name: "scoped key", in: "/tmp/project::chrome-devtools", wantErr: true},
		{name: "contains slash", in: "scope/chrome-devtools", wantErr: true},
		{name: "empty", in: "   ", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMCPLogicalServerName(tc.in)
			if tc.wantErr && err == nil {
				t.Fatalf("validateMCPLogicalServerName(%q) expected error", tc.in)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validateMCPLogicalServerName(%q) unexpected error: %v", tc.in, err)
			}
		})
	}
}

func TestMCPToolRegistryBuildAdapters_PrunesDuplicateLogicalToolNamesDeterministically(t *testing.T) {
	registry := NewMCPToolRegistry(nil, "/tmp/project")
	serverTools := map[string][]mcp.Tool{
		"alpha": {
			{Name: "dup", Description: "second"},
			{Name: "dup", Description: "first"},
		},
	}

	adapted, err := registry.buildAdaptersFromServerTools(serverTools)
	if err != nil {
		t.Fatalf("buildAdaptersFromServerTools returned error: %v", err)
	}
	if len(adapted) != 1 {
		t.Fatalf("expected duplicate logical tool names to be pruned to 1, got %d", len(adapted))
	}
	if _, ok := adapted["mcp__alpha__dup"]; !ok {
		t.Fatalf("expected deterministic keeper tool name mcp__alpha__dup")
	}
}
