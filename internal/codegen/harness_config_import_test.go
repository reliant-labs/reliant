package codegen

import (
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
)

// writeHarnessFixture writes files (path → content) under a fresh project
// root and returns it.
func writeHarnessFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return root
}

// noConfigWidget is a wired contract package (contract.go, no
// //forge:not-a-component) whose Deps takes no config: the shape reliant's
// contract packages have, in a project that had no pkg/config.
const noConfigWidget = `package widget

import "log/slog"

type Service interface{ Name() string }

type Deps struct {
	Logger *slog.Logger
}

type widget struct{}

func (widget) Name() string { return "widget" }

func New(deps Deps) Service { return widget{} }
`

// readHarness generates the test harness for root and returns the helper
// file written into the component directory rel.
func readHarness(t *testing.T, root, rel string, services []ServiceDef, pkgs []BootstrapPackageData) string {
	t.Helper()
	if err := GenerateBootstrapTesting(BootstrapTestingGenInput{
		GenContext: GenContext{ProjectDir: root, ModulePath: "example.com/proj"},
		Services:   services,
		Packages:   pkgs,
	}); err != nil {
		t.Fatalf("GenerateBootstrapTesting: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel), componentTestHelperFile))
	if err != nil {
		t.Fatalf("read %s helpers: %v", rel, err)
	}
	if _, perr := parser.ParseFile(token.NewFileSet(), componentTestHelperFile, data, parser.AllErrors); perr != nil {
		t.Fatalf("%s helpers do not parse: %v\n%s", rel, perr, data)
	}
	return string(data)
}

// TestGenerateBootstrapTesting_NoTypedConfig is the regression test for a
// harness that imported `<module>/pkg/config` for every component. A project
// without typed config (no proto/config, so no pkg/config) — reliant, when it
// adopted contracts — got a helpers_gen_test.go that did not compile beside
// every wired contract package and every service.
func TestGenerateBootstrapTesting_NoTypedConfig(t *testing.T) {
	root := writeHarnessFixture(t, map[string]string{
		"internal/widget/contract.go": noConfigWidget,
		"internal/handlers/api/service.go": `package api

import "log/slog"

type Deps struct {
	Logger *slog.Logger
}

type Service struct{ deps Deps }

func New(deps Deps) (*Service, error) { return &Service{deps: deps}, nil }
`,
	})
	services := []ServiceDef{{Name: "APIService", ModulePath: "example.com/proj"}}
	pkgs := []BootstrapPackageData{{Name: "widget", Package: "widget", ImportPath: "widget", FieldName: "Widget", VarName: "widget"}}

	for rel, got := range map[string]string{
		"internal/widget":       readHarness(t, root, "internal/widget", services, pkgs),
		"internal/handlers/api": readHarness(t, root, "internal/handlers/api", services, pkgs),
	} {
		for _, absent := range []string{"pkg/config", "config.Config", "WithConfig", "deps.Config", "cfg.cfg"} {
			if strings.Contains(got, absent) {
				t.Errorf("%s: a component that takes no config must get no config plumbing, but the harness has %q:\n%s",
					rel, absent, got)
			}
		}
	}
}

// TestGenerateBootstrapTesting_ConfigImportFollowsDeps is the other half: a
// component whose Deps does take a config gets the default, the WithConfig
// option and the import, and the import is the one the component's own Deps
// names rather than an assumed pkg/config.
func TestGenerateBootstrapTesting_ConfigImportFollowsDeps(t *testing.T) {
	root := writeHarnessFixture(t, map[string]string{
		// The scaffolded shape: the project's generated pkg/config.
		"internal/widget/contract.go": `package widget

import "example.com/proj/pkg/config"

type Service interface{ Name() string }

type Deps struct {
	Config *config.Config
}

func New(deps Deps) Service { return nil }
`,
		// A config from elsewhere, under its own name.
		"internal/gadget/contract.go": `package gadget

import "example.com/proj/internal/settings"

type Service interface{ Name() string }

type Deps struct {
	Config *settings.Config
}

func New(deps Deps) Service { return nil }
`,
		"internal/handlers/api/service.go": `package api

import (
	"log/slog"

	"example.com/proj/pkg/config"
)

type Deps struct {
	Logger *slog.Logger
	Config *config.Config
}

type Service struct{ deps Deps }

func New(deps Deps) (*Service, error) { return &Service{deps: deps}, nil }
`,
	})
	services := []ServiceDef{{Name: "APIService", ModulePath: "example.com/proj"}}
	pkgs := []BootstrapPackageData{
		{Name: "widget", Package: "widget", ImportPath: "widget", FieldName: "Widget", VarName: "widget"},
		{Name: "gadget", Package: "gadget", ImportPath: "gadget", FieldName: "Gadget", VarName: "gadget"},
	}

	cases := []struct {
		rel  string
		want []string
	}{
		{"internal/widget", []string{`"example.com/proj/pkg/config"`, "func WithConfig(cfg *config.Config) TestOption", "Config: cfg.cfg"}},
		{"internal/gadget", []string{`config "example.com/proj/internal/settings"`, "func WithConfig(cfg *config.Config) TestOption", "Config: cfg.cfg"}},
		{"internal/handlers/api", []string{`"example.com/proj/pkg/config"`, "func WithConfig(cfg *config.Config) TestOption", "deps.Config = cfg.cfg"}},
	}
	for _, c := range cases {
		got := readHarness(t, root, c.rel, services, pkgs)
		for _, want := range c.want {
			if !strings.Contains(got, want) {
				t.Errorf("%s: harness missing %q:\n%s", c.rel, want, got)
			}
		}
	}
}

// TestGenerateBootstrapTesting_NoTypedConfigCompiles builds the harness of a
// contract package in a real module with no pkg/config. The string checks
// above say what the harness no longer mentions; this says the file it writes
// compiles where the old one named a package that does not exist.
func TestGenerateBootstrapTesting_NoTypedConfigCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: builds the fixture module with go vet; runs in task test")
	}
	forgeRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve forge root: %v", err)
	}
	sum, err := os.ReadFile(filepath.Join(forgeRoot, "go.sum"))
	if err != nil {
		t.Fatalf("read forge go.sum: %v", err)
	}
	root := writeHarnessFixture(t, map[string]string{
		"go.mod": "module example.com/proj\n\ngo " + pkgGoVersion(t, forgeRoot) + "\n\n" +
			"require github.com/reliant-labs/forge v0.0.0\n\n" +
			"replace github.com/reliant-labs/forge => " + filepath.ToSlash(forgeRoot) + "\n",
		"go.sum":                      string(sum),
		"internal/widget/contract.go": noConfigWidget,
	})
	readHarness(t, root, "internal/widget", nil,
		[]BootstrapPackageData{{Name: "widget", Package: "widget", ImportPath: "widget", FieldName: "Widget", VarName: "widget"}})

	// go vet type-checks the package WITH its _test.go files, which is where
	// the harness lives.
	cmd := exec.Command("go", "vet", "./internal/widget/")
	cmd.Dir = root
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off"))
	if out, verr := cmd.CombinedOutput(); verr != nil {
		helpers, _ := os.ReadFile(filepath.Join(root, "internal", "widget", componentTestHelperFile))
		t.Fatalf("the harness of a project without pkg/config does not compile: %v\n%s\n---- %s ----\n%s",
			verr, out, componentTestHelperFile, helpers)
	}
}

