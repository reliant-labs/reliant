package generator

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/templates"
)

func TestProjectGeneratorGenerateCreatesMigrationFirstLayout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sample-app")
	generator := NewProjectGenerator("sample-app", root, "example.com/sample-app")

	if err := generator.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	assertPathExists(t, filepath.Join(root, "db", "migrations"))
	assertPathExists(t, filepath.Join(root, "docker-compose.yml"))
	assertCompositionRootDeferred(t, root, "sample-app")
	assertPathExists(t, filepath.Join(root, "cmd", "sample-app", "cmd", "root.go"))
	assertPathExists(t, filepath.Join(root, "cmd", "sample-app", "cmd", "version.go"))

	if _, err := os.Stat(filepath.Join(root, "migrations")); !os.IsNotExist(err) {
		t.Fatalf("expected legacy migrations/ directory to be absent, err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "proto", "forge", "options.proto")); !os.IsNotExist(err) {
		t.Fatalf("expected legacy proto/forge/options.proto to be absent, err = %v", err)
	}

	// The thin auth-policy middleware file should always be generated
	// (mechanisms live in forge/pkg/{authn,middleware}).
	assertPathExists(t, filepath.Join(root, "pkg", "middleware", "middleware.go"))

	// CORS must be wired even without a frontend. serve.go is scaffold-once
	// (written by the codegen pipeline, not this lane), so this asserts the
	// render this project is born with rather than a file on disk.
	serveSrc := renderServeFor(t, generator)
	if !strings.Contains(serveSrc, "CORSMiddleware") {
		t.Fatalf("serve.go should wire CORSMiddleware even without a frontend, got:\n%s", serveSrc)
	}
}

func TestProjectGeneratorGenerateWritesDatabaseConfigAndCompose(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sample-db")
	generator := NewProjectGenerator("sample-db", root, "example.com/sample-db")

	if err := generator.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	// The scaffolded forge.yaml is minimal — the database section is
	// derived at load time. Assert the LOADED config resolves the
	// canonical database defaults for a service project.
	cfg, err := ReadProjectConfig(filepath.Join(root, "forge.yaml"))
	if err != nil {
		t.Fatalf("ReadProjectConfig: %v", err)
	}
	if cfg.Database.MigrationsDir != "db/migrations" {
		t.Fatalf("loaded config Database.MigrationsDir = %q, want db/migrations (derived)", cfg.Database.MigrationsDir)
	}
	if cfg.Database.Driver != "postgres" {
		t.Fatalf("loaded config Database.Driver = %q, want postgres (derived)", cfg.Database.Driver)
	}

	composeContents := readFile(t, filepath.Join(root, "docker-compose.yml"))
	if strings.Contains(composeContents, "can't evaluate field") {
		t.Fatalf("docker-compose template rendered invalid output:\n%s", composeContents)
	}
}

