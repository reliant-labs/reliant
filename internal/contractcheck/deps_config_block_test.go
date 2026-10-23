package contractcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The two Deps rules used to contradict each other, with no way out.
//
// forge-config-deps rejects naked scalars on a Deps struct (`BaseURL string`)
// because a scalar has no type for the composition to resolve against, so it
// is silently wired to a zero value; the remedy it prints is to group the
// scalars into a config message and take `Cfg config.FooConfig`. Doing that
// produced a Deps field whose type is a protobuf message — which carries
// generated getters, so it missed the method-less-data exemption and
// forge-deps-are-interfaces rejected it instead. This rule supports no
// suppression, so an author who followed the first rule's instructions had no
// way to make the tree lint.
func TestDepsAreInterfaces_AllowsGeneratedConfigBlock(t *testing.T) {
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "internal", "idp")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}

	src := `package idp

import (
	"log/slog"
	"net/http"

	configv1 "github.com/acme/proj/gen/config/v1"
)

type Deps struct {
	Logger     *slog.Logger
	HTTPClient *http.Client
	Cfg        *configv1.IdpConfig
}
`
	if err := os.WriteFile(filepath.Join(pkgDir, "adapter.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := lintDepsAreInterfaces(dir)
	if err != nil {
		t.Fatalf("lint: %v", err)
	}
	findings := res.Findings
	for _, f := range findings {
		if strings.Contains(f.Message, `"Cfg"`) {
			t.Errorf("config block flagged, leaving the author stuck between two rules: %s", f.Message)
		}
	}
}

// The exemption is keyed on the generated-config IMPORT PATH, not on the type
// name, so a repository does not slip through by being called *FooConfig.
func TestDepsAreInterfaces_StillFlagsConcreteNonConfig(t *testing.T) {
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "internal", "orders")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}

	src := `package orders

import (
	"log/slog"

	"github.com/acme/proj/internal/db"
)

type Deps struct {
	Logger *slog.Logger
	Repo   *db.PostgresConfig
}
`
	if err := os.WriteFile(filepath.Join(pkgDir, "service.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := lintDepsAreInterfaces(dir)
	if err != nil {
		t.Fatalf("lint: %v", err)
	}
	findings := res.Findings
	var flagged bool
	for _, f := range findings {
		if strings.Contains(f.Message, `"Repo"`) {
			flagged = true
		}
	}
	if !flagged {
		t.Error("a concrete type from a non-config package should still be flagged")
	}
}