// pkgGoVersion is the go directive of forge's go.mod, so the fixture module
// tracks it.
func pkgGoVersion(t *testing.T, forgeRoot string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(forgeRoot, "go.mod"))
	if err != nil {
		t.Fatalf("read forge go.mod: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "go "); ok {
			return strings.TrimSpace(v)
		}
	}
	t.Fatal("forge's go.mod has no go directive")
	return ""
}

// TestWithPlaceholderDepsSaysAConfigMessageIsNotInert: WithPlaceholderDeps
// was documented as filling each field with a "never-initialised stand-in",
// but for a generated config message new(T) is a valid, empty config the
// service runs on — reliant's model endpoints read it as self-hosted, the
// permissive network policy. The generated doc says so.
func TestWithPlaceholderDepsSaysAConfigMessageIsNotInert(t *testing.T) {
	root := writeHarnessFixture(t, map[string]string{
		"internal/handlers/api/service.go": `package api

import (
	"database/sql"
	"log/slog"
)

type Deps struct {
	Logger *slog.Logger
	SQL    *sql.DB
}

type Service struct{ deps Deps }

func New(deps Deps) (*Service, error) { return &Service{deps: deps}, nil }
`,
	})
	got := readHarness(t, root, "internal/handlers/api",
		[]ServiceDef{{Name: "APIService", ModulePath: "example.com/proj"}}, nil)
	if !strings.Contains(got, "func WithPlaceholderDeps()") {
		t.Fatalf("no WithPlaceholderDeps in the harness:\n%s", got)
	}
	for _, want := range []string{"generated config message", "valid, EMPTY config"} {
		if !strings.Contains(got, want) {
			t.Errorf("WithPlaceholderDeps' doc does not say %q:\n%s", want, got)
		}
	}
}

