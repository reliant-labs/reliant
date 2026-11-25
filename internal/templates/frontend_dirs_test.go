package templates

import (
	"reflect"
	"strings"
	"testing"
)

// Two frontends declared on one directory (a web and a desktop frontend
// built from one package.json) are one directory to install: the first of
// them stands for it, and the order of the rest is kept.
func TestUniqueFrontendDirs_FirstFrontendOfEachDirectory(t *testing.T) {
	in := []FrontendCIConfig{
		{Name: "admin", Path: "frontends/admin"},
		{Name: "desktop", Path: "frontends/web"},
		{Name: "web", Path: "frontends/web"},
		{Name: "docs", Path: "docs"},
	}
	got := UniqueFrontendDirs(in)
	want := []FrontendCIConfig{in[0], in[1], in[3]}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("UniqueFrontendDirs = %+v, want %+v", got, want)
	}
	if len(in) != 4 {
		t.Errorf("UniqueFrontendDirs modified its input: %+v", in)
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
	if strings.Contains(body, "Install web dependencies") {
		t.Errorf("the second frontend of frontends/web got its own install step:\n%s", body)
	}
	if n := strings.Count(body, "            frontends/web/pnpm-lock.yaml\n"); n != setups {
		t.Errorf("frontends/web's lockfile is a cache key %d times in %d Node setups, want once per setup", n, setups)
	}
}
