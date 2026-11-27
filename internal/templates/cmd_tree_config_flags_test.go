package templates_test

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// `db migrate` loads no typed config — its whole configuration is
// DATABASE_URL — so it must not register the server's config flags:
// `<bin> db migrate --help` listed ~60 CORS/OIDC/JWT/TLS/pprof flags none of
// which it reads (friction #25).
func TestCmdTreeDB_RegistersNoServerConfigFlags(t *testing.T) {
	src := renderDB(t, "cmd-tree-db.go.tmpl", true)
	body := dbFuncBody(t, src, "newDBCmd")
	if strings.Contains(body, "RegisterFlags") {
		t.Errorf("newDBCmd registers config flags; the migrator reads none of them:\n%s", body)
	}
	for _, imp := range dbFileImports(t, src) {
		if strings.HasSuffix(imp, "/pkg/config") && !strings.Contains(imp, "reliant-labs/forge") {
			t.Errorf("rendered db.go imports the project's typed config %q, which it no longer uses", imp)
		}
	}
}

// The commands.go a new project starts from points at the narrow
// registration, not at the server's whole flag surface.
func TestCmdTreeCommands_RecommendsFieldFlags(t *testing.T) {
	raw, err := templates.ProjectTemplates().Render("cmd-tree-commands.go.tmpl", struct{ Name string }{Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	if !strings.Contains(out, "config.RegisterFieldFlags") {
		t.Errorf("commands.go does not point at config.RegisterFieldFlags:\n%s", out)
	}
}
