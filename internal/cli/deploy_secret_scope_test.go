package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/deploytarget"
)

// The declared-Secret preflight in a two-cluster env, driven end to end
// through runDeployPreflightForEnv: a real KCL render, the deploy's own
// router, and a fake `kubectl` on PATH standing in for the two clusters.
//
// The shape is control-plane's prod at 78210543. The primary gets the app and
// cert-manager; the daemon cluster gets PriorityClasses and a kata pre-pull
// DaemonSet, and nothing in control-plane-prod or cert-manager.

const (
	scopePrimary = "gke_reliant-labs-475814_us-central1_prod"
	scopeDaemon  = "gke_reliant-labs-475814_us-central1-a_prod-daemon-v2"
	scopeAppNS   = "control-plane-prod"
)

const scopeProdManifests = `
_on = lambda kctx: str -> {str:str} {
    {"forge.dev/cluster": kctx}
}
output = {manifests = [
    {apiVersion = "v1", kind = "Namespace", metadata = {name = "control-plane-prod", labels = _on("` + scopePrimary + `")}}
    {apiVersion = "v1", kind = "ConfigMap", metadata = {name = "admin-server-config", namespace = "control-plane-prod", labels = _on("` + scopePrimary + `")}, data = {a = "b"}}
    {apiVersion = "scheduling.k8s.io/v1", kind = "PriorityClass", metadata = {name = "workspace-critical", labels = _on("` + scopeDaemon + `")}, value = 1000}
    {apiVersion = "v1", kind = "Namespace", metadata = {name = "kata-prepull", labels = _on("` + scopeDaemon + `")}}
    {apiVersion = "apps/v1", kind = "DaemonSet", metadata = {name = "workspace-base-prepull", namespace = "kata-prepull", labels = _on("` + scopeDaemon + `")}, spec = {selector.matchLabels = {app = "p"}, template = {metadata.labels = {app = "p"}, spec.containers = [{name = "p", image = "localhost:5000/prepull:1"}]}}}
]}
`

