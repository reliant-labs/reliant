package codegen

import (
	"strings"
	"testing"
)

const sharedEnumGoPackage = "example.com/test/gen/shop/shared/v1"

// sharedEnumService is productSvcAndMethods with ProductStatus moved to a
// sibling proto file in the SAME proto package but a DIFFERENT go_package —
// the control-plane shared.proto shape (DaemonType in controlplane/v1/shared.proto,
// used by services/daemon/v1/daemon.proto).
func sharedEnumService(goPackage string) (ServiceDef, []CRUDMethod) {
	svc, methods := productSvcAndMethods()
	svc.EnumGoRefs = map[string]EnumGoRef{
		"shop.v1.ProductStatus": {ImportPath: goPackage, GoName: "ProductStatus"},
	}
	return svc, methods
}

func TestCreateRequestFactory_EnumInSiblingGoPackage_UsesThatPackage(t *testing.T) {
	svc, methods := sharedEnumService(sharedEnumGoPackage)
	fx := fixtureModel(t, fixtureSchema(), "products")

	specs := buildCreateRequestSpecs(t.Context(), svc, methods, fx)
	if len(specs) != 1 {
		t.Fatalf("want 1 create spec, got %d", len(specs))
	}
	out := string(renderEntityFactoryFile("example.com/test/internal/db", factoryGroup{
		pkg: "shop", pbImport: svc.GoPackage, creates: specs,
	}))

	if strings.Contains(out, "pb.ProductStatus_") {
		t.Errorf("enum constant qualified with the service's pb package, which does not declare it:\n%s", out)
	}
	if !strings.Contains(out, "ProductStatus_PRODUCT_STATUS_ACTIVE") {
		t.Errorf("want a real enum constant, got:\n%s", out)
	}
	if !strings.Contains(out, `"`+sharedEnumGoPackage+`"`) {
		t.Errorf("factory file does not import the enum's own Go package:\n%s", out)
	}
}

func TestCreateRequestFactory_EnumInServiceGoPackage_StaysPb(t *testing.T) {
	svc, methods := sharedEnumService("")
	svc.EnumGoRefs["shop.v1.ProductStatus"] = EnumGoRef{ImportPath: svc.GoPackage, GoName: "ProductStatus"}
	fx := fixtureModel(t, fixtureSchema(), "products")

	specs := buildCreateRequestSpecs(t.Context(), svc, methods, fx)
	out := string(renderEntityFactoryFile("example.com/test/internal/db", factoryGroup{
		pkg: "shop", pbImport: svc.GoPackage, creates: specs,
	}))
	if !strings.Contains(out, "pb.ProductStatus_PRODUCT_STATUS_ACTIVE") {
		t.Errorf("same-go-package enum should stay pb-qualified:\n%s", out)
	}
}

func TestPromoteEnum_DecidesByGoPackageNotProtoPackage(t *testing.T) {
	svc, _ := sharedEnumService(sharedEnumGoPackage)
	field := EntityField{Name: "status", Kind: FieldKindEnum, MessageType: "shop.v1.ProductStatus", GoType: "ProductStatus"}

	if got := promoteEnum(field, svc, false); strings.HasPrefix(got.GoType, "pb.") {
		t.Errorf("enum from another go_package promoted to %q; pb does not declare it", got.GoType)
	}

	svc.EnumGoRefs["shop.v1.ProductStatus"] = EnumGoRef{ImportPath: svc.GoPackage, GoName: "ProductStatus"}
	if got := promoteEnum(field, svc, false); got.GoType != "pb.ProductStatus" {
		t.Errorf("same-go_package enum promoted to %q, want pb.ProductStatus", got.GoType)
	}
}
