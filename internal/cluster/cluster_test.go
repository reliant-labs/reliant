package cluster

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// contains reports whether ss contains the exact string s.
func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// indexOf returns the position of s in ss, or -1.
func indexOf(ss []string, s string) int {
	for i, v := range ss {
		if v == s {
			return i
		}
	}
	return -1
}

// TestRenderDArgs_QuotesStringArgs pins the forge-deploy-prod regression
// fix: the forge-controlled string args (image_tag, namespace, env) are
// emitted as QUOTED KCL string literals so KCL types an all-digit
// git-describe tag as `str` rather than coercing it to `int` (which
// fails RenderEnv.image_tag's `str` type). envCfgKV values are left
// unquoted — their KCL type is intentional project config.
func TestRenderDArgs_QuotesStringArgs(t *testing.T) {
	// Numeric tag: the regression trigger. Must come out QUOTED.
	got := renderDArgs("3826648", "cp-forge-prod", "prod", nil, nil)
	for _, want := range []string{
		`image_tag="3826648"`,
		`namespace="cp-forge-prod"`,
		`env="prod"`,
	} {
		if !contains(got, want) {
			t.Errorf("expected %q in dArgs, got %v", want, got)
		}
	}

	// Non-numeric tag is also quoted (uniform handling).
	got = renderDArgs("v1.2.3", "ns", "dev", nil, nil)
	if !contains(got, `image_tag="v1.2.3"`) {
		t.Errorf("expected image_tag=\"v1.2.3\" in dArgs, got %v", got)
	}

	// Empty env produces no env= binding.
	got = renderDArgs("tag", "ns", "", nil, nil)
	for _, a := range got {
		if strings.HasPrefix(a, "env=") {
			t.Errorf("expected no env= entry for empty env, got %v", got)
		}
	}

	// envCfgKV values are appended sorted by key and UNquoted.
	got = renderDArgs("tag", "ns", "", map[string]string{"B": "2", "A": "1"}, nil)
	if !contains(got, "A=1") {
		t.Errorf("expected unquoted A=1 in dArgs, got %v", got)
	}
	if !contains(got, "B=2") {
		t.Errorf("expected unquoted B=2 in dArgs, got %v", got)
	}
	if ia, ib := indexOf(got, "A=1"), indexOf(got, "B=2"); ia == -1 || ib == -1 || ia > ib {
		t.Errorf("expected A=1 before B=2 (sorted), got %v", got)
	}

	// image_digests is emitted as a QUOTED JSON string (so KCL types it as
	// `str` and json.decodes it). encoding/json sorts object keys, so the
	// JSON is deterministic.
	got = renderDArgs("tag", "ns", "", nil, map[string]string{
		"reliant":       "sha256:12ac",
		"control-plane": "sha256:d181",
	})
	wantDigest := `image_digests="{\"control-plane\":\"sha256:d181\",\"reliant\":\"sha256:12ac\"}"`
	if !contains(got, wantDigest) {
		t.Errorf("expected %q in dArgs, got %v", wantDigest, got)
	}

	// Empty / nil image_digests produces NO image_digests binding — so a
	// plain `kcl run` renders byte-identically (option yields None → {}).
	got = renderDArgs("tag", "ns", "", nil, nil)
	for _, a := range got {
		if strings.HasPrefix(a, "image_digests=") {
			t.Errorf("expected no image_digests entry for nil map, got %v", got)
		}
	}
}

