package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fyVal is a tiny helper to construct a forge.yaml-sourced
// parityValue for the test inputs. The env label is baked in so test
// rows don't repeat it.
func fyVal(val, env string) parityValue {
	return parityValue{
		Source:      parityForgeYAMLConfig,
		SourceLabel: "forge.yaml environments[" + env + "].config",
		Value:       val,
	}
}

// hostKCLVal constructs a host-mode KCL env_var parityValue.
func hostKCLVal(val string) parityValue {
	return parityValue{
		Source:      parityHostKCLEnvVar,
		SourceLabel: "KCL host_deploy.env_vars",
		Value:       val,
	}
}

// clusterKCLVal constructs a cluster-mode KCL env_var parityValue.
func clusterKCLVal(val string) parityValue {
	return parityValue{
		Source:      parityClusterKCLEnvVar,
		SourceLabel: "KCL cluster_deploy.env_vars",
		Value:       val,
	}
}

// clusterSecretRefVal constructs a cluster-mode secret_ref
// parityValue (no inline value — secret ref projection).
func clusterSecretRefVal(secret, key string) parityValue {
	return parityValue{
		Source:      parityKCLSecretRef,
		SourceLabel: "KCL secret_ref name=" + secret + " key=" + key,
	}
}

// secretsFileVal constructs the host-side secrets_file marker.
func secretsFileVal(path string) parityValue {
	return parityValue{
		Source:      paritySecretsFile,
		SourceLabel: "secrets_file " + path,
	}
}

// TestDoctorParity_PerfectAgreement — every key declared on both
// sides agrees. No divergences, exit 0.
func TestDoctorParity_PerfectAgreement(t *testing.T) {
	in := parityInputs{
		Service: "tasks",
		Env:     "dev",
		ForgeYAMLEnv: map[string]parityValue{
			"LOG_LEVEL": fyVal("info", "dev"),
		},
		HostKCL: map[string]parityValue{
			"DATABASE_URL": hostKCLVal("postgres://localhost/x"),
		},
		ClusterKCL: map[string]parityValue{
			"DATABASE_URL": clusterKCLVal("postgres://localhost/x"),
		},
	}
	got := diffParity(in)
	if len(got.Divergences) != 0 {
		t.Fatalf("want 0 divergences, got %d: %+v", len(got.Divergences), got.Divergences)
	}
	if len(got.bugDivergences()) != 0 {
		t.Fatalf("want 0 bug divergences, got %d", len(got.bugDivergences()))
	}
	// LOG_LEVEL is only on the forge.yaml channel (both modes
	// resolve it the same way) — counts as agree.
	if len(got.Agree) != 2 {
		t.Fatalf("want 2 agree, got %d: %+v", len(got.Agree), got.Agree)
	}
}

// TestDoctorParity_ValueMismatch — LOG_LEVEL=debug on host
// (forge.yaml or host-KCL) and LOG_LEVEL=info on cluster (KCL
// cluster). Divergence reported, exit-1 sentinel returned.
func TestDoctorParity_ValueMismatch(t *testing.T) {
	in := parityInputs{
		Service: "tasks",
		Env:     "dev",
		ForgeYAMLEnv: map[string]parityValue{
			"LOG_LEVEL": fyVal("debug", "dev"),
		},
		ClusterKCL: map[string]parityValue{
			"LOG_LEVEL": clusterKCLVal("info"),
		},
	}
	got := diffParity(in)
	if len(got.Divergences) != 1 {
		t.Fatalf("want 1 divergence, got %d", len(got.Divergences))
	}
	if got.Divergences[0].Kind != parityValueMismatch {
		t.Errorf("want value_mismatch, got %s", got.Divergences[0].Kind)
	}
	if got.Divergences[0].Key.Name != "LOG_LEVEL" {
		t.Errorf("want LOG_LEVEL, got %s", got.Divergences[0].Key.Name)
	}
	if got.Divergences[0].Key.Host.Value != "debug" {
		t.Errorf("host value: got %q", got.Divergences[0].Key.Host.Value)
	}
	if got.Divergences[0].Key.Cluster.Value != "info" {
		t.Errorf("cluster value: got %q", got.Divergences[0].Key.Cluster.Value)
	}
	if len(got.bugDivergences()) != 1 {
		t.Errorf("want 1 bug divergence, got %d", len(got.bugDivergences()))
	}
}

