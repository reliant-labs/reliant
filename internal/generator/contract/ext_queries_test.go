package contract

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeExtFile(t *testing.T, name, src string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func collectExt(t *testing.T, src string) ExtQuerySet {
	t.Helper()
	set, err := CollectExtQueries(writeExtFile(t, "plan_repo_ext.go", src))
	if err != nil {
		t.Fatalf("CollectExtQueries: %v", err)
	}
	return set
}

func queryNames(set ExtQuerySet) []string {
	var out []string
	for _, q := range set.Queries {
		out = append(out, q.Name)
	}
	return out
}

func skipReasons(set ExtQuerySet) map[string]string {
	out := map[string]string{}
	for _, s := range set.Skipped {
		out[s.Name] = s.Reason
	}
	return out
}

// The shape the seam's own header teaches is collected, the handle dropped;
// every other exported function that takes a handle is named with why, and
// one that takes none (a mapper) is not a query and is not mentioned.
func TestCollectExtQueries_CollectsTheSeamShapeAndNamesTheRest(t *testing.T) {
	set := collectExt(t, `package store

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"

	"example.com/proj/internal/core"
)

// GetPlan returns the plan.
//
//go:noinline
func GetPlan(ctx context.Context, db orm.Context, id string) (*core.Plan, error) { return nil, nil }

// ListPlans lists with filters.
func ListPlans(ctx context.Context, db orm.Context, threadID string, opts ...orm.QueryOption) ([]*core.Plan, error) {
	return nil, nil
}

func CountAndNewest(ctx context.Context, db orm.Context) (n int64, newest *Plan, err error) { return 0, nil, nil }

func Touch(ctx context.Context, db orm.Context, a, b string) {}

func Generic[T any](ctx context.Context, db orm.Context, v T) error { return nil }

func Private(ctx context.Context, db orm.Context) (*planRow, error) { return nil, nil }

func WithTx(ctx context.Context, db orm.Context) error { return nil }

func HandleSecond(db orm.Context, ctx context.Context) error { return nil }

func NoHandle(ctx context.Context, id string) error { return nil }

func PlanToCore(p *Plan) *core.Plan { return nil }

func unexported(ctx context.Context, db orm.Context) error { return nil }

func (p *Plan) Method(ctx context.Context, db orm.Context) error { return nil }

type Plan struct{}
type planRow struct{}
`)
	if got, want := queryNames(set), []string{"CountAndNewest", "GetPlan", "ListPlans", "Touch"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("queries = %v, want %v (sorted)", got, want)
	}
	reasons := skipReasons(set)
	for name, want := range map[string]string{
		"Generic":      "generic",
		"Private":      "names the unexported type planRow",
		"WithTx":       "named WithTx",
		"HandleSecond": "not func(ctx context.Context, db orm.Context",
		"NoHandle":     "not func(ctx context.Context, db orm.Context",
	} {
		if !strings.Contains(reasons[name], want) {
			t.Errorf("skip reason for %s = %q, want it to say %q", name, reasons[name], want)
		}
	}
	for _, quiet := range []string{"PlanToCore", "unexported", "Method"} {
		if _, ok := reasons[quiet]; ok {
			t.Errorf("%s is not a query and must not be reported: %v", quiet, set.Skipped)
		}
	}

	byName := map[string]ExtQuery{}
	for _, q := range set.Queries {
		byName[q.Name] = q
	}
	for name, want := range map[string]string{
		"GetPlan":        "(ctx context.Context, id string) (*core.Plan, error)",
		"ListPlans":      "(ctx context.Context, threadID string, opts ...orm.QueryOption) ([]*core.Plan, error)",
		"CountAndNewest": "(ctx context.Context) (n int64, newest *Plan, err error)",
		"Touch":          "(ctx context.Context, a string, b string)",
	} {
		if got := byName[name].String(); got != want {
			t.Errorf("%s signature = %q, want %q", name, got, want)
		}
	}
	if p := byName["ListPlans"].Params; !p[len(p)-1].Variadic {
		t.Errorf("ListPlans' opts must be marked variadic: %+v", p)
	}
	if got := byName["GetPlan"].Doc; !reflect.DeepEqual(got, []string{"// GetPlan returns the plan."}) {
		t.Errorf("GetPlan doc = %q, want the prose with the //go: directive dropped", got)
	}

	if set.ORMName != "orm" || set.Package != "store" {
		t.Errorf("ORMName, Package = %q, %q", set.ORMName, set.Package)
	}
	var paths []string
	for _, imp := range set.Imports {
		paths = append(paths, imp.Path)
	}
	if want := []string{"context", "example.com/proj/internal/core", ORMImportPath}; !reflect.DeepEqual(paths, want) {
		t.Errorf("imports = %v, want %v (sorted, only what the signatures name)", paths, want)
	}
}

func TestCollectExtQueries_DistinguishesPredeclaredTypesFromLocalShadows(t *testing.T) {
	path := writeExtFile(t, "plan_repo_ext.go", `package store
import (
	"context"
	"github.com/reliant-labs/forge/pkg/orm"
)

type Filter[T any] struct{}
func UsesUniverse(ctx context.Context, db orm.Context, v Filter[rune], x interface{ Echo(byte) any }) complex128 { return 0 }
func UsesShadow(ctx context.Context, db orm.Context, v Filter[error]) uint { return 0 }
`)
	// A type declared in a sibling source file hides a predeclared type;
	// a declaration in an internal test file does not affect generated code.
	for name, src := range map[string]string{
		"local.go":      "package store; type error struct{}",
		"local_test.go": "package store; type rune struct{}",
	} {
		if err := os.WriteFile(filepath.Join(filepath.Dir(path), name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	set, err := CollectExtQueries(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := queryNames(set), []string{"UsesUniverse"}; !reflect.DeepEqual(got, want) {
		t.Errorf("queries = %v, want %v", got, want)
	}
	if got := skipReasons(set)["UsesShadow"]; got != "names the unexported type error" {
		t.Errorf("UsesShadow skip reason = %q", got)
	}
}

// Aliases are the file's: an interface rendered with the same imports reads
// exactly like the source, and orm.Context is recognised under any alias.
func TestCollectExtQueries_KeepsTheFilesAliases(t *testing.T) {
	set := collectExt(t, `package store

import (
	stdctx "context"

	forgeorm "github.com/reliant-labs/forge/pkg/orm"
	pb "example.com/proj/gen/plans/v1"
)

func GetPlan(ctx stdctx.Context, db forgeorm.Context, id string) (*pb.Plan, error) { return nil, nil }
`)
	if got := queryNames(set); !reflect.DeepEqual(got, []string{"GetPlan"}) {
		t.Fatalf("queries = %v, skipped = %v", got, set.Skipped)
	}
	if set.ORMName != "forgeorm" {
		t.Errorf("ORMName = %q, want the file's alias forgeorm", set.ORMName)
	}
	if got := set.Queries[0].String(); got != "(ctx stdctx.Context, id string) (*pb.Plan, error)" {
		t.Errorf("signature = %q", got)
	}
	var decls []string
	for _, imp := range set.Imports {
		decls = append(decls, imp.Declaration())
	}
	want := []string{`stdctx "context"`, `pb "example.com/proj/gen/plans/v1"`, `forgeorm "github.com/reliant-labs/forge/pkg/orm"`}
	if !reflect.DeepEqual(decls, want) {
		t.Errorf("imports = %v, want %v", decls, want)
	}
}

// Unnamed and blank parameters get names a forwarding call can pass on.
func TestCollectExtQueries_NamesUnnamedParameters(t *testing.T) {
	set := collectExt(t, `package store

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"
)

func Unnamed(context.Context, orm.Context, string, int) error { return nil }

func Blank(ctx context.Context, _ orm.Context, _ string, p1 int) (_ int, err error) { return 0, nil }
`)
	byName := map[string]ExtQuery{}
	for _, q := range set.Queries {
		byName[q.Name] = q
	}
	if got := byName["Unnamed"].String(); got != "(p0 context.Context, p1 string, p2 int) error" {
		t.Errorf("Unnamed = %q", got)
	}
	// p1 is the user's; the blank one at index 1 must not take it.
	if got := byName["Blank"].String(); got != "(ctx context.Context, p1_2 string, p1 int) (int, error)" {
		t.Errorf("Blank = %q", got)
	}
}

// A qualifier no import binds is named, not rendered into a file that cannot
// compile.
func TestCollectExtQueries_SkipsAnUnboundQualifier(t *testing.T) {
	set := collectExt(t, `package store

import (
	ctx "context"

	orm "github.com/reliant-labs/forge/pkg/orm"
)

func Get(c ctx.Context, db orm.Context) (*missing.Plan, error) { return nil, nil }
`)
	if len(set.Queries) != 0 || !strings.Contains(skipReasons(set)["Get"], "missing.") {
		t.Fatalf("queries = %v, skipped = %v", queryNames(set), set.Skipped)
	}
}

// A file the collector cannot read faithfully is refused whole, as a typed
// error the generator reports and moves past.
func TestCollectExtQueries_RefusesAnUnusableFile(t *testing.T) {
	for name, tc := range map[string]struct{ file, src, want string }{
		"parse error":      {"plan_repo_ext.go", "package store\n\nfunc (", "does not parse"},
		"dot import":       {"plan_repo_ext.go", "package store\n\nimport . \"context\"\n\nvar _ Context\n", "dot import"},
		"build constraint": {"plan_repo_ext.go", "//go:build linux\n\npackage store\n", "build constraints"},
		"GOOS suffix":      {"plan_repo_ext_linux.go", "package store\n", "build constraints"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := CollectExtQueries(writeExtFile(t, tc.file, tc.src))
			var unusable *ErrExtFileUnusable
			if !errors.As(err, &unusable) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want an ErrExtFileUnusable saying %q", err, tc.want)
			}
		})
	}
}