func TestProjectGeneratorGenerateWritesScaffoldThatBuildsCleanlyByDefault(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sample-full")
	generator := NewProjectGenerator("sample-full", root, "example.com/sample-full")
	generator.ServiceName = "api"
	generator.FrontendName = "web"

	if err := generator.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	// The scaffolded forge.yaml names no frontend, no features and no database:
	// all three derive from the tree just written.
	configContents := readFile(t, filepath.Join(root, "forge.yaml"))
	for _, removed := range []string{"frontends:", "features:", "stack:", "frontend:", "driver:", "migrations_dir:"} {
		if strings.Contains(configContents, "\n"+removed) {
			t.Fatalf("scaffolded forge.yaml must not emit the removed key %q:\n%s", removed, configContents)
		}
	}
	loaded, err := ReadProjectConfig(filepath.Join(root, "forge.yaml"))
	if err != nil {
		t.Fatalf("scaffolded forge.yaml must load: %v", err)
	}
	if len(loaded.Frontends) != 1 || loaded.Frontends[0].Name != "web" || loaded.Frontends[0].Type != "nextjs" {
		t.Fatalf("the scaffolded frontend must be derived from frontends/web as nextjs, got %+v", loaded.Frontends)
	}
	if !loaded.Features.FrontendEnabled() {
		t.Fatal("the frontend feature must derive on from the scaffolded frontend")
	}

	serviceContents := readFile(t, filepath.Join(root, "internal", "handlers", "api", "service.go"))
	if !strings.Contains(serviceContents, "gen/services/api/v1/apiv1connect") {
		t.Fatalf("expected service template to import the generated Connect package path, got:\n%s", serviceContents)
	}
	if !strings.Contains(serviceContents, "UnimplementedAPIServiceHandler") {
		t.Fatalf("expected service template to embed unimplemented handler, got:\n%s", serviceContents)
	}

	// New bootstrapping assertions: no init(), has Deps, has New(deps)
	if strings.Contains(serviceContents, "func init()") {
		t.Fatalf("service.go should not contain init() function, got:\n%s", serviceContents)
	}
	if !strings.Contains(serviceContents, "type Deps struct") {
		t.Fatalf("service.go should contain Deps struct, got:\n%s", serviceContents)
	}
	// New is now always fallible — validateDeps runs at construction so
	// per-RPC `if s.deps.X == nil` guards are no longer needed.
	if !strings.Contains(serviceContents, "func New(deps Deps) (*Service, error)") {
		t.Fatalf("service.go should have New(deps Deps) (*Service, error), got:\n%s", serviceContents)
	}

	// The old name-matched pkg/app DI unit (bootstrap.go / wire_gen.go /
	// services_gen.go / services.go) is retired, AND the dead substrate that
	// only existed to make a user-owned setup.go compile (app_gen.go /
	// app_extras.go / setup.go / post_bootstrap.go) is no longer scaffolded.
	// The LIVE DI (internal/app: OpenInfra → NewComponents) is emitted by the
	// post-scaffold generate. At scaffold time pkg/app carries only the test
	// harness (testing.go) + CONVENTIONS.md.
	assertPathNotExists(t, filepath.Join(root, "pkg", "app", "bootstrap.go"))
	assertPathNotExists(t, filepath.Join(root, "pkg", "app", "wire_gen.go"))
	assertPathNotExists(t, filepath.Join(root, "pkg", "app", "services_gen.go"))
	assertPathNotExists(t, filepath.Join(root, "pkg", "app", "services.go"))
	assertPathNotExists(t, filepath.Join(root, "pkg", "app", "app_gen.go"))
	assertPathNotExists(t, filepath.Join(root, "pkg", "app", "app_extras.go"))
	assertPathNotExists(t, filepath.Join(root, "pkg", "app", "setup.go"))
	assertPathNotExists(t, filepath.Join(root, "pkg", "app", "post_bootstrap.go"))
	// The composition root and the command-group subpackages it imports are
	// written together by the generate pipeline's cmd-group step, not here.
	assertCompositionRootDeferred(t, root, "sample-full")

	// The serve pipeline this project is born with (§2 hybrid DI): it opens
	// the owned infra and constructs the EXPLICIT component graph via
	// app.NewComponents — there is NO by-type injector, and the old appkit
	// Bootstrap / app.Build path is retired. Asserted over the parsed CALL
	// set rather than substrings, so a step that merely moved packages does
	// not read as a step that disappeared.
	serveSrc := renderServeFor(t, generator)
	serveCalls := serveCallees(t, serveSrc)
	if serveCalls["Build"] {
		t.Fatalf("serve.go calls the retired by-type injector app.Build(), got:\n%s", serveSrc)
	}
	for _, want := range []string{"NewComponents", "OpenInfra", "Run"} {
		if !serveCalls[want] {
			t.Fatalf("serve.go must call %s(...) — explicit composition handing off to serverkit; got:\n%s", want, serveSrc)
		}
	}

	// The user-owned cmd/sample-full/cmd/commands.go extension point that
	// newRootCmd consumes must be scaffolded.
	commandsContents := readFile(t, filepath.Join(root, "cmd", "sample-full", "cmd", "commands.go"))
	if !strings.Contains(commandsContents, "func userCommands(deps Deps) []*cobra.Command {") {
		t.Fatalf("cmd/sample-full/cmd/commands.go should scaffold the userCommands extension point, got:\n%s", commandsContents)
	}
	rootContents := readFile(t, filepath.Join(root, "cmd", "sample-full", "cmd", "root.go"))
	if !strings.Contains(rootContents, "userCommands(deps)") {
		t.Fatalf("cmd/sample-full/cmd/root.go should consume userCommands(deps), got:\n%s", rootContents)
	}
	// A7: Server should wire the CORS middleware factory when frontend exists.
	// Serverkit drives the actual wrap based on Config.CORSOrigins.
	if !strings.Contains(serveSrc, "CORSMiddleware") {
		t.Fatalf("serve.go should wire middleware.CORSMiddleware into serverkit.Server.CORSMiddleware when a frontend exists, got:\n%s", serveSrc)
	}

	// services/all should NOT exist
	if _, err := os.Stat(filepath.Join(root, "internal", "handlers", "all")); !os.IsNotExist(err) {
		t.Fatalf("expected handlers/all/ directory to not exist")
	}
	// pkg/registry should NOT exist
	if _, err := os.Stat(filepath.Join(root, "pkg", "registry")); !os.IsNotExist(err) {
		t.Fatalf("expected pkg/registry/ directory to not exist")
	}

	// handlers.go is intentionally not emitted at scaffold (zero RPC methods
	// in the generated proto stub). The service package is provided by
	// service.go until a real RPC is added and forge generate produces
	// handlers_gen.go.
	if _, err := os.Stat(filepath.Join(root, "internal", "handlers", "api", "handlers.go")); !os.IsNotExist(err) {
		t.Fatalf("expected handlers/api/handlers.go to not exist at scaffold, got err=%v", err)
	}
	if !strings.HasPrefix(serviceContents, "package api") {
		t.Fatalf("expected service.go package to match generated service package, got:\n%s", serviceContents)
	}

	helpersContents := readFile(t, filepath.Join(root, "e2e", "api", "helpers_test.go"))
	if !strings.Contains(helpersContents, "package api_test") {
		t.Fatalf("expected e2e helpers to use api_test package, got:\n%s", helpersContents)
	}
	if !strings.Contains(helpersContents, "//go:build e2e") {
		t.Fatalf("expected e2e helpers to have //go:build e2e tag, got:\n%s", helpersContents)
	}
	if strings.Contains(helpersContents, "UnknownRequest") {
		t.Fatalf("expected e2e helpers to avoid placeholder request types, got:\n%s", helpersContents)
	}

	serviceTestContents := readFile(t, filepath.Join(root, "e2e", "api", "service_test.go"))
	if !strings.Contains(serviceTestContents, "TestHealthCheck") {
		t.Fatalf("expected e2e template to generate a health check test, got:\n%s", serviceTestContents)
	}

	feBufGenContents := readFile(t, filepath.Join(root, "frontends", "web", "buf.gen.yaml"))
	// The frontend buf.gen.yaml is invoked from the project root via
	// `buf generate --template frontends/<name>/buf.gen.yaml --path proto/services`
	// (see runBufGenerateTypeScript in internal/cli/generate.go). That means
	// the template supplies `out:` relative to the project root and must NOT
	// define an `inputs:` section (inputs come from --path). The old
	// expectation of `directory: ../../proto` belonged to an earlier design
	// where buf ran from the frontend dir. Enforce the current contract.
	if !strings.Contains(feBufGenContents, "out: frontends/web/src/gen") {
		t.Fatalf("expected frontend buf.gen.yaml to emit project-root-relative out:, got:\n%s", feBufGenContents)
	}
	if strings.Contains(feBufGenContents, "inputs:") {
		t.Fatalf("frontend buf.gen.yaml must not declare inputs: (buf runs with --path), got:\n%s", feBufGenContents)
	}

	fePackageContents := readFile(t, filepath.Join(root, "frontends", "web", "package.json"))
	if !strings.Contains(fePackageContents, `"build": "NODE_ENV=production next build --webpack"`) {
		t.Fatalf("expected frontend package.json build script to force production NODE_ENV, got:\n%s", fePackageContents)
	}
	// --webpack is load-bearing on Next.js 16, not a leftover. Turbopack is
	// the default bundler there and REFUSES to run against a config carrying
	// a `webpack` key ("This build is using Turbopack, with a `webpack`
	// config and no `turbopack` config", exit 1). next.config.ts carries one
	// in all three output branches, for an extensionAlias mapping and a
	// per-module resolve override that Turbopack cannot express — so every
	// script that boots Next has to pick the bundler explicitly.
	for _, script := range []string{
		`"dev": "next dev --webpack"`,
		`"analyze": "ANALYZE=true next build --webpack"`,
	} {
		if !strings.Contains(fePackageContents, script) {
			t.Fatalf("frontend package.json must pin the bundler with --webpack (%s) — Next.js 16 defaults to Turbopack and hard-fails on next.config.ts's webpack key; got:\n%s", script, fePackageContents)
		}
	}

	// A6/A7: the project keeps ONE thin middleware file wiring auth
	// policy; logging/recovery/CORS mechanisms come from the forge
	// libraries (pkg/observe, pkg/middleware) and are not photocopied
	// into the scaffold anymore.
	thinMiddleware := readFile(t, filepath.Join(root, "pkg", "middleware", "middleware.go"))
	if !strings.Contains(thinMiddleware, "forge/pkg/authn") {
		t.Fatalf("middleware/middleware.go should delegate to forge/pkg/authn, got:\n%s", thinMiddleware)
	}
	// It is RENDERED: it names the project's native sign-in cookie, which the
	// interceptor reads and internal/app/login_broker.go sets. An unrendered
	// copy compiles — the template action sits inside a string — and then
	// reads a cookie no browser ever has.
	if strings.Contains(thinMiddleware, "{{") {
		t.Fatalf("middleware/middleware.go was written unrendered:\n%s", thinMiddleware)
	}
	if want := `const SessionCookieName = "` + filepath.Base(root) + `_session"`; !strings.Contains(thinMiddleware, want) {
		t.Fatalf("middleware/middleware.go must declare %s, got:\n%s", want, thinMiddleware)
	}
	for _, retired := range []string{"logging.go", "recovery.go", "cors.go"} {
		if _, err := os.Stat(filepath.Join(root, "pkg", "middleware", retired)); !os.IsNotExist(err) {
			t.Fatalf("pkg/middleware/%s should no longer be scaffolded (library-fied), err=%v", retired, err)
		}
	}

	// M1: db/migrations/.gitkeep should exist
	assertPathExists(t, filepath.Join(root, "db", "migrations", ".gitkeep"))

	rootGoModContents := readFile(t, filepath.Join(root, "go.mod"))
	if !strings.Contains(rootGoModContents, "require (") || !strings.Contains(rootGoModContents, "example.com/sample-full/gen v0.0.0") {
		t.Fatalf("expected root go.mod to require the local gen module, got:\n%s", rootGoModContents)
	}
	if !strings.Contains(rootGoModContents, "replace example.com/sample-full/gen => ./gen") {
		t.Fatalf("expected root go.mod to replace the local gen module path, got:\n%s", rootGoModContents)
	}
}