// TestGenerateBootstrapTesting_ConfigDefaultsToProductionDefaults is the
// regression test for #749. The harness defaulted every component's config to
// `&config.Config{}`: nested config blocks nil, every proto default_value
// zero — a config production never loads. Where the config package offers
// the generated `Defaults()` (the config Load yields with nothing set), the
// harness uses it; a config package without one keeps the zero value, the
// only thing the harness can construct there.
func TestGenerateBootstrapTesting_ConfigDefaultsToProductionDefaults(t *testing.T) {
	root := writeHarnessFixture(t, map[string]string{
		// The generated pkg/config, reduced to what the harness reads.
		"pkg/config/config_gen.go": `package config

type Config struct{}

func Defaults() *Config { return &Config{} }
`,
		"internal/widget/contract.go": `package widget

import "example.com/proj/pkg/config"

type Service interface{ Name() string }

type Deps struct {
	Config *config.Config
}

func New(deps Deps) Service { return nil }
`,
		// A hand-written config package: Config, but no Defaults.
		"internal/settings/settings.go": `package settings

type Config struct{}

// Defaults is a method here, not the package-level constructor.
func (c *Config) Defaults() *Config { return c }
`,
		"internal/gadget/contract.go": `package gadget

import "example.com/proj/internal/settings"

type Service interface{ Name() string }

type Deps struct {
	Config *settings.Config
}

func New(deps Deps) Service { return nil }
`,
		"internal/handlers/api/service.go": `package api

import "example.com/proj/pkg/config"

type Deps struct {
	Config *config.Config
}

type Service struct{ deps Deps }

func New(deps Deps) (*Service, error) { return &Service{deps: deps}, nil }
`,
	})
	services := []ServiceDef{{Name: "APIService", ModulePath: "example.com/proj"}}
	pkgs := []BootstrapPackageData{
		{Name: "widget", Package: "widget", ImportPath: "widget", FieldName: "Widget", VarName: "widget"},
		{Name: "gadget", Package: "gadget", ImportPath: "gadget", FieldName: "Gadget", VarName: "gadget"},
	}

	cases := []struct {
		rel        string
		want, deny string
	}{
		{"internal/widget", "cfg:    config.Defaults(),", "&config.Config{}"},
		{"internal/handlers/api", "cfg:    config.Defaults(),", "&config.Config{}"},
		{"internal/gadget", "cfg:    &config.Config{},", "config.Defaults()"},
	}
	for _, c := range cases {
		got := readHarness(t, root, c.rel, services, pkgs)
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: harness missing %q:\n%s", c.rel, c.want, got)
		}
		if strings.Contains(got, c.deny) {
			t.Errorf("%s: harness must not contain %q:\n%s", c.rel, c.deny, got)
		}
	}
}

