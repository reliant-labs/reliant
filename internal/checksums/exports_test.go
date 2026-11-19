// Tests for ExtractGoExports.
//
// Pins the public-symbol extraction behavior the rename-detection
// pass depends on: functions / types / vars / consts collected,
// receiver methods skipped, lowercase identifiers skipped, sorted
// deterministic output.
package checksums

import (
	"reflect"
	"testing"
)

func TestExtractGoExports_ParsesFunctionsTypesVars(t *testing.T) {
	src := []byte(`package forgedb

import "embed"

// MigrationsFS is the embed FS.
//go:embed migrations/*.sql
var MigrationsFS embed.FS

// Migrations is the legacy accessor (deprecated).
func Migrations() embed.FS { return MigrationsFS }

type Migrator struct{}

const MaxRetries = 3

var unexportedVar = "x"

func unexportedFunc() {}
`)
	exports, pkg := ExtractGoExports(src)
	if pkg != "forgedb" {
		t.Errorf("pkg = %q, want %q", pkg, "forgedb")
	}
	want := []string{"MaxRetries", "Migrations", "MigrationsFS", "Migrator"}
	if !reflect.DeepEqual(exports, want) {
		t.Errorf("exports = %v, want %v", exports, want)
	}
}

func TestExtractGoExports_SkipsMethods(t *testing.T) {
	src := []byte(`package forgedb

type Migrator struct{}

// Run is a method — should NOT be in the exports list (callers
// reach it via the Migrator receiver, not the package surface).
func (m *Migrator) Run() error { return nil }

// Hello is a package-level func — should be in exports.
func Hello() {}
`)
	exports, _ := ExtractGoExports(src)
	want := []string{"Hello", "Migrator"}
	if !reflect.DeepEqual(exports, want) {
		t.Errorf("exports = %v, want %v (methods must be skipped)", exports, want)
	}
}

func TestExtractGoExports_HandlesParseError(t *testing.T) {
	exports, pkg := ExtractGoExports([]byte("not valid go {"))
	if exports != nil {
		t.Errorf("exports = %v, want nil on parse error", exports)
	}
	if pkg != "" {
		t.Errorf("pkg = %q, want empty on parse error", pkg)
	}
}

func TestIsGoPath(t *testing.T) {
	cases := map[string]bool{
		"db/embed_gen.go":    true,
		"pkg/app/migrate.go": true,
		"a.go.tmpl":          false,
		"hooks.ts":           false,
		"deploy/main.k":      false,
		"":                   false,
	}
	for in, want := range cases {
		if got := IsGoPath(in); got != want {
			t.Errorf("IsGoPath(%q) = %v, want %v", in, got, want)
		}
	}
}
