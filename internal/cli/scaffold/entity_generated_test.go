// File: internal/cli/scaffold/entity_generated_test.go
//
// The `// forge:generated` birth pre-flight: every way the marker can fail
// to become the column its author wrote is refused BEFORE a birth writes
// anything, naming the marker's file and line — forge's own rules
// statically, postgres's by asking postgres.

package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

const generatedPkg = "services.estimates.v1"

// generatedProto renders a line-item entity whose line total carries the
// given forge:generated expression on line 10 (and whose balance, on line
// 12, carries balanceExpr).
func generatedProto(totalExpr, balanceExpr string) string {
	return `syntax = "proto3";

package services.estimates.v1;

// forge:entity
message LineItem {
  double quantity = 1;
  int64 unit_price_cents = 2;
  int64 amount_paid_cents = 3;
  int64 line_total_cents = 4; // forge:generated ` + totalExpr + `

  int64 balance_cents = 5; // forge:generated ` + balanceExpr + `
}

service EstimatesService {
}
`
}

// scanGenerated writes proto into a fresh project's service proto dir and
// returns the project root, the migrations dir, and the scanned message.
func scanGenerated(t *testing.T, proto string) (root, migDir string, scan *codegen.RawProtoScan, m codegen.RawProtoMessage) {
	t.Helper()
	root = t.TempDir()
	protoDir := filepath.Join(root, "proto", "services", "estimates", "v1")
	if err := os.MkdirAll(protoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(protoDir, "estimates.proto"), []byte(proto), 0o644); err != nil {
		t.Fatal(err)
	}
	migDir = filepath.Join(root, "db", "migrations")
	scan, err := codegen.ScanRawProtoDir(protoDir)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := scan.MessageByName("LineItem")
	if !ok {
		t.Fatal("LineItem not scanned")
	}
	return root, migDir, scan, m
}

func generatedSpecFor(m codegen.RawProtoMessage, scan *codegen.RawProtoScan) entityMigrationEmit {
	return entityMigrationEmit{
		table:   "line_items",
		sd:      codegen.ServiceDef{Package: generatedPkg, Enums: scan.Enums},
		msgName: m.Name,
		fields:  m.Fields,
	}
}

// Forge's own rules need no database: a marker with no expression and a
// field birth gives no single column are refused by line.
func TestGeneratedPreflight_StaticRefusals(t *testing.T) {
	proto := `syntax = "proto3";

package services.estimates.v1;

// forge:entity
message LineItem {
  int64 total_cents = 1; // forge:generated
  repeated int64 parts = 2; // forge:generated ARRAY[1, 2]
}
`
	_, migDir, scan, m := scanGenerated(t, proto)
	err := generatedPreflight(migDir, generatedSpecFor(m, scan).spec(), m)
	if err == nil {
		t.Fatal("a marker with no expression and a repeated generated field must be refused")
	}
	for _, want := range []string{
		"estimates.proto:7:", "carries no expression",
		"estimates.proto:8:", "single-valued fields only",
		"nothing was written",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal missing %q:\n%v", want, err)
		}
	}
}

func TestGeneratedPreflight_TwoMarkersOnOneField(t *testing.T) {
	proto := `syntax = "proto3";

package services.estimates.v1;

// forge:entity
message LineItem {
  int64 a = 1;
  // forge:generated a * 2
  int64 total_cents = 2; // forge:generated a * 3
}
`
	_, migDir, scan, m := scanGenerated(t, proto)
	err := generatedPreflight(migDir, generatedSpecFor(m, scan).spec(), m)
	if err == nil || !strings.Contains(err.Error(), "carries 2 forge:generated markers") ||
		!strings.Contains(err.Error(), "estimates.proto:8:") || !strings.Contains(err.Error(), "estimates.proto:9:") {
		t.Fatalf("two expressions for one column must be refused naming both lines, got: %v", err)
	}
}