// TestConfigPackageDeclaresDefaults pins what counts as the generated
// constructor: a package-level, parameterless Defaults with one result, in a
// project package. Anything else would make the harness call a function
// that is not there.
func TestConfigPackageDeclaresDefaults(t *testing.T) {
	root := writeHarnessFixture(t, map[string]string{
		"pkg/config/config_gen.go":   "package config\n\ntype Config struct{}\n\nfunc Defaults() *Config { return &Config{} }\n",
		"pkg/stub/stub.go":           "package stub\n\ntype Config struct{}\n",
		"pkg/method/method.go":       "package method\n\ntype Config struct{}\n\nfunc (c *Config) Defaults() *Config { return c }\n",
		"pkg/params/params.go":       "package params\n\ntype Config struct{}\n\nfunc Defaults(env string) *Config { return &Config{} }\n",
		"pkg/testonly/a.go":          "package testonly\n\ntype Config struct{}\n",
		"pkg/testonly/a_test.go":     "package testonly\n\nfunc Defaults() *Config { return &Config{} }\n",
		"pkg/twovalues/twovalues.go": "package twovalues\n\ntype Config struct{}\n\nfunc Defaults() (*Config, error) { return &Config{}, nil }\n",
	})
	const mod = "example.com/proj"
	for importPath, want := range map[string]bool{
		mod + "/pkg/config":    true,
		mod + "/pkg/stub":      false,
		mod + "/pkg/method":    false,
		mod + "/pkg/params":    false,
		mod + "/pkg/testonly":  false,
		mod + "/pkg/twovalues": false,
		mod + "/pkg/missing":   false,
		"other.com/pkg/config": false,
	} {
		if got := ConfigPackageDeclaresDefaults(root, mod, importPath); got != want {
			t.Errorf("ConfigPackageDeclaresDefaults(%q) = %v, want %v", importPath, got, want)
		}
	}
}

// blockConfigFixture is a project whose generated pkg/config (reduced to what
// the harness reads) aliases the root config and a TraderConfig block, each
// with its generated defaults constructor, beside shapes the harness must not
// mistake for a buildable config.
func blockConfigFixture(t *testing.T, components map[string]string) string {
	t.Helper()
	files := map[string]string{
		"pkg/config/config_gen.go": `package config

import configv1 "example.com/proj/gen/config/v1"

type Config = configv1.AppConfig

func Defaults() *Config { return &Config{} }

type TraderConfig = configv1.TraderConfig

func DefaultsTraderConfig() *TraderConfig { return &TraderConfig{MaxPerTick: 10} }

// NoDefaults is aliased with no constructor: nothing proves it buildable.
type NoDefaults = configv1.NoDefaults

// Mode is not a struct, and its "constructor" returns something else.
type Mode int

func DefaultsMode() int { return 0 }
`,
		"gen/config/v1/config.pb.go": `package configv1

type AppConfig struct{ Trader *TraderConfig }

type TraderConfig struct{ MaxPerTick int32 }

type NoDefaults struct{}
`,
	}
	for rel, src := range components {
		files[rel] = src
	}
	return writeHarnessFixture(t, files)
}

// configPackage returns a contract package taking `Config <typ>` with the
// given imports.
func configPackage(name, imports, typ string) string {
	return "package " + name + "\n\nimport (\n" + imports + "\n)\n\n" +
		"type Service interface{ Name() string }\n\n" +
		"type Deps struct {\n\tConfig " + typ + "\n}\n\n" +
		"func New(deps Deps) Service { return nil }\n"
}

