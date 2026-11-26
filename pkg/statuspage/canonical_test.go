package statuspage

import (
	"slices"
	"testing"
)

func TestCanonicalUnknownComponentReferences(t *testing.T) {
	permutations := [][]string{
		{"unknown-z", "second", "unknown-a", "first"},
		{"first", "unknown-a", "second", "unknown-z"},
		{"unknown-a", "unknown-z", "first", "second"},
	}
	want := []string{"first", "second", "unknown-a", "unknown-z"}
	for _, names := range permutations {
		t.Run(names[0], func(t *testing.T) {
			original := slices.Clone(names)
			d := Document{
				Components:  []Component{{Name: "first"}, {Name: "second"}},
				Incidents:   []Incident{{Components: slices.Clone(names)}},
				Maintenance: []Maintenance{{Components: slices.Clone(names)}},
			}
			got := d.Canonical()
			for label, refs := range map[string][]string{
				"incident":    got.Incidents[0].Components,
				"maintenance": got.Maintenance[0].Components,
			} {
				if !slices.Equal(refs, want) {
					t.Errorf("%s references = %v, want %v", label, refs, want)
				}
			}
			if !slices.Equal(d.Incidents[0].Components, original) ||
				!slices.Equal(d.Maintenance[0].Components, original) {
				t.Fatal("Canonical modified the input references")
			}
		})
	}
}
