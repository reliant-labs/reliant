//go:build !windows

// envtest's process package does not compile for windows (see
// eso_vap_envtest_test.go); the same tag applies here.

package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// Spec-driven reconciliation against a REAL kube-apiserver, through the real
// kubectl and the real apply path (KubectlApplyNamespaced). It reproduces the
// three shapes of drift prod's control-plane carried on 2026-11-04 — a
// PPROF_ENABLED env var `kubectl` (Update) added to admin-server, a `scratch`
// volume nobody owned on daemon-gateway, and a serviceAccountName nobody owned
// on zitadel — and proves one forge apply removes all three while keeping what
// the cluster itself wrote (a controller's annotation, a scale through the
// scale subresource, server defaults, a Service's allocated clusterIP). Before
// field_ownership.go every one of the three survived the apply.
//
// Needs the envtest binaries (KUBEBUILDER_ASSETS) and kubectl on PATH: skipped
// without them and under -short.

const ownershipNS = "prod"

// ownershipV1 is what an OLDER render declared (and what a legacy applier
// wrote): it still has the scratch volume and the service account.
const ownershipV1 = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: admin-server
  namespace: prod
  labels: {app.kubernetes.io/name: admin-server}
spec:
  selector: {matchLabels: {app.kubernetes.io/name: admin-server}}
  template:
    metadata: {labels: {app.kubernetes.io/name: admin-server}}
    spec:
      serviceAccountName: zitadel
      containers:
      - name: admin-server
        image: registry.example/admin-server:v1
        env: [{name: LOG_LEVEL, value: info}]
        volumeMounts: [{name: tmp, mountPath: /tmp}]
      volumes:
      - {name: tmp, emptyDir: {}}
      - {name: scratch, emptyDir: {}}
`

// ownershipV2 is today's declaration: no scratch, no service account, and no
// PPROF_ENABLED. It also declares no replicas (an autoscaler owns them).
const ownershipV2 = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: admin-server
  namespace: prod
  labels: {app.kubernetes.io/name: admin-server}
spec:
  selector: {matchLabels: {app.kubernetes.io/name: admin-server}}
  template:
    metadata: {labels: {app.kubernetes.io/name: admin-server}}
    spec:
      containers:
      - name: admin-server
        image: registry.example/admin-server:v2
        env: [{name: LOG_LEVEL, value: info}]
        volumeMounts: [{name: tmp, mountPath: /tmp}]
      volumes:
      - {name: tmp, emptyDir: {}}
---
apiVersion: v1
kind: Service
metadata:
  name: admin-server
  namespace: prod
spec:
  selector: {app.kubernetes.io/name: admin-server}
  ports: [{name: http, port: 80, targetPort: 8080}]
`

type ownershipEnv struct {
	t    *testing.T
	kctx string
}

func startOwnershipEnvtest(t *testing.T) *ownershipEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("starts kube-apiserver and etcd and execs kubectl; runs in task test")
	}
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		t.Skip("KUBEBUILDER_ASSETS not set (setup-envtest use); the claim logic is also pinned by field_ownership_test.go")
	}
	if _, err := exec.LookPath("kubectl"); err != nil {
		t.Skip("kubectl not on PATH")
	}
	env := &envtest.Environment{BinaryAssetsDirectory: assets}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	kc := clientcmdapi.NewConfig()
	kc.Clusters["envtest"] = &clientcmdapi.Cluster{Server: cfg.Host, CertificateAuthorityData: cfg.CAData}
	kc.AuthInfos["admin"] = &clientcmdapi.AuthInfo{ClientCertificateData: cfg.CertData, ClientKeyData: cfg.KeyData}
	kc.Contexts["envtest"] = &clientcmdapi.Context{Cluster: "envtest", AuthInfo: "admin"}
	kcPath := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*kc, kcPath); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kcPath)
	e := &ownershipEnv{t: t, kctx: "envtest"}
	e.kubectl("", "create", "namespace", ownershipNS)
	return e
}

func (e *ownershipEnv) kubectl(stdin string, args ...string) string {
	e.t.Helper()
	cmd := exec.Command("kubectl", append([]string{"--context", e.kctx}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		e.t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, errb.String())
	}
	return out.String()
}

