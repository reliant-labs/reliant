package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The service catalog is a projection forge regenerates whole, so two
// branches that each add a service — a line conflict in its lists — are
// settled by keeping this side and regenerating. Unless a side disowned it:
// then its content may be a person's, and the conflict stays, whichever side
// it was. These cases drive the decision through mergeGeneratedPath with the
// sides' disown records stubbed; TestMergeGenerated_ServiceCatalogRealMerge
// reads them from real merges.
func TestMergeGeneratedPath_UncertifiedConflictKeepsOursOnlyWhenNoSideDisowns(t *testing.T) {
	const rel = "gen/forge_service_catalog.json"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir) // projectRelative finds the project by its forge.yaml

	catalog := func(services ...string) []byte {
		return []byte("{\n  \"services\": [\n    \"" + strings.Join(services, "\",\n    \"") + "\"\n  ]\n}\n")
	}
	base := catalog("api")
	ours := catalog("api", "alpha")
	theirs := catalog("api", "beta")
	if _, clean, _ := mergeGenerated(context.Background(), base, ours, theirs, 7); clean {
		t.Fatal("precondition: two services appended to one list conflict as lines")
	}

	disowns := []byte(`{"files": {"` + rel + `": {"reason": "hand-curated"}}}`)
	other := []byte(`{"files": {"internal/app/compose.go": {}}}`)
	for _, tc := range []struct {
		name    string
		records [][]byte // HEAD, theirs, then the bases
		known   bool
		keeps   bool
	}{
		{"no side has a disown record", [][]byte{nil, nil, nil}, true, true},
		{"records that disown other files", [][]byte{other, other, other}, true, true},
		{"ours disowned it", [][]byte{disowns, nil, nil}, true, false},
		{"theirs disowned it", [][]byte{nil, disowns, nil}, true, false},
		{"the base disowned it", [][]byte{nil, nil, disowns}, true, false},
		{"a record that does not parse", [][]byte{nil, []byte("{not json"), nil}, true, false},
		{"a present but empty record", [][]byte{[]byte{}, nil, nil}, true, false},
		{"the sides cannot be named", nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prev := mergeSideDisownRecords
			mergeSideDisownRecords = func(context.Context, string, []byte, []byte) ([][]byte, bool) {
				return tc.records, tc.known
			}
			t.Cleanup(func() { mergeSideDisownRecords = prev })

			merged, clean, note, err := mergeGeneratedPath(context.Background(), rel, base, ours, theirs, 7)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.keeps {
				if clean {
					t.Fatalf("the conflict must stay; got a clean merge (note %q):\n%s", note, merged)
				}
				if !strings.Contains(string(merged), "beta") {
					t.Fatalf("the incoming side's line was dropped:\n%s", merged)
				}
				return
			}
			if !clean || string(merged) != string(ours) {
				t.Fatalf("want this side's copy, clean; clean=%v:\n%s", clean, merged)
			}
			if note != regenerateNote || !strings.Contains(note, "run `forge generate` to rebuild it") {
				t.Errorf("the note must tell the reader to regenerate; got %q", note)
			}
		})
	}
}
