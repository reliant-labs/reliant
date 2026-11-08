package codegen

import (
	"strings"
	"testing"
)

const composeWrapFixture = `package app

import (
	"fmt"

	"example.com/x/internal/billing"
	"example.com/x/internal/ledger"
	"example.com/x/internal/quiet"
)

type Components struct {
	Billing billing.Service
	Ledger  ledger.Service
	Quiet   quiet.Service
}

func NewComponents(infra *Infra) (*Components, error) {
	c := &Components{}
	c.Billing = billing.New(billing.Deps{
		DB: infra.DB, // by type
	})
	ledgerInst, err := ledger.New(ledger.Deps{})
	if err != nil {
		return nil, fmt.Errorf("construct Ledger: %w", err)
	}
	c.Ledger = ledgerInst
	c.Quiet = quiet.NewServiceWithForgeMiddleware(quiet.New(quiet.Deps{}))
	return c, nil
}
`

func TestComposeSyncWrapping(t *testing.T) {
	data := InjectGenData{Order: []InjectComponentData{
		{FieldName: "Billing", Alias: "billing", Wrapped: true, MiddlewareCtor: "NewServiceWithForgeMiddleware"},
		{FieldName: "Ledger", Alias: "ledger", Wrapped: true, Fallible: true, MiddlewareCtor: "NewServiceWithForgeMiddleware"},
		{FieldName: "Quiet", Alias: "quiet", Wrapped: false},
	}}
	out, changed := composeSyncWrapping(composeWrapFixture, data)
	if !changed {
		t.Fatal("changed = false, want true")
	}
	for _, want := range []string{
		"c.Billing = billing.NewServiceWithForgeMiddleware(billing.New(billing.Deps{\n\t\tDB: infra.DB, // by type\n\t}))",
		"c.Ledger = ledger.NewServiceWithForgeMiddleware(ledgerInst)",
		"c.Quiet = quiet.New(quiet.Deps{})",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	// Converged: a second pass changes nothing.
	again, changed := composeSyncWrapping(out, data)
	if changed || again != out {
		t.Errorf("second pass changed the file:\n%s", again)
	}
}

func TestComposeSyncWrappingLeavesUnknownAndUnparseableAlone(t *testing.T) {
	data := InjectGenData{Order: []InjectComponentData{
		{FieldName: "Other", Alias: "other", Wrapped: true, MiddlewareCtor: "NewServiceWithForgeMiddleware"},
	}}
	if _, changed := composeSyncWrapping(composeWrapFixture, data); changed {
		t.Error("changed a file with none of the data's components")
	}
	if _, changed := composeSyncWrapping("package app\nfunc NewComponents(", data); changed {
		t.Error("changed an unparseable file")
	}
}
