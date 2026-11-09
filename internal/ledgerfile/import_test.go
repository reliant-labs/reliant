package ledgerfile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// importedPromotion is a promotion as a git ledger holds it: an id and a
// timestamp this machine did not assign.
func importedPromotion(id, env, version string, at time.Time) release.Promotion {
	p := promotion(env, version)
	p.ID = id
	p.PromotedAt = at
	return p
}

func TestImportPromotionFromPreservesIDAndTime(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 9, 19, 1, 27, 23, 0, time.UTC)
	want := importedPromotion("20260919T012723Z-converted", "prod", "v1.5.18", at)

	if err := s.ImportPromotionFrom(want, "git:.forge/promotions/prod.jsonl@abc123"); err != nil {
		t.Fatalf("import: %v", err)
	}

	got, err := s.Promotions("prod")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d promotions, want 1", len(got))
	}
	// The ID is the contract: internal/cli's unimported-checkout refusal
	// matches on exactly this, so an id the import reassigned would leave
	// the refusal firing forever.
	if got[0].ID != want.ID {
		t.Errorf("id = %q, want %q (an import that renumbers never clears the refusal)", got[0].ID, want.ID)
	}
	if !got[0].PromotedAt.Equal(at) {
		t.Errorf("promoted_at = %v, want %v", got[0].PromotedAt, at)
	}
}

func TestImportPromotionFromRecordsItsSource(t *testing.T) {
	s := openTest(t)
	p := importedPromotion("id-1", "prod", "v1.0.0", time.Date(2026, 6, 25, 14, 57, 15, 0, time.UTC))
	const source = "git:.forge/promotions/prod.jsonl@deadbeef"

	if err := s.ImportPromotionFrom(p, source); err != nil {
		t.Fatalf("import: %v", err)
	}

	from, err := s.ImportedFrom("prod")
	if err != nil {
		t.Fatalf("read provenance: %v", err)
	}
	if from["id-1"] != source {
		t.Errorf("imported_from = %q, want %q", from["id-1"], source)
	}

	// The provenance is an EXTRA key on the line, so the line still
	// decodes as a plain promotion — which is what keeps it acceptable to
	// ImportLedger and to every existing reader.
	raw, err := os.ReadFile(filepath.Join(s.Dir(), "promotions", "prod.jsonl"))
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}
	var decoded release.Promotion
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &decoded); err != nil {
		t.Fatalf("an imported line must still decode as a promotion: %v", err)
	}
	if decoded.Release != "v1.0.0" {
		t.Errorf("release = %q, want v1.0.0", decoded.Release)
	}
}

func TestImportPromotionFromIsIdempotentOnTheSourceID(t *testing.T) {
	s := openTest(t)
	p := importedPromotion("id-1", "prod", "v1.0.0", time.Now().UTC().Truncate(time.Second))

	for i := 0; i < 3; i++ {
		if err := s.ImportPromotionFrom(p, "git:x@y"); err != nil {
			t.Fatalf("import %d: %v", i, err)
		}
	}

	got, err := s.Promotions("prod")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d promotions after three imports, want 1", len(got))
	}
}

// Consecutive promotions of ONE release are real history and must all land.
// release.Decide would collapse them to a no-op, which is exactly why the
// import does not go through the promote path.
func TestImportPromotionFromKeepsConsecutivePromotionsOfOneRelease(t *testing.T) {
	s := openTest(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, id := range []string{"a", "b", "c"} {
		p := importedPromotion(id, "prod", "v1.0.0", base.Add(time.Duration(i)*time.Hour))
		if err := s.ImportPromotionFrom(p, "git:x@"+id); err != nil {
			t.Fatalf("import %s: %v", id, err)
		}
	}

	got, err := s.Promotions("prod")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d promotions, want 3 — history is not re-decided", len(got))
	}
}

func TestImportPromotionFromRefusesAnUnnamedSource(t *testing.T) {
	s := openTest(t)
	p := importedPromotion("id-1", "prod", "v1.0.0", time.Now().UTC())

	err := s.ImportPromotionFrom(p, "   ")
	if err == nil {
		t.Fatal("expected a refusal: an imported record must name where it came from")
	}
	if !strings.Contains(err.Error(), "source is required") {
		t.Errorf("error = %v, want it to name the missing source", err)
	}
}

func TestImportPromotionFromRefusesAMissingID(t *testing.T) {
	s := openTest(t)
	p := promotion("prod", "v1.0.0") // no ID

	err := s.ImportPromotionFrom(p, "git:x@y")
	if err == nil {
		t.Fatal("expected a refusal: an import is identified by the record it came from")
	}
	if !strings.Contains(err.Error(), "source id is required") {
		t.Errorf("error = %v, want it to name the missing id", err)
	}
}

func TestImportPromotionFromRefusesAnInvalidPromotion(t *testing.T) {
	s := openTest(t)
	p := importedPromotion("id-1", "prod", "", time.Now().UTC()) // no release

	err := s.ImportPromotionFrom(p, "git:x@y")
	if !errors.Is(err, release.ErrInvalid) {
		t.Fatalf("err = %v, want release.ErrInvalid", err)
	}
}

// ImportedFrom distinguishes "recorded here" from "imported": a promotion
// this machine made is ABSENT from the map, not present-and-empty.
func TestImportedFromOmitsLocallyRecordedPromotions(t *testing.T) {
	s := openTest(t)
	local, err := s.AppendPromotion(promotion("prod", "v1.0.0"), decideOnly)
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	imported := importedPromotion("from-git", "prod", "v2.0.0", time.Now().UTC().Truncate(time.Second))
	if err := s.ImportPromotionFrom(imported, "git:x@y"); err != nil {
		t.Fatalf("import: %v", err)
	}

	from, err := s.ImportedFrom("prod")
	if err != nil {
		t.Fatalf("read provenance: %v", err)
	}
	if _, ok := from[local.ID]; ok {
		t.Errorf("promotion %q was recorded here, so it must not report a source", local.ID)
	}
	if from["from-git"] == "" {
		t.Error("the imported promotion must report its source")
	}
}
