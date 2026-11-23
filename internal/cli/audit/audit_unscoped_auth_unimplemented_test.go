package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/audittype"
)

const unimplementedHeader = "package shop\n\nimport (\n\t\"context\"\n\t\"connectrpc.com/connect\"\n\t\"github.com/reliant-labs/forge/pkg/svcerr\"\n)\n\n"

func writeUnimplementedReceiver(t *testing.T, dir, imp, field string) {
	t.Helper()
	src := "package shop\nimport " + imp + "\ntype Service struct { " + field + " }\n"
	if err := os.WriteFile(filepath.Join(dir, "internal", "handlers", "shop", "service.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestUnscopedAuth_EmbeddedStubIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, imp, field string
	}{
		{"fake package", `wire "example.com/app/internal/fake"`, "wire.UnimplementedShopServiceHandler"},
		{"named fake field", `wire "example.com/app/internal/fake"`, "UnimplementedShopServiceHandler wire.Fake"},
		{"wrong service", `wire "example.com/app/gen/services/shop/v1/shopv1connect"`, "wire.UnimplementedOtherServiceHandler"},
		{"renamed other service", `wire "example.com/app/gen/services/shop/v1/shopv1connect"`, "UnimplementedShopServiceHandler wire.UnimplementedOtherServiceHandler"},
		{"local fake type", `_ "example.com/app/gen/services/shop/v1/shopv1connect"`, "UnimplementedShopServiceHandler"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := unimplementedHeader + "func (s *Service) Later(ctx context.Context, req *connect.Request[pb.LaterRequest]) (*connect.Response[pb.LaterResponse], error) { return s.UnimplementedShopServiceHandler.Later(ctx, req) }\n"
			dir := writeProject(t, "ShopService", map[string]bool{"Later": true}, src)
			writeUnimplementedReceiver(t, dir, tc.imp, tc.field)
			cat := auditUnscopedAuth(nil, dir)
			if got := strings.Join(unscopedMethods(t, cat), ","); got != "Later" {
				t.Fatalf("unscoped = %q, want Later", got)
			}
			if got := unimplementedMethods(t, cat); len(got) != 0 {
				t.Fatalf("unimplemented = %v, want none", got)
			}
		})
	}
}

func unimplementedMethods(t *testing.T, cat audittype.Category) []string {
	t.Helper()
	found, ok := cat.Details["unimplemented_rpcs"].([]unimplementedRPC)
	if !ok {
		t.Fatalf("unimplemented_rpcs is %T, not []unimplementedRPC", cat.Details["unimplemented_rpcs"])
	}
	var names []string
	for _, f := range found {
		names = append(names, f.Method)
	}
	return names
}

// An rpc whose handler only answers CodeUnimplemented touches no data, so
// it is listed apart and is not an unscoped finding — in each of the three
// spellings a project uses.
func TestUnscopedAuth_UnimplementedHandlersAreNotFindings(t *testing.T) {
	src := unimplementedHeader +
		"func (s *Service) Embedded(ctx context.Context, req *connect.Request[pb.EmbeddedRequest]) (*connect.Response[pb.EmbeddedResponse], error) {\n" +
		"\treturn s.UnimplementedShopServiceHandler.Embedded(ctx, req)\n}\n" +
		"func (s *Service) Svcerr(ctx context.Context, req *connect.Request[pb.SvcerrRequest]) (*connect.Response[pb.SvcerrResponse], error) {\n" +
		"\treturn nil, svcerr.Unimplemented(\"gone\")\n}\n" +
		"func (s *Service) Stream(ctx context.Context, st *connect.BidiStream[pb.A, pb.B]) error {\n" +
		"\treturn svcerr.ToConnect(svcerr.Unimplemented(\"gone\"))\n}\n" +
		"func (s *Service) Connect(ctx context.Context, req *connect.Request[pb.ConnectRequest]) (*connect.Response[pb.ConnectResponse], error) {\n" +
		"\treturn nil, connect.NewError(connect.CodeUnimplemented, nil)\n}\n" +
		delegationBody("GetOrder")
	dir := writeProject(t, "ShopService", map[string]bool{
		"Embedded": true, "Svcerr": true, "Stream": true, "Connect": true, "GetOrder": true,
	}, src)

	writeUnimplementedReceiver(t, dir, "wire \"example.com/app/gen/services/shop/v1/shopv1connect\"", "wire.UnimplementedShopServiceHandler")
	cat := auditUnscopedAuth(nil, dir)

	if names := unscopedMethods(t, cat); len(names) != 1 || names[0] != "GetOrder" {
		t.Fatalf("unscoped = %v, want exactly [GetOrder] — the other four only answer CodeUnimplemented", names)
	}
	if got := strings.Join(unimplementedMethods(t, cat), ","); got != "Connect,Embedded,Stream,Svcerr" {
		t.Fatalf("unimplemented_rpcs = %s, want Connect,Embedded,Stream,Svcerr", got)
	}
	if !strings.Contains(cat.Summary, "1 of 1 authenticated") || !strings.Contains(cat.Summary, "4 more answer CodeUnimplemented") {
		t.Errorf("summary = %q, want the unimplemented four counted apart", cat.Summary)
	}
}