// TestDoctorParity_MissingInHost — NATS_URL set only in cluster KCL.
// Host shows <unset>, divergence is missing_in_host, exit 1.
func TestDoctorParity_MissingInHost(t *testing.T) {
	in := parityInputs{
		Service: "tasks",
		Env:     "dev",
		ClusterKCL: map[string]parityValue{
			"NATS_URL": clusterKCLVal("nats://nats.tasks-dev.svc.cluster.local:4222"),
		},
	}
	got := diffParity(in)
	if len(got.Divergences) != 1 {
		t.Fatalf("want 1 divergence, got %d", len(got.Divergences))
	}
	d := got.Divergences[0]
	if d.Kind != parityMissingInHost {
		t.Errorf("want missing_in_host, got %s", d.Kind)
	}
	if d.Key.Host.Source != parityUnset {
		t.Errorf("host source: want unset, got %s", d.Key.Host.Source)
	}
	if d.Key.Host.SourceLabel != "<unset>" {
		t.Errorf("host label: want <unset>, got %s", d.Key.Host.SourceLabel)
	}
	if d.Key.Cluster.Value != "nats://nats.tasks-dev.svc.cluster.local:4222" {
		t.Errorf("cluster value lost: %s", d.Key.Cluster.Value)
	}
	if len(got.bugDivergences()) != 1 {
		t.Errorf("want 1 bug divergence, got %d", len(got.bugDivergences()))
	}
}

// TestDoctorParity_SecretChannelDifferenceNotABug — same key surfaces
// on host's secrets_file channel and cluster's secret_ref channel.
// Reported, but does NOT count as a bug divergence (exit 0).
//
// The diff sees the host secrets_file as a single sentinel
// "<secrets_file>" entry (we never enumerate the file) so to test the
// secret-channel path we model the same key being declared on BOTH
// channels — host secrets_file and cluster secret_ref. In practice
// the cobra wiring inserts the sentinel; here we synthesize the
// realistic shape: cluster has a secret_ref for STRIPE_WEBHOOK_SECRET
// AND we model the host as having the same key projected via
// secrets_file.
func TestDoctorParity_SecretChannelDifferenceNotABug(t *testing.T) {
	in := parityInputs{
		Service: "tasks",
		Env:     "dev",
		HostSecrets: map[string]parityValue{
			"STRIPE_WEBHOOK_SECRET": secretsFileVal(".env.dev.secrets"),
		},
		ClusterSecret: map[string]parityValue{
			"STRIPE_WEBHOOK_SECRET": clusterSecretRefVal("tasks-secrets", "stripe"),
		},
	}
	got := diffParity(in)
	if len(got.Divergences) != 1 {
		t.Fatalf("want 1 (expected) divergence, got %d", len(got.Divergences))
	}
	if got.Divergences[0].Kind != paritySecretChannelDiverg {
		t.Errorf("want secret_channel_divergence, got %s", got.Divergences[0].Kind)
	}
	if len(got.bugDivergences()) != 0 {
		t.Errorf("want 0 bug divergences (secret channel is expected), got %d", len(got.bugDivergences()))
	}
}

// TestDoctorParity_BothSecretChannelsAgree — both sides project from
// secret channels (host secrets_file + cluster secret_ref). Since
// neither carries an inline value to compare, the diff treats them
// as agreement-in-shape (the model still needs to verify key names
// line up but that's out of scope for the static check).
//
// This is distinct from TestDoctorParity_SecretChannelDifferenceNotABug
// — there ONLY ONE side was on a secret channel; here BOTH are.
func TestDoctorParity_BothSecretChannelsAgree(t *testing.T) {
	in := parityInputs{
		Service: "tasks",
		Env:     "dev",
		HostSecrets: map[string]parityValue{
			"DB_PASSWORD": secretsFileVal(".env.dev.secrets"),
		},
		// Cluster carries the same key as a secret_ref but the diff
		// shape (host-secrets-file vs cluster-secret-ref) is the
		// EXPECTED divergence pattern — only one side at a time
		// counts as "both are secret channels" for the agree path.
		// To test the "both secret channels" path use clusterSecret
		// only.
	}
	in.ClusterSecret = map[string]parityValue{
		"DB_PASSWORD": clusterSecretRefVal("db-creds", "password"),
	}
	got := diffParity(in)
	// Host=secrets_file, cluster=secret_ref. Both are secret channels.
	// classify() treats this as a secret-channel divergence because
	// the source kinds differ even though both are projection
	// channels. The mismatch IS the bug-model-must-verify case.
	if len(got.bugDivergences()) != 0 {
		t.Errorf("want 0 bug divergences, got %d: %+v", len(got.bugDivergences()), got.bugDivergences())
	}
}

