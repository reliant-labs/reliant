package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/configproto"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/generator/contract"
	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/scaffold"
	"github.com/reliant-labs/forge/internal/templates"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// validGoPackageName matches a valid Go package name: lowercase letters, digits, underscores.
var validGoPackageName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// validPackageKinds lists the supported --kind values.
var validPackageKinds = map[string]bool{
	"eventbus": true,
	"client":   true,
}

// validPackageTypes lists the supported --type values for `forge scaffold package`.
//
// There are two, because there are only two things forge can tell apart:
//
//   - service: default; the Service/Deps/New(Deps) Service shape that
//     codegen wires. Every internal package is this shape, including the
//     ones you would call orchestrators or use-case interactors — those
//     are services whose deps happen to be other services. Nothing in
//     forge behaves differently for them, so there is no separate flag.
//   - adapter: the same shape, plus the `// forge:outbound-io` marker,
//     which asserts the package calls OUT and serves nothing inbound. The
//     marker is the difference — it is what lint and the observe heuristic
//     read. See `forge skill load adapter`.
var validPackageTypes = map[string]bool{
	"service": true,
	"adapter": true,
}

// packageTypeHelp is the long-form help text shown under `--type`.
const packageTypeHelp = `package shape: service|adapter (default service)

  service     Standard internal/<name>/ with Service/Deps/New — wired into
              the composition, callable by handlers. The default, and the
              right answer for a use-case orchestrator too: an orchestrator
              is a service whose Deps are other services' interfaces.
  adapter     The same shape plus '// forge:outbound-io', which asserts the
              package calls OUT to a third-party system and serves nothing
              inbound. Lint keeps RPC handlers out of it and the observe
              heuristic treats it as doing I/O.
              See: forge skill load adapter`

func newPackageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "package",
		Short: "Manage internal packages",
		Long: `Manage internal packages with Go interface contracts.

Internal packages live under internal/<name>/ and define their boundary
through a Go interface in contract.go. Unlike proto API services, internal
package contracts use native Go interfaces — supporting channels, complex
types, factories, and other constructs that proto cannot express.

Subcommands:
  forge package new <name>   Create a new internal package`,
	}

	packageNewCmd := &cobra.Command{
		Use:   "new <name>",
		Short: "Create a new internal package with contract interface",
		Long: `Create a new internal package under internal/<name>/ with:
  - contract.go  — Go interface that IS the package contract
  - service.go   — Implementation with unexported concrete type

It ends with a 'forge generate' run, which writes mock_gen.go and
middleware_gen.go and wires the package into internal/app/compose.go, so
the project lints clean straight away (--no-generate skips it). After you
add methods to contract.go, run 'forge generate' again.

` + packageTypeHelp + `

Example:
  forge package new cache
  forge package new notifications
  forge package new events --kind eventbus
  forge package new stripe-adapter --type adapter --base-url
  forge package new pubsub --type adapter --dry-run`,
		Args: cobra.ExactArgs(1),
		RunE: runPackageNewThenGenerate,
	}

	packageNewCmd.Flags().String("kind", "", "package kind template (e.g. eventbus, client)")
	packageNewCmd.Flags().String("type", "service", "package shape: service|adapter (see --help for details)")
	packageNewCmd.Flags().Bool("no-generate", false, "skip the post-scaffold 'forge generate' run (scaffold-only mode for parallel-agent rounds); run 'forge generate' before 'forge lint'")
	cmdutil.AddPackageScaffoldFlags(packageNewCmd)

	cmd.AddCommand(cmdutil.ConfiguresClone(packageNewCmd))

	return cmdutil.StrictGroup(cmd)
}

