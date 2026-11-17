//go:build e2e

package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// The scaffolded e2e harness starts its service with an argv — and an
// environment — the scaffolded binary actually serves on.
//
// It used to run `<bin> server <svc>` with only PORT set. The generated CLI
// has no such form: each service is a top-level `<bin> <svc>` command and
// `server` takes no service argument (cobra: `unknown command "<svc>" for
// "<bin> server"`), and DATABASE_URL is required config the harness never
// set. Every scaffolded e2e suite therefore failed to start its service
// (houndersclub PR #8).
//
// This reads the argv and env the scaffolded harness passes, runs the real
// binary with them — pointing DATABASE_URL at a real database rather than the
// compose one — and requires /healthz. `--help` would not do: cobra answers
// it before validating positional arguments, so it accepts the broken form.
func TestE2EScaffoldE2EHarnessCommand(t *testing.T) {
	sharedTestPostgres(t)
	t.Parallel()
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "harnessapp", "--mod", "example.com/harnessapp", "--service", "api-gateway")
	projectDir := filepath.Join(dir, "harnessapp")
	addCorpusForgePkgReplace(t, projectDir)
	runCmd(t, projectDir, forgeBin, "generate")
	runCmd(t, projectDir, "go", "mod", "tidy")
	runCmd(t, filepath.Join(projectDir, "gen"), "go", "mod", "tidy")

	main, err := os.ReadFile(filepath.Join(projectDir, "e2e", "api_gateway", "main_test.go"))
	if err != nil {
		t.Fatalf("read the scaffolded harness: %v", err)
	}
	src := string(main)
	m := regexp.MustCompile(`exec\.CommandContext\(ctx, "\./bin/harnessapp"((?:, "[^"]+")*)\)`).FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("harness does not start ./bin/harnessapp:\n%s", src)
	}
	var argv []string
	for _, a := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(m[1], -1) {
		argv = append(argv, a[1])
	}
	for _, want := range []string{`"DATABASE_URL=`, `"AUTO_MIGRATE=true"`} {
		if !strings.Contains(src, want) {
			t.Fatalf("harness does not set %s for the service it starts", strings.Trim(want, `"=`))
		}
	}

	bin := filepath.Join(projectDir, "bin", "harnessapp")
	runCmd(t, projectDir, "go", "build", "-o", bin, "./cmd/harnessapp")

	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("provision postgres: %v", err)
	}
	defer cleanup()
	port := freePortE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, argv...)
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PORT=%d", port),
		"DATABASE_URL="+dsn,
		"AUTO_MIGRATE=true",
		"ENVIRONMENT=development",
	)
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start `harnessapp %s`: %v", strings.Join(argv, " "), err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	if !waitForServer(t, fmt.Sprintf("http://127.0.0.1:%d/healthz", port), 20*time.Second) {
		t.Fatalf("`harnessapp %s` — the command the scaffolded e2e harness runs — never served /healthz:\n%s",
			strings.Join(argv, " "), out.String())
	}
}