func TestProjectGeneratorGenerateZeroServiceCLIOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds and generates a full project; runs in task test")
	}
	root := filepath.Join(t.TempDir(), "cli-only")
	gen := NewProjectGenerator("cli-only", root, "example.com/cli-only")
	// No ServiceName set — zero-service CLI-only project

	if err := gen.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	// Core files must exist
	assertCompositionRootDeferred(t, root, "cli-only")
	assertPathExists(t, filepath.Join(root, "cmd", "cli-only", "cmd", "root.go"))
	assertPathExists(t, filepath.Join(root, "cmd", "cli-only", "cmd", "version.go"))
	// The per-component test harness is emitted PER SERVICE, beside the
	// service. A zero-service project has no component to emit one for —
	// and there is no longer a project-wide pkg/app/testing.go standing in
	// for it (that file put package `testing` in the production binary).
	assertPathNotExists(t, filepath.Join(root, "pkg", "app", "testing.go"))
	assertPathExists(t, filepath.Join(root, "pkg", "middleware", "middleware.go"))
	assertPathExists(t, filepath.Join(root, "forge.yaml"))
	// The retired name-matched DI unit AND the dead pkg/app substrate must
	// NOT be scaffolded. The LIVE DI is internal/app (OpenInfra → NewComponents).
	assertPathNotExists(t, filepath.Join(root, "pkg", "app", "bootstrap.go"))
	assertPathNotExists(t, filepath.Join(root, "pkg", "app", "wire_gen.go"))
	assertPathNotExists(t, filepath.Join(root, "pkg", "app", "app_gen.go"))
	assertPathNotExists(t, filepath.Join(root, "pkg", "app", "setup.go"))
	assertPathNotExists(t, filepath.Join(root, "pkg", "app", "post_bootstrap.go"))

	// Service-specific directories should NOT exist
	if _, err := os.Stat(filepath.Join(root, "internal", "handlers", "api")); !os.IsNotExist(err) {
		t.Fatal("expected no service handler directory, but it exists")
	}

	// The cobra tree lives in cmd/cli-only/cmd; the composition root that names
	// its group constructors is written by the generate pipeline.
	rootContents := readFile(t, filepath.Join(root, "cmd", "cli-only", "cmd", "root.go"))
	if !strings.Contains(rootContents, "newRootCmd") {
		t.Fatal("cmd/cli-only/cmd/root.go missing newRootCmd")
	}

	// The inventory + kind derive from the project's real sources; nothing on
	// disk declares either. A zero-service SERVICE shell still reads as
	// "service" on reload because it carries the KCL deploy tree and the
	// pkg/app composition root.
	assertPathExists(t, filepath.Join(root, "deploy", "kcl"))
	configContents := readFile(t, filepath.Join(root, "forge.yaml"))
	if strings.Contains(configContents, "components:") {
		t.Fatalf("forge.yaml must be global-only (no components:), got:\n%s", configContents)
	}
	loaded, err := ReadProjectConfig(filepath.Join(root, "forge.yaml"))
	if err != nil {
		t.Fatalf("ReadProjectConfig: %v", err)
	}
	if loaded.EffectiveKind() != config.ProjectKindService {
		t.Errorf("zero-service shell EffectiveKind() = %q, want service", loaded.EffectiveKind())
	}
}

// TestProjectGeneratorKindCLIScaffold verifies that --kind cli produces a
// CLI-shaped project: cmd/<bin>/main.go exists, server-shaped scaffolding
// is suppressed, and forge.yaml records the kind.
func TestProjectGeneratorKindCLIScaffold(t *testing.T) {
	root := filepath.Join(t.TempDir(), "mycli")
	gen := NewProjectGenerator("mycli", root, "example.com/mycli")
	gen.Kind = "cli"

	if err := gen.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	// CLI-shaped files MUST exist.
	mustExist := []string{
		filepath.Join(root, "cmd", "mycli", "main.go"),
		filepath.Join(root, "cmd", "mycli", "version.go"),
		filepath.Join(root, "go.mod"),
		filepath.Join(root, "Taskfile.yml"),
		filepath.Join(root, "forge.yaml"),
		// NOT config_gen.go: a CLI project runs no codegen, so this is the
		// hand-owned Config{} stub generateCLIConfigStub writes, and it keeps
		// the plain name precisely because nothing regenerates it.
		filepath.Join(root, "pkg", "config", "config.go"),
	}
	for _, p := range mustExist {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}

	// Server-shaped files MUST NOT exist.
	mustNotExist := []string{
		filepath.Join(root, "internal", "cli", "serve.go"),
		filepath.Join(root, "internal", "cli", "server.go"),
		filepath.Join(root, "cmd", "main.go"), // service-shaped main.go at cmd/ root
		filepath.Join(root, "pkg", "middleware"),
		filepath.Join(root, "pkg", "app", "bootstrap.go"),
		filepath.Join(root, "internal", "handlers"),
		filepath.Join(root, "deploy"),
		filepath.Join(root, "Dockerfile"),
		filepath.Join(root, "docker-compose.yml"),
		filepath.Join(root, "proto", "services"),
		filepath.Join(root, "benchmarks", "k6"),
		filepath.Join(root, "e2e"),
	}
	for _, p := range mustNotExist {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected %s to be absent (kind=cli), err=%v", p, err)
		}
	}

	// kind is not a forge.yaml field — it derives from real sources. A cli
	// project reads as "cli" because it carries a cmd/<name>/main.go binary
	// and NONE of the service sources (no deploy/kcl, pkg/app,
	// internal/handlers, proto/services), all asserted absent above.
	cfg := readFile(t, filepath.Join(root, "forge.yaml"))
	if strings.Contains(cfg, "kind:") {
		t.Errorf("forge.yaml must not carry kind: (derives from real sources), got:\n%s", cfg)
	}
	loaded, err := ReadProjectConfig(filepath.Join(root, "forge.yaml"))
	if err != nil {
		t.Fatalf("ReadProjectConfig: %v", err)
	}
	if loaded.EffectiveKind() != config.ProjectKindCLI {
		t.Errorf("cli scaffold EffectiveKind() = %q, want cli", loaded.EffectiveKind())
	}

	// go.mod is the lean CLI variant: only cobra in `require` block.
	gomod := readFile(t, filepath.Join(root, "go.mod"))
	if !strings.Contains(gomod, "github.com/spf13/cobra") {
		t.Errorf("expected go.mod to require cobra, got:\n%s", gomod)
	}
	if strings.Contains(gomod, "connectrpc.com/connect") {
		t.Errorf("CLI go.mod should not reference connect-rpc, got:\n%s", gomod)
	}

	// main.go has Cobra plumbing.
	mainGo := readFile(t, filepath.Join(root, "cmd", "mycli", "main.go"))
	if !strings.Contains(mainGo, "rootCmd") {
		t.Errorf("expected cmd/mycli/main.go to define rootCmd, got:\n%s", mainGo)
	}
}