// runPackageNewThenGenerate is `forge package new`: the same scaffold as
// `forge scaffold package`, ending the same way — with a generate run under
// the project lock, so the new package's middleware_gen.go, mock and
// compose.go wiring exist before the next `forge lint` (without them the
// owned observe_chain.go seam is unreferenced, and an adapter's
// config.<Name>Config does not exist yet). The scaffold group runs the same
// tail through its own lock wrapper; see scaffold.syncDerivedFiles.
func runPackageNewThenGenerate(cmd *cobra.Command, args []string) error {
	root, err := projectRoot()
	if err != nil {
		return runPackageNew(cmd, args) // reports the missing project itself
	}
	if cmdutil.IsDryRun(cmd) {
		// The plan alone: no lock file, no generate.
		release, err := observeGenerateLock(root)
		if err != nil {
			return err
		}
		defer release()
		return runPackageNew(cmd, args)
	}
	return withGenerateLock(root, func() error {
		if err := runPackageNew(cmd, args); err != nil {
			return err
		}
		if dryRun, _ := cmd.Flags().GetBool("dry-run"); dryRun {
			return nil
		}
		if noGenerate, _ := cmd.Flags().GetBool("no-generate"); noGenerate {
			fmt.Printf("\n⏩ --no-generate: skipping the post-scaffold `forge generate` run.\n")
			fmt.Printf("   Package %q is written, but the files forge derives from it are not yet: run `forge generate` before `forge lint`.\n", args[0])
			return nil
		}
		fmt.Println("\n🔧 Running generation pipeline...")
		if err := runGeneratePipeline(root, false); err != nil {
			return fmt.Errorf("package %q was written and kept, but the post-scaffold `forge generate` failed: %w; fix it, then re-run `forge generate`", args[0], err)
		}
		return nil
	})
}

// packageTypeForCommand applies the service default even for callers without
// a registered --type flag, and gives invalid types a stable diagnostic.
func packageTypeForCommand(cmd *cobra.Command) (string, error) {
	pkgType, _ := cmd.Flags().GetString("type")
	pkgType = strings.TrimSpace(pkgType)
	if pkgType == "" {
		pkgType = "service"
	}
	if validPackageTypes[pkgType] {
		return pkgType, nil
	}
	valid := make([]string, 0, len(validPackageTypes))
	for k := range validPackageTypes {
		valid = append(valid, k)
	}
	sort.Strings(valid)
	return "", fmt.Errorf("invalid package type %q: valid types are %s", pkgType, strings.Join(valid, ", "))
}

