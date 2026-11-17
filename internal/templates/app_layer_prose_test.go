// File: internal/templates/app_layer_prose_test.go
//
// A comment shipped into a generated project is not documentation about
// forge — it is the map a user reads while inside their own codebase. When it
// names a file that does not exist, the user goes looking for it, and the
// thing forge was supposed to teach becomes the thing forge lied about. That
// is the whole cost: a wrong pointer in prose is indistinguishable from a
// wrong pointer in code until someone wastes an afternoon on it.
//
// The app layer moved (pkg/app/{bootstrap,services,setup,wire_gen}.go →
// internal/app/{providers,compose,lifecycle,mounts_services}.go) and the
// prose in the .go templates did not follow. This guard is the ratchet: every
// app-layer path a Go-source template names must be a file forge emits.

package templates

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// emittedAppLayerFiles is the app-layer surface forge actually writes into a
// generated project, each entry naming the template that writes it (or the
// codegen pass, for the template-less ones). Adding an app-layer file means
// adding it here; naming one that is not here fails the guard below.
//
// Every entry with a template is checked to exist on disk by
// TestEmittedAppLayerLedgerIsTight, so an entry cannot rot into an excuse for
// a file forge stopped emitting — which is exactly how the dead paths this
// guard removes survived in the first place.
var emittedAppLayerFiles = map[string]string{
	"internal/app/providers.go":           "project/providers.go.tmpl",
	"internal/app/compose.go":             "project/compose.go.tmpl",
	"internal/app/lifecycle.go":           "project/lifecycle.go.tmpl",
	"internal/app/mounts_services_gen.go": "project/mounts_services_gen.go.tmpl",
	"internal/app/auth.go":                "project/app-auth.go.tmpl",
	"internal/app/login_broker.go":        "project/app-login-broker.go.tmpl",
	"internal/app/inject_gen.go":          "", // internal/codegen/inject_gen.go, no template
	// The composition graph (graph mode): providers.go and compose.go are
	// the graph-mode renders of the same owned files.
	"internal/app/graph_gen.go":  "project/graph_gen.go.tmpl",
	"internal/app/routes_gen.go": "project/routes_gen.go.tmpl",
	// The per-component harness is no longer an app-layer file: it lives at
	// internal/handlers/<svc>/helpers_gen_test.go (a _test.go file, so
	// package `testing` stays out of the production binary).
	//
	// pkg/app/migrate.go is likewise gone: it was golang-migrate ceremony with
	// a single project-specific token in it, and now lives in
	// pkg/migratekit.AutoMigrate. The project keeps only the call site, in the
	// serve.go it already owns.
}

var appLayerPathRE = regexp.MustCompile(`(?:pkg|internal)/app/[a-z0-9_]+\.go`)

// TestGoTemplateProseNamesOnlyEmittedAppLayerFiles walks every template that
// renders Go source and fails on any app-layer path that forge does not emit.
func TestGoTemplateProseNamesOnlyEmittedAppLayerFiles(t *testing.T) {
	root := "."
	var offenders []string

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".go.tmpl") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, m := range appLayerPathRE.FindAllString(string(body), -1) {
			if _, ok := emittedAppLayerFiles[m]; !ok {
				offenders = append(offenders, path+" names "+m)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk templates: %v", err)
	}

	for _, o := range offenders {
		t.Errorf("shipped Go template points the user at a file forge does not emit: %s", o)
	}
	if len(offenders) > 0 {
		t.Log("if the path is real, add it to emittedAppLayerFiles with the template that writes it; " +
			"if it is a retired file, name the live replacement instead")
	}
}

// TestEmittedAppLayerLedgerIsTight keeps the ledger honest: an entry whose
// template no longer exists is an entry excusing a file forge no longer
// writes, which would turn this guard into one that reports green.
func TestEmittedAppLayerLedgerIsTight(t *testing.T) {
	for dest, tmpl := range emittedAppLayerFiles {
		if tmpl == "" {
			continue
		}
		if _, err := os.Stat(filepath.FromSlash(tmpl)); err != nil {
			t.Errorf("ledger entry %q names template %q, which does not exist: %v", dest, tmpl, err)
		}
	}
}
