package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// envConfigFixture is a rendered KCL contract with one service, one job and
// one frontend, each carrying config. It exercises the flattening across
// workload kinds without needing a KCL toolchain.
const envConfigFixture = `{
  "output": {
    "frontends": [
      {
        "name": "web",
        "env_vars": [
          {
            "name": "API_URL",
            "value": "http://localhost:8080"
          }
        ]
      }
    ],
    "workloads": [
      {
        "name": "api",
        "kind": "service",
        "runtime": {
          "type": "host"
        },
        "spec": {
          "kind": "service",
          "env": [
            {
              "name": "DATABASE_URL",
              "value": "postgres://localhost:5433/app"
            },
            {
              "name": "LOG_LEVEL",
              "value": "info"
            }
          ]
        }
      },
      {
        "name": "postgres",
        "kind": "service",
        "runtime": {
          "type": "host"
        },
        "spec": {
          "kind": "service"
        }
      },
      {
        "name": "migrate",
        "kind": "job",
        "runtime": {
          "type": "cluster"
        },
        "spec": {
          "kind": "job",
          "env": [
            {
              "name": "DATABASE_URL",
              "value": "postgres://localhost:5433/app"
            }
          ]
        }
      }
    ]
  }
}`

// withKCLFixture points RenderKCL at a literal contract for the duration of
// the test.
func withKCLFixture(t *testing.T, contract string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "render.json")
	if err := os.WriteFile(path, []byte(contract), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", path)
	// Run from a project of the test's own. From the package dir the command
	// walks up to FORGE's forge.yaml and asks git about forge's checkout
	// (worktree, branch), state a cached result cannot see change.
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "forge.yaml"),
		[]byte("name: demo\nmodule_path: github.com/example/demo\n"), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	t.Chdir(project)
}

// runEnvConfig executes the command and returns stdout.
func runEnvConfig(t *testing.T, args ...string) string {
	t.Helper()
	cmd := newEnvConfigCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("env config %v: %v", args, err)
	}
	return out.String()
}

// TestEnvConfigReportsEveryWorkloadKind is the core contract: the command
// answers "what is this environment configured with" for every kind of
// workload, not just services. A job that carries the only DATABASE_URL in
// an environment is a real shape (migrations-only envs), and omitting jobs
// would report that environment as having no database at all.
func TestEnvConfigReportsEveryWorkloadKind(t *testing.T) {
	withKCLFixture(t, envConfigFixture)

	var report envConfigReport
	if err := json.Unmarshal([]byte(runEnvConfig(t, "dev", "--json")), &report); err != nil {
		t.Fatalf("decode --json: %v", err)
	}

	byName := map[string]envConfigWorkload{}
	for _, w := range report.Workloads {
		byName[w.Name] = w
	}
	for name, kind := range map[string]string{
		"api": "service", "migrate": "job", "web": "frontend",
	} {
		w, ok := byName[name]
		if !ok {
			t.Errorf("workload %q missing from the report", name)
			continue
		}
		if w.Kind != kind {
			t.Errorf("workload %q kind = %q, want %q", name, w.Kind, kind)
		}
	}
	if got := byName["api"].Env["DATABASE_URL"]; got != "postgres://localhost:5433/app" {
		t.Errorf("api DATABASE_URL = %q, want the rendered value", got)
	}
	if got := byName["web"].Env["API_URL"]; got != "http://localhost:8080" {
		t.Errorf("web API_URL = %q, want the rendered value", got)
	}
}

// TestEnvConfigIsTechnologyAgnostic pins the design decision behind this
// command existing at all instead of a `db dsn`.
//
// forge must not privilege one variable name: a project may run two
// databases, none, or reach its store over something that is not a DSN. The
// command prints what the environment declares and the CALLER selects — so
// the report has to carry variables forge has no special knowledge of, and
// must not drop or rename anything.
func TestEnvConfigIsTechnologyAgnostic(t *testing.T) {
	withKCLFixture(t, `{
  "output": {
    "workloads": [
      {
        "name": "api",
        "kind": "service",
        "runtime": {
          "type": "host"
        },
        "spec": {
          "kind": "service",
          "env": [
            {
              "name": "SURREALDB_ENDPOINT",
              "value": "ws://localhost:8000"
            },
            {
              "name": "SOME_VENDOR_TOKEN",
              "value": "abc123"
            }
          ]
        }
      }
    ]
  }
}`)

	var report envConfigReport
	if err := json.Unmarshal([]byte(runEnvConfig(t, "dev", "--json")), &report); err != nil {
		t.Fatalf("decode --json: %v", err)
	}
	if len(report.Workloads) != 1 {
		t.Fatalf("want 1 workload, got %d", len(report.Workloads))
	}
	env := report.Workloads[0].Env
	if env["SURREALDB_ENDPOINT"] != "ws://localhost:8000" {
		t.Errorf("a store forge has never heard of must still be reported; got %+v", env)
	}
	if env["SOME_VENDOR_TOKEN"] != "abc123" {
		t.Errorf("every declared variable is reported verbatim; got %+v", env)
	}
}

