package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/audittype"
	"github.com/reliant-labs/forge/internal/config"
)

// writeAuditDevProject writes a forge project whose ONLY env is dev, with the
// given deploy/kcl/dev/main.k. There is deliberately no secrets/dev.yaml: every
// fixture is a fresh clone, because that store is gitignored and no clone,
// CI runner or teammate has it.
func writeAuditDevProject(t *testing.T, mainK string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"forge.yaml": "name: acme\nmodule_path: github.com/example/acme\n\n",
		// No `forge` dependency: the forge KCL module is supplied by the
		// rendering binary (ADR 0003), so a project's kcl.mod never names it.
		"deploy/kcl/kcl.mod":    "[package]\nname = \"acme_deploy\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n",
		"deploy/kcl/dev/main.k": mainK,
	}
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "secrets", "dev.yaml")); err == nil {
		t.Fatal("fixture must not carry a local secret store")
	}
	return dir
}

// auditDevFixture renders one workload that reads a credential from the
// "acme-secrets" Secret, bound to `runtime` under `provider` — the scaffolded
// dev shape on the workload model (ADR 0002). The SAME declaration is used by
// every case; only where it runs and who supplies its Secret vary, which are
// exactly the two facts the audit's supply check turns on.
func auditDevFixture(runtime, provider string) string {
	return auditDevFixtureTargeting(runtime, provider, "    cluster_target = _cluster\n")
}

// auditDevFixtureTargeting is auditDevFixture with the env-wide
// cluster_target line supplied by the caller ("" for an env that names no
// cluster at all).
func auditDevFixtureTargeting(runtime, provider, clusterTarget string) string {
	return `import forge
import forge.workloads as fw

_cluster = forge.ClusterTarget {
    cluster = "k3d-acme"
    namespace = "acme-dev"
}
_bundle = forge.Bundle {
` + clusterTarget + `    project = "acme"
    workloads = [fw.Workload {
        name = "api"
        image = "localhost:5050/acme"
        build = forge.GoBuild {cmd = "./cmd/acme", output_name = "acme"}
        args = ["server"]
        ports = [fw.Port {name = "http", port = 8080, expose = True}]
        env = {STRIPE_SECRET_KEY = forge.SecretRef {name = "acme-secrets", key = "stripe_secret_key"}}
        runtime = ` + runtime + `
    }]
    secret_provider = ` + provider + `
}
output = forge.render(_bundle)
`
}

// The scaffolded dev shape: the workload runs as a HOST process fed by the
// FileSecrets store, and the env still names a cluster_target for its support
// objects (Namespace, gateways), which forge DOES apply. A host-bound workload
// never enters that stream, so its Secret reference is not cluster demand and
// nothing can FailedMount — whether or not the store exists on this clone.
var auditDevHostPlaced = auditDevFixture(`forge.OnHost {listen_ports = [8080]}`, `forge.FileSecrets {path = "secrets/dev.yaml"}`)

// The same host workload in an env that names no cluster at all: forge
// applies nothing, so the supply check has no stream to judge and says so.
var auditDevHostOnly = auditDevFixtureTargeting(`forge.OnHost {listen_ports = [8080]}`, `forge.FileSecrets {path = "secrets/dev.yaml"}`, "")

// The same workload placed IN the cluster, with FileSecrets declared: the
// provider renders "acme-secrets" from its store at deploy time, so the mount
// is declared. That holds whether or not this checkout's store exists — the
// DECLARATION is what the audit checks; a missing value is `forge secret
// ensure`'s job, reported by deploy/up when it actually needs one.
var auditDevClusterPlacedFileSecrets = auditDevFixture(`forge.OnCluster {target = _cluster}`, `forge.FileSecrets {path = "secrets/dev.yaml"}`)

// A genuinely wrong DECLARATION: the workload runs in the cluster and reads
// "acme-secrets", but the env's provider (ExternalSecrets) never supplies a
// Secret itself and no forge.ExternalSecret promises this one. Nothing can
// ever provide it; the pod would sit in CreateContainerConfigError.
var auditDevClusterPlacedUndeclared = auditDevFixture(`forge.OnCluster {target = _cluster}`, `forge.ExternalSecrets {}`)

func auditPrereqsFor(t *testing.T, mainK string) audittype.Category {
	t.Helper()
	dir := writeAuditDevProject(t, mainK)
	cat := auditPrerequisites(&config.ProjectConfig{Name: "acme"}, dir)
	if strings.Contains(cat.Summary, "could not evaluate dev KCL") {
		t.Fatalf("fixture did not render: %s", cat.Summary)
	}
	return cat
}

func auditFindings(cat audittype.Category) string {
	findings, _ := cat.Details["findings"].([]string)
	return strings.Join(findings, "\n")
}