func (e *ownershipEnv) get(kind, name string) map[string]any {
	e.t.Helper()
	var o map[string]any
	if err := json.Unmarshal([]byte(e.kubectl("", "get", kind, name, "-n", ownershipNS, "-o", "json", "--show-managed-fields")), &o); err != nil {
		e.t.Fatal(err)
	}
	return o
}

func (e *ownershipEnv) forgeApply(manifests string) {
	e.t.Helper()
	if err := KubectlApplyNamespaced(context.Background(), e.kctx, ownershipNS, manifests); err != nil {
		e.t.Fatalf("forge apply: %v", err)
	}
}

// dropManager removes one manager's managedFields entry, which is how the
// fields only it owned become ownerless — the state prod's objects were in.
func (e *ownershipEnv) dropManager(kind, name, manager string) {
	e.t.Helper()
	o := e.get(kind, name)
	entries := o["metadata"].(map[string]any)["managedFields"].([]any)
	var kept []any
	for _, en := range entries {
		if en.(map[string]any)["manager"] != manager {
			kept = append(kept, en)
		}
	}
	if len(kept) == len(entries) {
		e.t.Fatalf("%s/%s has no %s entry to drop", kind, name, manager)
	}
	patch, _ := json.Marshal([]map[string]any{{"op": "replace", "path": "/metadata/managedFields", "value": kept}})
	e.kubectl("", "patch", kind, name, "-n", ownershipNS, "--type=json", "-p", string(patch))
}

func managers(o map[string]any) map[string]bool {
	out := map[string]bool{}
	for _, en := range o["metadata"].(map[string]any)["managedFields"].([]any) {
		m := en.(map[string]any)
		out[m["manager"].(string)+"/"+m["operation"].(string)] = true
	}
	return out
}