// TestProjectGeneratorKindLibraryScaffold verifies that --kind library
// produces a library-shaped project: a doc.go in pkg/<name>/, no cmd/,
// no server scaffolding, and forge.yaml records the kind.
func TestProjectGeneratorKindLibraryScaffold(t *testing.T) {
	root := filepath.Join(t.TempDir(), "mylib")
	gen := NewProjectGenerator("mylib", root, "example.com/mylib")
	gen.Kind = "library"

	if err := gen.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	mustExist := []string{
		filepath.Join(root, "go.mod"),
		filepath.Join(root, "Taskfile.yml"),
		filepath.Join(root, "forge.yaml"),
		filepath.Join(root, "pkg", "mylib", "doc.go"),
	}
	for _, p := range mustExist {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}

	mustNotExist := []string{
		filepath.Join(root, "cmd"),
		filepath.Join(root, "pkg", "middleware"),
		filepath.Join(root, "pkg", "app"),
		filepath.Join(root, "internal", "handlers"),
		filepath.Join(root, "deploy"),
		filepath.Join(root, "Dockerfile"),
		filepath.Join(root, "docker-compose.yml"),
		filepath.Join(root, "proto", "services"),
		filepath.Join(root, ".github", "workflows", "ci.yml"), // CI off-by-default for libs
	}
	for _, p := range mustNotExist {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected %s to be absent (kind=library), err=%v", p, err)
		}
	}

	// kind is not a forge.yaml field — it derives from real sources. A pure
	// module carries no service source and no cmd/<name>/main.go entrypoint
	// (both asserted absent above), and THAT is the "library" signal on
	// reload.
	cfg := readFile(t, filepath.Join(root, "forge.yaml"))
	if strings.Contains(cfg, "kind:") {
		t.Errorf("forge.yaml must not carry kind: (derives from real sources), got:\n%s", cfg)
	}
	loaded, err := ReadProjectConfig(filepath.Join(root, "forge.yaml"))
	if err != nil {
		t.Fatalf("ReadProjectConfig: %v", err)
	}
	if loaded.EffectiveKind() != config.ProjectKindLibrary {
		t.Errorf("library scaffold EffectiveKind() = %q, want library", loaded.EffectiveKind())
	}
}

// TestProjectGeneratorKindServiceDefault verifies that the default (no Kind
// set) preserves all current behavior — the regression guard for back-compat.
func TestProjectGeneratorKindServiceDefault(t *testing.T) {
	root := filepath.Join(t.TempDir(), "mysvc")
	gen := NewProjectGenerator("mysvc", root, "example.com/mysvc")
	// No Kind set — should default to "service"

	if err := gen.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	// Service-shaped scaffolding must still be present. (cmd/mysvc/main.go is
	// not in the list — see assertCompositionRootDeferred below.)
	mustExist := []string{
		filepath.Join(root, "cmd", "mysvc", "cmd", "version.go"),
		filepath.Join(root, "pkg", "middleware", "middleware.go"),
		filepath.Join(root, "Dockerfile"),
		filepath.Join(root, "docker-compose.yml"),
	}
	for _, p := range mustExist {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("service mode regression: expected %s, err=%v", p, err)
		}
	}
	assertCompositionRootDeferred(t, root, "mysvc")
	// A new project composes with the graph: there is no server.go — `server`
	// is the default root's command, generated into roots_gen.go by the
	// codegen pipeline (composition graph PR 4).
	assertPathNotExists(t, filepath.Join(root, "cmd", "mysvc", "cmd", "server.go"))

	// forge.yaml omits kind: for the default service kind —
	// EffectiveKind() on the loaded config must resolve it.
	loaded, err := ReadProjectConfig(filepath.Join(root, "forge.yaml"))
	if err != nil {
		t.Fatalf("ReadProjectConfig: %v", err)
	}
	if loaded.EffectiveKind() != config.ProjectKindService {
		t.Errorf("EffectiveKind() = %q, want service", loaded.EffectiveKind())
	}
}

func TestParseGoVersion(t *testing.T) {
	tests := []struct {
		input     string
		wantMajor int
		wantMinor int
		wantPatch int
		wantOK    bool
	}{
		{"1.25.3", 1, 25, 3, true},
		{"1.25", 1, 25, 0, true},
		{"1.25.0", 1, 25, 0, true},
		{"2.0.1", 2, 0, 1, true},
		{"1", 0, 0, 0, false},
		{"", 0, 0, 0, false},
		{"abc.def", 0, 0, 0, false},
		{"1.abc", 0, 0, 0, false},
		{"1.25.abc", 0, 0, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			major, minor, patch, ok := parseGoVersion(tt.input)
			if ok != tt.wantOK {
				t.Fatalf("parseGoVersion(%q) ok = %v, want %v", tt.input, ok, tt.wantOK)
			}
			if ok && (major != tt.wantMajor || minor != tt.wantMinor || patch != tt.wantPatch) {
				t.Errorf("parseGoVersion(%q) = (%d, %d, %d), want (%d, %d, %d)",
					tt.input, major, minor, patch, tt.wantMajor, tt.wantMinor, tt.wantPatch)
			}
		})
	}
}