func runPackageNew(cmd *cobra.Command, args []string) error {
	name := args[0]

	kind, _ := cmd.Flags().GetString("kind")
	kind = strings.TrimSpace(kind)

	pkgType, err := packageTypeForCommand(cmd)
	if err != nil {
		return err
	}

	// --kind and --type compose only on the default "service" type.
	// An adapter gets a fixed scaffold; layering an eventbus/client kind
	// on top would silently overwrite it.
	if pkgType != "service" && kind != "" {
		return fmt.Errorf("--kind cannot be combined with --type=%s; the type owns the scaffold shape", pkgType)
	}

	// Validate --kind if provided
	if kind != "" && !validPackageKinds[kind] {
		valid := make([]string, 0, len(validPackageKinds))
		for k := range validPackageKinds {
			valid = append(valid, k)
		}
		return fmt.Errorf("invalid package kind %q: valid kinds are %s", kind, strings.Join(valid, ", "))
	}

	// --base-url is an adapter's config leaf; no other shape reads one.
	// --base-url-env names its env var, and asks for the leaf by doing so.
	wantBaseURL, _ := cmd.Flags().GetBool("base-url")
	baseURLEnv, _ := cmd.Flags().GetString("base-url-env")
	baseURLEnv = strings.TrimSpace(baseURLEnv)
	if (wantBaseURL || baseURLEnv != "") && pkgType != "adapter" {
		return fmt.Errorf("--base-url applies only to --type=adapter (a %s package takes its settings as a config block you declare)", pkgType)
	}
	if wantBaseURL && baseURLEnv == "" {
		baseURLEnv = strings.ToUpper(name) + "_BASE_URL"
	}
	if baseURLEnv != "" && !validEnvVarName.MatchString(baseURLEnv) {
		return fmt.Errorf("--base-url-env=%s: an env var name is upper-case letters, digits and underscores, starting with a letter", baseURLEnv)
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	// Validate name is a valid Go package name
	if !validGoPackageName.MatchString(name) {
		return fmt.Errorf("invalid package name %q: must be lowercase, start with a letter, and contain only letters, digits, and underscores", name)
	}
	if cmdutil.GoKeywords[name] {
		return fmt.Errorf("%q is a Go keyword", name)
	}

	root, err := projectRoot()
	if err != nil {
		return err
	}

	// The directory IS the registry: a package exists because
	// internal/<name>/ holds a contract.go, which is what both the bootstrap
	// codegen and every reporter discover it from.
	pkgDir := filepath.Join(root, "internal", name)
	if dirExists(pkgDir) {
		if pkgType == "adapter" && fileExists(filepath.Join(pkgDir, "adapter.go")) && codegen.HasOutboundIODirective(pkgDir) {
			fmt.Printf("Adapter %q already exists at %s; leaving its owned files and config unchanged.\n", name, pkgDir)
			return nil
		}
		return fmt.Errorf("internal package %q already exists at %s", name, pkgDir)
	}

	configPath := filepath.Join(root, "forge.yaml")
	cfg, err := generator.ReadProjectConfig(configPath)
	if err != nil {
		return fmt.Errorf("read project config: %w", err)
	}

	if !dryRun {
		fmt.Printf("Creating internal package '%s'...\n", name)
	}

	// Template data. ImportPath mirrors Name for now because `forge package
	// new` only accepts flat names (validGoPackageName rejects "/"). When
	// nested-path support lands, ImportPath should carry the full
	// module-relative path under internal/ (e.g. "mcp/database").
	//
	// Flavor identifies the scaffold shape: the --kind when given, else the
	// --type ("service" for the default empty-interface scaffold). The owned
	// observe_chain.go seam is flavor-independent (per-method wrappers live in
	// the GENERATED decorator, not the seam), so Flavor no longer selects
	// observe scaffolding — it still routes the contract/service/test tree.
	flavor := pkgType
	if kind != "" {
		flavor = kind
	}
	data := struct {
		Name       string
		ImportPath string
		Module     string
		Flavor     string
		LogLevel   string
		// HasConfig: the package's Deps takes Config, and its files import
		// <module>/pkg/config, only when the project has typed config — the
		// rule service.go follows. A project adopted in place has none.
		HasConfig bool
		// ConfigBlock is the adapter's config block message (AlpacaConfig)
		// when one is composed on AppConfig — the adapter then takes it as
		// `Cfg *config.<ConfigBlock>` — and empty when the project has no
		// AppConfig to compose it on. ConfigEnvVar is its base_url leaf's
		// env var.
		ConfigBlock  string
		ConfigEnvVar string
	}{
		Name:       name,
		ImportPath: name,
		Module:     cfg.ModulePath,
		Flavor:     flavor,
		HasConfig:  codegen.ProjectHasTypedConfig(root),
		// Seed the owned observe_chain.go seam's success-log level from the
		// project's observability config (default: slog.LevelDebug).
		LogLevel: cfg.Observability.SlogLevelExpr(),
	}

	// An adapter's base URL, when it has one, is configuration, so it is a
	// leaf of its own config block in proto/<project>/config/v1/config.proto
	// rather than a scalar Deps field (which forge-config-deps rejects, and
	// which compose.go could only ever wire to ""). It is opt-in (--base-url):
	// an adapter over a queue or a vendor SDK has no base URL, and a leaf
	// named for one would be configuration nothing reads. The proto edit runs
	// first because its outcome decides which Deps shape the templates render.
	if baseURLEnv != "" {
		block, err := ensureAdapterConfigBlock(root, name, baseURLEnv, dryRun)
		if err != nil {
			return err
		}
		data.ConfigBlock, data.ConfigEnvVar = block, baseURLEnv
	}

	if dryRun {
		return printPackagePlan(name, pkgType, kind, data.ConfigBlock != "")
	}

	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		return fmt.Errorf("create package directory: %w", err)
	}

	// Render the scaffold. --type=adapter and --kind both render a full
	// template tree from a dedicated subdir under internal-package/; the
	// default renders the generic contract/service/test set.
	switch {
	case pkgType == "adapter":
		if err := renderPackageKindTree(pkgDir, pkgType, data, true); err != nil {
			return err
		}
	case kind != "":
		if err := renderPackageKindTree(pkgDir, kind, data, false); err != nil {
			return err
		}
	default:
		if err := renderDefaultPackageScaffold(pkgDir, data); err != nil {
			return err
		}
	}

	// EVERY scaffold that produced a contract.go gets its stub mock_gen.go
	// here, not just the default one. When only the default path emitted it,
	// an author who scaffolded an adapter or an eventbus opened the directory,
	// saw no mock, and hand-rolled a fake — which is how a `fakeStore` ends up
	// in the same package as, and adjacent to, a generated `MockStore`. The
	// generator mocks EVERY interface in contract.go, so the stub also covers
	// the dep interfaces declared alongside Service.
	//
	// Soft warning rather than a hard error: the canonical fix for any
	// generator hiccup is to run `forge generate` once the project is
	// quiescent, and a broken stub should not block package creation.
	if contractPath := filepath.Join(pkgDir, "contract.go"); fileExists(contractPath) {
		if err := contract.Generate(contractPath); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not emit stub mock_gen.go for %s: %v\n", name, err)
			fmt.Fprintln(os.Stderr, "         run `forge generate` to retry.")
		}
	}

	// Every contract package ALSO gets the OWNED observability seam
	// (observe_chain.go: newObserveChain() — the in-process middleware chain
	// the generated decorator routes through). It is the user's file from day
	// 0 and is THE extension point (add/drop a middleware, change the log
	// level). The per-method wrapper itself is GENERATED from the interface
	// (middleware_gen.go) — adding a Service method regenerates it, no
	// hand-maintenance. The composition site wires `pkg.New<Concrete>WithForgeMiddleware(pkg.New(...))`
	// automatically whenever this seam exists and New returns the interface.
	observeContent, err := templates.InternalPkgTemplates().Render("observe_chain.go.tmpl", data)
	if err != nil {
		return fmt.Errorf("render observe_chain.go: %w", err)
	}
	if err := atomicfile.WriteFile(filepath.Join(pkgDir, "observe_chain.go"), observeContent, 0o644); err != nil {
		return fmt.Errorf("write observe_chain.go: %w", err)
	}

	// forge.yaml is NOT touched. A package is declared by its own source —
	// internal/<name>/contract.go, plus the `//forge:outbound-io` marker the
	// adapter scaffold stamps — and that is what codegen wires and what
	// `forge project map` / `forge project audit` / the architecture doc
	// report. A `packages:` list next to it could only ever be a copy that
	// goes stale.

	// Report what was ACTUALLY written, by the noun the user asked for.
	//
	// The summary is the most-read documentation forge emits — every
	// invocation ends here, and it is what decides which file the author
	// opens next. It used to announce "Internal package created" for a
	// `--type adapter` request (reading like a silently downgraded ask,
	// since `scaffold package` is a different noun) and name ONE of the six
	// files it wrote — the one that is not where the work is. An author who
	// trusted it edited contract.go and never opened adapter.go, cache.go
	// or observe_chain.go, which is where the scaffold's best guidance
	// lives.
	printScaffoldedPackageSummary(pkgDir, name, pkgType)
	// Every caller ends with a generate run unless --no-generate skipped it;
	// only then is "run generate first" still advice the user needs.
	if noGenerate, _ := cmd.Flags().GetBool("no-generate"); data.ConfigBlock != "" && noGenerate {
		// adapter.go names config.<ConfigBlock>, which pkg/config declares
		// only once generate has compiled the block the proto edit added.
		fmt.Printf("\n   Run `forge generate` first: it generates config.%s from proto/<project>/config/v1 and wires\n", data.ConfigBlock)
		fmt.Printf("   internal/%s, with its Cfg, into internal/app/compose.go. Set %s per environment.\n", name, data.ConfigEnvVar)
	}

	return nil
}

