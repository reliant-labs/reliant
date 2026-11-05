package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

// The deploy checks render each deploy/kcl/<env> package, and that render
// must be told WHICH env it is.
//
// `option("env")` is the builtin every env-conditional in a deploy module
// keys off, and forge's own kcl/schema.k gates on it — RenderedSecretKey's
// `from = "literal"` check reads `option("env") in ["dev", "e2e"]` to allow an
// inlined secret in a throwaway environment and refuse it everywhere else.
// Rendering with no `-D env=<name>` leaves the option Undefined, so that
// check fails for EVERY environment including the two it explicitly permits,
// and `forge doctor` / `forge ci validate-kcl` report a project as
// non-applyable over a conditional that would have been satisfied.
//
// renderKCLRaw (internal/cli) has always passed `env=<env>`. This pins the
// doctor's own renderer to the same contract, since the two disagreeing is
// precisely the failure: `forge env deploy e2e` renders fine and CI rejects
// the identical source.
func TestRenderDeployEnvs_PassesTheEnvNameAsAKCLOption(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL through the embedded runtime")
	}
	projectDir := t.TempDir()

	// A main.k whose output is a function of option("env") alone. If the
	// option does not arrive, `observed` renders as the undefined marker and
	// no amount of manifest shape can disguise it.
	writeEnvProbeModule(t, projectDir, "e2e")
	writeEnvProbeModule(t, projectDir, "prod")

	renders := renderDeployEnvs(projectDir, nil)
	if len(renders) != 2 {
		t.Fatalf("expected both env packages to render, got %d", len(renders))
	}

	for _, r := range renders {
		if r.err != nil {
			t.Fatalf("%s: render failed: %v", r.env, r.err)
		}
		if len(r.objects) != 1 {
			t.Fatalf("%s: expected the probe ConfigMap, got %d object(s)", r.env, len(r.objects))
		}
		observed, _ := r.objects[0].Spec["observed"].(string)
		if observed != r.env {
			t.Errorf("%s: deploy module observed option(\"env\") = %q, want %q.\n"+
				"The module cannot see which environment it is, so every env-conditional "+
				"(including forge's own RenderedSecretKey dev/e2e allowance in "+
				"kcl/schema.k) evaluates against an undefined option — and the doctor "+
				"reports a project non-applyable over a check that would have passed.",
				r.env, observed, r.env)
		}
	}
}

// writeEnvProbeModule lays down deploy/kcl/<env>/main.k that echoes
// option("env") into an otherwise-valid manifest render.
func writeEnvProbeModule(t *testing.T, projectDir, env string) {
	t.Helper()
	dir := filepath.Join(projectDir, "deploy", "kcl", env)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// `spec` rather than a ConfigMap's `data`, because that is the field the
	// doctor's own k8sObject parse retains — the probe has to be readable
	// through the same struct the real checks use.
	// Under `output.manifests`, the one applyable stream a render exports
	// (a top-level `manifests` is a stray root no deploy reads).
	body := `_env = option("env")

output = {
    manifests = [
        {
            apiVersion = "v1"
            kind = "ConfigMap"
            metadata = {name = "env-probe"}
            spec = {observed = _env}
        }
    ]
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.k"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
