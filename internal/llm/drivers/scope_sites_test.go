// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The scope seam (scope.go) is only as good as the scopes handed to it. The
// compiler makes every account read take a Scope; these tests make every
// place a Scope is MADE visible, so a call site that settles for the personal
// scope where it has a run's org is a reviewed decision, not an accident.
//
// A Scope comes from exactly three places, and each one outside this
// package's own pass-throughs is listed below with the reason it is right:
//
//   - PersonalScope(u): the person's own accounts only;
//   - a Scope{...} literal: a scope built from a row the server read;
//   - an llmCallSpec{...} literal (the activities' request spec) whose OrgID
//     is not chatOrgID(chat): its scope() is the personal scope, or an org
//     that did not come from the chat row.
//
// A new site fails TestScopeOriginsAreReviewed until it is added to
// scopeOrigins. Passing the run's scope is always preferred: add a personal
// site only for a call that belongs to no run (key validation, a probe, the
// picker outside a chat), or one whose org the later phases thread through
// (marked "P5").

// scopeOrigin is one place a Scope is made, keyed by file, enclosing function
// and kind. A function with two such sites is listed twice.
type scopeOrigin struct {
	site   string
	reason string
}

var scopeOrigins = []scopeOrigin{
	{"internal/llm/drivers/scope.go: PersonalScope: Scope{}", "the personal scope's own constructor"},
	{"internal/llm/drivers/org_accounts.go: UsableOrgAccounts: Scope{}", "the chat's org and owner, read from the chat row, to show which org key serves it (never routes)"},

	// The activity edge: the run's org and attendance, read from the chat row.
	{"internal/workflow/runtime/activities/handlers/llm_request.go: llmCallSpec.scope: PersonalScope", "a call with no org: no chat, or a chat that predates orgs (§4.1)"},
	{"internal/workflow/runtime/activities/handlers/llm_request.go: llmCallSpec.scope: Scope{}", "the run's scope, from OrgID/Attended the activity read off the chat row"},
	{"internal/launch/org_accounts_only.go: (*Launcher).checkOrgAccountsOnly: Scope{}", "an org_only automation's launch: the unattended, org-funded-only scope of the run it would start, from the new chat row's owner and org (§11.4)"},
	{"internal/workflow/runtime/activities/handlers/draft_automation.go: (*DraftAutomationActivity).Execute: llmCallSpec{no OrgID}", "a builder draft belongs to no chat or run; a person is drafting (P5: the automation's org)"},
	{"internal/workflow/runtime/activities/handlers/llm_probe.go: ProbeLLMCall: llmCallSpec{no OrgID}", "the dev probe belongs to no run"},

	// One-shot and send-time callers with no run scope in hand yet.
	{"internal/llm/drivers/media_resolution.go: CheckMediaAvailable: PersonalScope", "load_tool's media check carries only the user (P5: the chat's scope)"},
	{"internal/llm/drivers/media_resolution.go: resolveMediaModel: PersonalScope", "media generation carries only the user (P5: the chat's scope)"},
	{"internal/handlers/catalog/catalog.go: (*Service).ListModels: PersonalScope", "the picker reads the person's accounts (P5/P6: the active org's)"},
	{"internal/handlers/catalog/catalog.go: (*Service).ListModels: PersonalScope", "the picker's per-account availability (P5/P6: the active org's)"},
	{"internal/handlers/catalog/catalog.go: (*Service).ListAvailableModels: PersonalScope", "the picker reads the person's accounts (P5/P6: the active org's)"},
	{"internal/handlers/chat/chat_usage_limit.go: usableFallbackModel: PersonalScope", "a person answering a wait (P5: the chat's org)"},
	{"internal/launch/provider_policy.go: (*Launcher).primaryDriver: PersonalScope", "send-time provider default (P5: the chat's org)"},
	{"internal/launch/provider_policy.go: (*Launcher).ResolvedModelAtDriver: PersonalScope", "send-time model wake (P5: the chat's org)"},
	{"internal/launch/selected_model.go: (*Launcher).ValidateSelectedModel: PersonalScope", "send-time model check (P5: the chat's org)"},
	{"internal/launch/workflows.go: (*Launcher).validateChatWorkflowInputsAt: PersonalScope", "launch input validation (P5: the run's org)"},
	{"internal/launch/workflows.go: (*Launcher).SubstituteUnservableModels: PersonalScope", "send-time substitution (P5: the chat's org)"},
	{"tools/reliant-dev/models_probe_run.go: newModelsProbeCmd: PersonalScope", "the dev probe belongs to no run"},
}

func TestScopeOriginsAreReviewed(t *testing.T) {
	root := moduleRoot(t)
	got := collectScopeOrigins(t, root)

	want := make([]string, 0, len(scopeOrigins))
	for _, o := range scopeOrigins {
		if strings.TrimSpace(o.reason) == "" {
			t.Errorf("scope origin %q has no reason", o.site)
		}
		want = append(want, o.site)
	}
	sort.Strings(want)

	missing, extra := multisetDiff(want, got)
	for _, s := range extra {
		t.Errorf("unreviewed scope origin: %s\n\tPass the run's scope if the call has one (llmCallSpec.scope, chatScope).\n\tOnly a call with no run takes PersonalScope; then add it to scopeOrigins with the reason.", s)
	}
	for _, s := range missing {
		t.Errorf("scopeOrigins lists %s, which no longer exists: remove it", s)
	}
}