// TestDetectDepsConfig pins which Deps.Config fields the harness builds a
// config for, of which type, and with which defaults: the root Config from
// any package (as before), and a component's own block or per-process message
// only where the project package proves it buildable — a generated
// Defaults<M>() *<M>, or a struct of that name.
func TestDetectDepsConfig(t *testing.T) {
	const (
		cfgImp  = `	"example.com/proj/pkg/config"`
		genImp  = `	configv1 "example.com/proj/gen/config/v1"`
		cfgPath = "example.com/proj/pkg/config"
		genPath = "example.com/proj/gen/config/v1"
	)
	cases := []struct {
		name, imports, typ string
		want               DepsConfig
	}{
		{"root", cfgImp, "*config.Config", DepsConfig{cfgPath, "Config", "Defaults"}},
		{"block", cfgImp, "*config.TraderConfig", DepsConfig{cfgPath, "TraderConfig", "DefaultsTraderConfig"}},
		{"genblock", genImp, "*configv1.TraderConfig", DepsConfig{genPath, "TraderConfig", ""}},
		{"genroot", genImp, "*configv1.AppConfig", DepsConfig{genPath, "AppConfig", ""}},
		{"settings", `	"example.com/proj/internal/settings"`, "*settings.Config", DepsConfig{"example.com/proj/internal/settings", "Config", ""}},
		{"value", cfgImp, "config.TraderConfig", DepsConfig{}},                       // a copy of a proto message: never built
		{"nodefaults", cfgImp, "*config.NoDefaults", DepsConfig{}},                   // an alias nothing proves buildable
		{"mode", cfgImp, "*config.Mode", DepsConfig{}},                               // not a struct; DefaultsMode is not *Mode
		{"missing", cfgImp, "*config.Absent", DepsConfig{}},                          // not declared at all
		{"foreign", `	"other.com/lib/config"`, "*config.TraderConfig", DepsConfig{}}, // outside the project
		{"foreignroot", `	"other.com/lib/config"`, "*config.Config", DepsConfig{"other.com/lib/config", "Config", ""}},
	}
	components := map[string]string{
		"internal/settings/settings.go": "package settings\n\ntype Config struct{}\n",
	}
	for _, c := range cases {
		components["internal/"+c.name+"/contract.go"] = configPackage(c.name, c.imports, c.typ)
	}
	components["internal/none/contract.go"] = noConfigWidget
	root := blockConfigFixture(t, components)

	for _, c := range cases {
		if got := DetectDepsConfig(filepath.Join(root, "internal", c.name), root, "example.com/proj"); got != c.want {
			t.Errorf("%s (Config %s): got %+v, want %+v", c.name, c.typ, got, c.want)
		}
	}
	if got := DetectDepsConfig(filepath.Join(root, "internal", "none"), root, "example.com/proj"); got != (DepsConfig{}) {
		t.Errorf("a Deps with no Config field: got %+v, want none", got)
	}
}

// TestGenerateBootstrapTesting_BlockTypedConfig is the regression test for a
// component that takes its own config block (`Config *config.TraderConfig`,
// the composition graph's K3). The harness recognised only `*<pkg>.Config`,
// so such a component got no default and no WithConfig, and every
// NewTest<X> handed New a nil Config. Now the option and the default are
// typed as the block, and the default is the generated DefaultsTraderConfig().
func TestGenerateBootstrapTesting_BlockTypedConfig(t *testing.T) {
	root := blockConfigFixture(t, map[string]string{
		"internal/trader/contract.go": configPackage("trader", `	"example.com/proj/pkg/config"`, "*config.TraderConfig"),
		"internal/gentrader/contract.go": configPackage("gentrader",
			`	configv1 "example.com/proj/gen/config/v1"`, "*configv1.TraderConfig"),
		"internal/handlers/api/service.go": `package api

import "example.com/proj/pkg/config"

type Deps struct {
	Config *config.TraderConfig
}

type Service struct{ deps Deps }

func New(deps Deps) (*Service, error) { return &Service{deps: deps}, nil }
`,
	})
	services := []ServiceDef{{Name: "APIService", ModulePath: "example.com/proj"}}
	pkgs := []BootstrapPackageData{
		{Name: "trader", Package: "trader", ImportPath: "trader", FieldName: "Trader", VarName: "trader"},
		{Name: "gentrader", Package: "gentrader", ImportPath: "gentrader", FieldName: "Gentrader", VarName: "gentrader"},
	}

	cases := []struct {
		rel        string
		want, deny []string
	}{
		{"internal/trader", []string{
			`"example.com/proj/pkg/config"`,
			"cfg    *config.TraderConfig",
			"func WithConfig(cfg *config.TraderConfig) TestOption",
			"cfg:    config.DefaultsTraderConfig(),",
			"Config: cfg.cfg",
		}, []string{"config.Config", "config.Defaults()"}},
		{"internal/handlers/api", []string{
			"func WithConfig(cfg *config.TraderConfig) TestOption",
			"cfg:    config.DefaultsTraderConfig(),",
			"deps.Config = cfg.cfg",
		}, []string{"config.Config", "config.Defaults()"}},
		// The generated proto type itself: no constructor there, so the zero
		// block, as for a config package without Defaults().
		{"internal/gentrader", []string{
			`config "example.com/proj/gen/config/v1"`,
			"func WithConfig(cfg *config.TraderConfig) TestOption",
			"cfg:    &config.TraderConfig{},",
		}, []string{"config.Config", "Defaults"}},
	}
	for _, c := range cases {
		got := readHarness(t, root, c.rel, services, pkgs)
		for _, want := range c.want {
			if !strings.Contains(got, want) {
				t.Errorf("%s: harness missing %q:\n%s", c.rel, want, got)
			}
		}
		for _, deny := range c.deny {
			if strings.Contains(got, deny) {
				t.Errorf("%s: harness must not contain %q:\n%s", c.rel, deny, got)
			}
		}
	}
}

