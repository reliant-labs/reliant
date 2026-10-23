package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// storeTestEntities is a two-entity schema with different primary-key types,
// so a store interface that hardcoded `string` ids fails visibly.
func storeTestEntities() []config.PlanEntity {
	return []config.PlanEntity{
		{
			Name:       "Estimate",
			Timestamps: true,
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "string", PrimaryKey: true},
				{Name: "customer_id", Type: "string", NotNull: true},
				{Name: "total_cents", Type: "int64", NotNull: true},
			},
		},
		{
			Name: "Counter",
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "int64", PrimaryKey: true},
				{Name: "label", Type: "string", NotNull: true},
			},
		},
	}
}

func storeGenSource(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := GeneratePlanORM(root, config.ORMConfig{}, storeTestEntities(), nil); err != nil {
		t.Fatalf("GeneratePlanORM: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "internal", "db", storeGenFile))
	if err != nil {
		t.Fatalf("store file was not generated: %v", err)
	}
	return string(b)
}

// A domain package's Deps are INTERFACES, resolved by type in the generated
// compose.go. The ORM is free functions, which are not a type — so before
// this file existed, every project that followed forge's own architecture
// rule hand-wrote an interface plus a passthrough adapter to bridge the gap.
// Two measured forge-one-shot runs wrote 723 and 464 lines of exactly that.
func TestStoreInterfaces_ExposeTheDelegatesAsAType(t *testing.T) {
	src := storeGenSource(t)

	for _, want := range []string{
		"type EstimateStore interface",
		"CreateEstimate(ctx context.Context, msg *Estimate) error",
		"GetEstimateByID(ctx context.Context, id string) (*Estimate, error)",
		"ListEstimate(ctx context.Context, opts ...orm.QueryOption) ([]*Estimate, error)",
		"CountEstimate(ctx context.Context, opts ...orm.QueryOption) (int64, error)",
		"UpdateEstimate(ctx context.Context, msg *Estimate, opts ...crud.UpdateOption) error",
		"UpdateEstimateMasked(ctx context.Context, msg *Estimate, fields []string, opts ...crud.UpdateOption) error",
		"DeleteEstimate(ctx context.Context, id string) error",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("store file must expose %q — a Deps field cannot name a free function", want)
		}
	}
}

// The interface must describe the SAME schema as the delegates it abstracts.
// A non-string primary key is where a lazy render breaks first.
func TestStoreInterfaces_PrimaryKeyTypeMatchesTheEntity(t *testing.T) {
	src := storeGenSource(t)

	if !strings.Contains(src, "GetCounterByID(ctx context.Context, id int64) (*Counter, error)") {
		t.Error("Counter has an int64 primary key; its store must take int64, not a hardcoded string")
	}
	if !strings.Contains(src, "DeleteCounter(ctx context.Context, id int64) error") {
		t.Error("Delete must take the entity's real primary-key type")
	}
}

// The compile-time assertions are the whole safety story: if a delegate's
// signature ever drifts from its interface, the GENERATED file stops
// compiling at generate time, rather than the mismatch surfacing later in a
// user's package with a confusing error.
func TestStoreInterfaces_AssertAdaptersSatisfyThem(t *testing.T) {
	src := storeGenSource(t)

	for _, want := range []string{
		"var _ EstimateStore = estimateStore{}",
		"var _ CounterStore = counterStore{}",
		"var _ Store = store{}",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing compile-time assertion %q — without it a delegate/interface drift is invisible until a user hits it", want)
		}
	}
}

// Two real shapes, not one forced default: a package touching one entity
// wants a narrow store; one genuinely spanning tables (a reporting service)
// wants the aggregate rather than eight Deps fields. The aggregate EMBEDS
// the per-entity interfaces so the two views cannot drift apart.
func TestStoreInterfaces_AggregateEmbedsEveryEntityStore(t *testing.T) {
	src := storeGenSource(t)

	idx := strings.Index(src, "type Store interface {")
	if idx < 0 {
		t.Fatal("the aggregate Store interface must exist for services that span entities")
	}
	end := strings.Index(src[idx:], "}")
	body := src[idx : idx+end]

	for _, want := range []string{"EstimateStore", "CounterStore"} {
		if !strings.Contains(body, want) {
			t.Errorf("aggregate Store must embed %s; restating methods would let the two views drift", want)
		}
	}
	if !strings.Contains(src, "func NewStore(db orm.Context) Store") {
		t.Error("the aggregate needs a constructor to bind into Infra")
	}
}

// The generated surface must NOT try to enumerate hand-written queries. Those
// live in the scaffold-once <entity>_repo_ext.go seam, which forge never
// regenerates — so a generated interface naming them would either clobber the
// user's work or go stale against it.
func TestStoreInterfaces_PointAtTheRepoExtSeamForRawSQL(t *testing.T) {
	src := storeGenSource(t)

	if !strings.Contains(src, "_repo_ext.go") {
		t.Error("the store file must tell the reader where a raw-SQL/hand-written query goes, or they will add it here and lose it on regen")
	}
}

// The property that only surfaced by USING the thing: a store must be callable
// with nothing but the caller's own arguments.
//
// The first cut mirrored the delegates exactly — orm.Context and all — and
// compiled fine, so a compile-only check passed it. Writing a service against
// it then showed it was useless: the consumer still had to hold an orm.Context
// beside the store and still wrote `s.store.GetX(ctx, s.db, id)`, which is the
// passthrough layer this generator exists to delete. A dependency you cannot
// call without a second dependency is not a dependency.
func TestStoreInterfaces_AreCallableWithoutAHandle(t *testing.T) {
	src := storeGenSource(t)

	idx := strings.Index(src, "type EstimateStore interface {")
	if idx < 0 {
		t.Fatal("EstimateStore must exist")
	}
	body := src[idx : idx+strings.Index(src[idx:], "\n}")]

	if strings.Contains(body, "db orm.Context") {
		t.Errorf("no store METHOD may take an orm.Context — the adapter holds it.\n"+
			"Otherwise a consumer needs the handle as a second dep and writes the very "+
			"passthrough this file removes.\ngot:\n%s", body)
	}
	if !strings.Contains(body, "WithTx(tx orm.Context) EstimateStore") {
		t.Error("binding the handle must not cost the transaction seam: WithTx rebinds the store to a tx handle")
	}
}

// Binding a handle is only safe if a multi-table use case can still commit
// atomically, which is what the aggregate's rebinder is for.
//
// The aggregate exposes ACCESSORS rather than embedding the per-entity
// interfaces. That is not a style choice: each per-entity store declares its
// own WithTx, so embedding N of them makes the selector ambiguous and the
// aggregate cannot satisfy its own interface. `forge generate` rejected
// exactly that render with "ambiguous selector store.WithTx", which is how
// the accessor shape was arrived at.
func TestStoreInterfaces_AggregateRebindsEveryStoreToOneTx(t *testing.T) {
	src := storeGenSource(t)

	if !strings.Contains(src, "WithTx(tx orm.Context) Store") {
		t.Error("the aggregate needs a rebinder so a use case spanning tables shares one transaction")
	}
	for _, want := range []string{
		"Estimates() EstimateStore",
		"Counters() CounterStore",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("aggregate must expose %q as an accessor; embedding the interfaces makes WithTx ambiguous and will not compile", want)
		}
	}
}