// ensureAdapterConfigBlock composes the adapter's config block on AppConfig
// in proto/<project>/config/v1 — `message <Name>Config { string base_url = 1 [...]; }`
// plus `<Name>Config <name> = <next tag>;` — and returns the block's message
// name and its base_url env var. The edit is idempotent (an adapter
// re-scaffolded after its directory was deleted finds its block in place) and
// never renumbers (scaffold.EnsureConfigBlock).
//
// A project with no AppConfig must declare its config composition explicitly
// before scaffolding an adapter. Refuse before creating its package rather
// than emitting the scalar Deps field forge-config-deps rejects.
func ensureAdapterConfigBlock(root, name, envVar string, dryRun bool) (block string, err error) {
	block = naming.ToPascalCase(name) + "Config"
	spec := scaffold.ConfigBlockSpec{
		Message: block,
		Field:   name,
		Doc: []string{
			fmt.Sprintf("%s configures the %s adapter (internal/%s), composed on", block, name, name),
			fmt.Sprintf("%s as %#q. internal/app/compose.go hands it to the adapter as", scaffold.ConfigRootMessage, name),
			"Deps.Cfg, so each leaf reaches the adapter from its env var, a --config",
			"file, or deploy/kcl/<env>/config.k. Add the adapter's other settings here",
			"as leaves: account IDs, timeouts, and API keys with `sensitive: true`.",
		},
		FieldDoc: fmt.Sprintf("Configuration of the %s adapter (internal/%s).", name, name),
		Leaves: []scaffold.ConfigBlockLeaf{{
			Type:        "string",
			Name:        "base_url",
			EnvVar:      envVar,
			Description: fmt.Sprintf("Base URL of the %s downstream, e.g. https://api.example.com (no trailing slash). The adapter's requests are made against it.", name),
		}},
	}
	layout := configproto.ForProject(root)
	ensure := scaffold.EnsureConfigBlock
	if dryRun {
		ensure = scaffold.PlanConfigBlock
	}
	res, err := ensure(filepath.Join(root, filepath.FromSlash(layout.Dir())), spec)
	switch {
	case errors.Is(err, scaffold.ErrNoConfigRoot):
		return "", fmt.Errorf("scaffold adapter %s --base-url requires %s with an AppConfig message; declare the config root before scaffolding the adapter, or drop --base-url (no adapter files were written)", name, layout.RootPath())
	case err != nil:
		return "", fmt.Errorf("compose the %s config block: %w", block, err)
	}
	rel, relErr := filepath.Rel(root, res.File)
	if relErr != nil {
		rel = res.File
	}
	// --dry-run reports the same edit in the conditional.
	declare, composed, composeExisting := "✅ Declared", "composed", "✅ Composed"
	if dryRun {
		declare, composed, composeExisting = "Would declare", "compose", "Would compose"
	}
	switch {
	case res.AddedMessage && res.AddedField:
		fmt.Printf("%s message %s in %s and %s it on %s as %s = %d (base_url from %s)\n",
			declare, block, rel, composed, scaffold.ConfigRootMessage, name, res.FieldNumber, envVar)
	case res.AddedField:
		fmt.Printf("%s the existing message %s on %s in %s as %s = %d\n",
			composeExisting, block, scaffold.ConfigRootMessage, rel, name, res.FieldNumber)
	case res.AddedMessage:
		fmt.Printf("%s message %s in %s (already composed on %s)\n", declare, block, rel, scaffold.ConfigRootMessage)
	default:
		fmt.Printf("   %s is already declared and composed on %s in %s\n", block, scaffold.ConfigRootMessage, rel)
	}
	return block, nil
}