func TestGoVersionMinor(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"1.25.3", "1.25"},
		{"1.25.0", "1.25"},
		{"1.25", "1.25"},
		{"1", "1"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := goVersionMinor(tt.input)
			if got != tt.want {
				t.Errorf("goVersionMinor(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestResolveGoVersionOverride(t *testing.T) {
	// Override at or above the forge minimum is honored as-is.
	hi := &ProjectGenerator{GoVersionOverride: "1.99"}
	if got := hi.resolveGoVersion(); got != "1.99.0" {
		t.Errorf("resolveGoVersion() with override '1.99' = %q, want '1.99.0'", got)
	}

	// Overrides below the forge minimum are clamped up. forge's pkg/orm
	// requires defaultGoVersion, so writing a lower value into go.work /
	// go.mod produces a project that doesn't build.
	low := &ProjectGenerator{GoVersionOverride: "1.22.5"}
	if got := low.resolveGoVersion(); got != defaultGoVersion {
		t.Errorf("resolveGoVersion() with override '1.22.5' = %q, want %q (clamped to minimum)", got, defaultGoVersion)
	}

	// Invalid override falls back to detected (then clamped if needed).
	bad := &ProjectGenerator{GoVersionOverride: "garbage"}
	got := bad.resolveGoVersion()
	if _, _, _, ok := parseGoVersion(got); !ok {
		t.Errorf("resolveGoVersion() with invalid override returned unparseable version %q", got)
	}
	if compareGoVersion(got, defaultGoVersion) < 0 {
		t.Errorf("resolveGoVersion() with invalid override returned %q, which is below minimum %q", got, defaultGoVersion)
	}
}

func TestCompareGoVersion(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.26.2", "1.26.2", 0},
		{"1.22.0", "1.26.2", -1},
		{"1.27.0", "1.26.2", 1},
		{"1.26.1", "1.26.2", -1},
		{"1.26.3", "1.26.2", 1},
		{"2.0.0", "1.99.99", 1},
	}
	for _, tt := range tests {
		if got := compareGoVersion(tt.a, tt.b); got != tt.want {
			t.Errorf("compareGoVersion(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func assertPathExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected path %s to exist: %v", path, err)
	}
}

func assertPathNotExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected path %s to NOT exist, but err = %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	return string(contents)
}

// renderServeFor renders the serve.go this generator's project will be born
// with, from the SAME ConfigFields the generator derives for it.
//
// serve.go is scaffold-once and written by the codegen pipeline, not by this
// lane, so there is no file on disk here to read. What is still testable —
// and is what these assertions were ever really about — is the render: which
// ConfigFields-gated blocks a project with these features gets. Deriving the
// fields from the generator rather than restating them keeps the assertion
// pinned to the producer; a feature flag that stops stripping a field shows
// up here instead of silently passing.
func renderServeFor(t *testing.T, g *ProjectGenerator) string {
	t.Helper()
	fields := g.forScaffold().ConfigFields
	if len(fields) == 0 {
		t.Fatal("generator produced an EMPTY ConfigFields set — every ConfigFields-gated " +
			"assertion below would vacuously pass against a serve.go with no gated blocks at all")
	}
	out, err := templates.ProjectTemplates().Render("cmd-tree-serve.go.tmpl", struct {
		Module       string
		ConfigFields map[string]bool
		RESTEnabled  bool
	}{Module: g.ModulePath, ConfigFields: fields})
	if err != nil {
		t.Fatalf("render cmd-tree-serve.go.tmpl: %v", err)
	}
	return string(out)
}

// assertCompositionRootDeferred pins the composition-root contract for a
// codegen service scaffold: Generate() must NOT write cmd/<bin>/main.go, nor
// the command-group subpackages it imports.
//
// main.go NAMES every group constructor (services.New<Svc>Cmd), so it can only
// be written by the pass that knows the service inventory — the generate
// pipeline's cmd-group step (codegen.GenerateCmdGroups), which
// `forge project new` runs immediately afterwards via bootstrapGeneratedCode.
// And because main.go is write-if-absent OWNED code, a bare cmd.Execute()
// written HERE would be the FINAL content: every generated per-service
// subcommand would stay unreferenced and `<bin> <service>` would not exist.
// That was a real regression (TestE2ECmdAsCodeSubcommands), so it is pinned
// rather than left implicit.
func assertCompositionRootDeferred(t *testing.T, root, bin string) {
	t.Helper()
	assertPathNotExists(t, filepath.Join(root, "cmd", bin, "main.go"))
	// serve.go is deferred for the same reason main.go is, and it matters
	// MORE: it is scaffold-once, so whichever lane writes it first owns it
	// forever. This lane only has the DEFAULT config field set, while the
	// codegen pipeline has the project's real one parsed from proto/config.
	// A copy born here would freeze ConfigFields-gated blocks that disagree
	// with the project's actual config, with nothing left to correct it.
	assertPathNotExists(t, filepath.Join(root, "cmd", bin, "cmd", "serve.go"))
	// The services anchor is Tier-1 (register_gen.go); the workers/operators
	// anchors project nothing and are scaffold-once (register.go).
	assertPathNotExists(t, filepath.Join(root, "cmd", bin, "cmd", "services", "register_gen.go"))
	for _, group := range []string{"workers", "operators"} {
		assertPathNotExists(t, filepath.Join(root, "cmd", bin, "cmd", group, "register.go"))
	}
}

func TestProjectGeneratorDoesNotWriteSkillsToDisk(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills-app")
	generator := NewProjectGenerator("skills-app", root, "example.com/skills-app")

	if err := generator.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	// Skills are served via `forge skill load`, not written to disk.
	skillsDir := filepath.Join(root, ".reliant", "skills")
	if _, err := os.Stat(skillsDir); err == nil {
		t.Fatalf("expected .reliant/skills/ to NOT exist (skills now served via CLI), but it does")
	}

	// Skills should still be accessible via the embedded templates.
	skillFiles, err := templates.ProjectTemplates().List("skills")
	if err != nil {
		t.Fatalf("expected embedded skill templates to be accessible: %v", err)
	}
	if len(skillFiles) < 20 {
		t.Fatalf("expected at least 20 embedded skill files, got %d", len(skillFiles))
	}
}

func TestProjectGeneratorWritesReliantMemoryFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory-app")
	generator := NewProjectGenerator("memory-app", root, "example.com/memory-app")

	if err := generator.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	// Top-level reliant.md must NOT be written for the reliant harness:
	// reliant loads the framework content in-memory via
	// forgecli.RenderProjectMemory whenever it sees forge.yaml, so a
	// stale on-disk copy would just create upgrade drift.
	stubPath := filepath.Join(root, "reliant.md")
	if _, err := os.Stat(stubPath); err == nil {
		t.Fatalf("top-level reliant.md should NOT exist for --harness=reliant (the framework content is injected in-memory by reliant); found one at %s", stubPath)
	}

	// .reliant/reliant.md is no longer generated (nothing reads it).
	if _, err := os.Stat(filepath.Join(root, ".reliant", "reliant.md")); err == nil {
		t.Fatal(".reliant/reliant.md should not be generated")
	}

	// reliant-forge.md must NOT exist (conventions now served via CLI).
	forgeConventions := filepath.Join(root, ".reliant", "reliant-forge.md")
	if _, err := os.Stat(forgeConventions); err == nil {
		t.Fatalf("expected .reliant/reliant-forge.md to NOT exist, but it does")
	}
}

func TestProjectGeneratorPreservesExistingReliantMemoryFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "existing-memory-app")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll error = %v", err)
	}

	preExisting := "# my own memory file\n\nhand written by the user, do not touch.\n"
	memoryPath := filepath.Join(root, "reliant.md")
	if err := os.WriteFile(memoryPath, []byte(preExisting), 0o644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}

	generator := NewProjectGenerator("existing-memory-app", root, "example.com/existing-memory-app")
	if err := generator.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	// The user-owned reliant.md must be preserved verbatim.
	after := readFile(t, memoryPath)
	if after != preExisting {
		t.Fatalf("expected existing reliant.md to be preserved verbatim, got:\n%s", after)
	}

	// The forge-owned project.json must still be written.
	assertPathExists(t, filepath.Join(root, ".reliant", "project.json"))
}

func TestProjectGeneratorHarnessClaude(t *testing.T) {
	root := filepath.Join(t.TempDir(), "claude-app")
	gen := NewProjectGenerator("claude-app", root, "example.com/claude-app")
	gen.Harness = HarnessClaude

	if err := gen.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	// CLAUDE.md should exist.
	claudePath := filepath.Join(root, "CLAUDE.md")
	assertPathExists(t, claudePath)
	content := readFile(t, claudePath)
	if !strings.Contains(content, "# claude-app") {
		t.Fatalf("expected CLAUDE.md to contain project name heading, got:\n%s", content)
	}

	// reliant.md should NOT exist at the top level.
	if _, err := os.Stat(filepath.Join(root, "reliant.md")); err == nil {
		t.Fatal("reliant.md should not exist when --harness=claude")
	}
}

func TestProjectGeneratorHarnessCursor(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cursor-app")
	gen := NewProjectGenerator("cursor-app", root, "example.com/cursor-app")
	gen.Harness = HarnessCursor

	if err := gen.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	assertPathExists(t, filepath.Join(root, ".cursorrules"))
	content := readFile(t, filepath.Join(root, ".cursorrules"))
	if !strings.Contains(content, "# cursor-app") {
		t.Fatalf("expected .cursorrules to contain project name heading, got:\n%s", content)
	}

	if _, err := os.Stat(filepath.Join(root, "reliant.md")); err == nil {
		t.Fatal("reliant.md should not exist when --harness=cursor")
	}
}

func TestProjectGeneratorHarnessCopilot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "copilot-app")
	gen := NewProjectGenerator("copilot-app", root, "example.com/copilot-app")
	gen.Harness = HarnessCopilot

	if err := gen.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	copilotPath := filepath.Join(root, ".github", "copilot-instructions.md")
	assertPathExists(t, copilotPath)
	content := readFile(t, copilotPath)
	if !strings.Contains(content, "# copilot-app") {
		t.Fatalf("expected copilot-instructions.md to contain project name heading, got:\n%s", content)
	}

	if _, err := os.Stat(filepath.Join(root, "reliant.md")); err == nil {
		t.Fatal("reliant.md should not exist when --harness=copilot")
	}
}

func TestProjectGeneratorHarnessCodex(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds and generates a full project; runs in task test")
	}
	root := filepath.Join(t.TempDir(), "codex-app")
	gen := NewProjectGenerator("codex-app", root, "example.com/codex-app")
	gen.Harness = HarnessCodex

	if err := gen.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	assertPathExists(t, filepath.Join(root, "AGENTS.md"))
	content := readFile(t, filepath.Join(root, "AGENTS.md"))
	if !strings.Contains(content, "# codex-app") {
		t.Fatalf("expected AGENTS.md to contain project name heading, got:\n%s", content)
	}

	if _, err := os.Stat(filepath.Join(root, "reliant.md")); err == nil {
		t.Fatal("reliant.md should not exist when --harness=codex")
	}
}