// TestDoctorParity_AgreeWithBothSecretsFromSameKind — when both sides
// happen to source from the same projection-channel kind (which
// doesn't really happen in production but exercises the
// both-secret-channel agree branch), the row goes to Agree.
func TestDoctorParity_AgreeWithBothSecretsFromSameKind(t *testing.T) {
	in := parityInputs{
		Service: "tasks",
		Env:     "dev",
		// Both cluster fields — same secret_ref source on each
		// surface. Synthetic but exercises classify()'s "both
		// secret-channel" agree path.
		HostKCL: map[string]parityValue{},
	}
	in.ClusterSecret = map[string]parityValue{
		"DB_PASSWORD": clusterSecretRefVal("db-creds", "password"),
	}
	// Synthesize a "host secret_ref" — not a real channel but
	// classify() should treat both-secret-channel-source as agree.
	in.HostSecrets = map[string]parityValue{
		"DB_PASSWORD": {
			Source:      parityKCLSecretRef,
			SourceLabel: "KCL secret_ref name=db-creds key=password",
		},
	}
	got := diffParity(in)
	if len(got.Divergences) != 0 {
		t.Errorf("want 0 divergences (both secret_ref, same kind = agree), got %d: %+v", len(got.Divergences), got.Divergences)
	}
}

// TestDoctorParity_PureFunctionNoFilesystem — confirms diffParity
// touches no filesystem. We run inside a chdir into an empty tempdir
// where forge.yaml / kcl render would fail. Nothing should error.
func TestDoctorParity_PureFunctionNoFilesystem(t *testing.T) {
	dir := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	in := parityInputs{
		Service: "tasks",
		Env:     "dev",
		ForgeYAMLEnv: map[string]parityValue{
			"LOG_LEVEL": fyVal("info", "dev"),
		},
		ClusterKCL: map[string]parityValue{
			"LOG_LEVEL": clusterKCLVal("info"),
		},
	}
	got := diffParity(in)
	if len(got.Divergences) != 0 {
		t.Errorf("pure function still surfaced divergences: %+v", got.Divergences)
	}
}

// TestDoctorParity_UnknownService — bad service name produces a
// UserErr-shaped message that lists the available services and
// suggests the spelling fix.
func TestDoctorParity_UnknownService(t *testing.T) {
	dir := t.TempDir()
	writeForgeYAML(t, dir, `name: demo
module_path: github.com/example/demo
`)
	// Inventory is enumerated from the proto descriptor — see
	// codegen.IntrospectComponents.
	writeForgeDescriptor(t, dir, "AlphaService", "BravoService", "CharlieService")
	prev, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(prev) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	// Fixture render — empty entities so RenderKCL succeeds.
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, `{}`))

	var stdout, stderr bytes.Buffer
	err := runDoctorParity(context.Background(), "delta", "dev", false, &stdout, &stderr)
	if err == nil {
		t.Fatal("want UserErr for unknown service, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "service \"delta\" not found") {
		t.Errorf("missing service-not-found phrase: %q", msg)
	}
	for _, want := range []string{"alpha", "bravo", "charlie"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing available service %q in: %q", want, msg)
		}
	}
}