// validEnvVarName is what --base-url accepts as the leaf's env var.
var validEnvVarName = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// printPackagePlan is --dry-run's report: every file the scaffold would write
// under internal/<name>/, from the same template sets the real run renders,
// and the generate run that would follow. Nothing is written.
func printPackagePlan(name, pkgType, kind string, hasConfigBlock bool) error {
	var files []string
	switch {
	case pkgType == "adapter" || kind != "":
		set := pkgType
		if kind != "" {
			set = kind
		}
		tmplFiles, err := templates.InternalPkgKindTemplates(set).ListFlat("")
		if err != nil {
			return fmt.Errorf("list %s templates: %w", set, err)
		}
		for _, f := range tmplFiles {
			files = append(files, strings.TrimSuffix(f, ".tmpl"))
		}
	default:
		files = []string{"contract.go", "service.go", "contract_test.go"}
	}
	files = append(files, "mock_gen.go", "observe_chain.go")
	sort.Strings(files)

	noun := "internal package"
	if pkgType == "adapter" {
		noun = "adapter"
	}
	fmt.Printf("Would create %s '%s':\n", noun, name)
	for _, file := range files {
		if role := packageFileRoles[file]; role != "" {
			fmt.Printf("   internal/%s/%-18s %s\n", name, file, role)
		} else {
			fmt.Printf("   internal/%s/%s\n", name, file)
		}
	}
	if pkgType == "adapter" && !hasConfigBlock {
		fmt.Printf("   (no config block: pass --base-url for an HTTP API with one base URL)\n")
	}
	fmt.Printf("Then `forge generate` (unless --no-generate) writes middleware_gen.go and wires internal/%s into internal/app/compose.go.\n", name)
	fmt.Println("--dry-run: nothing was written.")
	return nil
}

