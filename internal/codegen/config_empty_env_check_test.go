package codegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/kcltest"
)

// emptyEnvCheckMessages is a config with a defaulted root str (with a flag),
// a block whose str leaf is defaulted (no flag), and fields the check must
// leave alone: a str whose default is empty, an int, and a secret.
func emptyEnvCheckMessages() []ConfigMessage {
	return []ConfigMessage{
		{Name: "AppConfig", Fields: []ConfigField{
			{Name: "pprof_addr", GoName: "PprofAddr", GoType: "string", ProtoType: "string", EnvVar: "PPROF_ADDR", Flag: "pprof-addr", DefaultValue: "127.0.0.1:6060"},
			{Name: "public_url", GoName: "PublicUrl", GoType: "string", ProtoType: "string", EnvVar: "PUBLIC_URL"},
			{Name: "port", GoName: "Port", GoType: "int32", ProtoType: "int32", EnvVar: "PORT", DefaultValue: "8080"},
			{Name: "api_key", GoName: "ApiKey", GoType: "string", ProtoType: "string", EnvVar: "API_KEY", Sensitive: true, Optional: true},
			{Name: "temporal", GoName: "Temporal", ProtoType: "message", MessageType: "TemporalConfig"},
		}},
		{Name: "TemporalConfig", Fields: []ConfigField{
			{Name: "host", GoName: "Host", GoType: "string", ProtoType: "string", EnvVar: "TEMPORAL_HOST", DefaultValue: "localhost"},
		}},
	}
}

// TestEmptyEnvCheck_RendersOnlyForDefaultedStrings pins which fields get a
// check: a str whose default is non-empty, in the root schema and in a block.
func TestEmptyEnvCheck_RendersOnlyForDefaultedStrings(t *testing.T) {
	module, err := GenerateConfigKCL(FlattenBlockLeaves(emptyEnvCheckMessages(), "AppConfig"), "proj")
	if err != nil {
		t.Fatalf("GenerateConfigKCL: %v", err)
	}
	for _, want := range []string{
		`pprof_addr != "", "pprof_addr = \"\" cannot reach the process: an empty PPROF_ADDR counts as unset, so it would load the default \"127.0.0.1:6060\" — set a value, or pass --pprof-addr= in the workload's args to make it empty"`,
		`host != "", "host = \"\" cannot reach the process: an empty TEMPORAL_HOST counts as unset, so it would load the default \"localhost\" — set a value, or change its default in the proto"`,
	} {
		if !strings.Contains(module, want) {
			t.Errorf("module missing check %s\n%s", want, module)
		}
	}
	for _, field := range []string{"public_url !=", "port !=", "api_key !="} {
		if strings.Contains(module, field) {
			t.Errorf("module checks %q, which has no default to fall back to (or is not a str):\n%s", field, module)
		}
	}
}

// TestEmptyEnvCheck_RefusesAnEmptyValueTheProcessWouldIgnore evaluates the
// module with real kcl. `pprof_addr = ""` used to render `PPROF_ADDR: ""`,
// which the loader reads as unset, so the process kept pprof on at the
// default address while the manifest said off. The render fails instead.
func TestEmptyEnvCheck_RefusesAnEmptyValueTheProcessWouldIgnore(t *testing.T) {
	if testing.Short() {
		t.Skip("runs kcl in a subprocess; runs in task test")
	}
	if _, err := exec.LookPath("kcl"); err != nil {
		t.Skip("kcl not on PATH")
	}
	module, err := GenerateConfigKCL(FlattenBlockLeaves(emptyEnvCheckMessages(), "AppConfig"), "proj")
	if err != nil {
		t.Fatalf("GenerateConfigKCL: %v", err)
	}

	run := func(t *testing.T, instance string) (string, error) {
		t.Helper()
		dir := t.TempDir()
		for name, body := range map[string]string{
			"kcl.mod":                 "[package]\nname = \"empty_env_check\"\n",
			ConfigSchemaModule + ".k": module,
			"forge/kcl.mod":           "[package]\nname = \"forge\"\n",
			"forge/core.k":            "schema SecretRef:\n    name: str\n    key: str\n    optional?: bool\n    store_key?: str\n",
			"main.k":                  "import " + ConfigSchemaModule + " as config_gen\n\n_c = config_gen.AppConfig " + instance + "\nenv = config_gen.appConfigEnvMap(_c, [])\n",
		} {
			p := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		out, err := kcltest.Run(t.Context(), dir, "run", ".", "--format", "json")
		return string(out), err
	}

	if out, err := run(t, `{public_url = "", pprof_addr = "0.0.0.0:6060", temporal.host = "temporal:7233"}`); err != nil {
		t.Fatalf("values the process can see must render: %v\n%s", err, out)
	}
	for name, instance := range map[string]string{
		"root field":  `{pprof_addr = ""}`,
		"block field": `{temporal.host = ""}`,
	} {
		t.Run(name, func(t *testing.T) {
			out, err := run(t, instance)
			if err == nil {
				t.Fatalf("an empty value the loader reads as unset rendered cleanly:\n%s", out)
			}
			if !strings.Contains(out+err.Error(), "cannot reach the process") {
				t.Fatalf("the render failed without the check's message: %v\n%s", err, out)
			}
		})
	}
}
