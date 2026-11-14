package cli

import (
	"strings"
	"testing"
)

// The refusal prints the user's own command with the two flags added, so an
// agent can paste it; --link-forge is a bool, so it never takes the path.
func TestLinkForgeRerun(t *testing.T) {
	name := Name()
	for _, tc := range []struct {
		name string
		args []string
		root string
		want string
	}{
		{"checkout known", []string{"project", "new", "app1", "--service", "tasks", "--frontend", "web"}, "/src/forge",
			name + " project new app1 --service tasks --frontend web --link-forge --forge-root /src/forge"},
		{"checkout unknown", []string{"project", "new", "app1"}, "",
			name + " project new app1 --link-forge --forge-root <path-to-forge-checkout>"},
		{"flags already present are not repeated", []string{"project", "new", "app1", "--link-forge", "--forge-root", "/old", "--mod=x/y"}, "/src/forge",
			name + " project new app1 --mod=x/y --link-forge --forge-root /src/forge"},
		{"root with a space is quoted", []string{"project", "new", "a"}, "/my src/forge",
			name + " project new a --link-forge --forge-root '/my src/forge'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := linkForgeRerun(tc.args, tc.root); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestScaffoldForgeResolutionPrintsTheRunnableCommand(t *testing.T) {
	err := scaffoldForgeResolution("service", "", "", "/src/forge", []string{"project", "new", "app1", "--service", "tasks"})
	if err == nil {
		t.Fatal("want a refusal")
	}
	want := Name() + " project new app1 --service tasks --link-forge --forge-root /src/forge"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("refusal lacks the exact command %q:\n%v", want, err)
	}
	if strings.Contains(err.Error(), "task install:dev") || strings.Contains(err.Error(), "make dev") {
		t.Errorf("the refusal still sends the reader to a build step:\n%v", err)
	}
}