// packageFileRoles describes what each scaffolded file is for, so the
// summary teaches rather than just lists. A file with no entry here is
// still printed — an unexplained file beats an omitted one, and this map
// going stale must not silently hide a new template.
var packageFileRoles = map[string]string{
	"adapter.go":       "Deps + concrete service + New — THE implementation seam, start here",
	"adapter_test.go":  "httptest-backed test, already passing",
	"cache.go":         "empty by design: where the TTL/rate-budget goes when you add one",
	"contract.go":      "the Service interface — the boundary callers depend on",
	"service.go":       "the implementation",
	"contract_test.go": "contract-level test (yours after this scaffold)",
	"mock_gen.go":      "generated: one mock per interface in contract.go",
	"observe_chain.go": "owned observability seam (add/drop middleware, set log level)",
	"client.go":        "HTTP client implementation",
	"eventbus.go":      "event bus implementation",
}

// printScaffoldedPackageSummary lists every file that landed in pkgDir with
// a one-line role, then points at the seam to open next.
//
// The list is read off DISK rather than from a hardcoded set, so a template
// added to a scaffold tree shows up here automatically instead of being
// silently under-reported — the exact failure this replaced.
func printScaffoldedPackageSummary(pkgDir, name, pkgType string) {
	noun := "Internal package"
	if pkgType == "adapter" {
		noun = "Adapter"
	}
	fmt.Printf("\n✅ %s '%s' created!\n", noun, name)

	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		// Non-fatal: the files exist regardless, and a summary that cannot
		// enumerate them should not fail the scaffold.
		fmt.Fprintf(os.Stderr, "warning: could not list %s: %v\n", pkgDir, err)
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, file := range names {
		if role := packageFileRoles[file]; role != "" {
			fmt.Printf("   internal/%s/%-18s %s\n", name, file, role)
		} else {
			fmt.Printf("   internal/%s/%s\n", name, file)
		}
	}

	// A package with the owned observability seam gets a generated decorator,
	// and `forge lint --tracing` reports any hand construction of it that
	// calls New. Say so here rather than at the end of the first lint run.
	if _, statErr := os.Stat(filepath.Join(pkgDir, "observe_chain.go")); statErr == nil {
		fmt.Printf("\n   Wherever you construct it by hand (providers.go, a *_wire.go, another binary) call\n")
		fmt.Printf("   %s.NewInstrumented(deps) — New's signature, returned already traced. compose.go is wired by\n", name)
		fmt.Printf("   `forge generate`; a bare %s.New(...) elsewhere is reported by `forge lint --tracing` (--fix rewrites it).\n", name)
	}

	// The next step differs by shape: an adapter arrives with a working
	// HealthCheck to extend, so the work is in adapter.go; the default
	// package arrives with an empty interface, so the work starts in
	// contract.go.
	if pkgType == "adapter" {
		fmt.Printf("\n   Next: implement the downstream calls in internal/%s/adapter.go, declaring each\n", name)
		fmt.Printf("         one on the Service interface in internal/%s/contract.go as you go, then run\n", name)
		fmt.Printf("         `forge generate` to refresh internal/%s/mock_gen.go — one mock per interface.\n", name)
		return
	}
	fmt.Printf("\n   Next: edit internal/%s/contract.go to declare the Service interface (and any dep interfaces),\n", name)
	fmt.Printf("         then run `forge generate` to refresh internal/%s/mock_gen.go — one mock per interface, ready for tests.\n", name)
}

