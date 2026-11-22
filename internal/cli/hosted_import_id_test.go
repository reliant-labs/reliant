package cli

import (
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// The hosted ImportLedger refuses a promotion with no id (the id is how a
// re-import and forge's never-imported check recognise history), so the wire
// row must carry the FILE's id verbatim.
func TestImportPromotionRowsCarryTheFilesID(t *testing.T) {
	rows, err := importPromotionRows([]importPromotion{{
		Promotion: release.Promotion{
			ID: "20261002T125413Z-66c6bb601daa92c8", Env: "prod", Release: "v1.7.13",
		},
		PromotedAt:   time.Date(2026, 10, 2, 12, 54, 13, 0, time.UTC),
		ImportedFrom: "git:.forge/promotions/prod.jsonl@x",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := rows[0]["id"]; got != "20261002T125413Z-66c6bb601daa92c8" {
		t.Fatalf("wire row id = %v, want the file's id preserved", got)
	}
}
