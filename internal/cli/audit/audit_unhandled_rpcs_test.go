package audit

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/audittype"
)

// unhandledNames pulls the `Service.RPC` pairs out of the category.
func unhandledNames(t *testing.T, cat audittype.Category) []string {
	t.Helper()
	raw, ok := cat.Details["unhandled_rpcs"].([]unhandledRPC)
	if !ok {
		t.Fatalf("details.unhandled_rpcs is %T, want []unhandledRPC", cat.Details["unhandled_rpcs"])
	}
	var out []string
	for _, r := range raw {
		out = append(out, r.Service+"."+r.RPC)
	}
	return out
}

// TestAuditUnhandledRPCs_FlagsAnRPCWithNoMethod pins the visibility half of
// the `//forge:no-orm` gap: an rpc forge gave neither a stub nor a shim
// compiles (the Service embeds Unimplemented…Handler) and serves 501s with
// nothing in the tree to find. The audit names it.
func TestAuditUnhandledRPCs_FlagsAnRPCWithNoMethod(t *testing.T) {
	handlers := "package shop\n\nimport \"context\"\n\n" +
		"type Service struct {\n\tshopv1connect.UnimplementedShopServiceHandler\n\tdeps Deps\n}\n\n" +
		"func (s *Service) GetOrder(ctx context.Context) error { return nil }\n"
	dir := writeProject(t, "ShopService", map[string]bool{"GetOrder": true, "CreateOrder": true}, handlers)

	cat := auditUnhandledRPCs(testFactory(auditAPIConfig{projectDefinesConnectServices: true}), dir)
	if cat.Status != audittype.StatusWarn {
		t.Errorf("status = %s, want warn", cat.Status)
	}
	if got := unhandledNames(t, cat); len(got) != 1 || got[0] != "ShopService.CreateOrder" {
		t.Errorf("unhandled = %v, want [ShopService.CreateOrder]", got)
	}
	if !strings.Contains(cat.Summary, "ShopService.CreateOrder") {
		t.Errorf("summary %q does not name the rpc", cat.Summary)
	}
}

func TestAuditUnhandledRPCs_EveryRPCHandledIsOK(t *testing.T) {
	handlers := "package shop\n\nimport \"context\"\n\n" +
		"func (s *Service) GetOrder(ctx context.Context) error { return nil }\n" +
		"func (s *Service) CreateOrder(ctx context.Context) error { return nil }\n"
	dir := writeProject(t, "ShopService", map[string]bool{"GetOrder": true, "CreateOrder": true}, handlers)
	cat := auditUnhandledRPCs(testFactory(auditAPIConfig{projectDefinesConnectServices: true}), dir)
	if cat.Status != audittype.StatusOK {
		t.Errorf("status = %s (%s), want ok", cat.Status, cat.Summary)
	}
}

// A Service that embeds another type may get the method by promotion, which
// a declaration scan cannot see: report the service as unresolved rather
// than accuse it.
func TestAuditUnhandledRPCs_EmbeddedTypeIsUnresolvedNotAccused(t *testing.T) {
	handlers := "package shop\n\ntype Service struct {\n\tlegacyHandlers\n}\n"
	dir := writeProject(t, "ShopService", map[string]bool{"GetOrder": true}, handlers)
	cat := auditUnhandledRPCs(testFactory(auditAPIConfig{projectDefinesConnectServices: true}), dir)
	if got := unhandledNames(t, cat); len(got) != 0 {
		t.Errorf("unhandled = %v, want none for a service with an embedded type", got)
	}
	if u, _ := cat.Details["unresolved_services"].([]string); len(u) != 1 || u[0] != "ShopService" {
		t.Errorf("unresolved_services = %v, want [ShopService]", cat.Details["unresolved_services"])
	}
}