func TestProjectGeneratorPreservesExistingMemoryFileNonReliant(t *testing.T) {
	root := filepath.Join(t.TempDir(), "existing-claude-app")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll error = %v", err)
	}

	preExisting := "# my claude instructions\n\ndo not touch.\n"
	claudePath := filepath.Join(root, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(preExisting), 0o644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}

	gen := NewProjectGenerator("existing-claude-app", root, "example.com/existing-claude-app")
	gen.Harness = HarnessClaude
	if err := gen.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	after := readFile(t, claudePath)
	if after != preExisting {
		t.Fatalf("expected existing CLAUDE.md to be preserved verbatim, got:\n%s", after)
	}

	assertPathExists(t, filepath.Join(root, ".reliant", "project.json"))
}

func TestProjectGeneratorWritesMCPConfig(t *testing.T) {
	root := filepath.Join(t.TempDir(), "mcp-app")
	generator := NewProjectGenerator("mcp-app", root, "example.com/mcp-app")

	if err := generator.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	mcpPath := filepath.Join(root, ".mcp.json")
	assertPathExists(t, mcpPath)

	contents := readFile(t, mcpPath)
	if !strings.Contains(contents, "chrome-devtools") {
		t.Fatalf("expected .mcp.json to include chrome-devtools MCP, got:\n%s", contents)
	}
	if !strings.Contains(contents, "reliant-docs") {
		t.Fatalf("expected .mcp.json to include reliant-docs MCP, got:\n%s", contents)
	}

	// The file must parse as clean JSON.
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(contents), &parsed); err != nil {
		t.Fatalf(".mcp.json is not valid JSON: %v\n%s", err, contents)
	}

	// Top-level must have only mcpServers. No invented $-prefixed keys like
	// $comment or $disabled (those are not honored by any MCP client).
	for key := range parsed {
		if strings.HasPrefix(key, "$") {
			t.Fatalf(".mcp.json must not contain $-prefixed top-level keys, found %q", key)
		}
	}
	servers, ok := parsed["mcpServers"].(map[string]interface{})
	if !ok {
		t.Fatalf(".mcp.json must have an mcpServers object, got: %v", parsed["mcpServers"])
	}
	if len(servers) == 0 {
		t.Fatalf(".mcp.json mcpServers object must not be empty")
	}
	for name, raw := range servers {
		if strings.HasPrefix(name, "$") {
			t.Fatalf(".mcp.json must not contain $-prefixed server names, found %q", name)
		}
		entry, ok := raw.(map[string]interface{})
		if !ok {
			t.Fatalf(".mcp.json server %q must be an object, got: %v", name, raw)
		}
		if _, ok := entry["command"].(string); !ok {
			t.Fatalf(".mcp.json server %q must have a string command, got: %v", name, entry["command"])
		}
		if _, ok := entry["args"].([]interface{}); !ok {
			t.Fatalf(".mcp.json server %q must have an args array, got: %v", name, entry["args"])
		}
	}

	// The opt-in MCP example file must also be written.
	assertPathExists(t, filepath.Join(root, ".mcp.json.example"))
}

func TestProjectGeneratorPreservesExistingMCPConfig(t *testing.T) {
	root := filepath.Join(t.TempDir(), "existing-mcp-app")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll error = %v", err)
	}

	customMCP := `{"mcpServers":{"my-custom-mcp":{"command":"custom"}}}`
	mcpPath := filepath.Join(root, ".mcp.json")
	if err := os.WriteFile(mcpPath, []byte(customMCP), 0o644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}

	generator := NewProjectGenerator("existing-mcp-app", root, "example.com/existing-mcp-app")
	if err := generator.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	after := readFile(t, mcpPath)
	if after != customMCP {
		t.Fatalf("expected existing .mcp.json to be preserved verbatim, got:\n%s", after)
	}
}

// TestProjectGeneratorIsIdempotentForforgeOwnedFiles verifies that
// re-running the metadata writer produces byte-identical forge-owned
// files. This is the contract of the file ownership model: forge-owned
// files can be regenerated freely because they depend only on the project
// name, not on any mutable state.
func TestProjectGeneratorIsIdempotentForforgeOwnedFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "idem-app")
	generator := NewProjectGenerator("idem-app", root, "example.com/idem-app")
	if err := generator.Generate(); err != nil {
		t.Fatalf("first Generate() error = %v", err)
	}

	owned := []string{
		filepath.Join(root, ".reliant", "project.json"),
		filepath.Join(root, ".reliant", "README.md"),
	}
	before := make(map[string]string, len(owned))
	for _, p := range owned {
		before[p] = readFile(t, p)
	}

	// Re-run only the metadata writer. We can't call Generate() again
	// because it would try to write cmd/, proto/, etc. on top of themselves.
	if err := generator.writeProjectMetadata(); err != nil {
		t.Fatalf("second writeProjectMetadata() error = %v", err)
	}

	for _, p := range owned {
		after := readFile(t, p)
		if before[p] != after {
			t.Errorf("forge-owned file %s changed on regeneration", p)
		}
	}
}