// The account reads take a Scope by type, so an unscoped caller does not
// compile. This pins that the entry points stay that way.
func TestAccountReadsTakeAScope(t *testing.T) {
	scopeType := reflect.TypeOf(Scope{})
	for name, fn := range map[string]any{
		"BuildAvailableDrivers":      BuildAvailableDrivers,
		"LookupAvailableDrivers":     LookupAvailableDrivers,
		"GetAvailableDrivers":        GetAvailableDrivers,
		"GetDriver":                  GetDriver,
		"ValidateModelSelector":      ValidateModelSelector,
		"ListAvailableModels":        ListAvailableModels,
		"ListAvailableModelsOrEmpty": ListAvailableModelsOrEmpty,
	} {
		ft := reflect.TypeOf(fn)
		found := false
		for i := range ft.NumIn() {
			found = found || ft.In(i) == scopeType
		}
		if !found {
			t.Errorf("%s(%s) reads accounts but takes no drivers.Scope", name, ft)
		}
	}
}

func TestPersonalScope(t *testing.T) {
	s := PersonalScope("u1")
	if s != (Scope{UserID: "u1", Attended: true}) {
		t.Fatalf("PersonalScope = %+v; want the user alone, attended", s)
	}
	if !s.Personal() {
		t.Fatal("PersonalScope names an org")
	}
	if (Scope{UserID: "u1", OrgID: "o1"}).Personal() {
		t.Fatal("a scope naming an org reports Personal")
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil && bytes.HasPrefix(b, []byte("module github.com/reliant-labs/reliant\n")) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("module root not found")
		}
		dir = parent
	}
}

const driversImportPath = "github.com/reliant-labs/reliant/internal/llm/drivers"

// collectScopeOrigins walks the module's non-test Go code (the test process
// reads every file itself, so Go's test cache sees each one) and returns
// every scope origin as "<path>: <func>: <kind>", sorted.
func collectScopeOrigins(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	for _, top := range []string{"cmd", "internal", "pkg", "tools"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if name := d.Name(); name == "testdata" || name == "node_modules" || strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !bytes.Contains(src, []byte("Scope")) && !bytes.Contains(src, []byte("llmCallSpec{")) {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			out = append(out, scopeOriginsIn(t, filepath.ToSlash(rel), src)...)
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	sort.Strings(out)
	return out
}

func scopeOriginsIn(t *testing.T, rel string, src []byte) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	inDrivers := strings.HasPrefix(rel, "internal/llm/drivers/") && filepath.Dir(rel) == "internal/llm/drivers"
	// The name this file imports the drivers package under, "" when it does not.
	alias := ""
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == driversImportPath {
			alias = "drivers"
			if imp.Name != nil {
				alias = imp.Name.Name
			}
		}
	}
	isDriversName := func(e ast.Expr, name string) bool {
		switch x := e.(type) {
		case *ast.Ident:
			return inDrivers && x.Name == name
		case *ast.SelectorExpr:
			id, ok := x.X.(*ast.Ident)
			return ok && alias != "" && id.Name == alias && x.Sel.Name == name
		}
		return false
	}

	var out []string
	for _, decl := range f.Decls {
		fn := enclosingName(decl)
		ast.Inspect(decl, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				if isDriversName(x.Fun, "PersonalScope") {
					out = append(out, fmt.Sprintf("%s: %s: PersonalScope", rel, fn))
				}
			case *ast.CompositeLit:
				if isDriversName(x.Type, "Scope") {
					out = append(out, fmt.Sprintf("%s: %s: Scope{}", rel, fn))
				}
				if id, ok := x.Type.(*ast.Ident); ok && id.Name == "llmCallSpec" {
					// A spec scoped from the chat row (OrgID: chatOrgID(chat))
					// is not an origin; one with no OrgID, or an OrgID from
					// anywhere else, is.
					kind := "llmCallSpec{no OrgID}"
					for _, el := range x.Elts {
						kv, ok := el.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						if k, ok := kv.Key.(*ast.Ident); !ok || k.Name != "OrgID" {
							continue
						}
						kind = "llmCallSpec{OrgID: not from the chat row}"
						if call, ok := kv.Value.(*ast.CallExpr); ok {
							if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "chatOrgID" {
								kind = ""
							}
						}
					}
					if kind != "" {
						out = append(out, fmt.Sprintf("%s: %s: %s", rel, fn, kind))
					}
				}
			}
			return true
		})
	}
	return out
}

func enclosingName(decl ast.Decl) string {
	fd, ok := decl.(*ast.FuncDecl)
	if !ok {
		return "<package>"
	}
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	recv := fd.Recv.List[0].Type
	star := ""
	if s, ok := recv.(*ast.StarExpr); ok {
		star, recv = "*", s.X
	}
	if ix, ok := recv.(*ast.IndexExpr); ok {
		recv = ix.X
	}
	name := "?"
	if id, ok := recv.(*ast.Ident); ok {
		name = id.Name
	}
	if star != "" {
		return fmt.Sprintf("(*%s).%s", name, fd.Name.Name)
	}
	return fmt.Sprintf("%s.%s", name, fd.Name.Name)
}

// multisetDiff returns what want has that got lacks, and the reverse, both
// counting duplicates.
func multisetDiff(want, got []string) (missing, extra []string) {
	count := map[string]int{}
	for _, s := range want {
		count[s]++
	}
	for _, s := range got {
		count[s]--
	}
	for s, n := range count {
		for ; n > 0; n-- {
			missing = append(missing, s)
		}
		for ; n < 0; n++ {
			extra = append(extra, s)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}