// Postgres's rules: the rendered migration is applied to the shadow, and a
// rejection names the field whose expression postgres refused, with
// postgres's own message.
func TestGeneratedPreflight_PostgresVerdict(t *testing.T) {
	if testing.Short() {
		t.Skip("applies the born migration to a real postgres shadow; skipped under -short")
	}
	cases := []struct {
		name           string
		total, balance string
		wantOK         bool
		want           []string
		notWant        []string
	}{
		{
			name:    "valid expressions apply",
			total:   "round(quantity * unit_price_cents)::BIGINT",
			balance: "round(quantity * unit_price_cents)::BIGINT - amount_paid_cents",
			wantOK:  true,
		},
		{
			name:    "unknown column names the one bad marker",
			total:   "round(quantityx * unit_price_cents)::BIGINT",
			balance: "unit_price_cents - amount_paid_cents",
			want:    []string{"estimates.proto:10:", `quantityx`, "does not exist", "nothing was written"},
			notWant: []string{"estimates.proto:12:"},
		},
		{
			name:    "a non-immutable function is refused",
			total:   "quantity::BIGINT",
			balance: "extract(epoch from now())::BIGINT",
			want:    []string{"estimates.proto:12:", "immutable"},
			notWant: []string{"estimates.proto:10:"},
		},
		{
			// Each expression applies alone; together postgres refuses one
			// generated column reading another. Both are named, against
			// postgres's message saying why.
			name:    "a generated column reading another is attributed to both",
			total:   "round(quantity * unit_price_cents)::BIGINT",
			balance: "line_total_cents - amount_paid_cents",
			want:    []string{"estimates.proto:10:", "estimates.proto:12:", "generated column"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, migDir, scan, m := scanGenerated(t, generatedProto(c.total, c.balance))
			err := generatedPreflight(migDir, generatedSpecFor(m, scan).spec(), m)
			if c.wantOK {
				if err != nil {
					t.Fatalf("valid expressions refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("postgres should have rejected the expression")
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("refusal missing %q:\n%v", w, err)
				}
			}
			for _, nw := range c.notWant {
				if strings.Contains(err.Error(), nw) {
					t.Errorf("refusal blames the wrong marker (%q):\n%v", nw, err)
				}
			}
		})
	}
}

// End to end through the sweep's birth: a rejected expression writes NOTHING
// — no migration, and the author's proto untouched by the managed-field
// injection — while an accepted one births the GENERATED column.
func TestBirthMarkedEntity_GeneratedColumn(t *testing.T) {
	if testing.Short() {
		t.Skip("applies the born migration to a real postgres shadow; skipped under -short")
	}

	t.Run("rejected expression writes nothing", func(t *testing.T) {
		proto := generatedProto("round(quantityx * unit_price_cents)::BIGINT", "unit_price_cents - amount_paid_cents")
		root, migDir, scan, m := scanGenerated(t, proto)
		_, err := birthMarkedEntity(migDir, root, scan, m, map[string]bool{"line_items": true}, entityOpts{}, newFKRegistry(nil))
		if err == nil || !strings.Contains(err.Error(), "estimates.proto:10:") {
			t.Fatalf("birth must refuse naming the marker's line, got: %v", err)
		}
		if entries, _ := os.ReadDir(migDir); len(entries) != 0 {
			t.Errorf("a refused birth wrote %d migration file(s)", len(entries))
		}
		after, _ := os.ReadFile(m.File)
		if string(after) != proto {
			t.Errorf("a refused birth edited the proto:\n%s", after)
		}
	})

	t.Run("accepted expression births the generated column", func(t *testing.T) {
		proto := generatedProto("round(quantity * unit_price_cents)::BIGINT", "round(quantity * unit_price_cents)::BIGINT - amount_paid_cents")
		root, migDir, scan, m := scanGenerated(t, proto)
		rep, err := birthMarkedEntity(migDir, root, scan, m, map[string]bool{"line_items": true}, entityOpts{}, newFKRegistry(nil))
		if err != nil {
			t.Fatalf("birth: %v", err)
		}
		up, err := os.ReadFile(rep.UpPath)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"line_total_cents BIGINT NOT NULL GENERATED ALWAYS AS (round(quantity * unit_price_cents)::BIGINT) STORED",
			"balance_cents BIGINT NOT NULL GENERATED ALWAYS AS (round(quantity * unit_price_cents)::BIGINT - amount_paid_cents) STORED",
		} {
			if !strings.Contains(string(up), want) {
				t.Errorf("born migration missing %q:\n%s", want, up)
			}
		}
		// The generated fields are read-only, so the born Create request
		// leaves them out.
		svc, _ := os.ReadFile(m.File)
		createReq := string(svc)
		if i := strings.Index(createReq, "message CreateLineItemRequest {"); i >= 0 {
			createReq = createReq[i:]
			createReq = createReq[:strings.Index(createReq, "}")]
		} else {
			t.Fatalf("no CreateLineItemRequest was injected:\n%s", svc)
		}
		if strings.Contains(createReq, "line_total_cents") || strings.Contains(createReq, "balance_cents") {
			t.Errorf("a generated field must not be client-writable on Create:\n%s", createReq)
		}
		if !strings.Contains(createReq, "unit_price_cents") {
			t.Errorf("plain fields stay on Create:\n%s", createReq)
		}
	})
}