// TestKubectlArgs_ThreadsContext is the BUG 1 regression test: when an
// ApplyOpts.Context is set, every kubectl invocation in the apply/wait
// path must carry `--context <ctx>` as the leading argument (a global
// kubectl flag, valid before any subcommand) — NOT a global
// `kubectl config use-context` switch. Per-command threading is what
// makes concurrent multi-cluster `forge env deploy` safe: two deploys
// sharing one kubeconfig but targeting different clusters can no longer
// race on the single global active context (the cross-cluster
// contamination incident). An empty context leaves the args untouched
// (current/default context — unchanged single-cluster behaviour).
func TestKubectlArgs_ThreadsContext(t *testing.T) {
	// With a context: --context <ctx> is prepended before the subcommand.
	got := KubectlArgs("prod-cluster", "apply", "--server-side", "-f", "-")
	want := []string{"--context", "prod-cluster", "apply", "--server-side", "-f", "-"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("[%d]: got %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
	// --context must be the LEADING arg (global flag, ahead of the
	// subcommand) so kubectl accepts it for every verb.
	if ic, ia := indexOf(got, "--context"), indexOf(got, "apply"); ic == -1 || ic > ia {
		t.Errorf("expected --context ahead of the subcommand, got %v", got)
	}

	// Empty context: args pass through verbatim (no --context injected).
	got = KubectlArgs("", "rollout", "status", "deployment/x")
	if contains(got, "--context") {
		t.Errorf("expected no --context for empty context, got %v", got)
	}
	if len(got) != 3 || got[0] != "rollout" {
		t.Errorf("expected unchanged args for empty context, got %v", got)
	}
}

// TestKubectlCmd_IncludesContext confirms the *exec.Cmd built by the
// single construction point (kubectlCmd) carries `--context <ctx>` in
// its argv — every kubectl-executing function in this package
// (KubectlApply, WaitRollout, WaitJobComplete, ListManagedDeployments,
// Prune, diagnoseFailedRollout) goes through it, so this pins the
// per-command context threading for the whole apply/wait path.
func TestKubectlCmd_IncludesContext(t *testing.T) {
	cmd := kubectlCmd(context.Background(), "edge-cluster", "apply", "--server-side", "-f", "-")
	// cmd.Args[0] is the binary ("kubectl"); the rest is the argv.
	if !contains(cmd.Args, "--context") || !contains(cmd.Args, "edge-cluster") {
		t.Fatalf("expected --context edge-cluster in argv, got %v", cmd.Args)
	}
	ic, ia := indexOf(cmd.Args, "--context"), indexOf(cmd.Args, "apply")
	if ic == -1 || ia == -1 || ic > ia {
		t.Errorf("expected --context ahead of the apply subcommand, got %v", cmd.Args)
	}

	// No context → no --context flag.
	cmd = kubectlCmd(context.Background(), "", "get", "deployments")
	if contains(cmd.Args, "--context") {
		t.Errorf("expected no --context for empty context, got %v", cmd.Args)
	}
}

// TestKubectlApply_ForcesConflicts pins the deploy apply to Server-Side
// Apply with --force-conflicts. forge is the declarative source of
// truth, so its SSA field manager must win unconditionally: without
// --force-conflicts, a resource previously touched by a plain
// `kubectl apply` (manager kubectl-client-side-apply) makes SSA abort
// the whole deploy with a field-manager conflict ("exit status 1").
// Both flags are required together — --force-conflicts is an SSA-only
// flag and is a no-op without --server-side.
func TestKubectlApply_ForcesConflicts(t *testing.T) {
	// Mirror exactly the argv KubectlApply constructs.
	cmd := kubectlCmd(context.Background(), "prod-cluster", "apply", "--server-side", "--force-conflicts", "-f", "-")
	if !contains(cmd.Args, "--server-side") {
		t.Errorf("expected --server-side in apply argv, got %v", cmd.Args)
	}
	if !contains(cmd.Args, "--force-conflicts") {
		t.Errorf("expected --force-conflicts in apply argv, got %v", cmd.Args)
	}
	// --force-conflicts is meaningless without --server-side; assert the
	// SSA flag precedes it so the apply stays a valid SSA invocation.
	iss, ifc := indexOf(cmd.Args, "--server-side"), indexOf(cmd.Args, "--force-conflicts")
	if iss == -1 || ifc == -1 || iss > ifc {
		t.Errorf("expected --server-side ahead of --force-conflicts, got %v", cmd.Args)
	}
}

// TestRenderedDeploymentNames verifies the extractor parses the multi-
// document YAML stream forge produces from KCL, returning only
// Deployment kind names. Non-Deployments and malformed docs are skipped.
func TestRenderedDeploymentNames(t *testing.T) {
	manifests := `apiVersion: v1
kind: Service
metadata:
  name: workspace-controller
spec:
  ports: []
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: workspace-controller
  labels:
    app.kubernetes.io/managed-by: forge
spec: {}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: cp-forge-config
data:
  KEY: value
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: workspace-proxy
spec: {}
`
	got := RenderedDeploymentNames(manifests)
	want := []string{"workspace-controller", "workspace-proxy"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

// TestExtractManifests_ReadsOutputManifests confirms the applyable stream is
// `output.manifests` — the ONE entrypoint (`output = forge.render(bundle)`) —
// and that the rest of `output` (the §9.1 entity contract: workloads,
// frontends, …) is not mistaken for manifests. Plain objects pass through
// in order, `---`-separated.
func TestExtractManifests_ReadsOutputManifests(t *testing.T) {
	in := `output:
  project: example
  workloads:
  - name: local
    kind: service
    runtime: {type: host}
    spec: {kind: service}
  frontends: []
  manifests:
  - apiVersion: v1
    kind: Namespace
    metadata:
      name: example-dev
  - apiVersion: v1
    kind: ConfigMap
    metadata:
      name: app-config
`
	got, err := ExtractManifests([]byte(in))
	if err != nil {
		t.Fatalf("extractManifests: %v", err)
	}
	if !strings.Contains(got, "kind: Namespace") || !strings.Contains(got, "kind: ConfigMap") {
		t.Errorf("expected Namespace + ConfigMap in output, got:\n%s", got)
	}
	if strings.Index(got, "kind: Namespace") > strings.Index(got, "kind: ConfigMap") {
		t.Errorf("stream order not preserved:\n%s", got)
	}
	if !strings.Contains(got, "---") {
		t.Errorf("expected `---` document separator, got:\n%s", got)
	}
	if strings.Contains(got, "runtime") || strings.Contains(got, "name: local") {
		t.Errorf("an output.workloads entry leaked into the applyable stream:\n%s", got)
	}
}

// TestExtractManifests_UnexpectedSiblingStillWarns confirms a stray public
// top-level var beside `output` does not break extraction (it warns — fire
// and forget — so a project doesn't silently lose content into a binding
// nothing reads), while output.manifests is still extracted.
func TestExtractManifests_UnexpectedSiblingStillWarns(t *testing.T) {
	in := `output:
  manifests:
  - apiVersion: v1
    kind: Namespace
    metadata:
      name: example-dev
stray_var:
  something: else
`
	got, err := ExtractManifests([]byte(in))
	if err != nil {
		t.Fatalf("extractManifests: %v", err)
	}
	if !strings.Contains(got, "kind: Namespace") {
		t.Errorf("expected Namespace in output, got:\n%s", got)
	}
}

// TestRenderedDeploymentNames_EmptyAndMalformed confirms the extractor
// degrades gracefully on edge cases: empty input, all-non-Deployment,
// and unparseable docs all return an empty slice rather than panicking.
func TestRenderedDeploymentNames_EmptyAndMalformed(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"whitespace", "   \n\n  "},
		{"no Deployments", "kind: Service\nmetadata:\n  name: x\n"},
		{"malformed YAML", "this is not yaml: : :"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RenderedDeploymentNames(c.in); len(got) != 0 {
				t.Errorf("expected empty slice, got %v", got)
			}
		})
	}
}

// filterTestManifests is a representative env bundle as RenderManifests
// emits it: two app Deployments (each carrying its own SERVICE group
// `app.kubernetes.io/name`), plus a Namespace and a ConfigMap that belong
// to NO service and therefore carry NO `app.kubernetes.io/name` group — the
// shared/infra shape. The group is always the service; shared infra is
// ungrouped, so a service `--target` drops it. `\n---\n`-joined exactly
// like the production stream.
const filterTestManifests = `apiVersion: v1
kind: Namespace
metadata:
  name: example-dev
  labels:
    app.kubernetes.io/managed-by: forge
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: example-config
  namespace: example-dev
  labels:
    app.kubernetes.io/managed-by: forge
    app.kubernetes.io/part-of: example-dev
data:
  KEY: value
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: admin-server
  namespace: example-dev
  labels:
    app.kubernetes.io/name: admin-server
    app.kubernetes.io/managed-by: forge
spec: {}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: workspace-proxy
  namespace: example-dev
  labels:
    app.kubernetes.io/name: workspace-proxy
    app.kubernetes.io/managed-by: forge
spec: {}`

// TestSelectManifestsByGroup_ExclusiveSingleGroup is the core assertion of
// the exclusive --target filter: targeting ONE service group keeps EXACTLY
// that group's manifests and drops everything else — including the ungrouped
// env-shared Namespace and ConfigMap. This is the EXCLUSIVE behaviour: no
// shared-base is auto-kept; a manifest is kept only when its KCL-declared
// service group is named.
func TestSelectManifestsByGroup_ExclusiveSingleGroup(t *testing.T) {
	got := SelectManifestsByGroup(filterTestManifests, []string{"admin-server"})
	if !strings.Contains(got, "name: admin-server") {
		t.Errorf("expected targeted group admin-server kept, got:\n%s", got)
	}
	if strings.Contains(got, "kind: Namespace") {
		t.Errorf("EXCLUSIVE: ungrouped env-shared Namespace must be DROPPED under a service target, got:\n%s", got)
	}
	if strings.Contains(got, "name: example-config") {
		t.Errorf("EXCLUSIVE: ungrouped env-shared ConfigMap must be DROPPED under a service target, got:\n%s", got)
	}
	if strings.Contains(got, "name: workspace-proxy") {
		t.Errorf("non-targeted app workspace-proxy must be dropped, got:\n%s", got)
	}
}

// TestSelectManifestsByGroup_NoSyntheticNamespaceGroup proves there is NO
// synthetic env/namespace group: the ungrouped shared manifests are NOT
// reachable by naming the namespace as a --target (it is not a service
// group). Targeting `example-dev` matches nothing, so the result is empty —
// shared infra rides only a bare deploy, never a --target.
func TestSelectManifestsByGroup_NoSyntheticNamespaceGroup(t *testing.T) {
	got := SelectManifestsByGroup(filterTestManifests, []string{"example-dev"})
	if strings.TrimSpace(got) != "" {
		t.Errorf("namespace is NOT a group — --target=example-dev must select nothing, got:\n%s", got)
	}
}

// TestSelectManifestsByGroup_MultiTargetUnion confirms multi-target is the
// UNION of ONLY the named service groups — and nothing else implicitly.
// Targeting the two app groups keeps both Deployments and DROPS the
// ungrouped env-shared Namespace + ConfigMap.
func TestSelectManifestsByGroup_MultiTargetUnion(t *testing.T) {
	got := SelectManifestsByGroup(filterTestManifests, []string{"admin-server", "workspace-proxy"})
	for _, want := range []string{"name: admin-server", "name: workspace-proxy"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in union output, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "kind: Namespace") {
		t.Errorf("EXCLUSIVE: ungrouped Namespace must NOT be implicitly kept, got:\n%s", got)
	}
	if strings.Contains(got, "name: example-config") {
		t.Errorf("EXCLUSIVE: ungrouped ConfigMap must NOT be implicitly kept, got:\n%s", got)
	}
}

// TestSelectManifestsByGroup_NoTargetIsCallerEverything documents that the
// "no --target" case is NOT this function's job — Apply keeps everything
// and never calls SelectManifestsByGroup when Targets is empty. Passing an
// empty target set here keeps nothing, which is exactly why Apply gates the
// call behind len(Targets) > 0.
func TestSelectManifestsByGroup_NoTargetIsCallerEverything(t *testing.T) {
	got := SelectManifestsByGroup(filterTestManifests, nil)
	if strings.TrimSpace(got) != "" {
		t.Errorf("empty target set selects nothing (Apply handles 'everything' itself); got:\n%s", got)
	}
}

// TestSelectManifestsByGroup_UnknownTargetSelectsNothing confirms a typo'd
// target simply matches no group and yields an empty stream — the friendly
// "did you mean" guard lives in the CLI (validateDeployTargets), not in
// this mechanical filter.
func TestSelectManifestsByGroup_UnknownTargetSelectsNothing(t *testing.T) {
	got := SelectManifestsByGroup(filterTestManifests, []string{"nope"})
	if strings.TrimSpace(got) != "" {
		t.Errorf("unknown group selects nothing, got:\n%s", got)
	}
}

// operatorFilterManifests mirrors what forge renders for an Operator:
// a Deployment plus its cluster RBAC trio (ServiceAccount, ClusterRole,
// ClusterRoleBinding), every doc carrying app.kubernetes.io/name =
// <operator>. A peer service Deployment (different app label) and a
// shared Namespace (NO app label — ungrouped) round it out — the exact
// shape the control-plane `workspace-controller` operator produces
// alongside its app services.
const operatorFilterManifests = `apiVersion: v1
kind: Namespace
metadata:
  name: example-prod
  labels:
    app.kubernetes.io/managed-by: forge
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: workspace-controller
  namespace: example-prod
  labels:
    app.kubernetes.io/name: workspace-controller
    app.kubernetes.io/managed-by: forge
spec: {}
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: workspace-controller
  namespace: example-prod
  labels:
    app.kubernetes.io/name: workspace-controller
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: workspace-controller-clusterrole
  labels:
    app.kubernetes.io/name: workspace-controller
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: workspace-controller-clusterrolebinding
  labels:
    app.kubernetes.io/name: workspace-controller
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: admin-server
  namespace: example-prod
  labels:
    app.kubernetes.io/name: admin-server
    app.kubernetes.io/managed-by: forge
spec: {}`

// TestSelectManifestsByGroup_Operator asserts an operator is a first-class
// --target group: targeting the operator name keeps EXACTLY that operator's
// Deployment AND its cluster RBAC (ServiceAccount / ClusterRole /
// ClusterRoleBinding all carry the operator's group), and DROPS everything
// else — both the unrelated service Deployment AND the ungrouped env-shared
// Namespace. This is the EXCLUSIVE
// `forge env deploy prod --target workspace-controller`.
func TestSelectManifestsByGroup_Operator(t *testing.T) {
	got := SelectManifestsByGroup(operatorFilterManifests, []string{"workspace-controller"})
	for _, want := range []string{
		"name: workspace-controller\n",                  // Deployment + ServiceAccount
		"name: workspace-controller-clusterrole\n",      // ClusterRole
		"name: workspace-controller-clusterrolebinding", // ClusterRoleBinding
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in scoped output, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "kind: Namespace") {
		t.Errorf("EXCLUSIVE: ungrouped env-shared Namespace must be dropped when only the operator is targeted, got:\n%s", got)
	}
	if strings.Contains(got, "name: admin-server") {
		t.Errorf("expected unrelated service admin-server to be dropped, got:\n%s", got)
	}
}

// jobStreamManifests is a multi-doc stream mirroring a real forge
// render: RuntimeClass + Namespace first, then a ConfigMap and a
// Secret, then a workload Deployment, a schedule=="" one-shot Job
// (the migrate pattern), and a scheduled CronJob. Used by the GAP 1
// (config-first ordering) and GAP 2 (manifest-derived Job wait) tests.
const jobStreamManifests = `apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: gvisor
handler: runsc
---
apiVersion: v1
kind: Namespace
metadata:
  name: cp-forge
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: admin-server
  namespace: cp-forge
spec: {}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
  namespace: cp-forge
data:
  KEY: value
---
apiVersion: v1
kind: Secret
metadata:
  name: db-credentials
  namespace: cp-forge
data:
  database-url: aHR0cA==
---
apiVersion: batch/v1
kind: Job
metadata:
  name: cp-forge-migrate
  namespace: cp-forge
spec: {}
---
apiVersion: batch/v1
kind: CronJob
metadata:
  name: nightly-report
  namespace: cp-forge
spec:
  schedule: "0 0 * * *"
`

// TestPartitionConfigManifests_ConfigFirst pins the GAP 1 fix: the
// config kinds (Namespace, ConfigMap, Secret) are split into the
// first-pass stream, every other kind (RuntimeClass, Deployment, Job,
// CronJob) into the second. Apply applies the config pass — and waits
// for kubectl to return — before the rest pass, so a workload pod never
// schedules ahead of the ConfigMap/Secret it references.
func TestPartitionConfigManifests_ConfigFirst(t *testing.T) {
	config, rest := PartitionConfigManifests(jobStreamManifests)

	// Config pass: exactly Namespace + ConfigMap + Secret.
	for _, want := range []string{"kind: Namespace", "name: app-config", "name: db-credentials"} {
		if !strings.Contains(config, want) {
			t.Errorf("config pass missing %q:\n%s", want, config)
		}
	}
	for _, notWant := range []string{"kind: Deployment", "kind: Job", "kind: CronJob", "kind: RuntimeClass"} {
		if strings.Contains(config, notWant) {
			t.Errorf("config pass should not contain %q:\n%s", notWant, config)
		}
	}

	// Rest pass: workloads + cluster-scoped config, NOT the namespaced
	// ConfigMap/Secret.
	for _, want := range []string{"kind: Deployment", "kind: Job", "kind: CronJob", "kind: RuntimeClass"} {
		if !strings.Contains(rest, want) {
			t.Errorf("rest pass missing %q:\n%s", want, rest)
		}
	}
	for _, notWant := range []string{"name: app-config", "name: db-credentials"} {
		if strings.Contains(rest, notWant) {
			t.Errorf("rest pass should not contain %q:\n%s", notWant, rest)
		}
	}
}

// TestPartitionConfigManifests_NoConfig confirms a bundle with no config
// kinds yields an empty config pass (so Apply skips it) and routes
// everything to rest — the unchanged single-apply behaviour.
func TestPartitionConfigManifests_NoConfig(t *testing.T) {
	const noConfig = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: admin-server
spec: {}
`
	config, rest := PartitionConfigManifests(noConfig)
	if strings.TrimSpace(config) != "" {
		t.Errorf("expected empty config pass, got:\n%s", config)
	}
	if !strings.Contains(rest, "kind: Deployment") {
		t.Errorf("expected Deployment in rest pass, got:\n%s", rest)
	}
}

// TestRenderedJobNames_PicksUpOneShotJob is the GAP 2 regression test:
// a schedule=="" CronJob renders as a `kind: Job`, and the wait set is
// derived from the rendered manifest stream, so the migrate Job is
// blocked on however it entered the bundle — including when it never
// surfaced in the typed entity list at all. A scheduled CronJob renders
// as `kind: CronJob` and is excluded.
func TestRenderedJobNames_PicksUpOneShotJob(t *testing.T) {
	got := RenderedJobNames(jobStreamManifests)
	if !contains(got, "cp-forge-migrate") {
		t.Errorf("expected one-shot Job %q in wait set, got %v", "cp-forge-migrate", got)
	}
	// The scheduled CronJob (kind: CronJob) must NOT be waited on.
	if contains(got, "nightly-report") {
		t.Errorf("scheduled CronJob should not be in the one-shot wait set, got %v", got)
	}
	// Nothing else should sneak in (Deployment, Namespace, etc.).
	if len(got) != 1 {
		t.Errorf("expected exactly one one-shot Job, got %v", got)
	}
}

// TestPodSelectorForDeploy pins the rollout-diagnostic pod selector to
// the AppNameLabel constant. A literal "app=<deploy>" selector matched
// zero pods (forge stamps app.kubernetes.io/name, not app), silently
// producing empty diagnostics on a failed rollout. Building the selector
// from the constant makes that drift impossible.
func TestPodSelectorForDeploy(t *testing.T) {
	got := podSelectorForDeploy("daemon-gateway")
	want := AppNameLabel + "=daemon-gateway"
	if got != want {
		t.Errorf("podSelectorForDeploy: got %q, want %q", got, want)
	}
	// Guard against a regression to the bare `app=` selector.
	if strings.HasPrefix(got, "app=") {
		t.Errorf("selector must use %q, not a bare app= label; got %q", AppNameLabel, got)
	}
}

// multiClusterManifests mirrors what the e2e env renders in the DECLARED-
// CLUSTER-ONLY model. The env-level Namespace stays UNLABELED (it is
// genuinely env-wide and replicated to every cluster). The ConfigMap and
// Gateway are OWNED by an image-less infra service "cp-infra" — they carry
// `app.kubernetes.io/name: cp-infra` (stamped by render_owned_manifests) so
// they route to cp-infra's declared cluster, NOT by a primary heuristic.
// admin-server + workspace-controller live on the control-plane cluster;
// workspace-proxy + its per-service RBAC `deploy` to k3d-cp-daemon. This is
// the exact shape the multi-cluster bug cross-contaminated.
const multiClusterManifests = `apiVersion: v1
kind: Namespace
metadata:
  name: control-plane-e2e
  labels:
    app.kubernetes.io/managed-by: forge
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
  namespace: control-plane-e2e
  labels:
    app.kubernetes.io/managed-by: forge
    app.kubernetes.io/name: cp-infra
data:
  KEY: value
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: public-gateway
  namespace: control-plane-e2e
  labels:
    app.kubernetes.io/name: cp-infra
spec: {}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: workspace-controller
  namespace: control-plane-e2e
  labels:
    app.kubernetes.io/name: workspace-controller
    app.kubernetes.io/managed-by: forge
spec: {}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: admin-server
  namespace: control-plane-e2e
  labels:
    app.kubernetes.io/name: admin-server
    app.kubernetes.io/managed-by: forge
spec: {}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: workspace-proxy
  namespace: control-plane-e2e
  labels:
    app.kubernetes.io/name: workspace-proxy
    app.kubernetes.io/managed-by: forge
spec: {}
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: workspace-proxy
  namespace: control-plane-e2e
  labels:
    app.kubernetes.io/name: workspace-proxy`

// TestScopeManifestsToGroup_ControlPlaneKeepsOwnAndInfra proves the
// control-plane cluster receives its own services' workloads AND the
// cp-infra-owned env-level resources (the ConfigMap + Gateway, which carry
// the infra service's app label) plus the env-wide Namespace — but NOT the
// daemon cluster's service (workspace-proxy) or its RBAC. cp-infra is in
// OwnApps because the infra service declares the control-plane cluster.
func TestScopeManifestsToGroup_ControlPlaneKeepsOwnAndInfra(t *testing.T) {
	scope := GroupScope{
		OwnApps:   map[string]struct{}{"admin-server": {}, "workspace-controller": {}, "cp-infra": {}},
		OtherApps: map[string]struct{}{"workspace-proxy": {}},
	}
	got := ScopeManifestsToGroup(multiClusterManifests, scope)

	for _, want := range []string{
		"name: admin-server",         // own service
		"name: workspace-controller", // own service (operator workload)
		"kind: Namespace",            // env-wide, replicated
		"name: app-config",           // cp-infra-owned ConfigMap
		"kind: Gateway",              // cp-infra-owned Gateway (the CRD-bearing doc)
	} {
		if !strings.Contains(got, want) {
			t.Errorf("control-plane cluster missing %q:\n%s", want, got)
		}
	}
	// The daemon cluster's service AND its RBAC must NOT land here.
	if strings.Contains(got, "name: workspace-proxy") {
		t.Errorf("daemon-cluster service workspace-proxy must not land on control-plane:\n%s", got)
	}
}

// TestScopeManifestsToGroup_DaemonOnlyOwnPlusNamespace is the load-bearing
// isolation assertion: the daemon cluster (hosting only workspace-proxy)
// receives ONLY its own service's workload + RBAC and the env-wide Namespace
// — and NONE of the other cluster's stack: no Gateway (the CRD it doesn't
// have), no cp-infra ConfigMap, no other-cluster services. This is the
// cross-contamination the bug caused, now prevented purely by ownership
// (cp-infra is in OtherApps here), no primary flag.
func TestScopeManifestsToGroup_DaemonOnlyOwnPlusNamespace(t *testing.T) {
	scope := GroupScope{
		OwnApps:   map[string]struct{}{"workspace-proxy": {}},
		OtherApps: map[string]struct{}{"admin-server": {}, "workspace-controller": {}, "cp-infra": {}},
	}
	got := ScopeManifestsToGroup(multiClusterManifests, scope)

	// Kept: the proxy Deployment, its RBAC, and the env-wide Namespace.
	if !strings.Contains(got, "name: workspace-proxy") {
		t.Errorf("daemon cluster must keep its own service workspace-proxy:\n%s", got)
	}
	if !strings.Contains(got, "kind: ServiceAccount") {
		t.Errorf("daemon cluster must keep the proxy's own RBAC:\n%s", got)
	}
	if !strings.Contains(got, "kind: Namespace") {
		t.Errorf("daemon cluster must keep the env-wide Namespace its workload lives in:\n%s", got)
	}
	// Dropped: everything owned by the other cluster (admin-server,
	// workspace-controller, and the cp-infra-owned ConfigMap/Gateway).
	for _, notWant := range []string{
		"kind: Gateway",              // cp-infra-owned CRD cp-daemon doesn't have — the hard-fail
		"name: app-config",           // cp-infra-owned ConfigMap
		"name: admin-server",         // other cluster's service
		"name: workspace-controller", // other cluster's service
	} {
		if strings.Contains(got, notWant) {
			t.Errorf("daemon cluster must NOT receive %q (owned by the other cluster):\n%s", notWant, got)
		}
	}
}

// TestScopeManifestsToGroup_NoCrossContamination is the round-trip
// invariant: partition the SAME rendered stream into the two clusters and
// assert the owned sets are disjoint and together cover every owned manifest
// exactly once — neither cluster sees the other's service or infra, the
// env-wide Namespace lands on both, and nothing is dropped entirely.
func TestScopeManifestsToGroup_NoCrossContamination(t *testing.T) {
	controlPlane := ScopeManifestsToGroup(multiClusterManifests, GroupScope{
		OwnApps:   map[string]struct{}{"admin-server": {}, "workspace-controller": {}, "cp-infra": {}},
		OtherApps: map[string]struct{}{"workspace-proxy": {}},
	})
	daemon := ScopeManifestsToGroup(multiClusterManifests, GroupScope{
		OwnApps:   map[string]struct{}{"workspace-proxy": {}},
		OtherApps: map[string]struct{}{"admin-server": {}, "workspace-controller": {}, "cp-infra": {}},
	})
	// admin-server only on control-plane.
	if !strings.Contains(controlPlane, "name: admin-server") || strings.Contains(daemon, "name: admin-server") {
		t.Errorf("admin-server must be on control-plane only")
	}
	// workspace-proxy only on daemon.
	if strings.Contains(controlPlane, "name: workspace-proxy") || !strings.Contains(daemon, "name: workspace-proxy") {
		t.Errorf("workspace-proxy must be on daemon only")
	}
	// The cp-infra-owned Gateway (CRD-bearing) only on control-plane — the
	// cp-daemon hard-fail guard, now purely ownership-driven.
	if !strings.Contains(controlPlane, "kind: Gateway") || strings.Contains(daemon, "kind: Gateway") {
		t.Errorf("Gateway must be on control-plane only (its owner cp-infra targets it)")
	}
	// The env-wide Namespace lands on BOTH (replicated, not guessed).
	if !strings.Contains(controlPlane, "kind: Namespace") || !strings.Contains(daemon, "kind: Namespace") {
		t.Errorf("the env-wide Namespace must be replicated to both clusters")
	}
}

// TestScopeManifestsToGroup_UnlabeledNonNamespaceReplicated documents the
// "replicate, don't guess" rule: an env-level resource the user left
// UNLABELED (not attributed to any cluster via an infra service) is kept on
// every cluster rather than routed to a guessed primary. To pin it to one
// cluster, the user declares it on an infra service's `manifests`.
func TestScopeManifestsToGroup_UnlabeledNonNamespaceReplicated(t *testing.T) {
	const stream = `apiVersion: v1
kind: ConfigMap
metadata:
  name: shared-config
  namespace: ns
data:
  KEY: value
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: admin-server
  labels:
    app.kubernetes.io/name: admin-server
spec: {}`
	controlPlane := ScopeManifestsToGroup(stream, GroupScope{
		OwnApps:   map[string]struct{}{"admin-server": {}},
		OtherApps: map[string]struct{}{"workspace-proxy": {}},
	})
	daemon := ScopeManifestsToGroup(stream, GroupScope{
		OwnApps:   map[string]struct{}{"workspace-proxy": {}},
		OtherApps: map[string]struct{}{"admin-server": {}},
	})
	if !strings.Contains(controlPlane, "name: shared-config") || !strings.Contains(daemon, "name: shared-config") {
		t.Errorf("an unlabeled env-level ConfigMap must be replicated to every cluster, not routed to a guessed primary")
	}
	// The labeled workload still routes by owner.
	if !strings.Contains(controlPlane, "name: admin-server") || strings.Contains(daemon, "name: admin-server") {
		t.Errorf("admin-server must route to its own cluster only")
	}
}

// TestScopeManifestsToGroup_UngroupedAppLabelKept covers the defensive
// branch: an app-labelled doc whose owner is in NEITHER OwnApps nor
// OtherApps (should not happen once every service is grouped) is KEPT rather
// than dropped or routed by guess — the safe non-heuristic default.
func TestScopeManifestsToGroup_UngroupedAppLabelKept(t *testing.T) {
	const stream = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: orphan
  labels:
    app.kubernetes.io/name: orphan
spec: {}`
	got := ScopeManifestsToGroup(stream, GroupScope{
		OwnApps:   map[string]struct{}{"admin-server": {}},
		OtherApps: map[string]struct{}{"workspace-proxy": {}},
	})
	if !strings.Contains(got, "name: orphan") {
		t.Errorf("an app-labelled doc owned by no group must be kept, not silently dropped:\n%s", got)
	}
}

// clusterPinnedManifests is a two-cluster ingress stream where the
// Gateway carries the FIRST-CLASS `forge.dev/cluster` routing label
// (forge's gateway builder stamps it when `cluster = "control-plane"` is
// set) instead of riding an unrelated service's app label.
const clusterPinnedManifests = `apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: cp-public
  labels:
    app.kubernetes.io/name: cp-public
    forge.dev/cluster: control-plane
spec: {}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: workspace-proxy
  labels:
    app.kubernetes.io/name: workspace-proxy
spec: {}`

// TestScopeManifestsToGroup_ClusterLabelKeepsOnNamedCluster asserts a
// manifest stamped `forge.dev/cluster: control-plane` lands on the
// control-plane group (whose GroupScope.Cluster matches) — routed by the
// first-class label, not by any app-label membership.
func TestScopeManifestsToGroup_ClusterLabelKeepsOnNamedCluster(t *testing.T) {
	got := ScopeManifestsToGroup(clusterPinnedManifests, GroupScope{
		Cluster:   "control-plane",
		OwnApps:   map[string]struct{}{"admin-server": {}},
		OtherApps: map[string]struct{}{"workspace-proxy": {}},
	})
	if !strings.Contains(got, "name: cp-public") {
		t.Errorf("a forge.dev/cluster=control-plane manifest must land on the control-plane group:\n%s", got)
	}
	// The other cluster's app-labelled workload still drops via OtherApps.
	if strings.Contains(got, "name: workspace-proxy") {
		t.Errorf("workspace-proxy is OtherApps and must be dropped from control-plane:\n%s", got)
	}
}

// TestScopeManifestsToGroup_ClusterLabelDroppedFromSibling is the
// isolation half: the SAME first-class-pinned manifest is dropped from a
// SIBLING cluster whose name doesn't match the routing label — even
// though that cluster has no app-label opinion about the Gateway. This is
// what makes `cluster = "control-plane"` keep the Gateway off the daemon
// cluster without the app-label collision trick.
func TestScopeManifestsToGroup_ClusterLabelDroppedFromSibling(t *testing.T) {
	got := ScopeManifestsToGroup(clusterPinnedManifests, GroupScope{
		Cluster:   "daemon",
		OwnApps:   map[string]struct{}{"workspace-proxy": {}},
		OtherApps: map[string]struct{}{"admin-server": {}},
	})
	if strings.Contains(got, "name: cp-public") {
		t.Errorf("a forge.dev/cluster=control-plane manifest must be dropped from the daemon cluster:\n%s", got)
	}
	// The daemon's own service is kept.
	if !strings.Contains(got, "name: workspace-proxy") {
		t.Errorf("daemon's own workspace-proxy must be kept:\n%s", got)
	}
}

// TestScopeManifestsToGroup_ClusterLabelIgnoredWhenScopeClusterUnset
// documents the fall-through: with GroupScope.Cluster empty, the
// first-class label is NOT consulted and the doc routes purely by its app
// label (here cp-public is ungrouped → kept defensively). This keeps the
// label inert for callers that haven't adopted the Cluster field.
func TestScopeManifestsToGroup_ClusterLabelIgnoredWhenScopeClusterUnset(t *testing.T) {
	got := ScopeManifestsToGroup(clusterPinnedManifests, GroupScope{
		OwnApps:   map[string]struct{}{"admin-server": {}},
		OtherApps: map[string]struct{}{"workspace-proxy": {}},
	})
	if !strings.Contains(got, "name: cp-public") {
		t.Errorf("with no scope.Cluster, cp-public routes by app label (ungrouped → kept):\n%s", got)
	}
}

// immutableJobManifests is a minimal warm-redeploy bundle: a namespaced
// Job (the kind whose spec.template k8s treats as immutable) plus an
// unrelated Deployment. The recovery must scope its delete to the Job and
// nothing else.
const immutableJobManifests = `apiVersion: batch/v1
kind: Job
metadata:
  name: control-plane-migrate
  namespace: app
spec:
  template:
    spec:
      containers:
        - name: migrate
          image: repo/migrate:newtag
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: app
spec:
  replicas: 1`

// realImmutableStderr is the kubectl error a warm `forge env up` produces when
// the migrate Job already exists with a different image tag (every commit
// changes the tag, and a Job's spec.template is immutable). This is the
// CLIENT-SIDE apply shape (`The <Kind> "<name>" is invalid:`).
const realImmutableStderr = `The Job "control-plane-migrate" is invalid: spec.template: Invalid value: ...: field is immutable`

// cloudImmutableStderr is the SERVER-SIDE apply shape of the very same
// failure — the one a warm CLOUD `forge env deploy` actually hits, because
// forge always applies with `--server-side`. kubectl encodes the resource
// as the GVR-style `<resource>.<group>` ("Job.batch") rather than the bare
// kind ("Job"). The recovery used to parse kind="Job.batch", fail to match
// any `kind: Job` document, lose the namespace, delete in the wrong
// namespace as a no-op, and re-hit the immutable Job — exiting 1. The
// kind-normalization fix in parseInvalidResource makes both shapes recover
// the namespace identically.
const cloudImmutableStderr = `Job.batch "control-plane-migrate" is invalid: spec.template: Invalid value: core.PodTemplateSpec{...}: field is immutable`

// stderrOnlyApply lifts a stderr-only apply closure into the (stdout,
// stderr, err) shape applyWithImmutableRecovery takes. The
// immutable-recovery tests classify on stderr alone; the stdout half is
// the apply-completeness check's input and is exercised separately in
// apply_completeness_test.go.
func stderrOnlyApply(f func() (string, error)) func() (string, string, error) {
	return func() (string, string, error) {
		stderr, err := f()
		return "", stderr, err
	}
}

// noopWaitGone is the wait-for-deletion closure for tests that don't
// exercise the poll itself — the resource is considered immediately gone.
func noopWaitGone(immutableTarget) error { return nil }

// TestApplyWithImmutableRecovery_DeletesJobThenReapplies pins the warm
// re-deploy fix: a Job whose image tag changed makes the first apply fail
// with the immutable-field error; forge must delete THAT Job (scoped, in
// its namespace) and re-apply once, after which the deploy succeeds. The
// trigger keys off the immutable error + the Job kind parsed from it — it
// is NOT hardcoded to "control-plane-migrate".
func TestApplyWithImmutableRecovery_DeletesJobThenReapplies(t *testing.T) {
	var applies int
	var deleted []immutableTarget

	apply := func() (string, error) {
		applies++
		if applies == 1 {
			// First apply fails immutable, as a real warm re-deploy does.
			return realImmutableStderr, errors.New("exit status 1")
		}
		return "", nil // re-apply after the delete succeeds
	}
	del := func(t immutableTarget) error {
		deleted = append(deleted, t)
		return nil
	}

	if _, err := applyWithImmutableRecovery(immutableJobManifests, stderrOnlyApply(apply), del, noopWaitGone); err != nil {
		t.Fatalf("recovery should have healed the immutable apply, got %v", err)
	}
	if applies != 2 {
		t.Fatalf("expected exactly two applies (fail then re-apply), got %d", applies)
	}
	if len(deleted) != 1 {
		t.Fatalf("expected exactly one scoped delete, got %v", deleted)
	}
	got := deleted[0]
	if got.Kind != "Job" || got.Name != "control-plane-migrate" || got.Namespace != "app" {
		t.Fatalf("delete must target the failing Job in its namespace, got %+v", got)
	}
}

// TestApplyWithImmutableRecovery_NonImmutableErrorSurfaces confirms the
// recovery is surgical: an apply error that is NOT the immutable-field
// case is returned unchanged, with no delete and no second apply.
func TestApplyWithImmutableRecovery_NonImmutableErrorSurfaces(t *testing.T) {
	var applies, deletes int
	orig := errors.New("exit status 1")
	apply := func() (string, error) {
		applies++
		return "Error from server (NotFound): namespaces \"app\" not found", orig
	}
	del := func(immutableTarget) error { deletes++; return nil }

	_, err := applyWithImmutableRecovery(immutableJobManifests, stderrOnlyApply(apply), del, noopWaitGone)
	if !errors.Is(err, orig) {
		t.Fatalf("a non-immutable error must surface unchanged, got %v", err)
	}
	if applies != 1 || deletes != 0 {
		t.Fatalf("non-immutable failure must not delete or re-apply (applies=%d deletes=%d)", applies, deletes)
	}
}

// TestApplyWithImmutableRecovery_ReapplyStillFailsSurfacesOriginal pins
// the safety property: if EVERY re-apply after the delete still hits the
// same immutable error, the recovery exhausts its bounded retries and
// surfaces the immutable error rather than swallowing it (a genuinely
// stuck resource must NOT silently succeed). It also verifies the loop is
// bounded — apply runs the initial attempt plus exactly
// immutableRecoveryAttempts retries, never unbounded.
func TestApplyWithImmutableRecovery_ReapplyStillFailsSurfacesOriginal(t *testing.T) {
	orig := errors.New("exit status 1")
	var applies, deletes int
	apply := func() (string, error) { applies++; return realImmutableStderr, orig }
	del := func(immutableTarget) error { deletes++; return nil }

	if _, err := applyWithImmutableRecovery(immutableJobManifests, stderrOnlyApply(apply), del, noopWaitGone); !errors.Is(err, orig) {
		t.Fatalf("a still-failing re-apply must surface the immutable error, got %v", err)
	}
	if applies != 1+immutableRecoveryAttempts {
		t.Fatalf("recovery must be bounded: expected %d applies (initial + %d retries), got %d",
			1+immutableRecoveryAttempts, immutableRecoveryAttempts, applies)
	}
	if deletes != immutableRecoveryAttempts {
		t.Fatalf("expected %d scoped deletes (one per retry), got %d", immutableRecoveryAttempts, deletes)
	}
}

// TestImmutableResource_ParsesKindNameNamespace checks the pure detector:
// the immutable-field error yields the Job kind+name, and the namespace is
// recovered by matching that resource back to its manifest document.
func TestImmutableResource_ParsesKindNameNamespace(t *testing.T) {
	res, ok := immutableResource(realImmutableStderr, immutableJobManifests)
	if !ok {
		t.Fatal("an immutable-field error must be recognized as recoverable")
	}
	if res.Kind != "Job" || res.Name != "control-plane-migrate" || res.Namespace != "app" {
		t.Fatalf("parsed the wrong resource: %+v", res)
	}

	// Not an immutable error → not recoverable.
	if _, ok := immutableResource("Error from server (NotFound): ...", immutableJobManifests); ok {
		t.Fatal("a non-immutable error must not be treated as recoverable")
	}
	// Immutable error naming a resource absent from the bundle still
	// reports the kind+name (namespace just comes back empty).
	other := `The Job "absent" is invalid: spec.template: field is immutable`
	res, ok = immutableResource(other, immutableJobManifests)
	if !ok || res.Name != "absent" || res.Namespace != "" {
		t.Fatalf("unmatched resource should parse with empty namespace, got ok=%v res=%+v", ok, res)
	}
}

// TestImmutableResource_ServerSideApplyShape is the regression test for the
// CLOUD deploy bug: forge applies with `--server-side`, so a warm redeploy
// of a changed migrate Job fails with the GVR-style `Job.batch "..." is
// invalid:` shape — not the client-side `The Job "..."` shape. Before the
// fix, kind parsed as "Job.batch", matched no `kind: Job` document, and the
// namespace came back empty — so the recovery deleted in the wrong
// namespace and the re-apply re-hit the immutable Job (deploy exit 1). The
// fix normalizes the kind to the bare `Job`, recovering the namespace so
// the delete is scoped correctly.
func TestImmutableResource_ServerSideApplyShape(t *testing.T) {
	res, ok := immutableResource(cloudImmutableStderr, immutableJobManifests)
	if !ok {
		t.Fatal("the server-side-apply immutable error must be recognized as recoverable")
	}
	if res.Kind != "Job" {
		t.Fatalf("kind must be normalized from the GVR form Job.batch to the bare Job, got %q", res.Kind)
	}
	if res.Name != "control-plane-migrate" {
		t.Fatalf("name must parse from the SSA shape, got %q", res.Name)
	}
	if res.Namespace != "app" {
		t.Fatalf("CLOUD BUG: namespace must be recovered (matched back to the kind: Job document), got %q", res.Namespace)
	}
}

// TestApplyWithImmutableRecovery_CloudShape_DeleteWaitReapply is the
// end-to-end recovery test for the cloud apply path: the first apply fails
// with the server-side (`Job.batch …`) immutable error, the recovery must
// delete EXACTLY the failing Job in its namespace, and the re-apply must
// then succeed (exit 0). This is the warm-cloud-redeploy scenario that
// previously raced/failed.
func TestApplyWithImmutableRecovery_CloudShape_DeleteWaitReapply(t *testing.T) {
	var applies int
	var deleted []immutableTarget

	apply := func() (string, error) {
		applies++
		if applies == 1 {
			// First apply: the server-side immutable failure a warm cloud
			// redeploy produces.
			return cloudImmutableStderr, errors.New("exit status 1")
		}
		// After the scoped delete the Job is gone, so the re-apply creates
		// it cleanly — the deploy exits 0.
		return "", nil
	}
	del := func(target immutableTarget) error {
		deleted = append(deleted, target)
		return nil
	}

	if _, err := applyWithImmutableRecovery(immutableJobManifests, stderrOnlyApply(apply), del, noopWaitGone); err != nil {
		t.Fatalf("recovery should have healed the server-side immutable apply, got %v", err)
	}
	if applies != 2 {
		t.Fatalf("expected fail-then-reapply (2 applies), got %d", applies)
	}
	if len(deleted) != 1 {
		t.Fatalf("expected exactly one scoped delete, got %v", deleted)
	}
	got := deleted[0]
	if got.Kind != "Job" || got.Name != "control-plane-migrate" || got.Namespace != "app" {
		t.Fatalf("delete must target the failing Job in its namespace (kind normalized from Job.batch), got %+v", got)
	}
}

// TestApplyWithImmutableRecovery_ReapplyRacesThenSucceeds is the direct
// regression for the staging-0 / prod-1 split. On prod the FIRST re-apply
// after the delete still raced the not-yet-finalized Job and re-hit the
// immutable error, and the old single-shot recovery surfaced that as exit
// 1 — even though the resource was on its way out. The new bounded
// delete→wait-gone→re-apply loop must absorb that race: the second
// re-apply (after another delete+wait) creates the Job cleanly, so the
// OVERALL apply returns success (exit 0). Verifies the wait-gone poll runs
// before each re-apply, and that the delete+reapply was retried.
func TestApplyWithImmutableRecovery_ReapplyRacesThenSucceeds(t *testing.T) {
	var applies, deletes, waits int

	apply := func() (string, error) {
		applies++
		switch applies {
		case 1:
			// Initial apply: warm-redeploy immutable failure.
			return cloudImmutableStderr, errors.New("exit status 1")
		case 2:
			// First re-apply STILL races the finalizing Job (the prod flake).
			return cloudImmutableStderr, errors.New("exit status 1")
		default:
			// Second re-apply: the Job is finally gone, apply succeeds → exit 0.
			return "", nil
		}
	}
	del := func(immutableTarget) error { deletes++; return nil }
	waitGone := func(immutableTarget) error { waits++; return nil }

	if _, err := applyWithImmutableRecovery(immutableJobManifests, stderrOnlyApply(apply), del, waitGone); err != nil {
		t.Fatalf("recovery must absorb the re-apply race and exit 0, got %v", err)
	}
	if applies != 3 {
		t.Fatalf("expected 3 applies (initial + 2 retries, the second succeeding), got %d", applies)
	}
	if deletes != 2 {
		t.Fatalf("expected 2 deletes (one per retry cycle), got %d", deletes)
	}
	if waits != 2 {
		t.Fatalf("expected wait-for-deletion before each re-apply (2 waits), got %d", waits)
	}
}

// TestApplyWithImmutableRecovery_DeleteErrorSurfaces confirms a failing
// delete during recovery is reported (wrapped) rather than swallowed or
// retried into a loop — a delete that can't run is a genuine recovery
// failure, not the race the loop exists to absorb.
func TestApplyWithImmutableRecovery_DeleteErrorSurfaces(t *testing.T) {
	delErr := errors.New("kubectl delete: forbidden")
	var applies, deletes int
	apply := func() (string, error) {
		applies++
		return cloudImmutableStderr, errors.New("exit status 1")
	}
	del := func(immutableTarget) error { deletes++; return delErr }

	_, err := applyWithImmutableRecovery(immutableJobManifests, stderrOnlyApply(apply), del, noopWaitGone)
	if !errors.Is(err, delErr) {
		t.Fatalf("a delete failure must surface (wrapped), got %v", err)
	}
	if applies != 1 || deletes != 1 {
		t.Fatalf("a delete failure must abort immediately (applies=%d deletes=%d)", applies, deletes)
	}
}

// batchedImmutableStderr is the shape a warm CLOUD redeploy produces when the
// SINGLE server-side apply of the workload batch hits MORE THAN ONE immutable
// conflict at once — kubectl emits one `<Kind> "<name>" is invalid: … field is
// immutable` line per offending resource. Here the migrate Job AND the api
// Deployment both report an immutable field. (Both resources are present in
// immutableJobManifests so their namespaces recover.)
const batchedImmutableStderr = `Job.batch "control-plane-migrate" is invalid: spec.template: Invalid value: core.PodTemplateSpec{...}: field is immutable
Deployment.apps "api" is invalid: spec.selector: Invalid value: ...: field is immutable`

// TestImmutableResources_BatchReportsEveryConflict pins the batch-aware
// detector: a single apply whose stderr lists TWO immutable conflicts yields
// BOTH targets (kind/name/namespace each recovered), not just the first.
func TestImmutableResources_BatchReportsEveryConflict(t *testing.T) {
	got := immutableResources(batchedImmutableStderr, immutableJobManifests)
	if len(got) != 2 {
		t.Fatalf("a batched apply with two immutable conflicts must yield two targets, got %d: %+v", len(got), got)
	}
	want := map[string]string{
		"Job/control-plane-migrate": "app",
		"Deployment/api":            "app",
	}
	for _, tgt := range got {
		key := tgt.Kind + "/" + tgt.Name
		ns, ok := want[key]
		if !ok {
			t.Fatalf("unexpected target %+v", tgt)
		}
		if tgt.Namespace != ns {
			t.Fatalf("target %s: namespace must recover to %q, got %q", key, ns, tgt.Namespace)
		}
	}

	// A stderr with NO immutable conflict yields no targets — the caller
	// surfaces such an error unchanged.
	if got := immutableResources(`Error from server (NotFound): namespaces "app" not found`, immutableJobManifests); len(got) != 0 {
		t.Fatalf("a non-immutable error must yield no recoverable targets, got %+v", got)
	}
	// A batch where one resource is immutable but a DIFFERENT one failed for an
	// unrelated reason must recover ONLY the immutable one — the per-resource
	// body scoping keeps the non-immutable failure from masquerading.
	mixed := `Job.batch "control-plane-migrate" is invalid: spec.template: ...: field is immutable
Deployment.apps "api" is invalid: spec.replicas: Invalid value: must be non-negative`
	gotMixed := immutableResources(mixed, immutableJobManifests)
	if len(gotMixed) != 1 || gotMixed[0].Name != "control-plane-migrate" {
		t.Fatalf("only the immutable resource must be recoverable in a mixed batch, got %+v", gotMixed)
	}
}

// TestApplyWithImmutableRecovery_BatchedMultiConflict_ExitsZero is the direct
// regression for the prod `forge env deploy` exit-1-on-a-healthy-cluster bug. The
// workload batch is applied in ONE server-side apply, so a warm redeploy can
// report MORE THAN ONE immutable conflict at once (the migrate Job AND a
// second resource). The OLD recovery only re-entered for the SAME Kind/Name as
// the first conflict, so the second immutable resource was misclassified as
// "unrelated" and its non-zero status propagated to the deploy's exit code —
// exit 1 — even though deleting+re-applying it would have healed it and every
// pod was already running. The recovery must now delete EVERY immutable
// resource in the batch and re-apply, after which the apply succeeds (exit 0).
func TestApplyWithImmutableRecovery_BatchedMultiConflict_ExitsZero(t *testing.T) {
	var applies int
	var deleted []immutableTarget
	gone := map[string]bool{}

	apply := func() (string, error) {
		applies++
		// The re-apply only succeeds once BOTH offenders have actually been
		// deleted — realistic: re-applying the whole batch while a second
		// immutable resource is still present re-hits its immutable field.
		// (This is what makes single-resource recovery exit 1 on prod.)
		jobGone := gone["Job/control-plane-migrate"]
		depGone := gone["Deployment/api"]
		switch {
		case jobGone && depGone:
			return "", nil
		case jobGone && !depGone:
			// Job healed, Deployment still immutable.
			return `Deployment.apps "api" is invalid: spec.selector: Invalid value: ...: field is immutable`, errors.New("exit status 1")
		case !jobGone && depGone:
			return `Job.batch "control-plane-migrate" is invalid: spec.template: Invalid value: ...: field is immutable`, errors.New("exit status 1")
		default:
			// Neither deleted yet — both immutable (the initial batched apply).
			return batchedImmutableStderr, errors.New("exit status 1")
		}
	}
	del := func(target immutableTarget) error {
		deleted = append(deleted, target)
		gone[target.Kind+"/"+target.Name] = true
		return nil
	}

	if _, err := applyWithImmutableRecovery(immutableJobManifests, stderrOnlyApply(apply), del, noopWaitGone); err != nil {
		t.Fatalf("recovery must heal EVERY immutable conflict in the batch and exit 0, got %v", err)
	}
	if applies != 2 {
		t.Fatalf("expected fail-then-reapply (2 applies), got %d", applies)
	}
	if len(deleted) != 2 {
		t.Fatalf("recovery must delete BOTH immutable resources, deleted %d: %+v", len(deleted), deleted)
	}
	// Both offenders, each scoped to its namespace.
	gotNames := map[string]string{}
	for _, d := range deleted {
		gotNames[d.Kind+"/"+d.Name] = d.Namespace
	}
	for key, ns := range map[string]string{
		"Job/control-plane-migrate": "app",
		"Deployment/api":            "app",
	} {
		if gotNames[key] != ns {
			t.Fatalf("delete must scope %s to namespace %q, got %q (all: %+v)", key, ns, gotNames[key], deleted)
		}
	}
}

// scImmutableStderr is the apiserver's real rejection of a StorageClass
// `parameters` change (captured from a k3d cluster, server-side apply, forge's
// exact flags). StorageClass is cluster-scoped and owns nothing: bound PVs carry
// their own resolved disk spec, so delete + re-apply is safe for it.
const scImmutableStderr = `The StorageClass "workspace-ssd" is invalid: parameters: Invalid value: {"labels":"a=1","type":"pd-balanced"}: field is immutable`

const storageClassManifest = `apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: workspace-ssd
provisioner: pd.csi.storage.gke.io
parameters:
  type: pd-balanced`

// TestApplyWithImmutableRecovery_RecreatesStorageClass pins that a StorageClass
// parameters change is healed by deleting the class (cluster-scoped: empty
// namespace) and re-applying, not surfaced as a failed deploy.
func TestApplyWithImmutableRecovery_RecreatesStorageClass(t *testing.T) {
	var applies int
	var deleted []immutableTarget
	apply := func() (string, error) {
		applies++
		if applies == 1 {
			return scImmutableStderr, errors.New("exit status 1")
		}
		return "", nil
	}
	del := func(t immutableTarget) error { deleted = append(deleted, t); return nil }

	if _, err := applyWithImmutableRecovery(storageClassManifest, stderrOnlyApply(apply), del, noopWaitGone); err != nil {
		t.Fatalf("a StorageClass parameters change must recover, got %v", err)
	}
	if applies != 2 || len(deleted) != 1 {
		t.Fatalf("want fail, delete, re-apply (applies=2 deletes=1), got applies=%d deletes=%v", applies, deleted)
	}
	if got := deleted[0]; got.Kind != "StorageClass" || got.Name != "workspace-ssd" || got.Namespace != "" {
		t.Fatalf("delete must target the cluster-scoped StorageClass, got %+v", got)
	}
}

// gkeSCForbiddenStderr is GKE's (v1.35.8) rejection of the SAME StorageClass
// parameters change, verbatim from a failed `forge env deploy prod`. GKE's
// apiserver says "Forbidden: updates to parameters are forbidden", not k3d's
// "field is immutable" — and the recovery that only knew the k3d phrasing
// passed every test above while failing the real deploy.
const gkeSCForbiddenStderr = `The StorageClass "workspace-ssd" is invalid: parameters: Forbidden: updates to parameters are forbidden.`

func TestApplyWithImmutableRecovery_RecreatesStorageClassOnGKEPhrasing(t *testing.T) {
	var applies int
	var deleted []immutableTarget
	apply := func() (string, error) {
		applies++
		if applies == 1 {
			return gkeSCForbiddenStderr, errors.New("exit status 1")
		}
		return "", nil
	}
	del := func(t immutableTarget) error { deleted = append(deleted, t); return nil }

	if _, err := applyWithImmutableRecovery(storageClassManifest, stderrOnlyApply(apply), del, noopWaitGone); err != nil {
		t.Fatalf("GKE's forbidden-update phrasing must recover like k3d's, got %v", err)
	}
	if applies != 2 || len(deleted) != 1 || deleted[0].Kind != "StorageClass" || deleted[0].Name != "workspace-ssd" {
		t.Fatalf("want fail, delete StorageClass/workspace-ssd, re-apply; got applies=%d deletes=%+v", applies, deleted)
	}
}

// The forbidden-update phrasing widens what counts as IMMUTABLE, not what may
// be DELETED: a kind that owns data still fails loudly in that phrasing too.
func TestImmutableRecovery_ForbiddenUpdateOnDataKindIsNotRecreated(t *testing.T) {
	stderr := `The StatefulSet "db" is invalid: spec: Forbidden: updates to statefulset spec for fields other than 'replicas' are forbidden`
	if got := immutableResources(stderr, ""); len(got) != 0 {
		t.Fatalf("StatefulSet must never be auto-deleted, got %+v", got)
	}
}

// An unrelated Forbidden (RBAC, admission) is not an immutable update.
func TestImmutableRecovery_UnrelatedForbiddenIsNotRecoverable(t *testing.T) {
	stderr := `The StorageClass "workspace-ssd" is invalid: metadata.labels: Forbidden: label not allowed`
	if got := immutableResources(stderr, storageClassManifest); len(got) != 0 {
		t.Fatalf("a non-immutability Forbidden must surface unchanged, got %+v", got)
	}
}

// TestImmutableRecovery_RefusesKindsThatOwnData pins the safety boundary the
// recovery had implicitly: a PersistentVolumeClaim's immutable field must NEVER
// be answered by deleting it (that destroys a workspace's disk). Only kinds
// whose delete loses no state are recoverable.
func TestImmutableRecovery_RefusesKindsThatOwnData(t *testing.T) {
	for _, kind := range []string{"PersistentVolumeClaim", "PersistentVolume", "StatefulSet", "Namespace", "CustomResourceDefinition"} {
		stderr := fmt.Sprintf(`The %s "x" is invalid: spec: Forbidden: field is immutable`, kind)
		if got := immutableResources(stderr, ""); len(got) != 0 {
			t.Errorf("%s immutable conflict must not be auto-deleted, got %+v", kind, got)
		}
	}
	if got := immutableResources(`The Job "j" is invalid: spec.template: field is immutable`, ""); len(got) != 1 {
		t.Errorf("Job must stay recoverable, got %+v", got)
	}
}
