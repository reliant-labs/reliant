package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeTypedConfig gives root a pkg/config that declares Config — what
// codegen.ProjectHasTypedConfig reads as typed config.
func writeTypedConfig(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, "pkg", "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.go"), []byte("package config\n\ntype Config struct{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestGenerateWorkerFiles_FollowTypedConfig: a worker scaffolded into a
// project with no typed config (reliant, adopting forge in place) names no
// config — the rule service.go follows — and one with typed config keeps it.
// Both kinds, both files.
func TestGenerateWorkerFiles_FollowTypedConfig(t *testing.T) {
	for _, kind := range []string{"", "cron"} {
		for _, typed := range []bool{false, true} {
			root := t.TempDir()
			if typed {
				writeTypedConfig(t, root)
			}
			if err := GenerateWorkerFiles(root, "example.com/myapp", "sweeper", kind, "*/5 * * * *"); err != nil {
				t.Fatalf("GenerateWorkerFiles(kind %q): %v", kind, err)
			}
			for _, f := range []string{"worker.go", "worker_test.go"} {
				src := readFile(t, filepath.Join(root, "internal", "workers", "sweeper", f))
				names := strings.Contains(src, `"example.com/myapp/pkg/config"`) && strings.Contains(src, "Config")
				if names != typed {
					t.Errorf("kind %q, typed config %v: %s names pkg/config = %v:\n%s", kind, typed, f, names, src)
				}
			}
		}
	}
}

// TestGenerateWorkerFiles_CompilesWithoutTypedConfig builds a scaffolded
// worker, tests included, in a module with no pkg/config.
func TestGenerateWorkerFiles_CompilesWithoutTypedConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a module (go get + go vet); -short runs the string-level assertions only")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/plainmod\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("go", "-C", root, "get", "go.opentelemetry.io/otel@v1.34.0").CombinedOutput(); err != nil {
		t.Skipf("module fetch unavailable in this environment: %v\n%s", err, out)
	}
	if err := GenerateWorkerFiles(root, "example.com/plainmod", "sweeper", "", ""); err != nil {
		t.Fatalf("GenerateWorkerFiles: %v", err)
	}
	if out, err := exec.Command("go", "-C", root, "vet", "./internal/workers/sweeper").CombinedOutput(); err != nil {
		t.Fatalf("a worker scaffolded without typed config does not compile: %v\n%s", err, out)
	}
}

// TestGenerateWebhookFiles_TestFollowsServiceConfig: the webhook test builds
// the SERVICE's Deps, so it passes a Config exactly when that Deps takes
// one, from the package the service names.
func TestGenerateWebhookFiles_TestFollowsServiceConfig(t *testing.T) {
	cases := []struct {
		name, deps, want string
	}{
		{"no config", "type Deps struct {\n\tLogger *slog.Logger\n}\n", ""},
		{"own config package", "type Deps struct {\n\tLogger *slog.Logger\n\tConfig *settings.Config\n}\n", `config "example.com/myapp/internal/settings"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			svcDir := filepath.Join(root, "internal", "handlers", "billing")
			if err := os.MkdirAll(svcDir, 0o755); err != nil {
				t.Fatal(err)
			}
			src := "package billing\n\nimport (\n\t\"log/slog\"\n\n\t\"example.com/myapp/internal/settings\"\n)\n\nvar _ settings.Config\n\n" + c.deps
			if err := os.WriteFile(filepath.Join(svcDir, "service.go"), []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := GenerateWebhookFiles(root, "example.com/myapp", "billing", "stripe"); err != nil {
				t.Fatalf("GenerateWebhookFiles: %v", err)
			}
			test := readFile(t, filepath.Join(svcDir, "webhook_stripe_test.go"))
			if strings.Contains(test, "pkg/config") {
				t.Errorf("the webhook test must not assume <module>/pkg/config:\n%s", test)
			}
			if c.want == "" {
				if strings.Contains(test, "Config:") {
					t.Errorf("a service that takes no Config must not be given one:\n%s", test)
				}
				return
			}
			if !strings.Contains(test, c.want) || !strings.Contains(test, "Config: &config.Config{}") {
				t.Errorf("the webhook test must pass the service's own config (%s):\n%s", c.want, test)
			}
		})
	}
}
