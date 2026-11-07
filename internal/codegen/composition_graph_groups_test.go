package codegen

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func parseGroups(t *testing.T, src string) (map[string]groupHelp, error) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "roots.go", src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	groups := map[string]groupHelp{}
	return groups, discoverFileGroups(fset, file, "internal/app/roots.go", groups)
}

// A //forge:group comment is its group command's help, by the root doc's
// rule: the first paragraph the Short (on one line), the rest the Long.
// Before, every group command had forge's generic "The server roots".
func TestGroupHelp_TheDeclaredCommentIsTheGroupsHelp(t *testing.T) {
	groups, err := parseGroups(t, `package app

// Run cloud server
// components
//
// Each subcommand starts one server role
// of a split deployment.
//
//forge:group server

// Run the daemon connection gateway
//
//forge:root gateway command=server,gateway
type Gateway struct{}

//forge:root api command=server,api
type API struct{}
`)
	if err != nil {
		t.Fatal(err)
	}
	h, ok := groups["server"]
	if !ok || h.Short != "Run cloud server components" || h.Long != "Each subcommand starts one server role\nof a split deployment." || h.Line != 9 {
		t.Fatalf("groups = %+v", groups)
	}
	roots := []graphRoot{{Name: "gateway", Command: []string{"server", "gateway"}}, {Name: "api", Command: []string{"server", "api"}}}
	if err := checkGroupHelp(groups, roots); err != nil {
		t.Fatal(err)
	}
	cmds, _ := rootCommandViews([]graphRootView{
		{Name: "gateway", TypeName: "Gateway", Command: []string{"server", "gateway"}, Short: "Run the daemon connection gateway"},
		{Name: "api", TypeName: "API", Command: []string{"server", "api"}},
	}, groups)
	src, err := renderGoTemplate("roots_gen.go.tmpl", rootsGenView{Module: "example.com/x", Commands: cmds, Top: []string{"serverCmd"}})
	if err != nil {
		t.Fatal(err)
	}
	gen := string(src)
	for _, want := range []string{
		`Use:   "server",` + "\n\t\t" + `Short: "Run cloud server components",` + "\n\t\t" + `Long:  "Each subcommand starts one server role\nof a split deployment.",`,
	} {
		if !strings.Contains(gen, want) {
			t.Errorf("roots_gen.go lacks %q:\n%s", want, gen)
		}
	}
	if strings.Contains(gen, "The server roots") {
		t.Errorf("the declared help replaces the generated one:\n%s", gen)
	}

	// Undeclared, a group keeps the generated help.
	cmds, _ = rootCommandViews([]graphRootView{{Name: "api", Command: []string{"server", "api"}}}, nil)
	if cmds[0].Use != "server" || cmds[0].Short != "The server roots" || cmds[0].Long != "" {
		t.Errorf("an undeclared group's help = %+v", cmds[0])
	}
}

func TestGroupHelp_Refusals(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
	}{
		{"no text", "package app\n\n//forge:group server\n", "has no help text"},
		{"bad path", "package app\n\n// Help.\n//\n//forge:group Server\n", "want the group's command path"},
		{"no path", "package app\n\n// Help.\n//\n//forge:group\n", "want the group's command path"},
		{"shares a root's doc", "package app\n\n// Help.\n//\n//forge:group server\n//forge:root api command=server,api\ntype API struct{}\n", "shares a comment with a //forge:root"},
		{"twice", "package app\n\n// A.\n//\n//forge:group server\n\n// B.\n//\n//forge:group server\n", "already declared at internal/app/roots.go:5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseGroups(t, tc.src); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
	roots := []graphRoot{{Name: "api", TypeName: "API", Command: []string{"server", "api"}, File: "internal/app/roots.go", Line: 3}}
	for _, tc := range []struct {
		path, want string
	}{
		{"jobs", "no root runs beneath `jobs`"},
		{"server api", "names root api's own command"},
	} {
		groups := map[string]groupHelp{tc.path: {Short: "x", File: "internal/app/roots.go", Line: 9}}
		if err := checkGroupHelp(groups, roots); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("group %q: err = %v, want %q", tc.path, err, tc.want)
		}
	}
}
