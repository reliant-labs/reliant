package release

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Decode through the historical API so the regression proof compiles against
// the old implementation: equal image maps must not erase snapshot intent.
func TestRecutRejectsBindingOnlySnapshotChange(t *testing.T) {
	var first, second Release
	for i, target := range []*Release{&first, &second} {
		raw := `{"release":"v1","artifacts":{"registry.test/reliant":{"kind":"oci","mode":"shared","digests":{"*":"sha256:` + strings.Repeat("a", 64) + `"}}},"snapshot_intents":{"prod":"sha256:` + strings.Repeat([]string{"b", "c"}[i], 64) + `"}}`
		if err := json.Unmarshal([]byte(raw), target); err != nil {
			t.Fatal(err)
		}
	}
	if err := CheckRecut(first, second); !errors.Is(err, ErrReleaseConflict) {
		t.Fatalf("equal repository pins hid changed snapshot intent: %v", err)
	}
}
