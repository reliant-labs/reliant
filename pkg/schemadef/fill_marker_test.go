package schemadef

import "testing"

// TestColumn_FillStrategy pins the forge:fill=<strategy> parsing grammar
// against Column.Comment directly — no real postgres needed, since the
// catalog round-trip is already covered by
// TestApplyAndIntrospect_ColumnMarkersSurviveTheCatalog.
func TestColumn_FillStrategy(t *testing.T) {
	cases := []struct {
		name       string
		comment    string
		wantOK     bool
		wantResult string
	}{
		{"ulid", "forge:fill=ulid", true, "ulid"},
		{"handler", "forge:fill=handler", true, "handler"},
		{"absent", "forge:immutable", false, ""},
		{"trailing prose", "forge:fill=handler — stamped from claims", true, "handler"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			col := Column{Comment: c.comment}
			got, ok := col.FillStrategy()
			if ok != c.wantOK {
				t.Fatalf("FillStrategy() ok = %v, want %v", ok, c.wantOK)
			}
			if ok && got != c.wantResult {
				t.Errorf("FillStrategy() = %q, want %q", got, c.wantResult)
			}
		})
	}
}

// TestColumn_HasMarker_FindsFillDeclaration proves ColumnMarkerFill (the
// bare "forge:fill" name, without "=value") is a substring of any real
// declaration, so HasMarker(ColumnMarkerFill) still answers "was
// forge:fill declared at all" correctly.
func TestColumn_HasMarker_FindsFillDeclaration(t *testing.T) {
	col := Column{Comment: "forge:fill=ulid"}
	if !col.HasMarker(ColumnMarkerFill) {
		t.Error("HasMarker(forge:fill) must match a forge:fill=ulid declaration")
	}
}

func TestKnownColumnMarkers_IncludesFill(t *testing.T) {
	found := false
	for _, m := range KnownColumnMarkers {
		if m == ColumnMarkerFill {
			found = true
		}
	}
	if !found {
		t.Error("KnownColumnMarkers must include forge:fill so the column-markers lint recognizes it")
	}
}