// TestGenerateBootstrapTesting_BlockTypedConfigCompiles runs a test inside a
// real module against the harness of a package that takes its own block: the
// file compiles, and NewTest<X> hands New the block DefaultsTraderConfig()
// builds rather than nil.
func TestGenerateBootstrapTesting_BlockTypedConfigCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: builds and tests the fixture module with go test; runs in task test")
	}
	forgeRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve forge root: %v", err)
	}
	sum, err := os.ReadFile(filepath.Join(forgeRoot, "go.sum"))
	if err != nil {
		t.Fatalf("read forge go.sum: %v", err)
	}
	root := blockConfigFixture(t, map[string]string{
		"go.mod": "module example.com/proj\n\ngo " + pkgGoVersion(t, forgeRoot) + "\n\n" +
			"require github.com/reliant-labs/forge v0.0.0\n\n" +
			"replace github.com/reliant-labs/forge => " + filepath.ToSlash(forgeRoot) + "\n",
		"go.sum": string(sum),
		"internal/trader/contract.go": `package trader

import "example.com/proj/pkg/config"

type Service interface{ MaxPerTick() int32 }

type Deps struct {
	Config *config.TraderConfig
}

type trader struct{ cfg *config.TraderConfig }

func (t trader) MaxPerTick() int32 { return t.cfg.MaxPerTick }

func New(deps Deps) Service { return trader{cfg: deps.Config} }
`,
		"internal/trader/probe_test.go": `package trader

import (
	"testing"

	"example.com/proj/pkg/config"
)

func TestHarnessBlockConfig(t *testing.T) {
	if got := NewTestTrader(t).MaxPerTick(); got != 10 {
		t.Fatalf("default block: MaxPerTick = %d, want 10 from DefaultsTraderConfig", got)
	}
	if got := NewTestTrader(t, WithConfig(&config.TraderConfig{MaxPerTick: 3})).MaxPerTick(); got != 3 {
		t.Fatalf("WithConfig: MaxPerTick = %d, want 3", got)
	}
}
`,
	})
	readHarness(t, root, "internal/trader", nil,
		[]BootstrapPackageData{{Name: "trader", Package: "trader", ImportPath: "trader", FieldName: "Trader", VarName: "trader"}})

	cmd := exec.Command("go", "test", "-count=1", "./internal/trader/")
	cmd.Dir = root
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off"))
	if out, terr := cmd.CombinedOutput(); terr != nil {
		helpers, _ := os.ReadFile(filepath.Join(root, "internal", "trader", componentTestHelperFile))
		t.Fatalf("the harness of a block-typed config does not build or default it: %v\n%s\n---- %s ----\n%s",
			terr, out, componentTestHelperFile, helpers)
	}
}