// renderPackageKindTree renders every template in the internal-package
// kindOrType subdir (used for both --type=adapter and --kind, which share the
// full-tree shape) into pkgDir, stripping the .tmpl suffix. When
// requireNonEmpty is set, an empty template set is a hard error (the adapter
// path — an empty set there is a forge bug).
func renderPackageKindTree(pkgDir, kindOrType string, data any, requireNonEmpty bool) error {
	tmplFiles, err := templates.InternalPkgKindTemplates(kindOrType).ListFlat("")
	if err != nil {
		return fmt.Errorf("list %s templates: %w", kindOrType, err)
	}
	if requireNonEmpty && len(tmplFiles) == 0 {
		return fmt.Errorf("no templates found for --type=%s (this is a forge bug — please report)", kindOrType)
	}
	for _, tmplFile := range tmplFiles {
		content, err := templates.InternalPkgKindTemplates(kindOrType).Render(tmplFile, data)
		if err != nil {
			return fmt.Errorf("render %s: %w", tmplFile, err)
		}
		// Strip .tmpl suffix for the output filename.
		outName := strings.TrimSuffix(tmplFile, ".tmpl")
		if err := atomicfile.WriteFile(filepath.Join(pkgDir, outName), content, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", outName, err)
		}
	}
	return nil
}

// renderDefaultPackageScaffold renders the default internal-package set:
// contract.go, service.go, and a once-only contract_test.go (owned by the
// user after the first scaffold). The stub mock_gen.go is emitted by the
// caller, which does it for every scaffold shape rather than just this one.
func renderDefaultPackageScaffold(pkgDir string, data any) error {
	contractContent, err := templates.InternalPkgTemplates().Render("contract.go.tmpl", data)
	if err != nil {
		return fmt.Errorf("render contract.go: %w", err)
	}
	if err := atomicfile.WriteFile(filepath.Join(pkgDir, "contract.go"), contractContent, 0o644); err != nil {
		return fmt.Errorf("write contract.go: %w", err)
	}

	serviceContent, err := templates.InternalPkgTemplates().Render("service.go.tmpl", data)
	if err != nil {
		return fmt.Errorf("render service.go: %w", err)
	}
	if err := atomicfile.WriteFile(filepath.Join(pkgDir, "service.go"), serviceContent, 0o644); err != nil {
		return fmt.Errorf("write service.go: %w", err)
	}

	contractTestContent, err := templates.InternalPkgTemplates().Render("contract_test.go.tmpl", data)
	if err != nil {
		return fmt.Errorf("render contract_test.go: %w", err)
	}
	if err := atomicfile.WriteFile(filepath.Join(pkgDir, "contract_test.go"), contractTestContent, 0o644); err != nil {
		return fmt.Errorf("write contract_test.go: %w", err)
	}

	return nil
}