func podSpec(o map[string]any) map[string]any {
	return o["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
}

func TestFieldOwnership_ForgeApplyRemovesFieldsSetOutsideIt(t *testing.T) {
	e := startOwnershipEnvtest(t)

	// History: a legacy applier wrote v1; forge took over with v2, so scratch
	// and the service account stayed, owned by the legacy applier alone; then
	// that manager's entry was lost, leaving them owned by nobody.
	e.kubectl(ownershipV1, "apply", "--server-side", "--field-manager=legacy-applier", "-f", "-")
	e.forgeApply(ownershipV2)
	e.dropManager("deployment", "admin-server", "legacy-applier")

	// Hand edits, each as the manager the real command writes as.
	e.kubectl("", "patch", "deployment", "admin-server", "-n", ownershipNS, "--field-manager=kubectl", "--type=strategic", "-p",
		`{"spec":{"template":{"spec":{"containers":[{"name":"admin-server","env":[{"name":"PPROF_ENABLED","value":"false"}]}]}}}}`)
	e.kubectl("", "set", "env", "deployment/admin-server", "-n", ownershipNS, "DEBUG=1")
	e.kubectl("", "label", "deployment", "admin-server", "-n", ownershipNS, "hand-label=yes")
	// What the cluster itself writes, which must survive: an operator's
	// annotation and a scale through the scale subresource.
	e.kubectl("", "patch", "deployment", "admin-server", "-n", ownershipNS, "--field-manager=some-operator", "--type=merge", "-p",
		`{"metadata":{"annotations":{"operator.example/nudge":"1"}}}`)
	e.kubectl("", "scale", "deployment", "admin-server", "-n", ownershipNS, "--replicas=3")
	svcIP := e.get("service", "admin-server")["spec"].(map[string]any)["clusterIP"]

	before := e.get("deployment", "admin-server")
	if vols := podSpec(before)["volumes"].([]any); len(vols) != 2 {
		t.Fatalf("setup: want the ownerless scratch volume beside tmp, got %v", vols)
	}
	if podSpec(before)["serviceAccountName"] != "zitadel" {
		t.Fatalf("setup: want the ownerless serviceAccountName, got %v", podSpec(before)["serviceAccountName"])
	}

	e.forgeApply(ownershipV2)

	after := e.get("deployment", "admin-server")
	ps := podSpec(after)
	if vols := ps["volumes"].([]any); len(vols) != 1 || vols[0].(map[string]any)["name"] != "tmp" {
		t.Errorf("the ownerless scratch volume survived the apply: %v", vols)
	}
	if sa, ok := ps["serviceAccountName"]; ok {
		t.Errorf("the ownerless serviceAccountName survived the apply: %v", sa)
	}
	if sa, ok := ps["serviceAccount"]; ok {
		t.Errorf("the deprecated serviceAccount alias survived the apply: %v", sa)
	}
	env := ps["containers"].([]any)[0].(map[string]any)["env"].([]any)
	if len(env) != 1 || env[0].(map[string]any)["name"] != "LOG_LEVEL" {
		t.Errorf("hand-set env vars survived the apply, want only LOG_LEVEL: %v", env)
	}
	md := after["metadata"].(map[string]any)
	if _, ok := md["labels"].(map[string]any)["hand-label"]; ok {
		t.Errorf("the kubectl-label label survived the apply: %v", md["labels"])
	}
	if md["annotations"].(map[string]any)["operator.example/nudge"] != "1" {
		t.Errorf("an operator's annotation was removed; only imperative clients' fields are forge's: %v", md["annotations"])
	}
	if r := after["spec"].(map[string]any)["replicas"]; r != float64(3) {
		t.Errorf("replicas = %v, want the scale subresource's 3 kept", r)
	}
	if ps["dnsPolicy"] != "ClusterFirst" || ps["containers"].([]any)[0].(map[string]any)["terminationMessagePath"] == nil {
		t.Errorf("server defaults must stay: dnsPolicy=%v", ps["dnsPolicy"])
	}
	if img := ps["containers"].([]any)[0].(map[string]any)["image"]; img != "registry.example/admin-server:v2" {
		t.Errorf("image = %v", img)
	}
	got := managers(after)
	for _, m := range []string{"kubectl-set/Update", "kubectl-label/Update", "kubectl/Update"} {
		if got[m] {
			t.Errorf("the %s entry is still there; forge should have taken it over: %v", m, got)
		}
	}
	for _, m := range []string{"kubectl/Apply", "some-operator/Update"} {
		if !got[m] {
			t.Errorf("want the %s entry kept: %v", m, got)
		}
	}
	if ip := e.get("service", "admin-server")["spec"].(map[string]any)["clusterIP"]; ip != svcIP {
		t.Errorf("the Service's allocated clusterIP changed: %v -> %v", svcIP, ip)
	}

	// Converged: the next apply writes nothing at all.
	rv := md["resourceVersion"]
	e.forgeApply(ownershipV2)
	if again := e.get("deployment", "admin-server")["metadata"].(map[string]any)["resourceVersion"]; again != rv {
		t.Errorf("a converged object was written again (resourceVersion %v -> %v)", rv, again)
	}
}

// clearManagedFields strips an object's whole ownership history: `[{}]` is the
// API server's spelling for "no managedFields at all".
func (e *ownershipEnv) clearManagedFields(kind, name string) {
	e.t.Helper()
	e.kubectl("", "patch", kind, name, "-n", ownershipNS, "--type=json", "-p",
		`[{"op":"replace","path":"/metadata/managedFields","value":[{}]}]`)
	if mf, ok := e.get(kind, name)["metadata"].(map[string]any)["managedFields"]; ok && len(mf.([]any)) != 0 {
		e.t.Fatalf("setup: %s/%s still has managedFields %v", kind, name, mf)
	}
}

// With every managedFields entry gone, each field is ownerless; forge's apply
// still removes what it no longer declares and keeps the server's defaults.
func TestFieldOwnership_ForgeApplyConvergesAnObjectWithNoManagedFields(t *testing.T) {
	e := startOwnershipEnvtest(t)
	e.kubectl(ownershipV1, "apply", "--server-side", "--field-manager=legacy-applier", "-f", "-")
	e.forgeApply(ownershipV2)
	e.clearManagedFields("deployment", "admin-server")
	svcIP := e.get("service", "admin-server")["spec"].(map[string]any)["clusterIP"]
	e.clearManagedFields("service", "admin-server")

	e.forgeApply(ownershipV2)

	ps := podSpec(e.get("deployment", "admin-server"))
	if vols := ps["volumes"].([]any); len(vols) != 1 || vols[0].(map[string]any)["name"] != "tmp" {
		t.Errorf("the ownerless scratch volume survived the apply: %v", vols)
	}
	if sa, ok := ps["serviceAccountName"]; ok {
		t.Errorf("the ownerless serviceAccountName survived the apply: %v", sa)
	}
	if ps["dnsPolicy"] != "ClusterFirst" || ps["containers"].([]any)[0].(map[string]any)["terminationMessagePath"] == nil {
		t.Errorf("server defaults must stay: dnsPolicy=%v", ps["dnsPolicy"])
	}
	if ip := e.get("service", "admin-server")["spec"].(map[string]any)["clusterIP"]; ip != svcIP {
		t.Errorf("the Service's allocated clusterIP changed: %v -> %v", svcIP, ip)
	}
}

const widgetCRD = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata: {name: widgets.example.com}
spec:
  group: example.com
  scope: Namespaced
  names: {kind: Widget, plural: widgets, singular: widget, listKind: WidgetList}
  versions:
  - name: v1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
            properties:
              size: {type: integer}
              extra: {type: string}
`

const widgetV1 = `apiVersion: example.com/v1
kind: Widget
metadata: {name: w, namespace: prod}
spec: {size: 1, extra: legacy}
`

const widgetV2 = `apiVersion: example.com/v1
kind: Widget
metadata: {name: w, namespace: prod}
spec: {size: 2}
`

// A custom resource's update must name a resourceVersion; the dry-run replace
// that judges its ownerless fields carries the live one, so an ownerless field
// on a custom resource is removed like one on a built-in kind.
func TestFieldOwnership_ForgeApplyRemovesOwnerlessFieldsOfACustomResource(t *testing.T) {
	e := startOwnershipEnvtest(t)
	e.kubectl(widgetCRD, "apply", "--server-side", "-f", "-")
	e.kubectl("", "wait", "--for=condition=Established", "--timeout=60s", "crd/widgets.example.com")
	e.kubectl(widgetV1, "apply", "--server-side", "--field-manager=legacy-applier", "-f", "-")
	e.forgeApply(widgetV2)
	e.dropManager("widget", "w", "legacy-applier")
	if spec := e.get("widget", "w")["spec"].(map[string]any); spec["extra"] != "legacy" {
		t.Fatalf("setup: want the ownerless extra, got %v", spec)
	}

	e.forgeApply(widgetV2)

	spec := e.get("widget", "w")["spec"].(map[string]any)
	if _, ok := spec["extra"]; ok {
		t.Errorf("the custom resource's ownerless field survived the apply: %v", spec)
	}
	if spec["size"] != float64(2) {
		t.Errorf("size = %v, want 2", spec["size"])
	}
}

// An object annotated forge.dev/field-ownership: shared keeps other writers'
// fields: forge converges only what it declares.
func TestFieldOwnership_SharedObjectKeepsOtherWritersFields(t *testing.T) {
	e := startOwnershipEnvtest(t)
	shared := strings.Replace(ownershipV2, "  labels: {app.kubernetes.io/name: admin-server}\n",
		"  labels: {app.kubernetes.io/name: admin-server}\n  annotations: {forge.dev/field-ownership: shared}\n", 1)
	e.forgeApply(shared)
	e.kubectl("", "set", "env", "deployment/admin-server", "-n", ownershipNS, "PPROF_ENABLED=false")
	e.forgeApply(shared)
	env := podSpec(e.get("deployment", "admin-server"))["containers"].([]any)[0].(map[string]any)["env"].([]any)
	if len(env) != 2 {
		t.Errorf("a shared object's hand-set env var was removed: %v", env)
	}
}

// A Secret declared with stringData is converged after one apply: a re-apply
// of the same declaration writes nothing (TestE2EDirectApplySecretsLandAndStayOutOfTheBundle
// on main since #1721: "a re-deploy rewrote an unchanged Secret").
func TestFieldOwnership_UnchangedStringDataSecretIsNotRewritten(t *testing.T) {
	e := startOwnershipEnvtest(t)
	const secret = `apiVersion: v1
kind: Secret
metadata:
  name: store-creds
  namespace: prod
type: Opaque
stringData:
  token: s3cret
`
	e.forgeApply(secret)
	before := e.get("secret", "store-creds")["metadata"].(map[string]any)["resourceVersion"]
	e.forgeApply(secret)
	after := e.get("secret", "store-creds")
	if rv := after["metadata"].(map[string]any)["resourceVersion"]; rv != before {
		raw, _ := json.MarshalIndent(after["metadata"].(map[string]any)["managedFields"], "", "  ")
		t.Errorf("a re-apply rewrote an unchanged Secret (resourceVersion %v -> %v); managedFields:\n%s", before, rv, raw)
	}
}