// scopeFakeKubectl puts a kubectl on PATH that serves every Secret on the
// clusters in `has` and reports NotFound elsewhere, and returns a reader for
// the `<context> <namespace>/<name>` of every Secret it was asked for.
func scopeFakeKubectl(t *testing.T, has ...string) func() []string {
	t.Helper()
	requirePOSIXFake(t, "kubectl")
	dir := t.TempDir()
	log := filepath.Join(dir, "secret-gets.log")
	script := `#!/bin/sh
ctx=""
if [ "$1" = "--context" ]; then ctx="$2"; shift 2; fi
case "$1" in
api-resources)
  printf 'daemonsets ds apps/v1 true DaemonSet [get]\nstatefulsets sts apps/v1 true StatefulSet [get]\npriorityclasses pc scheduling.k8s.io/v1 false PriorityClass [get]\n'
  exit 0 ;;
get)
  name="$3"; ns=""; all="$*"
  while [ $# -gt 0 ]; do [ "$1" = "-n" ] && ns="$2"; shift; done
  echo "$ctx $ns/$name" >> "` + log + `"
  case " ` + strings.Join(has, " ") + ` " in
  *" $ctx "*)
    # A keys-only read (KubectlSecretGetter's go-template) gets keys; a
    # value read (the byte-match check's -o json) gets the object.
    case "$all" in
    *go-template=*) printf 'token\napi-token\n' ;;
    *) echo '{"data":{"token":"dg==","api-token":"dg=="}}' ;;
    esac
    exit 0 ;;
  esac
  echo "Error from server (NotFound): secrets \"$name\" not found" >&2
  exit 1 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		b, _ := os.ReadFile(log)
		return strings.Fields(strings.ReplaceAll(string(b), "\n", " | "))
	}
}

// scopeProdInput builds the preflight input for the prod shape plus any extra
// manifests and declared Secrets a case needs.
func scopeProdInput(t *testing.T, extraKCL string, secrets ...ExternalSecretEntity) (deployPreflightEnvInput, *deployReport) {
	t.Helper()
	root := t.TempDir()
	mainK := filepath.Join(root, "deploy", "kcl", "prod", "main.k")
	if err := os.MkdirAll(filepath.Dir(mainK), 0o755); err != nil {
		t.Fatal(err)
	}
	src := scopeProdManifests
	if extraKCL != "" {
		src = strings.Replace(src, "\n]}\n", "\n"+extraKCL+"\n]}\n", 1)
	}
	if err := os.WriteFile(mainK, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	entities := &KCLEntities{
		ClusterTarget:   &ClusterTargetEntity{Cluster: scopePrimary, Namespace: scopeAppNS},
		HelmCharts:      []HelmChartEntity{{Name: "cert-manager", Namespace: "cert-manager"}},
		RequiredSecrets: secrets,
	}
	groups := []deploytarget.ServiceGroup{
		{ProviderID: "k8s-cluster", Cluster: scopeDaemon, Services: []deploytarget.ResolvedService{{Name: "kata-prepull"}}},
		{ProviderID: "k8s-cluster", Cluster: scopePrimary, Services: []deploytarget.ResolvedService{{Name: "admin-server"}}},
	}
	report := newDeployReport("prod", true)
	return deployPreflightEnvInput{
		entities: entities, groups: groups, mainK: mainK, imageTag: "v1",
		namespace: scopeAppNS, envName: "prod", envCfgKV: map[string]string{},
		deployContext: scopePrimary, report: report,
	}, report
}

var scopeProdSecrets = []ExternalSecretEntity{
	{Name: "control-plane-zitadel-db", Namespace: scopeAppNS, Keys: []string{"token"}},
	{Name: "control-plane-zitadel-masterkey", Namespace: scopeAppNS, Keys: []string{"token"}},
	{Name: "cloudflare-api-token", Namespace: "cert-manager", Keys: []string{"api-token"}},
}

// TestPreflightChecksSecretsOnlyWhereConsumed is the regression for control-plane
// prod deploy run 36270770397: every declared Secret was required on
// prod-daemon-v2, which consumes none of them, so a prod whose Secrets were all
// in place could not deploy. On 2e7b0186 this fails with the daemon cluster
// named in each finding.
func TestPreflightChecksSecretsOnlyWhereConsumed(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	gets := scopeFakeKubectl(t, scopePrimary) // every Secret exists on the primary only
	in, report := scopeProdInput(t, "", scopeProdSecrets...)

	if err := runDeployPreflightForEnv(context.Background(), in); err != nil {
		t.Fatalf("preflight refused a prod whose Secrets all exist where they are used:\n%v", err)
	}
	for _, g := range gets() {
		if strings.Contains(g, scopeDaemon) {
			t.Errorf("asked the daemon cluster for a Secret it never consumes: %v", gets())
			break
		}
	}
	doc := report.document()
	if doc.Preflight.Status != deployPreflightRan || doc.Preflight.Blocking != 0 {
		t.Errorf("preflight = %+v, want ran with 0 blocking", doc.Preflight)
	}

	// The report is a different question and keeps its answer: the deploy
	// still writes to both clusters.
	report.setTarget(in.deployContext, in.namespace, declaredClusterContexts(in.entities, in.deployContext, in.groups)...)
	if all := report.document().Target.AllKubeContexts; len(all) != 2 {
		t.Errorf("all_kube_contexts = %v, want both clusters", all)
	}
}

// TestPreflightStillChecksSecretsOnTheSecondaryThatUsesThem: scoping must not
// turn into "primary only". A Secret in a namespace the daemon cluster
// receives is still required there, and a miss names that cluster.
func TestPreflightStillChecksSecretsOnTheSecondaryThatUsesThem(t *testing.T) {
	scopeFakeKubectl(t, scopePrimary)
	daemonConsumer := `    {apiVersion = "v1", kind = "ConfigMap", metadata = {name = "proxy", namespace = "workspaces", labels = _on("` + scopeDaemon + `")}, data = {a = "b"}}`
	in, report := scopeProdInput(t, daemonConsumer,
		ExternalSecretEntity{Name: "workspace-proxy-secrets", Namespace: "workspaces", Keys: []string{"token"}})

	err := runDeployPreflightForEnv(context.Background(), in)
	if err == nil {
		t.Fatal("preflight passed while the daemon cluster lacked a Secret it consumes")
	}
	if !strings.Contains(err.Error(), scopeDaemon+":workspaces/workspace-proxy-secrets") {
		t.Errorf("miss not attributed to the daemon cluster:\n%v", err)
	}
	if strings.Contains(err.Error(), scopePrimary+":workspaces/") {
		t.Errorf("primary asked for a Secret only the daemon consumes:\n%v", err)
	}
	if report.document().Preflight.Blocking != 1 {
		t.Errorf("blocking = %d, want 1", report.document().Preflight.Blocking)
	}
}

// TestPreflightKeepsUnattributableSecretsOnEveryCluster: a Secret in a
// namespace nothing renders into cannot be placed, so it keeps the
// conservative every-cluster check, and the refusal says why.
func TestPreflightKeepsUnattributableSecretsOnEveryCluster(t *testing.T) {
	scopeFakeKubectl(t, scopePrimary)
	in, _ := scopeProdInput(t, "", ExternalSecretEntity{Name: "orphan", Namespace: "nowhere", Keys: []string{"token"}})

	err := runDeployPreflightForEnv(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), scopeDaemon+":nowhere/orphan") {
		t.Fatalf("unattributable Secret not checked on the daemon cluster: %v", err)
	}
	if !strings.Contains(err.Error(), "cannot tell which cluster consumes it") {
		t.Errorf("refusal does not explain the every-cluster check:\n%v", err)
	}
}

// TestPreflightTargetedDaemonDeployChecksNoHubSecret is D4 as reported:
// `forge env deploy prod --target prod-daemon-cluster --dry-run` on
// control-plane main was refused for the hub's zitadel / openbao / sentry
// Secrets "missing" on prod-daemon-v2.
//
// The deploy writes ONLY the daemon cluster. Before the fix the targeted
// deploy handed the preflight the target-FILTERED groups — one cluster — so
// the attribution saw a single-cluster env, gave up scoping, and required
// every declared Secret on it. Now the attribution runs against the whole
// env's topology and the check is narrowed to the clusters this deploy writes:
// no hub Secret is asked for anywhere, because the hub is not being written.
func TestPreflightTargetedDaemonDeployChecksNoHubSecret(t *testing.T) {
	gets := scopeFakeKubectl(t) // no Secret exists anywhere
	in, report := scopeProdInput(t, "", scopeProdSecrets...)
	// The whole env stays the topology; this deploy dispatches only the
	// daemon cluster's group (what targetedK8sGroups yields for
	// --target kata-prepull / prod-daemon-cluster).
	in.topology, in.topologyEntities = in.groups, in.entities
	in.groups = []deploytarget.ServiceGroup{in.groups[0]}
	in.targets = []string{"kata-prepull"}

	if err := runDeployPreflightForEnv(context.Background(), in); err != nil {
		t.Fatalf("a deploy that writes only the daemon cluster was refused for Secrets it never uses:\n%v", err)
	}
	if asked := gets(); len(asked) != 0 {
		t.Errorf("asked for Secrets this deploy cannot break: %v", asked)
	}
	if doc := report.document(); doc.Preflight.Blocking != 0 {
		t.Errorf("preflight = %+v, want 0 blocking", doc.Preflight)
	}
}

// TestPreflightTargetedDeployStillChecksTheSecretsItWrites: narrowing to the
// written clusters must not turn into "check nothing". A targeted deploy of
// the daemon cluster still requires a Secret that cluster consumes.
func TestPreflightTargetedDeployStillChecksTheSecretsItWrites(t *testing.T) {
	scopeFakeKubectl(t)
	daemonConsumer := `    {apiVersion = "v1", kind = "ConfigMap", metadata = {name = "proxy", namespace = "workspaces", labels = _on("` + scopeDaemon + `")}, data = {a = "b"}}`
	in, _ := scopeProdInput(t, daemonConsumer, append([]ExternalSecretEntity{
		{Name: "workspace-proxy-secrets", Namespace: "workspaces", Keys: []string{"token"}},
	}, scopeProdSecrets...)...)
	in.topology, in.topologyEntities = in.groups, in.entities
	in.groups = []deploytarget.ServiceGroup{in.groups[0]}
	in.targets = []string{"kata-prepull"}

	err := runDeployPreflightForEnv(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), scopeDaemon+":workspaces/workspace-proxy-secrets") {
		t.Fatalf("the daemon cluster's own Secret was not required: %v", err)
	}
	// Findings are the "Secret <ctx>:<ns>/<name> missing" lines; the
	// remediation prose below them names cloudflare-api-token as an example.
	for _, line := range strings.Split(err.Error(), "\n") {
		if strings.Contains(line, "missing keys") && !strings.Contains(line, "workspace-proxy-secrets") {
			t.Errorf("a hub Secret leaked into a daemon-only deploy's findings: %s", strings.TrimSpace(line))
		}
	}
}

// TestScopeRequiredSecretsSingleClusterUntouched: one cluster, nothing to scope.
func TestScopeRequiredSecretsSingleClusterUntouched(t *testing.T) {
	in := []cluster.RequiredSecret{{Name: "s", Namespace: "ns"}}
	groups := []deploytarget.ServiceGroup{{ProviderID: "k8s-cluster", Cluster: "only"}}
	got, unattributed := scopeRequiredSecretsToClusters(in, "", groups, &KCLEntities{}, "ns")
	if len(got) != 1 || len(got[0].Contexts) != 0 || len(unattributed) != 0 {
		t.Errorf("single-cluster env was scoped: %+v, unattributed %+v", got, unattributed)
	}
}

// scopeObservabilityKCL is the control-plane prod observability stack that
// blocked v1.7.39: workloads in namespaces other than the deploy namespace,
// on both clusters, each reading a Secret that exists where it runs.
const scopeObservabilityKCL = `    {apiVersion = "apps/v1", kind = "StatefulSet", metadata = {name = "clickhouse", namespace = "observability-data", labels = _on("` + scopePrimary + `")}, spec = {selector.matchLabels = {app = "c"}, template = {metadata.labels = {app = "c"}, spec.containers = [{name = "c", image = "localhost:5000/clickhouse:1", env = [{name = "P", valueFrom.secretKeyRef = {name = "obs-credentials", key = "token"}}]}]}}}
    {apiVersion = "apps/v1", kind = "DaemonSet", metadata = {name = "otel-agent", namespace = "observability", labels = _on("` + scopeDaemon + `")}, spec = {selector.matchLabels = {app = "o"}, template = {metadata.labels = {app = "o"}, spec.containers = [{name = "o", image = "localhost:5000/otel:1", env = [{name = "T", valueFrom.secretKeyRef = {name = "otel-agent-token", key = "token"}}]}]}}}`

// TestPreflightChecksManifestSecretsInTheirNamespaceOnTheirCluster is the
// regression for control-plane's prod release v1.7.39, refused for
// control-plane-prod/obs-credentials and control-plane-prod/otel-agent-token:
// the manifest-driven check looked every referenced Secret up in the deploy
// namespace on the primary cluster. Each is now read where its pod reads it.
func TestPreflightChecksManifestSecretsInTheirNamespaceOnTheirCluster(t *testing.T) {
	gets := scopeFakeKubectl(t, scopePrimary, scopeDaemon) // every Secret exists on both clusters
	in, report := scopeProdInput(t, scopeObservabilityKCL)

	if err := runDeployPreflightForEnv(context.Background(), in); err != nil {
		t.Fatalf("preflight refused Secrets that exist where their workloads run:\n%v", err)
	}
	asked := strings.Join(gets(), " ")
	for _, want := range []string{
		scopePrimary + " observability-data/obs-credentials",
		scopeDaemon + " observability/otel-agent-token",
	} {
		if !strings.Contains(asked, want) {
			t.Errorf("never read %q; lookups: %v", want, gets())
		}
	}
	for _, wrong := range []string{
		scopeAppNS + "/obs-credentials",
		scopeAppNS + "/otel-agent-token",
		scopePrimary + " observability/otel-agent-token",
	} {
		if strings.Contains(asked, wrong) {
			t.Errorf("read %q, where no pod resolves it; lookups: %v", wrong, gets())
		}
	}
	if doc := report.document(); doc.Preflight.Blocking != 0 {
		t.Errorf("preflight = %+v, want 0 blocking", doc.Preflight)
	}
}

// TestPreflightNamesTheClusterAndNamespaceOfAMissingManifestSecret: a Secret
// absent from the cluster its workload runs on is still refused, reported as
// <context>:<namespace>/<name>.
func TestPreflightNamesTheClusterAndNamespaceOfAMissingManifestSecret(t *testing.T) {
	scopeFakeKubectl(t, scopePrimary) // nothing exists on the daemon cluster
	in, _ := scopeProdInput(t, scopeObservabilityKCL)

	err := runDeployPreflightForEnv(context.Background(), in)
	if err == nil {
		t.Fatal("preflight passed while the daemon cluster lacks observability/otel-agent-token")
	}
	if !strings.Contains(err.Error(), "Secret "+scopeDaemon+":observability/otel-agent-token missing keys") {
		t.Errorf("miss not named by cluster and namespace:\n%v", err)
	}
	if strings.Contains(err.Error(), "obs-credentials") {
		t.Errorf("obs-credentials exists on the hub, where clickhouse runs:\n%v", err)
	}
}