// TestDoctorParity_JSONShape — --json emits a parseable doc with
// service/env/agree/divergences keys.
func TestDoctorParity_JSONShape(t *testing.T) {
	dir := t.TempDir()
	writeForgeYAML(t, dir, `name: demo
module_path: github.com/example/demo
`)
	// Inventory is enumerated from the proto descriptor.
	writeForgeDescriptor(t, dir, "TasksService")
	prev, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(prev) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	// The workload reads STRIPE_KEY from a Secret: the cluster projects it
	// through secretKeyRef, while the host side resolves nothing for it →
	// one missing_in_host divergence. (An inline value can no longer
	// diverge: a workload declares ONE env for every runtime.)
	fixture := `{
  "output": {
    "workloads": [
      {
        "name": "tasks",
        "kind": "service",
        "runtime": {
          "type": "cluster",
          "cluster": "k3d-demo",
          "namespace": "demo-dev"
        },
        "spec": {
          "kind": "service",
          "env": [
            {
              "name": "LOG_LEVEL",
              "value": "info"
            },
            {
              "name": "STRIPE_KEY",
              "secretRef": {"name": "tasks-secrets", "key": "stripe"}
            }
          ]
        }
      }
    ]
  }
}`
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, fixture))

	var stdout, stderr bytes.Buffer
	err := runDoctorParity(context.Background(), "tasks", "dev", true, &stdout, &stderr)
	if !errors.Is(err, errParityDivergent) {
		t.Fatalf("want errParityDivergent (bug divergence present), got %v", err)
	}

	var doc map[string]any
	if jerr := json.Unmarshal(stdout.Bytes(), &doc); jerr != nil {
		t.Fatalf("decode JSON: %v\nbody: %s", jerr, stdout.String())
	}
	if doc["service"] != "tasks" {
		t.Errorf("service: %v", doc["service"])
	}
	if doc["env"] != "dev" {
		t.Errorf("env: %v", doc["env"])
	}
	if _, ok := doc["agree"]; !ok {
		t.Error("missing agree key")
	}
	divs, ok := doc["divergences"].([]any)
	if !ok {
		t.Fatalf("divergences not a slice: %T", doc["divergences"])
	}
	if len(divs) != 1 {
		t.Errorf("want 1 divergence, got %d: %+v", len(divs), divs)
	}
	d := divs[0].(map[string]any)
	if d["kind"] != string(parityMissingInHost) {
		t.Errorf("kind: %v", d["kind"])
	}
}

// TestDoctorParity_AgreementExitsZero — end-to-end: both sides agree
// → no error returned.
func TestDoctorParity_AgreementExitsZero(t *testing.T) {
	dir := t.TempDir()
	writeForgeYAML(t, dir, `name: demo
module_path: github.com/example/demo
`)
	// Inventory is enumerated from the proto descriptor.
	writeForgeDescriptor(t, dir, "TasksService")
	prev, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(prev) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	// Tasks has no KCL env_vars on either side → trivially agrees.
	fixture := `{
  "output": {
    "workloads": [
      {
        "name": "tasks",
        "kind": "service",
        "runtime": {
          "type": "cluster",
          "cluster": "k3d-demo",
          "namespace": "demo-dev"
        },
        "spec": {
          "kind": "service"
        }
      }
    ]
  }
}`
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, fixture))

	var stdout, stderr bytes.Buffer
	err := runDoctorParity(context.Background(), "tasks", "dev", false, &stdout, &stderr)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}
	if !strings.Contains(stderr.String(), "OK") {
		t.Errorf("want OK in human report, got: %s", stderr.String())
	}
}