// Only a body that is NOTHING but the Unimplemented answer is exempt. Work
// before it, a different embedded method, or a same-named identifier that
// is not the svcerr/connect import keeps the handler judged as usual.
func TestUnscopedAuth_UnimplementedExemptionIsExact(t *testing.T) {
	src := "package shop\n\nimport (\n\t\"context\"\n\t\"connectrpc.com/connect\"\n\tsvcerr \"example.com/app/internal/fakeerr\"\n)\n\n" +
		"func (s *Service) Work(ctx context.Context, req *connect.Request[pb.WorkRequest]) (*connect.Response[pb.WorkResponse], error) {\n" +
		"\ts.deps.Store.Delete(ctx, req.Msg.Id)\n\treturn nil, connect.NewError(connect.CodeUnimplemented, nil)\n}\n" +
		"func (s *Service) Other(ctx context.Context, req *connect.Request[pb.OtherRequest]) (*connect.Response[pb.OtherResponse], error) {\n" +
		"\treturn s.UnimplementedShopServiceHandler.Work(ctx, nil)\n}\n" +
		"func (s *Service) Fake(ctx context.Context, req *connect.Request[pb.FakeRequest]) (*connect.Response[pb.FakeResponse], error) {\n" +
		"\treturn nil, svcerr.Unimplemented(\"not forge's svcerr\")\n}\n"
	dir := writeProject(t, "ShopService", map[string]bool{"Work": true, "Other": true, "Fake": true}, src)

	writeUnimplementedReceiver(t, dir, "wire \"example.com/app/gen/services/shop/v1/shopv1connect\"", "wire.UnimplementedShopServiceHandler")
	cat := auditUnscopedAuth(nil, dir)

	if got := strings.Join(unscopedMethods(t, cat), ","); got != "Fake,Other,Work" {
		t.Fatalf("unscoped = %s, want Fake,Other,Work — none of them is only an Unimplemented answer", got)
	}
	if names := unimplementedMethods(t, cat); len(names) != 0 {
		t.Fatalf("unimplemented_rpcs = %v, want none", names)
	}
}

// A project whose every authenticated rpc is either scoped or unimplemented
// is clean, and says how many it did not judge.
func TestUnscopedAuth_OnlyUnimplementedLeftIsOK(t *testing.T) {
	src := unimplementedHeader + scopedBody("ListMyOrders") +
		"func (s *Service) Later(ctx context.Context, req *connect.Request[pb.LaterRequest]) (*connect.Response[pb.LaterResponse], error) {\n" +
		"\treturn s.UnimplementedShopServiceHandler.Later(ctx, req)\n}\n"
	dir := writeProject(t, "ShopService", map[string]bool{"ListMyOrders": true, "Later": true}, src)

	writeUnimplementedReceiver(t, dir, "wire \"example.com/app/gen/services/shop/v1/shopv1connect\"", "wire.UnimplementedShopServiceHandler")
	cat := auditUnscopedAuth(nil, dir)

	if cat.Status != audittype.StatusOK {
		t.Fatalf("status = %q, want ok\nsummary: %s", cat.Status, cat.Summary)
	}
	if !strings.Contains(cat.Summary, "all 1 implemented") || !strings.Contains(cat.Summary, "1 more answer CodeUnimplemented") {
		t.Errorf("summary = %q", cat.Summary)
	}
}
