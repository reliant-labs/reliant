package catalog

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNormalizeTools(t *testing.T) {
	tools := []*mcp.Tool{
		{Name: "x", Description: "y", InputSchema: map[string]interface{}{"type": "object"}},
	}
	got := NormalizeTools(tools)
	if len(got) != 1 {
		t.Fatalf("expected 1 tool got %d", len(got))
	}
	if got[0].Name != "x" {
		t.Fatalf("unexpected name: %s", got[0].Name)
	}
	if got[0].InputSchema["type"] != "object" {
		t.Fatalf("unexpected schema: %#v", got[0].InputSchema)
	}
}