// TestAuditPrerequisites_HostPlacedWorkloadOnFreshClone is the reported bug:
// `forge project audit` failed with `undeclared-mount: Secret "<project>-secrets"`
// on every checkout of a scaffolded project whose workload declares a
// config_secrets credential, which made the scaffolded pre-push hook block
// every push. The mount was in a Deployment nothing applies.
func TestAuditPrerequisites_HostPlacedWorkloadOnFreshClone(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; skipped in -short")
	}
	for _, tc := range []struct {
		name, mainK string
		// supplyChecked: whether forge applies a manifest stream for this
		// env at all. The scaffold shape does (its support objects land in
		// the named cluster), so the check runs — and must find nothing,
		// because a host-bound workload never enters that stream.
		supplyChecked bool
	}{
		{"scaffold shape: support objects in a cluster", auditDevHostPlaced, true},
		{"no cluster named", auditDevHostOnly, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cat := auditPrereqsFor(t, tc.mainK)
			if cat.Status == audittype.StatusError {
				t.Fatalf("status = error, want ok: a host-placed workload's Secret is never mounted by a pod.\nsummary: %s\nfindings:\n%s",
					cat.Summary, auditFindings(cat))
			}
			if got := cat.Details["undeclared_secret_mounts"]; got != 0 {
				t.Errorf("undeclared_secret_mounts = %v, want 0", got)
			}
			_, skipped := cat.Details["secret_supply_check"].(string)
			if tc.supplyChecked && skipped {
				t.Errorf("forge applies this env's support stream, so the supply check must run; details = %v", cat.Details)
			}
			if !tc.supplyChecked && !skipped {
				t.Errorf("the skipped supply check must say why it is n/a; details = %v", cat.Details)
			}
		})
	}
}

// TestAuditPrerequisites_FileSecretsDeclaredOnFreshClone: a Secret the
// FileSecrets provider is DECLARED to supply is declared, whether or not this
// machine's store has been populated.
func TestAuditPrerequisites_FileSecretsDeclaredOnFreshClone(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; skipped in -short")
	}
	cat := auditPrereqsFor(t, auditDevClusterPlacedFileSecrets)
	if cat.Status == audittype.StatusError {
		t.Fatalf("status = error, want ok: FileSecrets declares %q for the cluster workload.\nfindings:\n%s",
			"acme-secrets", auditFindings(cat))
	}
	if _, skipped := cat.Details["secret_supply_check"]; skipped {
		t.Error("a cluster-placed workload must keep the supply check")
	}
}

// TestAuditPrerequisites_UndeclaredClusterMountStillFails pins the other half:
// scoping the gate to cluster-placed workloads must not let a real
// declaration defect through.
func TestAuditPrerequisites_UndeclaredClusterMountStillFails(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; skipped in -short")
	}
	cat := auditPrereqsFor(t, auditDevClusterPlacedUndeclared)
	if cat.Status != audittype.StatusError {
		t.Fatalf("status = %q, want error for a cluster Secret nothing supplies.\nsummary: %s", cat.Status, cat.Summary)
	}
	if got := auditFindings(cat); !strings.Contains(got, `undeclared-mount: Secret "acme-secrets"`) {
		t.Errorf("findings should name the undeclared Secret:\n%s", got)
	}
}

// TestEnvAppliesManifestsToCluster pins the placement rule the audit shares
// with the deploy preflight's gate.
func TestEnvAppliesManifestsToCluster(t *testing.T) {
	cases := []struct {
		name string
		json string
		want bool
	}{
		{"nil", "", false},
		{
			"host only",
			`{
  "output": {
    "workloads": [
      {
        "name": "api",
        "kind": "service",
        "runtime": {
          "type": "host"
        },
        "spec": {
          "kind": "service"
        }
      }
    ]
  }
}`,
			false,
		},
		{
			"host process plus host-infra postgres",
			`{"output": {"workloads": [{"name": "api", "kind": "service", "runtime": {"type": "host"}, "spec": {"kind": "service"}}],
			  "infra": [{"name": "postgres", "engine": "postgres", "port": 5432}]}}`,
			false,
		},
		{
			"cluster",
			`{
  "output": {
    "workloads": [
      {
        "name": "api",
        "kind": "service",
        "runtime": {
          "type": "cluster",
          "cluster": "c",
          "namespace": "n"
        },
        "spec": {
          "kind": "service"
        }
      }
    ]
  }
}`,
			true,
		},
		{
			"mixed host and cluster",
			`{
  "output": {
    "workloads": [
      {
        "name": "a",
        "kind": "service",
        "runtime": {
          "type": "host"
        },
        "spec": {
          "kind": "service"
        }
      },
      {
        "name": "b",
        "kind": "service",
        "runtime": {
          "type": "cluster",
          "cluster": "c",
          "namespace": "n"
        },
        "spec": {
          "kind": "service"
        }
      }
    ]
  }
}`,
			true,
		},
		{
			"hosted workload with no control plane (refused at deploy; forge applies nothing)",
			`{
  "output": {
    "workloads": [
      {
        "name": "api",
        "kind": "service",
        "runtime": {
          "type": "hosted"
        },
        "spec": {
          "kind": "service",
          "image": "i"
        }
      }
    ]
  }
}`,
			false,
		},
		{
			"hosted workload (control plane applies)",
			`{
  "output": {
    "control_plane": {
      "type": "reliant",
      "endpoint": "https://cp"
    },
    "workloads": [
      {
        "name": "api",
        "kind": "service",
        "runtime": {
          "type": "hosted"
        },
        "spec": {
          "kind": "service",
          "image": "i"
        }
      }
    ]
  }
}`,
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var e *KCLEntities
			if tc.json != "" {
				var err error
				if e, err = parseKCLEntities([]byte(tc.json)); err != nil {
					t.Fatalf("parseKCLEntities: %v", err)
				}
			}
			if got := envAppliesManifestsToCluster(e); got != tc.want {
				t.Errorf("envAppliesManifestsToCluster = %v, want %v", got, tc.want)
			}
		})
	}
}
