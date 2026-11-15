package templates_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/internal/kclvendor"
)

// TestReleaseBuildScaffoldResolvesAndRenders is the regression guard for
// the bug that shipped: a RELEASE build of forge scaffolded a project
// whose KCL dependency could not be resolved at all.
//
// Every test that existed when that shipped asserted the dependency
// STRING — that kcl.mod carried the expected `tag = "kcl-v0.1.0"` line —
// and each of them passed, because the line was written exactly as
// intended. The tag had simply never been published. A string assertion
// cannot tell the difference between a dependency that resolves and one
// that does not, so this test resolves it instead: it renders the
// scaffold through forge's own evaluation seam and requires real
// manifests to come out the other end.
//
// It also pins the release/dev question closed. The scaffold is produced
// with a stamped pkg version — the exact discriminator that used to
// select the broken path — so if a release-only branch is ever
// reintroduced here, this fails.
//
// NOT parallel: buildinfo is process-global, and the test asserts the
// offline claim by breaking git for its own duration.
func TestReleaseBuildScaffoldResolvesAndRenders(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds and generates a full project; runs in task test")
	}
	// Build the project the way a released forge binary does.
	buildinfo.SetDevBuild(false)
	defer buildinfo.ClearDevBuild()

	// Offline, air-gapped, no credentials. A git-tag dependency needs the
	// network and git auth at render time; the vendored copy needs
	// neither, and this is where that claim is actually tested. `none` is
	// not a protocol git will ever allow, so any attempt to fetch a
	// remote dependency fails here instead of silently succeeding on a
	// machine that happens to have network and a cached credential.
	t.Setenv("GIT_ALLOW_PROTOCOL", "none")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")

	tmp := t.TempDir()
	g := generator.NewProjectGenerator("rel-render", tmp, "example.com/rel-render")
	g.Kind = config.ProjectKindService
	g.ApplyKindFeatureDefaults(config.ProjectKindService)
	if err := g.Generate(); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// A release scaffold must be born resolvable: the module materialized
	// and the dependency pointing at it. Not a string check for its own
	// sake — it localizes the failure when the render below breaks.
	kclModPath := filepath.Join(tmp, "deploy", "kcl", "kcl.mod")
	kclMod, err := os.ReadFile(kclModPath)
	if err != nil {
		t.Fatalf("read deploy/kcl/kcl.mod: %v", err)
	}
	if has, _ := kclvendor.HasForgeDep(kclModPath); has {
		t.Fatalf("release scaffold declares the forge KCL module — the binary supplies it; kcl.mod must not:\n%s", kclMod)
	}
	if _, err := os.Stat(filepath.Join(tmp, kclvendor.LegacyVendorDirName)); !os.IsNotExist(err) {
		t.Fatalf("release scaffold materialized a project-local %s/ (stat err %v)", kclvendor.LegacyVendorDirName, err)
	}

	// The pipeline-generated config trio, which a bare Generate has not
	// produced yet. Same stubs as TestScaffoldedIngressEvaluates.
	// Same stub as TestScaffoldedIngressEvaluates, including the two-arg
	// projection signature and a sensitive field, so this render also
	// exercises the per-workload secret gate rather than only the
	// credential-free path.
	configGenStub := `import forge

schema ConfigSecretRef:
    name: str
    key: str

schema AppConfig:
    port: int = 8080
    database_url: ConfigSecretRef = ConfigSecretRef { name = "app-secrets", key = "database_url" }

APP_CONFIG_SENSITIVE_ENV: [str] = ["DATABASE_URL"]

appConfigEnvMap = lambda c: AppConfig, config_secrets: [str] -> {str: str | forge.SecretRef} {
    _sensitive: {str: forge.SecretRef} = {
        "DATABASE_URL" = forge.SecretRef {name = c.database_url.name, key = c.database_url.key, store_key = "DATABASE_URL"}
    }
    assert all _n in config_secrets { _n in _sensitive }, "unknown config_secrets name"
    {
        "PORT" = str(c.port)
    } | {_k: _sensitive[_k] for _k in _sensitive if _k in config_secrets}
}
`
	if err := os.WriteFile(filepath.Join(tmp, "deploy/kcl", "config_gen.k"), []byte(configGenStub+serverRootStub(t, "DATABASE_URL")), 0644); err != nil {
		t.Fatalf("write config_gen.k stub: %v", err)
	}
	for _, env := range []string{"dev", "staging", "prod"} {
		configStub := `import config_gen

app_config: config_gen.AppConfig = {
}
`
		if err := os.WriteFile(filepath.Join(tmp, "deploy/kcl", env, "config.k"), []byte(configStub), 0644); err != nil {
			t.Fatalf("write %s config.k stub: %v", env, err)
		}
	}

	// THE ASSERTION THAT WOULD HAVE CAUGHT THIS: resolve the dependency
	// and render. Every env, because a per-env kcl.mod depth mistake only
	// shows in the env that has it.
	for _, env := range []string{"dev", "staging", "prod"} {
		out, err := kclrender.Run(tmp, filepath.Join(tmp, "deploy/kcl", env), []string{"env=" + env})
		if err != nil {
			t.Fatalf("render %s env from a release-build scaffold: %v", env, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(out, &doc); err != nil {
			t.Fatalf("unmarshal %s render: %v\n%s", env, err, out)
		}
		// Rendering "successfully" to nothing would satisfy a weaker
		// check, so require the contract and the applyable stream the deploy
		// path consumes: `output = forge.render(bundle)` is the one
		// entrypoint, and its `manifests` must not be empty.
		output, ok := doc["output"].(map[string]any)
		if !ok {
			t.Fatalf("%s render has no `output` contract:\n%s", env, out)
		}
		if _, legacy := doc["manifests"]; legacy {
			t.Fatalf("%s render has a top-level `manifests` var; output.manifests is the only stream", env)
		}
		// dev applies objects to a cluster forge creates; staging and prod
		// are hosted as scaffolded, so what they render is published to the
		// control plane — workloads and a managed database — and their
		// manifest stream is legitimately empty.
		if manifests, ok := output["manifests"].([]any); env == "dev" && (!ok || len(manifests) == 0) {
			t.Fatalf("%s render produced no output.manifests:\n%s", env, out)
		}
		if ws, _ := output["workloads"].([]any); len(ws) == 0 {
			t.Fatalf("%s render declares no workloads:\n%s", env, out)
		}
		if dbs, _ := output["databases"].([]any); env != "dev" && len(dbs) == 0 {
			t.Fatalf("%s render declares no managed database:\n%s", env, out)
		}
	}
}