// TestEnvConfigMergesHostDeployEnv covers the two places a service's config
// lives: the top-level per-env block every lowering sees, and the host
// deploy block `forge env up` layers on when the service runs as a host
// process. Reading only one of them under-reports what the process is
// actually launched with, which is the whole question this command answers.
func TestEnvConfigMergesHostDeployEnv(t *testing.T) {
	withKCLFixture(t, `{
  "output": {
    "workloads": [
      {
        "name": "api",
        "kind": "service",
        "runtime": {
          "type": "host"
        },
        "spec": {
          "kind": "service",
          "env": [
            {
              "name": "LOG_LEVEL",
              "value": "info"
            },
            {
              "name": "DATABASE_URL",
              "value": "postgres://localhost:5433/app"
            }
          ]
        }
      }
    ]
  }
}`)

	var report envConfigReport
	if err := json.Unmarshal([]byte(runEnvConfig(t, "dev", "--json")), &report); err != nil {
		t.Fatalf("decode: %v", err)
	}
	env := report.Workloads[0].Env
	if env["LOG_LEVEL"] != "info" {
		t.Errorf("top-level config missing from the report; got %+v", env)
	}
	if env["DATABASE_URL"] != "postgres://localhost:5433/app" {
		t.Errorf("host deploy config missing from the report; got %+v", env)
	}
}

// TestEnvConfigFiltersByWorkload covers --workload, including the error for
// a name the environment does not declare (silently printing nothing would
// read as "this workload has no config").
func TestEnvConfigFiltersByWorkload(t *testing.T) {
	withKCLFixture(t, envConfigFixture)

	out := runEnvConfig(t, "dev", "--workload", "migrate", "--json")
	var report envConfigReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(report.Workloads) != 1 || report.Workloads[0].Name != "migrate" {
		t.Fatalf("--workload must narrow to the named one; got %+v", report.Workloads)
	}

	cmd := newEnvConfigCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{"dev", "--workload", "nonexistent"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("an unknown workload name must be an error, not an empty report")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should name the workload asked for; got %v", err)
	}
}

// TestEnvConfigTableIsGrouped covers the human view: a workload header plus
// its variables, sorted so successive runs diff cleanly.
func TestEnvConfigTableIsGrouped(t *testing.T) {
	withKCLFixture(t, envConfigFixture)

	out := runEnvConfig(t, "dev")
	if !strings.Contains(out, "api (service)") {
		t.Errorf("table should head each block with the workload and kind; got:\n%s", out)
	}
	if !strings.Contains(out, "migrate (job)") {
		t.Errorf("table should include jobs; got:\n%s", out)
	}
	// A workload declaring nothing says so, rather than rendering a bare
	// header that reads like truncated output.
	if !strings.Contains(out, "no configuration declared") {
		t.Errorf("a workload with no env should say so; got:\n%s", out)
	}
	if strings.Index(out, "DATABASE_URL") > strings.Index(out, "LOG_LEVEL") {
		t.Errorf("variables should be sorted by name; got:\n%s", out)
	}
}

// Forge report 2186e95b: `forge env config dev --json | jq` must read one
// document. A frontend whose env_vars shadow a typed config value makes the
// render warn, and that warning used to land on stdout above the JSON.
func TestEnvConfigJSONStdoutIsTheDocumentAlone(t *testing.T) {
	withKCLFixture(t, `{"output": {"frontends": [{
	  "name": "web", "type": "vite-spa",
	  "config": {"api_url": "http://localhost:8080"},
	  "env_vars": [{"name": "VITE_API_URL", "value": "http://localhost:9090"}]
	}]}}`)
	stdout, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	prevOut, prevErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdout, stderr
	cmd := newEnvConfigCmd()
	cmd.SetArgs([]string{"dev", "--json"})
	runErr := cmd.Execute()
	os.Stdout, os.Stderr = prevOut, prevErr
	// Closed before t.TempDir's cleanup: Windows refuses to remove an open file.
	_ = stdout.Close()
	_ = stderr.Close()
	if runErr != nil {
		t.Fatalf("env config dev --json: %v", runErr)
	}
	out, _ := os.ReadFile(stdout.Name())
	diag, _ := os.ReadFile(stderr.Name())

	var report envConfigReport
	dec := json.NewDecoder(bytes.NewReader(out))
	if err := dec.Decode(&report); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\nstdout:\n%s", err, out)
	}
	if rest, _ := io.ReadAll(dec.Buffered()); len(bytes.TrimSpace(rest)) != 0 {
		t.Errorf("stdout carries more than the document: %q", rest)
	}
	if !strings.Contains(string(diag), "overriding the typed config value") {
		t.Errorf("the shadowing warning should be on stderr; stderr:\n%s", diag)
	}
}
