package codegen

import (
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

func TestChooseORMBinding(t *testing.T) {
	none := func(string) bool { return false }
	takenSet := func(names ...string) func(string) bool {
		m := map[string]bool{}
		for _, n := range names {
			m[n] = true
		}
		return func(n string) bool { return m[n] }
	}
	cases := []struct {
		name  string
		orm   config.ORMConfig
		taken func(string) bool
		want  ORMBinding
	}{
		{"default", config.ORMConfig{}, none, ORMBinding{Spec: "example.com/app/internal/db", Qual: "db"}},
		{"nested db", config.ORMConfig{Package: "internal/data/db"}, none, ORMBinding{Spec: "example.com/app/internal/data/db", Qual: "db"}},
		{"store", config.ORMConfig{Package: "internal/store"}, none, ORMBinding{Spec: "example.com/app/internal/store", Qual: "store"}},
		// A package named like forge/pkg/orm, which the same file imports,
		// keeps the legacy db alias.
		{"orm", config.ORMConfig{Package: "internal/orm"}, takenSet("orm"), ORMBinding{Spec: "db example.com/app/internal/orm", Qual: "db"}},
		{"store and db taken", config.ORMConfig{Package: "internal/store"}, takenSet("store", "db"), ORMBinding{Spec: "ormstore example.com/app/internal/store", Qual: "ormstore"}},
		{"default db taken", config.ORMConfig{}, takenSet("db"), ORMBinding{Spec: "ormstore example.com/app/internal/db", Qual: "ormstore"}},
		{"every name taken", config.ORMConfig{Package: "internal/store"}, takenSet("store", "db", "ormstore"), ORMBinding{Spec: "ormstore2 example.com/app/internal/store", Qual: "ormstore2"}},
		// A name the generated bodies use for a local is never the qualifier.
		{"local name", config.ORMConfig{Package: "internal/rows"}, none, ORMBinding{Spec: "db example.com/app/internal/rows", Qual: "db"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ChooseORMBinding("example.com/app", c.orm, c.taken); got != c.want {
				t.Errorf("ChooseORMBinding = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestEnsureImportLine_ORMBinding(t *testing.T) {
	src := "package x\n\nimport (\n\t\"context\"\n\t\"" + facadePath + "\"\n)\n"
	b := ChooseORMBinding("example.com/test", config.ORMConfig{Package: "internal/store"}, func(name string) bool { return name == "db" || name == "context" })
	got := ensureImportLine(src, b.Spec)
	bindings := importBindings(t, got)
	if bindings["store"] != storePath || bindings["db"] != facadePath {
		t.Errorf("bindings = %v:\n%s", bindings, got)
	}
}