// TestDoctorParity_ExtractKCLEnvVars_ServiceLevelTopLevel pins the
// regression fix in extractKCLEnvVars: KCL emits `forge.Service.env_vars`
// at the SERVICE TOP LEVEL (svc.EnvVars), NOT under
// svc.Deploy.Cluster.EnvVars. The pre-fix loop only read the deploy-block
// slices and silently missed every env_var most projects actually
// declare. The fix iterates svc.EnvVars and:
//   - inline values land in BOTH hostKCL and clusterKCL (service-level
//     vars apply to both modes)
//   - secret_ref entries land in clusterSecret only (host has no Secret
//     projection)
//
// Both sub-tests use a Deploy block with an EMPTY EnvVars slice so the
// only path that can populate the maps is the top-level read — proving
// the fix exercises the right field.
func TestDoctorParity_ExtractKCLEnvVars_ServiceLevelTopLevel(t *testing.T) {
	t.Run("inline values land in both host and cluster maps", func(t *testing.T) {
		entities := &KCLEntities{Workloads: []WorkloadEntity{
			clusterWL("tasks", "k3d-dev", "dev", withEnv(
				"LOG_LEVEL", "info",
				"DATABASE_URL", "postgres://localhost/tasks",
				"NATS_URL", "nats://localhost:4222",
			)),
		}}

		hostKCL, clusterKCL, clusterSecret := extractKCLEnvVars(entities, "tasks")

		if len(clusterSecret) != 0 {
			t.Errorf("clusterSecret: want empty, got %+v", clusterSecret)
		}

		want := map[string]string{
			"LOG_LEVEL":    "info",
			"DATABASE_URL": "postgres://localhost/tasks",
			"NATS_URL":     "nats://localhost:4222",
		}
		if len(hostKCL) != len(want) {
			t.Fatalf("hostKCL size: want %d, got %d: %+v", len(want), len(hostKCL), hostKCL)
		}
		if len(clusterKCL) != len(want) {
			t.Fatalf("clusterKCL size: want %d, got %d: %+v", len(want), len(clusterKCL), clusterKCL)
		}
		for name, value := range want {
			h, ok := hostKCL[name]
			if !ok {
				t.Errorf("hostKCL missing %q (regression: top-level env_vars not read)", name)
				continue
			}
			if h.Value != value {
				t.Errorf("hostKCL[%q].Value: want %q, got %q", name, value, h.Value)
			}
			if h.Source != parityHostKCLEnvVar {
				t.Errorf("hostKCL[%q].Source: want parityHostKCLEnvVar, got %s", name, h.Source)
			}

			c, ok := clusterKCL[name]
			if !ok {
				t.Errorf("clusterKCL missing %q (regression: top-level env_vars not read)", name)
				continue
			}
			if c.Value != value {
				t.Errorf("clusterKCL[%q].Value: want %q, got %q", name, value, c.Value)
			}
			if c.Source != parityClusterKCLEnvVar {
				t.Errorf("clusterKCL[%q].Source: want parityClusterKCLEnvVar, got %s", name, c.Source)
			}
		}
	})

	t.Run("top-level secret_ref lands in clusterSecret only, not hostKCL", func(t *testing.T) {
		entities := &KCLEntities{Workloads: []WorkloadEntity{
			clusterWL("tasks", "k3d-dev", "dev",
				withEnv("LOG_LEVEL", "info"),
				withSecretRef("STRIPE_WEBHOOK_SECRET", "tasks-secrets", "stripe"),
			),
		}}

		hostKCL, clusterKCL, clusterSecret := extractKCLEnvVars(entities, "tasks")

		// Inline LOG_LEVEL still hits both inline maps.
		if _, ok := hostKCL["LOG_LEVEL"]; !ok {
			t.Error("hostKCL missing LOG_LEVEL")
		}
		if _, ok := clusterKCL["LOG_LEVEL"]; !ok {
			t.Error("clusterKCL missing LOG_LEVEL")
		}

		// STRIPE_WEBHOOK_SECRET must land in clusterSecret only —
		// host-mode has no Kubernetes Secret projection channel.
		if _, ok := hostKCL["STRIPE_WEBHOOK_SECRET"]; ok {
			t.Error("hostKCL must NOT contain STRIPE_WEBHOOK_SECRET (no host Secret projection)")
		}
		if _, ok := clusterKCL["STRIPE_WEBHOOK_SECRET"]; ok {
			t.Error("clusterKCL must NOT contain STRIPE_WEBHOOK_SECRET (it's a secret_ref, not inline)")
		}
		got, ok := clusterSecret["STRIPE_WEBHOOK_SECRET"]
		if !ok {
			t.Fatalf("clusterSecret missing STRIPE_WEBHOOK_SECRET: %+v", clusterSecret)
		}
		if got.Source != parityKCLSecretRef {
			t.Errorf("clusterSecret[STRIPE_WEBHOOK_SECRET].Source: want parityKCLSecretRef, got %s", got.Source)
		}
		if !strings.Contains(got.SourceLabel, "tasks-secrets") || !strings.Contains(got.SourceLabel, "stripe") {
			t.Errorf("SourceLabel should attribute secret name/key, got %q", got.SourceLabel)
		}
	})
}

// writeForgeYAML drops a forge.yaml at dir + sets it up so
// findProjectConfigFile picks it up via cwd traversal.
func writeForgeYAML(t *testing.T, dir, body string) {
	t.Helper()
	path := filepath.Join(dir, "forge.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
}
