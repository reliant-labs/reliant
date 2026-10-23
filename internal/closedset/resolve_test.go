package closedset

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A project's own set is read from its source by the type checker: members
// named by constants read as their values, and nothing that is not provably
// a constant is accepted.
func TestResolveGo(t *testing.T) {
	// `go list` reads only the scratch module in t.TempDir(), so the test is
	// cache-sound, and a dependency-free module type-checks well under 2s.
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/app\n\ngo 1.22\n")
	write("billing/tiers.go", `package billing

type Tier string

const (
	TierFree Tier = "free"
	TierPro  Tier = "pro"
)

var AllTiers = []Tier{TierFree, TierPro, "enterprise"}

var Levels = [...]int{1, 2}

var Alias = AllTiers

func tiers() []Tier { return nil }

var Computed = tiers()
`)
	got, err := ResolveGo(dir, "example.com/app/billing.AllTiers")
	if err != nil || !slices.Equal(got, []string{"free", "pro", "enterprise"}) {
		t.Fatalf("AllTiers = %q, %v", got, err)
	}
	if got, err := ResolveGo(dir, "example.com/app/billing.Levels"); err != nil || !slices.Equal(got, []string{"1", "2"}) {
		t.Fatalf("Levels = %q, %v", got, err)
	}
	for name, want := range map[string]string{
		"example.com/app/billing.Alias":    "not initialized by a literal (it is AllTiers)",
		"example.com/app/billing.Computed": "not initialized by a literal",
		"example.com/app/billing.Missing":  "declares no package-level variable Missing",
		"example.com/app/nope.X":           "load",
	} {
		if _, err := ResolveGo(dir, name); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", name, err, want)
		}
	}
}
