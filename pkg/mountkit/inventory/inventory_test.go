package inventory

// Tests for the DATA-ONLY ComponentInfo descriptor + reader helpers extracted
// from the generated inventory_gen.go (lib-boundary extraction —
// FORGE_SHAPE_REDESIGN §1/§2). The generated `var Inventory =
// []inventory.ComponentInfo{...}` is data over this type; `forge project map` /
// `audit` / `services` read it via these helpers.

import (
	"reflect"
	"testing"
)

func sampleInventory() []ComponentInfo {
	return []ComponentInfo{
		{Name: "billing", ConnectPath: "acme.billing.v1.BillingService", BaseService: "billing", Version: "v1", Kind: "service"},
		{Name: "users", ConnectPath: "acme.users.v1.UsersService", BaseService: "users", Version: "v1", Kind: "service"},
		{Name: "ledger", ConnectPath: "acme.ledger.LedgerService", BaseService: "ledger", Version: "", Kind: "service"},
	}
}

func TestFindByName_Hit(t *testing.T) {
	got, ok := FindByName(sampleInventory(), "users")
	if !ok {
		t.Fatal("FindByName(users) should hit")
	}
	if got.ConnectPath != "acme.users.v1.UsersService" {
		t.Errorf("ConnectPath = %q, want acme.users.v1.UsersService", got.ConnectPath)
	}
}

func TestFindByName_Miss(t *testing.T) {
	got, ok := FindByName(sampleInventory(), "nope")
	if ok {
		t.Fatalf("FindByName(nope) should miss, got %+v", got)
	}
	if got != (ComponentInfo{}) {
		t.Errorf("miss must return the zero ComponentInfo, got %+v", got)
	}
}

func TestFindByName_EmptyInventory(t *testing.T) {
	if _, ok := FindByName(nil, "anything"); ok {
		t.Error("FindByName over nil inventory must miss")
	}
}

func TestGroupByBaseService_SingleVersionPerBase(t *testing.T) {
	groups := GroupByBaseService(sampleInventory())
	if len(groups) != 3 {
		t.Fatalf("want 3 base-service buckets, got %d (%v)", len(groups), groups)
	}
	for base, rows := range groups {
		if len(rows) != 1 {
			t.Errorf("base %q: want 1 row today (one version per base), got %d", base, len(rows))
		}
	}
}

func TestGroupByBaseService_MultiVersionSeam(t *testing.T) {
	// The version-aware seam: a future billing.v1 + billing.v2 collapses to
	// one BaseService bucket with two rows, first-seen order preserved.
	inv := []ComponentInfo{
		{Name: "billing", ConnectPath: "acme.billing.v1.BillingService", BaseService: "billing", Version: "v1", Kind: "service"},
		{Name: "billing", ConnectPath: "acme.billing.v2.BillingService", BaseService: "billing", Version: "v2", Kind: "service"},
		{Name: "users", ConnectPath: "acme.users.v1.UsersService", BaseService: "users", Version: "v1", Kind: "service"},
	}
	groups := GroupByBaseService(inv)
	if len(groups) != 2 {
		t.Fatalf("want 2 buckets, got %d", len(groups))
	}
	billing := groups["billing"]
	if len(billing) != 2 {
		t.Fatalf("billing bucket: want 2 rows, got %d", len(billing))
	}
	gotVersions := []string{billing[0].Version, billing[1].Version}
	if !reflect.DeepEqual(gotVersions, []string{"v1", "v2"}) {
		t.Errorf("billing versions = %v, want [v1 v2] in first-seen order", gotVersions)
	}
}

func TestGroupByBaseService_Empty(t *testing.T) {
	groups := GroupByBaseService(nil)
	if len(groups) != 0 {
		t.Errorf("GroupByBaseService(nil) must be empty, got %v", groups)
	}
}