// TestProjectCreatedAtSurvivesRewrite pins why the test above is not
// clock-dependent: a rewrite keeps the recorded creation time instead of
// stamping a new one, which used to differ whenever the two writes straddled
// a second boundary.
func TestProjectCreatedAtSurvivesRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "project.json")
	if got := projectCreatedAt(path); got == "" {
		t.Fatal("a new project.json must get a creation time")
	} else if _, err := time.Parse(time.RFC3339, got); err != nil {
		t.Fatalf("creation time %q is not RFC3339: %v", got, err)
	}
	const recorded = "2001-02-03T04:05:06Z"
	if err := os.WriteFile(path, []byte(`{"created_at": "`+recorded+`", "name": "x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := projectCreatedAt(path); got != recorded {
		t.Errorf("projectCreatedAt = %q, want the recorded %q", got, recorded)
	}
}

// --- Feature flag gating tests ---

func TestFeatureFlag_MigrationsDisabled(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds and generates a full project; runs in task test")
	}
	root := filepath.Join(t.TempDir(), "no-migrations")
	gen := NewProjectGenerator("no-migrations", root, "example.com/no-migrations")
	gen.ServiceName = "api"
	gen.Features = gen.Features.With(config.FeatureMigrations, false).With(config.FeatureORM, false) // db/ is created when either is enabled

	if err := gen.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	// db/ directory should not exist
	assertPathNotExists(t, filepath.Join(root, "db"))

	// cmd/db.go should not exist
	assertPathNotExists(t, filepath.Join(root, "cmd", "no-migrations", "cmd", "db.go"))

	// pkg/app/migrate.go should not exist
	assertPathNotExists(t, filepath.Join(root, "pkg", "app", "migrate.go"))

	// The cmd tree still exists (codegen is enabled) but the serve.go this
	// project is born with must NOT invoke the migration step: pkg/app
	// /migrate.go is not generated when migrations are off, so a call to it
	// would not compile.
	//
	// Asserted over the PARSED render rather than a substring: the migrate
	// ceremony's body moved into pkg/serverkit, so the old marker strings
	// ("auto-migration failed") no longer appear in serve.go at all and
	// grepping for them now passes for the wrong reason. What still holds,
	// and is what actually matters, is that no CALL to a migrate entry point
	// is emitted.
	assertPathExists(t, filepath.Join(root, "cmd", "no-migrations", "cmd", "root.go"))
	serveSrc := renderServeFor(t, gen)
	if called := serveCallees(t, serveSrc); called["AutoMigrate"] {
		t.Fatalf("serve.go calls a migrate entry point when migrations are disabled — "+
			"pkg/app/migrate.go is not generated for this project, so this does not compile; got:\n%s", serveSrc)
	}
}

// serveCallees parses src and returns the set of function names it CALLS,
// by trailing identifier (so `serverkit.AutoMigrate(...)`, `pkgapp.AutoMigrate(...)`
// and a bare `AutoMigrate(...)` all register as "AutoMigrate"). Package
// qualification is deliberately ignored: these assertions care which step
// runs, not which package currently hosts it — that is exactly the detail
// that moved during the serverkit split.
func serveCallees(t *testing.T, src string) map[string]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "serve.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("rendered serve.go does not parse: %v\n%s", err, src)
	}
	called := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			called[fn.Name] = true
		case *ast.SelectorExpr:
			called[fn.Sel.Name] = true
		}
		return true
	})
	if len(called) == 0 {
		t.Fatal("parsed serve.go yielded ZERO call expressions — the derived set is empty, " +
			"so every membership check against it would vacuously pass")
	}
	return called
}

func TestFeatureFlag_CodegenDisabled(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds and generates a full project; runs in task test")
	}
	root := filepath.Join(t.TempDir(), "no-codegen")
	gen := NewProjectGenerator("no-codegen", root, "example.com/no-codegen")
	// No ServiceName — setting one would create proto/services/<svc>/v1
	// unconditionally via MkdirAll.
	gen.Features = gen.Features.With(config.FeatureCodegen, false)

	if err := gen.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	// The codegen-gated cmd tree files must not exist. server.go is the
	// anchor: like serve.go it depends on pkg/config + pkg/app, but unlike
	// serve.go it is still written by THIS lane, so its absence is what this
	// lane can actually assert. (root.go is deliberately not checked — it is
	// emitted for every service project regardless of the codegen flag.)
	assertPathNotExists(t, filepath.Join(root, "cmd", "no-codegen", "cmd", "server.go"))
	assertPathNotExists(t, filepath.Join(root, "cmd", "otel.go")) // otel.go shim deleted — never emitted
	assertPathNotExists(t, filepath.Join(root, "cmd", "no-codegen", "cmd", "db.go"))

	// Codegen-specific proto dirs should not exist
	assertPathNotExists(t, filepath.Join(root, "proto", "api"))
	assertPathNotExists(t, filepath.Join(root, "proto", "services"))
	assertPathNotExists(t, filepath.Join(root, "proto", "config"))
	assertPathNotExists(t, filepath.Join(root, "proto", "forge"))

	// pkg/app/bootstrap.go should not exist (generated by codegen pipeline)
	assertPathNotExists(t, filepath.Join(root, "pkg", "app", "bootstrap.go"))

	// Core files that don't depend on codegen should still exist
	assertPathExists(t, filepath.Join(root, "cmd", "no-codegen", "main.go"))
	assertPathExists(t, filepath.Join(root, "cmd", "no-codegen", "cmd", "version.go"))
}

// TestFeatureFlag_DeployScaffoldEmittedRegardlessOfOptIn locks in
// the contract: the SCAFFOLD always emits the deploy artefacts for a
// service-kind project — deploy derives on for service kind, and even
// an explicit `features.deploy: false` keeps the tree on disk so the
// user can flip the flag back with no rescaffold. The previous
// "deploy=false strips Dockerfile" behaviour is gone — the runtime
// gate lives on `forge env deploy` itself.
func TestFeatureFlag_DeployScaffoldEmittedRegardlessOfOptIn(t *testing.T) {
	root := filepath.Join(t.TempDir(), "deploy-shape")
	gen := NewProjectGenerator("deploy-shape", root, "example.com/deploy-shape")
	gen.ServiceName = "api"
	// Zero-value FeaturesConfig — experimental.Deploy is false.
	gen.Features = config.FeaturesConfig{}

	if err := gen.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	// Service-kind scaffolds always ship the deploy shape so the user
	// can flip the experimental flag without rescaffolding.
	assertPathExists(t, filepath.Join(root, "Dockerfile"))
	assertPathExists(t, filepath.Join(root, ".dockerignore"))
	assertPathExists(t, filepath.Join(root, "docker-compose.yml"))
	assertPathExists(t, filepath.Join(root, "deploy", "kcl"))

	// Non-deploy files exist too.
	assertCompositionRootDeferred(t, root, "deploy-shape")
	assertPathExists(t, filepath.Join(root, "cmd", "deploy-shape", "cmd", "root.go"))
}

func TestFeatureFlag_CIDisabled(t *testing.T) {
	root := filepath.Join(t.TempDir(), "no-ci")
	gen := NewProjectGenerator("no-ci", root, "example.com/no-ci")
	gen.ServiceName = "api"
	gen.Features = gen.Features.With(config.FeatureCI, false)

	if err := gen.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	// CI workflow files should not exist
	assertPathNotExists(t, filepath.Join(root, ".github", "workflows", "ci.yml"))
	assertPathNotExists(t, filepath.Join(root, ".github", "workflows", "deploy.yml"))
	assertPathNotExists(t, filepath.Join(root, ".github", "workflows", "build-images.yml"))
	assertPathNotExists(t, filepath.Join(root, ".github", "workflows", "e2e.yml"))

	// pre-commit workflow is also gated on CI
	assertPathNotExists(t, filepath.Join(root, ".github", "workflows", "pre-commit.yml"))

	// .pre-commit-config.yaml is from DX, not CI — it should still exist
	assertPathExists(t, filepath.Join(root, ".pre-commit-config.yaml"))

	// Non-CI files should still exist
	assertCompositionRootDeferred(t, root, "no-ci")
	assertPathExists(t, filepath.Join(root, "cmd", "no-ci", "cmd", "root.go"))
}

func TestFeatureFlag_HotReloadDisabled(t *testing.T) {
	root := filepath.Join(t.TempDir(), "no-hotreload")
	gen := NewProjectGenerator("no-hotreload", root, "example.com/no-hotreload")
	gen.ServiceName = "api"
	gen.Features = gen.Features.With(config.FeatureHotReload, false)

	if err := gen.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	// Air config files should not exist
	assertPathNotExists(t, filepath.Join(root, ".air.toml"))
	assertPathNotExists(t, filepath.Join(root, ".air-debug.toml"))

	// Other files should still exist
	assertCompositionRootDeferred(t, root, "no-hotreload")
	assertPathExists(t, filepath.Join(root, "cmd", "no-hotreload", "cmd", "root.go"))
}

func TestFeatureFlag_ObservabilityDisabled(t *testing.T) {
	root := filepath.Join(t.TempDir(), "no-observability")
	gen := NewProjectGenerator("no-observability", root, "example.com/no-observability")
	gen.ServiceName = "api"
	gen.Features = gen.Features.With(config.FeatureObservability, false)

	if err := gen.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	// The local ClickStack's inputs should not exist
	assertPathNotExists(t, filepath.Join(root, "deploy", "observability", "otel-collector.yaml"))
	assertPathNotExists(t, filepath.Join(root, "deploy", "observability", "dashboards"))
	assertPathNotExists(t, filepath.Join(root, "deploy", "observability", "otel-profiles.yaml"))
	assertPathNotExists(t, filepath.Join(root, "deploy", "observability", "profiles-schema.sql"))

	// OTel is owned by serverkit now — there is no generated cmd/otel.go shim
	// regardless of the observability flag.
	assertPathNotExists(t, filepath.Join(root, "cmd", "otel.go"))

	// Other files should still exist
	assertCompositionRootDeferred(t, root, "no-observability")
	assertPathExists(t, filepath.Join(root, "cmd", "no-observability", "cmd", "root.go"))
}

func TestFeatureFlag_AllEnabled(t *testing.T) {
	root := filepath.Join(t.TempDir(), "all-features")
	gen := NewProjectGenerator("all-features", root, "example.com/all-features")
	gen.ServiceName = "api"
	// Features is zero-value — all *bool fields are nil, meaning all enabled

	if err := gen.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	// Migrations
	assertPathExists(t, filepath.Join(root, "db", "migrations"))
	assertPathExists(t, filepath.Join(root, "cmd", "all-features", "cmd", "db.go"))

	// Codegen. db.go (above) is this lane's codegen anchor: serve.go is born
	// in the codegen pipeline, and a graph-mode project has no server.go —
	// each root's command, `server` included, is generated into
	// roots_gen.go there.
	assertPathNotExists(t, filepath.Join(root, "cmd", "all-features", "cmd", "server.go"))
	// OTel is owned by serverkit — no cmd/otel.go shim.
	assertPathNotExists(t, filepath.Join(root, "cmd", "otel.go"))
	assertPathExists(t, filepath.Join(root, "proto"))
	// Per-service test harness, beside the service, as a _test.go file so
	// package `testing` stays out of the production binary.
	assertPathExists(t, filepath.Join(root, "internal", "handlers", "api", "helpers_gen_test.go"))
	assertPathNotExists(t, filepath.Join(root, "pkg", "app", "testing.go"))

	// Deploy
	assertPathExists(t, filepath.Join(root, "Dockerfile"))
	assertPathExists(t, filepath.Join(root, ".dockerignore"))
	assertPathExists(t, filepath.Join(root, "docker-compose.yml"))
	assertPathExists(t, filepath.Join(root, "deploy", "kcl"))

	// CI
	assertPathExists(t, filepath.Join(root, ".github", "workflows", "ci.yml"))

	// Hot reload. exclude_dir entries are root-relative, so the bare
	// "node_modules" entry does NOT cover frontends/<name>/node_modules
	// — air walked ~4,235 node_modules dirs per boot (journey
	// fr-12520ad3d2) until "frontends" itself was excluded. No Go code
	// lives under frontends/; npm owns that dev loop.
	assertPathExists(t, filepath.Join(root, ".air.toml"))
	assertPathExists(t, filepath.Join(root, ".air-debug.toml"))
	for _, airFile := range []string{".air.toml", ".air-debug.toml"} {
		airContents := readFile(t, filepath.Join(root, airFile))
		if !strings.Contains(airContents, `"frontends"`) {
			t.Errorf("%s exclude_dir must contain \"frontends\" (air watches frontends/*/node_modules otherwise), got:\n%s", airFile, airContents)
		}
	}

	// Observability: the collector pipeline and the dashboard provisioner dir
	assertPathExists(t, filepath.Join(root, "deploy", "observability", "otel-collector.yaml"))
	assertPathExists(t, filepath.Join(root, "deploy", "observability", "dashboards", "README.md"))
	assertPathExists(t, filepath.Join(root, "deploy", "observability", "otel-profiles.yaml"))
	assertPathExists(t, filepath.Join(root, "deploy", "observability", "profiles-schema.sql"))
	assertPathNotExists(t, filepath.Join(root, "deploy", "alloy-config.alloy"))

	// serve.go must actually INVOKE the migrate step when migrations are
	// enabled — the mirror of TestFeatureFlag_MigrationsDisabled. Asserted
	// over the parsed call set: a substring match on "AutoMigrate" also hits
	// the config-field projection (`AutoMigrate: cfg.AutoMigrate`), which is
	// present whether or not anything ever runs a migration.
	serveSrc := renderServeFor(t, gen)
	if !serveCallees(t, serveSrc)["AutoMigrate"] {
		t.Fatalf("serve.go must call the migrate step when migrations are enabled, got:\n%s", serveSrc)
	}
}

// TestFreshScaffoldDefaults locks in the strict-by-default contract floor and
// the all-features-on default for `--kind service` projects. If you change
// the defaults, update this test deliberately (and update the scaffold
// migration tip in MIGRATION_TIPS.md).
func TestFreshScaffoldDefaults(t *testing.T) {
	root := filepath.Join(t.TempDir(), "fresh")
	gen := NewProjectGenerator("fresh", root, "example.com/fresh")
	gen.ServiceName = "api"
	if err := gen.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	raw := readFile(t, filepath.Join(root, "forge.yaml"))

	// The scaffolded file is minimal: no features: block, no contracts:
	// block — both derive at load time. Presence on disk would mean the
	// minimal-scaffold contract regressed.
	for _, line := range strings.Split(raw, "\n") {
		switch {
		case strings.HasPrefix(line, "features:"),
			strings.HasPrefix(line, "contracts:"),
			strings.HasPrefix(line, "database:"),
			strings.HasPrefix(line, "ci:"):
			t.Errorf("forge.yaml should not materialize %q (derived at load); got:\n%s", line, raw)
		}
	}

	cfg, err := ReadProjectConfig(filepath.Join(root, "forge.yaml"))
	if err != nil {
		t.Fatalf("ReadProjectConfig: %v", err)
	}

	// Contracts: the rules are unconditional, so a scaffold declares no
	// escape hatches — no excluded packages, no extra interface types.
	if len(cfg.Contracts.Exclude) != 0 || len(cfg.Contracts.InterfaceTypes) != 0 {
		t.Errorf("scaffold should declare no contract escape hatches, got %+v", cfg.Contracts)
	}

	// Features: everything on for --kind service with a DB, except
	// frontend (no --frontend passed → frontends list empty → derived
	// off). `deploy` is experimental — default-off.
	eff := cfg.Features.EffectiveFeatures()
	wantOn := []string{"orm", "codegen", "migrations", "ci", "build", "contracts", "observability", "hot_reload"}
	for _, name := range wantOn {
		if !eff[name] {
			t.Errorf("loaded config feature %q = false, want true (derived)", name)
		}
	}
	if eff["frontend"] {
		t.Error("loaded config feature \"frontend\" = true, want false (no frontends declared)")
	}
}

// TestFreshScaffoldDefaultsWithFrontend mirrors TestFreshScaffoldDefaults but
// with --frontend, where the frontend feature should flip to true.
func TestFreshScaffoldDefaultsWithFrontend(t *testing.T) {
	root := filepath.Join(t.TempDir(), "fresh-fe")
	gen := NewProjectGenerator("fresh-fe", root, "example.com/fresh-fe")
	gen.ServiceName = "api"
	gen.FrontendName = "web"
	if err := gen.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	cfg, err := ReadProjectConfig(filepath.Join(root, "forge.yaml"))
	if err != nil {
		t.Fatalf("ReadProjectConfig: %v", err)
	}
	if !cfg.Features.FrontendEnabled() {
		t.Error("loaded config FrontendEnabled() = false, want true (frontends list non-empty)")
	}
}

// TestDockerComposePostgresFixedPort pins the J-round dev-loop fix: the
// compose template must publish a FIXED, loopback-bound host port for
// postgres. The old "0:5432" random mapping meant the scaffolded
// DATABASE_URL/host dev loop could never find the database without
// inspecting `docker compose ps`.
func TestDockerComposePostgresFixedPort(t *testing.T) {
	dir := t.TempDir()
	g := &ProjectGenerator{Name: "bookmarks", Path: dir}
	if err := g.generateDockerCompose(); err != nil {
		t.Fatalf("generateDockerCompose() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	compose := string(data)
	if strings.Contains(compose, `"0:5432"`) {
		t.Error("postgres still maps to a random host port (\"0:5432\") — host dev loop cannot find it")
	}
	if !strings.Contains(compose, `"127.0.0.1:${POSTGRES_PORT:-5432}:5432"`) {
		t.Errorf("postgres should publish a fixed loopback host port; compose:\n%s", compose)
	}
}
