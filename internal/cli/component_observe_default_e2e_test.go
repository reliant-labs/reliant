//go:build e2e

package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestE2ENightlyComponentObserveByDefault is the end-to-end proof that every
// contract package is instrumented by default:
//
//  1. `scaffold package --type adapter` gets a decorator.
//  2. A SECOND package added BY HAND — no `// forge:constructor`, no
//     observe_chain.go — ALSO gets one, built on observe.DefaultComponentChain,
//     and compose.go constructs it wrapped. Nothing is scaffolded into it.
//  3. `// forge:no-observe` on that package → its decorator is removed and
//     compose.go unwraps its construction, so the build stays green.
//  4. `// forge:no-observe` on a single METHOD of the instrumented package →
//     that method's wrapper delegates DIRECTLY (no chain), siblings still
//     route through it.
//  5. generate ×2 is idempotent; build + vet stay green throughout.
//
// NIGHTLY (the TestE2ENightly prefix; internal/e2eshard): it is among the
// suite's slowest single tests, and the decision, the decorator and the
// compose wrap sync are pinned piecewise by unit tests (internal/codegen
// observe_decision / compose_wrap_sync, internal/generator/contract observed).
func TestE2ENightlyComponentObserveByDefault(t *testing.T) {
	t.Parallel()
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "obsent", "--mod", "example.com/obsent", "--service", "api")
	projectDir := filepath.Join(dir, "obsent")
	addCorpusForgePkgReplace(t, projectDir)

	// ── (1) A scaffolded package gets a decorator ─────────────────────────
	runCmd(t, projectDir, forgeBin, "scaffold", "package", "checkout", "--type", "adapter")
	checkoutDir := filepath.Join(projectDir, "internal", "checkout")
	runCmd(t, projectDir, forgeBin, "generate")
	decoratorPath := filepath.Join(checkoutDir, "middleware_gen.go")
	assertPathExistsE2E(t, decoratorPath)

	// ── (2) A hand-written package with no marker and no seam ─────────────
	notifyDir := filepath.Join(projectDir, "internal", "notify")
	writeFileE2E(t, filepath.Join(notifyDir, "contract.go"), `package notify

import "context"

type Service interface {
	Ping(ctx context.Context) error
}
`)
	notifySrc := `package notify

import (
	"context"
	"log/slog"
)

type Deps struct {
	Logger *slog.Logger
}

type service struct{ deps Deps }

func New(deps Deps) (Service, error) {
	return &service{deps: deps}, nil
}

func (s *service) Ping(ctx context.Context) error { return nil }
`
	writeFileE2E(t, filepath.Join(notifyDir, "service.go"), notifySrc)

	runCmd(t, projectDir, forgeBin, "generate")
	notifyDec := readFileE2E(t, filepath.Join(notifyDir, "middleware_gen.go"))
	if !strings.Contains(notifyDec, `observe.DefaultComponentChain("example.com/obsent/internal/notify", "notify", slog.LevelDebug)`) {
		t.Fatalf("notify decorator does not build the default chain:\n%s", notifyDec)
	}
	assertPathNotExistsE2E(t, filepath.Join(notifyDir, "observe_chain.go"))
	composePath := filepath.Join(projectDir, "internal", "app", "compose.go")
	if compose := readFileE2E(t, composePath); !strings.Contains(compose, "notify.NewServiceWithForgeMiddleware(") {
		t.Fatalf("compose.go does not construct notify wrapped:\n%s", compose)
	}
	runCmd(t, projectDir, "go", "build", "./...")

	// ── (3) Opt notify OUT → decorator gone, construction unwrapped ───────
	writeFileE2E(t, filepath.Join(notifyDir, "service.go"), strings.Replace(notifySrc,
		"func New(deps Deps) (Service, error) {",
		"// forge:no-observe\nfunc New(deps Deps) (Service, error) {", 1))
	runCmd(t, projectDir, forgeBin, "generate")
	assertPathNotExistsE2E(t, filepath.Join(notifyDir, "middleware_gen.go"))
	if compose := readFileE2E(t, composePath); strings.Contains(compose, "notify.NewServiceWithForgeMiddleware(") {
		t.Fatalf("compose.go still wraps opted-out notify:\n%s", compose)
	}
	runCmd(t, projectDir, "go", "build", "./...")

	// ── (4) Method-level no-observe on the instrumented package ────────────
	contractPath := filepath.Join(checkoutDir, "contract.go")
	contractSrc := readFileE2E(t, contractPath)
	contractSrc = strings.Replace(contractSrc,
		"HealthCheck(ctx context.Context) error",
		"HealthCheck(ctx context.Context) error\n\n\t// forge:no-observe\n\tPing(ctx context.Context) error",
		1)
	writeFileE2E(t, contractPath, contractSrc)
	implPath := filepath.Join(checkoutDir, "adapter.go")
	writeFileE2E(t, implPath, readFileE2E(t, implPath)+
		"\n\n// Ping is a hot-path probe excluded from the observe chain.\nfunc (s *service) Ping(ctx context.Context) error { return nil }\n")

	runCmd(t, projectDir, forgeBin, "generate")
	dec := readFileE2E(t, decoratorPath)
	if !strings.Contains(dec, `o.chain.Run(ctx, "checkout.HealthCheck"`) {
		t.Fatalf("HealthCheck must still route through the chain:\n%s", dec)
	}
	if !strings.Contains(dec, "func (o *forgeMiddlewareService) Ping(ctx context.Context) error {\n\treturn o.inner.Ping(ctx)\n}") {
		t.Fatalf("Ping wrapper must delegate directly (no chain):\n%s", dec)
	}
	if strings.Contains(dec, `"checkout.Ping"`) {
		t.Fatalf("Ping must NOT be routed through the chain:\n%s", dec)
	}

	// ── (5) Idempotent generate + build + vet ──────────────────────────────
	runCmd(t, projectDir, forgeBin, "generate")
	if again := readFileE2E(t, decoratorPath); again != dec {
		t.Fatalf("decorator changed on a second generate (not idempotent)")
	}
	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./...")
}
