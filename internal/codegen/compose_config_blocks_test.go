package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeGeneratedConfig writes the two generated halves compose.go reads a
// project's config blocks from: pkg/config's alias of the root message, and
// the root's generated struct with the given extra fields.
func writeGeneratedConfig(t *testing.T, projectDir, rootFields string) {
	t.Helper()
	files := map[string]string{
		"pkg/config/config_gen.go": `package config

import configv1 "example.com/proj/gen/config/v1"

type Config = configv1.AppConfig

type AlpacaConfig = configv1.AlpacaConfig
`,
		"gen/config/v1/config.pb.go": `package configv1

import (
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	durationpb "google.golang.org/protobuf/types/known/durationpb"
)

type AppConfig struct {
	state         protoimpl.MessageState
	Port          int32
	PreStopDelay  *durationpb.Duration
` + rootFields + `
}

type AlpacaConfig struct {
	BaseUrl string
}
`,
	}
	for rel, src := range files {
		p := filepath.Join(projectDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// alpacaAdapter is the scaffolded adapter's Deps shape (#748): its base URL
// is a leaf of its config block, taken whole as Cfg.
const alpacaAdapter = `package alpaca

import (
	"log/slog"

	"example.com/proj/pkg/config"
)

type Service interface{ Do() }

type Deps struct {
	Logger *slog.Logger
	Cfg    *config.AlpacaConfig
}

func New(d Deps) (Service, error) { return nil, nil }
`

// TestGenerateCompose_ConfigBlockResolvesByType is the regression test for
// #748's composition half. A Deps field typed as a config block the loaded
// Config composes — the shape forge-config-deps asks for instead of a naked
// scalar — is filled from it by type. Before, nothing resolved it: the
// conventional name Cfg matched Infra's whole-config field instead, handing
// the block field a *config.Config, and the scaffold only avoided that by
// declaring the scalar the lint rejects (compose.go: `BaseURL: "", // TODO`).
func TestGenerateCompose_ConfigBlockResolvesByType(t *testing.T) {
	dir := newInjectProject(t)
	writeInfra(t, dir, "\tLog *slog.Logger\n\tCfg *config.Config")
	writeGeneratedConfig(t, dir, "\tAlpaca *AlpacaConfig")
	writeContract(t, dir, "internal/alpaca", alpacaAdapter)

	err := GenerateCompose(InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Packages: []BootstrapPackageData{
			{Name: "alpaca", Package: "alpaca", ImportPath: "alpaca", FieldName: "Alpaca", VarName: "alpaca", Fallible: true},
		},
	})
	if err != nil {
		t.Fatalf("GenerateCompose: %v", err)
	}
	out := readInject(t, dir)
	flat := strings.Join(strings.Fields(out), " ")
	if !strings.Contains(flat, "Cfg: infra.Cfg.GetAlpaca(),") {
		t.Fatalf("the adapter's config block must be wired from the loaded config by type:\n%s", out)
	}
	if strings.Contains(out, "TODO") {
		t.Fatalf("the scaffolded adapter's construction must carry no TODO hint:\n%s", out)
	}
}

// The resolution is by TYPE and proven, never by a name that looks right.
func TestConfigBlocks_Resolve(t *testing.T) {
	dir := t.TempDir()
	writeGeneratedConfig(t, dir, "\tAlpaca *AlpacaConfig\n\tBackup *AlpacaConfig\n\tTrader *TraderConfig")
	blocks := parseConfigBlocks(dir, "example.com/proj")
	imports := map[string]string{
		"config":   "example.com/proj/pkg/config",
		"configv1": "example.com/proj/gen/config/v1",
		"other":    "example.com/other/config",
	}
	for _, c := range []struct {
		depType, want string
	}{
		{"*config.TraderConfig", "infra.Cfg.GetTrader()"},
		{"*configv1.TraderConfig", "infra.Cfg.GetTrader()"}, // the generated type itself
		{"config.TraderConfig", ""},                         // a copy of a proto message is never wired
		{"*other.TraderConfig", ""},                         // another package's same-named type
		{"*config.AlpacaConfig", ""},                        // composed twice: ambiguous
		{"*config.Duration", ""},                            // not composed at all
		{"*durationpb.Duration", ""},                        // a well-known leaf, not a block
	} {
		got, ok := blocks.resolve(c.depType, imports)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("resolve(%q) = %q, %v; want %q", c.depType, got, ok, c.want)
		}
	}

	if none := parseConfigBlocks(t.TempDir(), "example.com/proj"); len(none.getters) != 0 {
		t.Errorf("a project with no generated config has no blocks, got %v", none.getters)
	}
}
