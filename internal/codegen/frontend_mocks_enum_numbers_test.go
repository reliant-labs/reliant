package codegen

import (
	"strconv"
	"testing"

	"github.com/reliant-labs/forge/pkg/seedplan"
)

// An enum's wire numbers are what protobuf-es types an enum field as, and
// they are NOT its declaration order once a value has been reserved:
//
//	enum OrderStatus {
//	  reserved 1;
//	  reserved "ORDER_STATUS_DRAFT";
//	  ORDER_STATUS_UNSPECIFIED = 0;
//	  ORDER_STATUS_PENDING = 2;
//	  ORDER_STATUS_ACTIVE = 3;
//	  ORDER_STATUS_CLOSED = 4;
//	}
//
// Reserving a removed value is the proto-breaking discipline, so this is the
// shape every mature enum ends up in. The mock generator used to emit `1` for
// every unseeded enum cell ("the first non-UNSPECIFIED value") and the
// declaration INDEX for every seeded one, so this enum mocked as 1 (reserved:
// `Type '1' is not assignable to type 'OrderStatus'`) and ACTIVE as 2 (which is
// PENDING) — a fixture file that fails the frontend typecheck, or type-checks
// and names the wrong member.

func reservedEnumTestService() ServiceDef {
	return ServiceDef{
		Name: "OrderService", Package: "orders.v1",
		Enums: map[string][]string{"orders.v1.OrderStatus": {
			"ORDER_STATUS_UNSPECIFIED",
			"ORDER_STATUS_PENDING",
			"ORDER_STATUS_ACTIVE",
			"ORDER_STATUS_CLOSED",
		}},
		EnumNumbers: map[string][]int32{"orders.v1.OrderStatus": {0, 2, 3, 4}},
	}
}

// declaredOrderStatusNumbers is the oracle: the set of numbers a TypeScript
// literal for this enum may be.
func declaredOrderStatusNumbers(svc ServiceDef) map[string]string {
	out := map[string]string{}
	names := svc.Enums["orders.v1.OrderStatus"]
	for i, n := range svc.EnumNumbers["orders.v1.OrderStatus"] {
		out[strconv.Itoa(int(n))] = names[i]
	}
	return out
}

// TestMockEnumWithoutDatasetIsADeclaredNonZeroValue: with no dataset to read,
// the enum cell must be a number the enum DECLARES, and not its zero value
// (UNSPECIFIED is "unset", which a fixture should not model). Here that is 2.
func TestMockEnumWithoutDatasetIsADeclaredNonZeroValue(t *testing.T) {
	svc := reservedEnumTestService()
	declared := declaredOrderStatusNumbers(svc)
	data := EntityDefToMockData(mockEnumTestOrderEntity(), svc, nil)
	if len(data.Records) == 0 {
		t.Fatal("no mock records rendered — every assertion below would pass vacuously")
	}
	for i, rec := range data.Records {
		got := mockRecordValue(t, rec, "status")
		if _, ok := declared[got]; !ok {
			t.Errorf("row %d: mock status = %s, which OrderStatus does not declare (declared: %v) — "+
				"the fixture fails the frontend typecheck", i, got, declared)
		}
		if got == "0" {
			t.Errorf("row %d: mock status = 0 (UNSPECIFIED); a fixture must model a set value", i)
		}
	}
}

// TestMockEnumFromDatasetIsTheDeclaredNumberOfTheSeededName: the seeded cell
// holds a value NAME; the mock must be that name's DECLARED number, not its
// position in the declaration.
func TestMockEnumFromDatasetIsTheDeclaredNumberOfTheSeededName(t *testing.T) {
	svc := reservedEnumTestService()
	declared := declaredOrderStatusNumbers(svc)
	seed := newSeedProjection(mockEnumTestSchema(), seedplan.DefaultConfig(), nil)
	if seed == nil {
		t.Fatal("the planner refused this schema")
	}
	data := EntityDefToMockData(mockEnumTestOrderEntity(), svc, seed)
	if len(data.Records) == 0 {
		t.Fatal("no mock records rendered — every assertion below would pass vacuously")
	}
	for i, rec := range data.Records {
		raw, ok := seed.Value("orders", "status", i)
		if !ok {
			t.Fatalf("row %d: the plan holds no value for orders.status — the comparison has no oracle", i)
		}
		got := mockRecordValue(t, rec, "status")
		if name := declared[got]; name != raw {
			t.Errorf("row %d: mock status = %s (%q), but the database will hold %q — "+
				"the fixture names a different member than the row it stands in for", i, got, name, raw)
		}
	}
}

// TestMockEnumWithoutDeclaredNumbersKeepsTheOrdinal: a descriptor written
// before EnumNumbers existed carries names only. Declaration order is then
// the only signal, exactly as before — this pins that the fallback is kept,
// not that it is right for a gapped enum.
func TestMockEnumWithoutDeclaredNumbersKeepsTheOrdinal(t *testing.T) {
	svc := mockEnumTestService() // names only, gap-free
	if got := mockGenerateValue(nil, "orders", mockEnumTestOrderEntity().Fields[3], 0, svc); got != "1" {
		t.Errorf("unseeded enum without declared numbers = %s, want 1 (the first non-zero ordinal)", got)
	}
}
