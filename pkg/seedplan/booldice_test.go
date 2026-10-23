package seedplan

import (
	"fmt"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// TestSeedBooleans_AreIndependentAcrossColumns pins that two boolean
// columns on the same table vary INDEPENDENTLY.
//
// The seeder's per-cell selector is FNV-1a over (salt, table, column, row).
// FNV-1a's low bit is a LINEAR function of its input: the prime is odd, so
// multiplication preserves bit 0, and only the XOR step changes it.
// `cellHash(...) % 2` therefore reduced to
//
//	(offset_basis ^ parity of the input bytes) & 1
//
// and salt, table and row contribute identically to both columns — so the
// relationship between two bool columns was fixed by the parity of their
// NAMES alone, for every salt, every table and every row. Two columns
// whose names had opposite parity were ALWAYS opposite; two with the same
// parity were ALWAYS identical.
//
// Measured: a products table where `active` (even parity) and
// `requires_prescription` (odd parity) disagreed in all 20 seeded rows —
// no product was both active and prescription-only, so the app's
// "prescription coverage for a prescription-only product" path had no
// valid row to exercise. That is a degenerate dataset, not a coincidence:
// with two bools a forge-seeded table could only ever produce two of the
// four combinations.
//
// The rule: seeded booleans must be able to take all four combinations.
func TestSeedBooleans_AreIndependentAcrossColumns(t *testing.T) {
	const rows = 24

	// Column names chosen so the PRE-FIX failure is present in the fixture:
	// pairs of both parities. The set under test is derived from the
	// table's bool columns, not written out pair by pair.
	table := schemadef.Table{
		Name: "products",
		Columns: []schemadef.Column{
			{Name: "id", Type: schemadef.TypeString, IsPK: true, NotNull: true, DeclType: "text"},
			{Name: "active", Type: schemadef.TypeBool, NotNull: true, DeclType: "boolean"},
			{Name: "requires_prescription", Type: schemadef.TypeBool, NotNull: true, DeclType: "boolean"},
			{Name: "is_featured", Type: schemadef.TypeBool, NotNull: true, DeclType: "boolean"},
			{Name: "archived", Type: schemadef.TypeBool, NotNull: true, DeclType: "boolean"},
		},
	}

	var boolCols []schemadef.Column
	for _, c := range table.Columns {
		if c.Type == schemadef.TypeBool {
			boolCols = append(boolCols, c)
		}
	}
	var pairs [][2]schemadef.Column
	for i := range boolCols {
		for j := i + 1; j < len(boolCols); j++ {
			pairs = append(pairs, [2]schemadef.Column{boolCols[i], boolCols[j]})
		}
	}
	if len(pairs) == 0 {
		t.Fatal("derived set is empty: the fixture declares fewer than two bool columns, so this test asserts nothing")
	}

	for _, salt := range []int{0, 1, 42} {
		plan, err := BuildPlan([]schemadef.Table{table}, EnumPools{}, Config{Rows: rows, Salt: salt})
		if err != nil {
			t.Fatalf("BuildPlan: %v", err)
		}
		for _, pair := range pairs {
			combos := map[string]int{}
			for i := 0; i < rows; i++ {
				a := plan.valueLiteral("products", pair[0], i)
				b := plan.valueLiteral("products", pair[1], i)
				combos[a+"/"+b]++
			}
			if len(combos) < 4 {
				t.Errorf("salt %d: %s x %s produced only %d of the 4 combinations over %d rows: %v — the two columns are not independent",
					salt, pair[0].Name, pair[1].Name, len(combos), rows, combos)
			}
		}
	}
}

// TestSeedBooleans_BothValuesAppear pins the single-column half: a bool
// column that only ever seeds one value gives a filter, a badge and a
// conditional branch nothing to exercise.
func TestSeedBooleans_BothValuesAppear(t *testing.T) {
	const rows = 16
	var cols []schemadef.Column
	for i := 0; i < 8; i++ {
		cols = append(cols, schemadef.Column{
			Name: fmt.Sprintf("flag_%d", i), Type: schemadef.TypeBool, NotNull: true, DeclType: "boolean",
		})
	}
	table := schemadef.Table{
		Name:    "widgets",
		Columns: append([]schemadef.Column{{Name: "id", Type: schemadef.TypeString, IsPK: true, NotNull: true, DeclType: "text"}}, cols...),
	}
	plan, err := BuildPlan([]schemadef.Table{table}, EnumPools{}, Config{Rows: rows, Salt: 0})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(cols) == 0 {
		t.Fatal("derived set is empty")
	}
	for _, c := range cols {
		seen := map[string]bool{}
		for i := 0; i < rows; i++ {
			seen[plan.valueLiteral("widgets", c, i)] = true
		}
		if len(seen) != 2 {
			t.Errorf("%s seeded only %v over %d rows", c.Name, seen, rows)
		}
	}
}
