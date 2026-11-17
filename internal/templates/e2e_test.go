package templates

import (
	"strings"
	"testing"
)

func TestE2EWorkflowTemplate_DockerCompose(t *testing.T) {
	data := E2EWorkflowData{
		ProjectName:  "myapp",
		Runtime:      "docker-compose",
		HasFrontends: false,
	}
	content, err := CITemplates("github").Render("e2e.yml.tmpl", data)
	if err != nil {
		t.Fatalf("render e2e.yml.tmpl: %v", err)
	}
	s := string(content)

	for _, want := range []string{
		"name: E2E Tests",
		"task test:e2e",
		"task build",
		"task deps",
		"run-e2e",
		"workflow_dispatch",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing in output: %q", want)
		}
	}
	if strings.Contains(s, "k3d") {
		t.Error("docker-compose runtime should not mention k3d")
	}
	// The harness brings up its OWN dependencies from
	// e2e/<svc>/docker-compose.e2e.yml. Starting the root docker-compose.yml
	// — the DEV stack, whose hot-reload app container compiles for ~2 min
	// cold against a ~65s health budget — failed `up --wait` on every run
	// (houndersclub PR #8), for a stack no e2e test talks to.
	if strings.Contains(s, "docker compose -f docker-compose.yml") {
		t.Error("e2e.yml starts the root dev compose stack; the harness starts its own dependencies")
	}
}

func TestE2EWorkflowTemplate_K3d(t *testing.T) {
	data := E2EWorkflowData{
		ProjectName:  "myapp",
		Runtime:      "k3d",
		HasFrontends: true,
		FrontendPath: "frontends/web",
	}
	content, err := CITemplates("github").Render("e2e.yml.tmpl", data)
	if err != nil {
		t.Fatalf("render e2e.yml.tmpl: %v", err)
	}
	s := string(content)

	for _, want := range []string{
		"k3d cluster delete e2e",
		"forge cluster up e2e --wait",
		"forge env build e2e --push",
		"forge env deploy e2e",
		installForgeRun(8),
		"frontends/web/package.json",
		"task test:e2e",
		"run-e2e",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing in output: %q", want)
		}
	}
	// Through forge, never around it: a raw KCL render cannot resolve
	// kcl_plugin.forge and skips the declared-context binding.
	for _, bypass := range []string{"kcl run", "kubectl apply", "install-cli.sh", "k3d image import"} {
		if strings.Contains(s, bypass) {
			t.Errorf("e2e.yml bypasses forge: it contains %q", bypass)
		}
	}
	if strings.Contains(s, "docker compose") {
		t.Error("k3d runtime should not mention docker compose")
	}
}
