package templates

import (
	"reflect"
	"strings"
	"testing"
)

// Two frontends declared on one directory (a web and a desktop frontend
// built from one package.json) are one directory to install: the one named
// like the directory stands for it, and the directories keep their order.
func TestUniqueFrontendDirs_OneFrontendPerDirectory(t *testing.T) {
	in := []FrontendCIConfig{
		{Name: "admin", Path: "frontends/admin"},
		{Name: "desktop", Path: "frontends/web"},
		{Name: "web", Path: "frontends/web"},
		{Name: "docs", Path: "docs"},
	}
	got := UniqueFrontendDirs(in)
	want := []FrontendCIConfig{in[0], in[2], in[3]}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("UniqueFrontendDirs = %+v, want %+v", got, want)
	}
	if len(in) != 4 {
		t.Errorf("UniqueFrontendDirs modified its input: %+v", in)
	}
}

// The frontend standing for a shared directory names its rows, which
// forge.yaml's `replaces:` refers to, so it never depends on declaration
// order: the one named like the directory, else the alphabetically first.
func TestUniqueFrontendDirs_ChoiceIgnoresDeclarationOrder(t *testing.T) {
	for _, tc := range []struct {
		in   []FrontendCIConfig
		want string
	}{
		{[]FrontendCIConfig{{Name: "forge-demo", Path: "frontends/web"}, {Name: "web", Path: "frontends/web"}, {Name: "desktop", Path: "frontends/web"}}, "web"},
		{[]FrontendCIConfig{{Name: "web", Path: "frontends/web"}, {Name: "desktop", Path: "frontends/web"}}, "web"},
		{[]FrontendCIConfig{{Name: "zeta", Path: "app"}, {Name: "alpha", Path: "app"}}, "alpha"},
		{[]FrontendCIConfig{{Name: "alpha", Path: "app"}, {Name: "zeta", Path: "app"}}, "alpha"},
	} {
		got := UniqueFrontendDirs(tc.in)
		if len(got) != 1 || got[0].Name != tc.want {
			t.Errorf("UniqueFrontendDirs(%+v) = %+v, want %s", tc.in, got, tc.want)
		}
		aliases := FrontendsSharingDir(tc.in)
		if len(aliases) != len(tc.in)-1 || aliases[tc.want] != "" {
			t.Errorf("FrontendsSharingDir(%+v) = %v, want every other frontend mapped to %s", tc.in, aliases, tc.want)
		}
		for other, canon := range aliases {
			if canon != tc.want {
				t.Errorf("FrontendsSharingDir maps %s to %s, want %s", other, canon, tc.want)
			}
		}
	}
}

// ci.yml installs a shared directory once and keys the Node cache on its
// lockfile once; each frontend still gets its own image.
func TestCIWorkflow_SharedFrontendDirectoryInstallsOnce(t *testing.T) {
	data := CIWorkflowData{
		ProjectName:  "demo",
		HasFrontends: true,
		HasServices:  true,
		LintFrontend: true,
		Frontends: []FrontendCIConfig{
			{Name: "desktop", Path: "frontends/web"},
			{Name: "web", Path: "frontends/web"},
		},
	}
	out, err := CITemplates("github").Render("ci.yml.tmpl", data)
	if err != nil {
		t.Fatal(err)
	}
	body := string(out)
	// Every job that sets up Node installs each directory once.
	setups := strings.Count(body, "- name: Setup Node.js")
	if setups == 0 {
		t.Fatalf("ci.yml sets up no Node toolchain:\n%s", body)
	}
	if n := strings.Count(body, "working-directory: frontends/web\n        run: pnpm install --frozen-lockfile"); n != setups {
		t.Errorf("frontends/web is installed %d times in %d Node setups, want once per setup:\n%s", n, setups, body)
	}
	if strings.Contains(body, "Install desktop dependencies") {
		t.Errorf("the second frontend of frontends/web got its own install step:\n%s", body)
	}
	if n := strings.Count(body, "            frontends/web/pnpm-lock.yaml\n"); n != setups {
		t.Errorf("frontends/web's lockfile is a cache key %d times in %d Node setups, want once per setup", n, setups)
	}
}
