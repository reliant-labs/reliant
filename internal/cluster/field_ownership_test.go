package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
	"sigs.k8s.io/structured-merge-diff/v6/value"
)

// The ownership claim that makes forge's apply converge an object to its
// declaration (field_ownership.go). These pin the decision on the shapes prod's
// control-plane carried on 2026-11-04; field_ownership_envtest_test.go proves
// the same against a real API server.

// adminServerManagedFields is prod admin-server's ownership, reduced: forge's
// apply (kubectl/Apply), the PPROF_ENABLED a `kubectl` Update added, the
// revision annotation kube-controller-manager writes, an operator's
// annotation, a rollout restart, and the status subresource.
const adminServerManagedFields = `[
 {"manager":"kubectl","operation":"Apply","apiVersion":"apps/v1","fieldsType":"FieldsV1","fieldsV1":
  {"f:spec":{"f:template":{"f:spec":{
    "f:containers":{"k:{\"name\":\"admin-server\"}":{".":{},"f:name":{},"f:image":{},"f:env":{".":{},"k:{\"name\":\"LOG_LEVEL\"}":{".":{},"f:name":{},"f:value":{}}}}},
    "f:volumes":{"k:{\"name\":\"tmp\"}":{".":{},"f:name":{},"f:emptyDir":{}}}}}}}},
 {"manager":"kubectl","operation":"Update","apiVersion":"apps/v1","fieldsType":"FieldsV1","fieldsV1":
  {"f:spec":{"f:template":{"f:spec":{"f:containers":{"k:{\"name\":\"admin-server\"}":{"f:env":{"k:{\"name\":\"PPROF_ENABLED\"}":{".":{},"f:name":{},"f:value":{}}}}}}}}}},
 {"manager":"kube-controller-manager","operation":"Update","apiVersion":"apps/v1","fieldsType":"FieldsV1","fieldsV1":
  {"f:metadata":{"f:annotations":{".":{},"f:deployment.kubernetes.io/revision":{}}}}},
 {"manager":"some-operator","operation":"Update","apiVersion":"apps/v1","fieldsType":"FieldsV1","fieldsV1":
  {"f:metadata":{"f:annotations":{"f:operator.example/nudge":{}}}}},
 {"manager":"kube-controller-manager","operation":"Update","apiVersion":"apps/v1","subresource":"status","fieldsType":"FieldsV1","fieldsV1":
  {"f:status":{"f:replicas":{}}}}
]`

// adminServerFields is every field the object holds, as a dry-run apply of it
// reports: the owned ones, the ownerless scratch volume, and two ownerless
// server defaults.
const adminServerFields = `{"f:metadata":{"f:annotations":{".":{},"f:deployment.kubernetes.io/revision":{},"f:operator.example/nudge":{}}},
 "f:spec":{"f:revisionHistoryLimit":{},"f:template":{"f:spec":{"f:dnsPolicy":{},
  "f:containers":{"k:{\"name\":\"admin-server\"}":{".":{},"f:name":{},"f:image":{},"f:terminationMessagePath":{},
    "f:env":{".":{},"k:{\"name\":\"LOG_LEVEL\"}":{".":{},"f:name":{},"f:value":{}},"k:{\"name\":\"PPROF_ENABLED\"}":{".":{},"f:name":{},"f:value":{}}}}},
  "f:volumes":{"k:{\"name\":\"tmp\"}":{".":{},"f:name":{},"f:emptyDir":{}},"k:{\"name\":\"scratch\"}":{".":{},"f:name":{},"f:emptyDir":{}}}}}}}`

func liveFixture(t *testing.T, managedFields string, spec map[string]any) liveObject {
	t.Helper()
	var mf []any
	if err := json.Unmarshal([]byte(managedFields), &mf); err != nil {
		t.Fatal(err)
	}
	raw := map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "admin-server", "namespace": "control-plane-prod", "resourceVersion": "41", "managedFields": mf},
		"spec":     spec,
	}
	o, err := decodeLiveObject(jsonCopy(raw))
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func setOf(t *testing.T, fieldsV1 string) *fieldpath.Set {
	t.Helper()
	s := &fieldpath.Set{}
	if err := s.FromJSON(strings.NewReader(fieldsV1)); err != nil {
		t.Fatal(err)
	}
	return s
}

func podTemplate(volumes ...string) map[string]any {
	var vols []any
	for _, v := range volumes {
		vols = append(vols, map[string]any{"name": v, "emptyDir": map[string]any{}})
	}
	return map[string]any{"revisionHistoryLimit": 10, "template": map[string]any{"spec": map[string]any{
		"dnsPolicy": "ClusterFirst",
		"containers": []any{map[string]any{"name": "admin-server", "image": "x", "terminationMessagePath": "/dev/termination-log",
			"env": []any{map[string]any{"name": "LOG_LEVEL", "value": "info"}}}},
		"volumes": vols,
	}}}
}

func managerNames(entries []metav1.ManagedFieldsEntry) []string {
	var out []string
	for _, e := range entries {
		n := e.Manager + "/" + string(e.Operation)
		if e.Subresource != "" {
			n += "/" + e.Subresource
		}
		out = append(out, n)
	}
	return out
}

func entrySet(t *testing.T, c *ownershipClaim, manager string, op metav1.ManagedFieldsOperationType) *fieldpath.Set {
	t.Helper()
	for _, e := range c.entries {
		if e.Manager == manager && e.Operation == op && e.Subresource == "" {
			s, err := fieldSet(e)
			if err != nil {
				t.Fatal(err)
			}
			return s
		}
	}
	t.Fatalf("no %s/%s entry in %v", manager, op, managerNames(c.entries))
	return nil
}

func mustPath(t *testing.T, parts ...any) fieldpath.Path {
	t.Helper()
	p, err := fieldpath.MakePath(parts...)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func containerPath(t *testing.T, rest ...any) fieldpath.Path {
	return mustPath(t, append([]any{"spec", "template", "spec", "containers", keyBy("name", "admin-server")}, rest...)...)
}

func TestPlanClaim_TakesBackHandEditsAndOwnerlessFields(t *testing.T) {
	live := liveFixture(t, adminServerManagedFields, podTemplate("tmp", "scratch"))
	// The server's answer for the object with its ownerless fields removed:
	// the defaults came back, the scratch volume did not.
	trimmed := map[string]any{"spec": podTemplate("tmp")}

	c, err := planClaim(live, setOf(t, adminServerFields), trimmed, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if c == nil {
		t.Fatal("no claim: the PPROF_ENABLED a kubectl Update added and the ownerless scratch volume would survive the apply")
	}
	forge := entrySet(t, c, "kubectl", metav1.ManagedFieldsOperationApply)
	runOps := containerPath(t, "env", keyBy("name", "PPROF_ENABLED"))
	scratch := mustPath(t, "spec", "template", "spec", "volumes", keyBy("name", "scratch"))
	for _, p := range []fieldpath.Path{runOps, scratch, append(scratch.Copy(), fieldpath.PathElement{FieldName: strPtr("emptyDir")})} {
		if !forge.Has(p) {
			t.Errorf("forge's entry does not own %s, so the apply cannot remove it", p)
		}
	}
	for _, p := range []fieldpath.Path{
		mustPath(t, "spec", "template", "spec", "dnsPolicy"),
		mustPath(t, "spec", "revisionHistoryLimit"),
		containerPath(t, "terminationMessagePath"),
		mustPath(t, "metadata", "annotations", "operator.example/nudge"),
		mustPath(t, "metadata", "annotations", "deployment.kubernetes.io/revision"),
	} {
		if forge.Has(p) {
			t.Errorf("forge claimed %s, which the server or another controller owns: the apply would remove it", p)
		}
	}
	if got, want := strings.Join(managerNames(c.entries), ","),
		"kubectl/Apply,kube-controller-manager/Update,some-operator/Update,kube-controller-manager/Update/status"; got != want {
		t.Errorf("entries = %s, want %s (the kubectl Update merged into forge's, the rest untouched)", got, want)
	}
	if strings.Join(c.takenFrom, ",") != "kubectl" || c.ownerless != 3 {
		t.Errorf("takenFrom=%v ownerless=%d, want [kubectl] and the scratch volume's 3 fields", c.takenFrom, c.ownerless)
	}
	var reported []string
	for _, p := range c.reportable {
		reported = append(reported, p.String())
	}
	if got := strings.Join(reported, " "); got != runOps.String()+" "+scratch.String() {
		t.Errorf("reported %s, want the env var and the volume by name", got)
	}
}

func TestPlanClaim_ConvergedObjectClaimsNothing(t *testing.T) {
	managed := `[
 {"manager":"kubectl","operation":"Apply","apiVersion":"apps/v1","fieldsType":"FieldsV1","fieldsV1":
  {"f:spec":{"f:template":{"f:spec":{"f:volumes":{"k:{\"name\":\"tmp\"}":{".":{},"f:name":{},"f:emptyDir":{}}}}}}}},
 {"manager":"kube-controller-manager","operation":"Update","apiVersion":"apps/v1","fieldsType":"FieldsV1","fieldsV1":
  {"f:metadata":{"f:annotations":{".":{},"f:deployment.kubernetes.io/revision":{}}}}}
]`
	probe := `{"f:metadata":{"f:annotations":{".":{},"f:deployment.kubernetes.io/revision":{}}},
 "f:spec":{"f:template":{"f:spec":{"f:dnsPolicy":{},"f:volumes":{"k:{\"name\":\"tmp\"}":{".":{},"f:name":{},"f:emptyDir":{}}}}}}}`
	live := liveFixture(t, managed, podTemplate("tmp"))
	c, err := planClaim(live, setOf(t, probe), map[string]any{"spec": podTemplate("tmp")}, time.Unix(0, 0))
	if err != nil || c != nil {
		t.Fatalf("a converged object must not be written: claim=%+v err=%v", c, err)
	}
}

// The server derives serviceAccount from serviceAccountName. While another
// manager owns serviceAccountName the ownerless alias comes back on every
// write, so claiming it would rewrite managedFields on every deploy and change
// nothing.
func TestPlanClaim_LeavesWhatTheServerPutsBack(t *testing.T) {
	managed := `[
 {"manager":"kubectl","operation":"Apply","apiVersion":"apps/v1","fieldsType":"FieldsV1","fieldsV1":{"f:spec":{"f:replicas":{}}}},
 {"manager":"legacy-applier","operation":"Apply","apiVersion":"apps/v1","fieldsType":"FieldsV1","fieldsV1":
  {"f:spec":{"f:template":{"f:spec":{"f:serviceAccountName":{}}}}}}
]`
	probe := `{"f:spec":{"f:replicas":{},"f:template":{"f:spec":{"f:serviceAccountName":{},"f:serviceAccount":{}}}}}`
	tmpl := map[string]any{"replicas": 1, "template": map[string]any{"spec": map[string]any{"serviceAccountName": "zitadel", "serviceAccount": "zitadel"}}}
	live := liveFixture(t, managed, tmpl)
	c, err := planClaim(live, setOf(t, probe), map[string]any{"spec": tmpl}, time.Unix(0, 0))
	if err != nil || c != nil {
		t.Fatalf("claimed a field the server re-derives: %+v err=%v", c, err)
	}
}

// Without the probe or the server's answer, ownerless fields cannot be told
// from defaults, so only the hand edits are taken back.
func TestPlanClaim_UnreadableObjectStillTakesBackHandEdits(t *testing.T) {
	live := liveFixture(t, adminServerManagedFields, podTemplate("tmp", "scratch"))
	for name, tc := range map[string]struct {
		probe   *fieldpath.Set
		trimmed map[string]any
	}{
		"no probe":   {nil, map[string]any{"spec": podTemplate("tmp")}},
		"no answer":  {setOf(t, adminServerFields), nil},
		"no neither": {nil, nil},
	} {
		c, err := planClaim(live, tc.probe, tc.trimmed, time.Unix(0, 0))
		if err != nil || c == nil {
			t.Fatalf("%s: claim=%v err=%v", name, c, err)
		}
		if c.ownerless != 0 {
			t.Errorf("%s: claimed %d ownerless field(s) it could not judge", name, c.ownerless)
		}
		if !entrySet(t, c, "kubectl", metav1.ManagedFieldsOperationApply).Has(containerPath(t, "env", keyBy("name", "PPROF_ENABLED"))) {
			t.Errorf("%s: the hand edit was not taken back", name)
		}
	}
}

// A set recorded at another API version names that version's fields. It is
// neither merged into forge's nor used to decide what nobody owns.
func TestPlanClaim_MixedVersionsAreLeftAlone(t *testing.T) {
	managed := strings.Replace(adminServerManagedFields, `"manager":"kubectl","operation":"Update","apiVersion":"apps/v1"`,
		`"manager":"kubectl","operation":"Update","apiVersion":"apps/v1beta2"`, 1)
	live := liveFixture(t, managed, podTemplate("tmp", "scratch"))
	c, err := planClaim(live, setOf(t, adminServerFields), map[string]any{"spec": podTemplate("tmp")}, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if c != nil {
		t.Fatalf("claimed across API versions: %v", managerNames(c.entries))
	}
}

// Every entry, forge's included, recorded at an older version than the one
// the object (and so the probe) was read at: the sets agree with one another
// but name the old version's fields, so they cannot say what nobody owns at
// the new one. The hand edit at forge's version is still taken back.
func TestPlanClaim_EntriesAtAnotherVersionThanTheProbe(t *testing.T) {
	managed := strings.ReplaceAll(adminServerManagedFields, `"apiVersion":"apps/v1"`, `"apiVersion":"apps/v1beta2"`)
	live := liveFixture(t, managed, podTemplate("tmp", "scratch"))
	if live.version != "apps/v1" {
		t.Fatalf("fixture read at %q", live.version)
	}
	c, err := planClaim(live, setOf(t, adminServerFields), map[string]any{"spec": podTemplate("tmp")}, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if c != nil && c.ownerless != 0 {
		t.Errorf("claimed %d ownerless field(s) judged against a probe at another version", c.ownerless)
	}
}

func TestTakenOverManager(t *testing.T) {
	for _, tc := range []struct {
		manager, op, sub string
		want             bool
	}{
		{"kubectl", "Update", "", true},
		{"kubectl-edit", "Update", "", true},
		{"kubectl-patch", "Update", "", true},
		{"kubectl-set", "Update", "", true},
		{"kubectl-label", "Update", "", true},
		{"kubectl-client-side-apply", "Update", "", true},
		{"kubectl-rollout", "Update", "", true},
		{"kubectl-last-applied", "Apply", "", true},
		{"before-first-apply", "Update", "", true},
		{"kubectl", "Apply", "", false}, // forge itself
		{"kubectl-scale", "Update", "scale", false},
		{"kube-controller-manager", "Update", "", false},
		{"kustomize-controller", "Apply", "", false},
		{"helm", "Update", "", false},
		{"control-plane", "Update", "", false},
	} {
		e := metav1.ManagedFieldsEntry{Manager: tc.manager, Operation: metav1.ManagedFieldsOperationType(tc.op), Subresource: tc.sub}
		if got := takenOverManager(e); got != tc.want {
			t.Errorf("takenOverManager(%s/%s/%s) = %v, want %v", tc.manager, tc.op, tc.sub, got, tc.want)
		}
	}
}

func TestPathPresentAndDeletePath(t *testing.T) {
	obj := jsonCopy(map[string]any{"spec": map[string]any{
		"ports":   []any{map[string]any{"port": 80, "protocol": "TCP"}, map[string]any{"port": 443, "protocol": "TCP"}},
		"ips":     []any{"10.0.0.1", "10.0.0.2"},
		"args":    []any{"a", "b"},
		"volumes": []any{map[string]any{"name": "tmp"}, map[string]any{"name": "scratch"}},
	}})
	port443 := mustPath(t, "spec", "ports", keyBy("port", 443, "protocol", "TCP"))
	ip2 := fieldpath.Path{{FieldName: strPtr("spec")}, {FieldName: strPtr("ips")}, fieldpath.PathElement{Value: valuePtr(t, `"10.0.0.2"`)}}
	arg1 := mustPath(t, "spec", "args", 1)
	scratch := mustPath(t, "spec", "volumes", keyBy("name", "scratch"))
	for _, p := range []fieldpath.Path{port443, ip2, arg1, scratch} {
		if !pathPresent(obj, p) {
			t.Errorf("%s not found", p)
		}
	}
	if pathPresent(obj, mustPath(t, "spec", "ports", keyBy("port", 8080, "protocol", "TCP"))) {
		t.Error("found an absent port")
	}
	var o any = obj
	for _, p := range []fieldpath.Path{port443, ip2, scratch} {
		o = deletePath(o, p)
		if pathPresent(o, p) {
			t.Errorf("%s still present after delete", p)
		}
	}
	got, _ := json.Marshal(o)
	if want := `{"spec":{"args":["a","b"],"ips":["10.0.0.1"],"ports":[{"port":80,"protocol":"TCP"}],"volumes":[{"name":"tmp"}]}}`; string(got) != want {
		t.Errorf("after deletes: %s\nwant %s", got, want)
	}
}

func strPtr(s string) *string { return &s }

// keyBy is an associative-list key element from plain name/value pairs, in
// the managedFields notation (k:{"name":"scratch"}).
func keyBy(nameValues ...any) fieldpath.PathElement {
	m := map[string]any{}
	for i := 0; i+1 < len(nameValues); i += 2 {
		m[nameValues[i].(string)] = nameValues[i+1]
	}
	b, _ := json.Marshal(m)
	pe, err := fieldpath.DeserializePathElement("k:" + string(b))
	if err != nil {
		panic(err)
	}
	return pe
}

func valuePtr(t *testing.T, js string) *value.Value {
	t.Helper()
	pe, err := fieldpath.DeserializePathElement("v:" + js)
	if err != nil {
		t.Fatal(err)
	}
	return pe.Value
}

// fakeOwnershipKubectl answers convergeFieldOwnership's kubectl calls from
// canned objects and records every call.
type fakeOwnershipKubectl struct {
	live    []map[string]any // what `get -f -` returns
	probe   string           // the probe's fieldsV1 for every object
	trimmed map[string]any   // what `replace --dry-run` returns
	getErr  string           // stderr of a failing get
	lose    int              // how many patches lose a race before one lands
	// raced, when set, is the cluster after the writer that wins a race:
	// every later read answers from it.
	raced    *fakeOwnershipKubectl
	calls    []string
	patches  []string
	replaces []string // the stdin of every replace
}

func (f *fakeOwnershipKubectl) run(_ context.Context, args []string, stdin []byte) ([]byte, []byte, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	switch {
	case args[0] == "get":
		b, _ := json.Marshal(map[string]any{"kind": "List", "items": f.live})
		if f.getErr != "" {
			return b, []byte(f.getErr), errors.New("exit status 1")
		}
		return b, nil, nil
	case args[0] == "apply" && strings.Contains(strings.Join(args, " "), "--dry-run=server"):
		var list struct{ Items []map[string]any }
		_ = json.Unmarshal(stdin, &list)
		var out []any
		for _, it := range list.Items {
			it = jsonCopy(it)
			md := it["metadata"].(map[string]any)
			md["managedFields"] = []any{map[string]any{"manager": ownershipProbeManager, "operation": "Apply", "apiVersion": "apps/v1",
				"fieldsType": "FieldsV1", "fieldsV1": json.RawMessage(f.probe)}}
			out = append(out, it)
		}
		b, _ := json.Marshal(map[string]any{"kind": "List", "items": out})
		return b, nil, nil
	case args[0] == "replace":
		f.replaces = append(f.replaces, string(stdin))
		b, _ := json.Marshal(f.trimmed)
		return b, nil, nil
	case args[0] == "patch":
		f.patches = append(f.patches, args[len(args)-1])
		if msg := f.patchRefusal(args[len(args)-1], stdin); msg != "" {
			return nil, []byte(msg), errors.New("exit status 1")
		}
		if f.lose > 0 {
			f.lose--
			if f.raced != nil {
				f.live, f.probe, f.trimmed = f.raced.live, f.raced.probe, f.raced.trimmed
			}
			return nil, []byte("The request is invalid: the server rejected our request due to an error in our request"), errors.New("exit status 1")
		}
		return nil, nil, nil
	}
	return nil, nil, nil
}

// patchRefusal is the API server's JSON-patch semantics for the patched live
// object: a `replace` (or `test`) of a member the object lacks is refused, as
// the server refuses it, so a patch that only works on a fake fails here too.
func (f *fakeOwnershipKubectl) patchRefusal(patch string, ident []byte) string {
	var ops []map[string]any
	if err := json.Unmarshal([]byte(patch), &ops); err != nil {
		return "error: unable to parse the patch: " + err.Error()
	}
	var id struct{ Metadata struct{ Name string } }
	_ = json.Unmarshal(ident, &id)
	var obj map[string]any
	for _, o := range f.live {
		if md, _ := o["metadata"].(map[string]any); md != nil && md["name"] == id.Metadata.Name {
			obj = o
		}
	}
	if obj == nil {
		return fmt.Sprintf("Error from server (NotFound): %q not found", id.Metadata.Name)
	}
	for _, op := range ops {
		path, _ := op["path"].(string)
		if op["op"] != "replace" && op["op"] != "test" {
			continue
		}
		var node any = obj
		for _, k := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
			m, _ := node.(map[string]any)
			v, ok := m[k]
			if !ok {
				return fmt.Sprintf("The request is invalid: jsonpatch %s operation does not apply: doc is missing key: %s", op["op"], path)
			}
			node = v
		}
	}
	return ""
}

const adminServerDoc = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: admin-server
spec: {}`

func TestConvergeFieldOwnership_PatchesManagedFieldsBeforeTheApply(t *testing.T) {
	live := liveFixture(t, adminServerManagedFields, podTemplate("tmp", "scratch")).raw
	trimmed := jsonCopy(map[string]any{"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "admin-server", "namespace": "control-plane-prod"}, "spec": podTemplate("tmp")})
	f := &fakeOwnershipKubectl{live: []map[string]any{live}, probe: adminServerFields, trimmed: trimmed}
	var out bytes.Buffer
	if err := convergeFieldOwnership(context.Background(), f.run, "control-plane-prod", adminServerDoc, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.patches) != 1 {
		t.Fatalf("want one managedFields patch, calls: %v", f.calls)
	}
	var ops []map[string]any
	if err := json.Unmarshal([]byte(f.patches[0]), &ops); err != nil {
		t.Fatal(err)
	}
	if ops[0]["op"] != "test" || ops[0]["path"] != "/metadata/resourceVersion" || ops[0]["value"] != "41" {
		t.Errorf("the patch must be guarded by the resourceVersion it was planned on: %v", ops[0])
	}
	if ops[1]["op"] != "add" || ops[1]["path"] != "/metadata/managedFields" {
		t.Errorf("want a managedFields add (it may be absent): %v", ops[1])
	}
	mf, _ := json.Marshal(ops[1]["value"])
	var entries []metav1.ManagedFieldsEntry
	if err := json.Unmarshal(mf, &entries); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(managerNames(entries), ","); got != "kubectl/Apply,kube-controller-manager/Update,some-operator/Update,kube-controller-manager/Update/status" {
		t.Errorf("managedFields after the claim: %s", got)
	}
	if forgeFields := entries[0].FieldsV1.GetRawString(); !strings.Contains(forgeFields, "scratch") || !strings.Contains(forgeFields, "PPROF_ENABLED") {
		t.Errorf("forge's entry does not hold the claimed fields: %s", forgeFields)
	}
	for _, want := range []string{"Deployment/control-plane-prod/admin-server", "kubectl, no owner", `env[name="PPROF_ENABLED"]`, `volumes[name="scratch"]`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report lacks %q:\n%s", want, out.String())
		}
	}
	if !strings.HasPrefix(f.calls[0], "get -f - -o json --ignore-not-found --show-managed-fields -n control-plane-prod") {
		t.Errorf("first call = %q", f.calls[0])
	}
	// A custom resource's update must name its resourceVersion, and the
	// answer must describe the revision the claim is planned on.
	if len(f.replaces) != 1 || !strings.Contains(f.replaces[0], `"resourceVersion":"41"`) {
		t.Errorf("the dry-run replace must carry the live resourceVersion: %v", f.replaces)
	}
}

func patchedEntries(t *testing.T, patch string) (rv any, entries []metav1.ManagedFieldsEntry) {
	t.Helper()
	var ops []map[string]any
	if err := json.Unmarshal([]byte(patch), &ops); err != nil {
		t.Fatal(err)
	}
	mf, _ := json.Marshal(ops[1]["value"])
	if err := json.Unmarshal(mf, &entries); err != nil {
		t.Fatal(err)
	}
	return ops[0]["value"], entries
}

// An object whose managedFields were cleared has every field ownerless. The
// server's answer for the declaration tells the orphans (absent from it) from
// the defaults (put back), and forge claims the orphans.
func TestConvergeFieldOwnership_ObjectWithNoManagedFields(t *testing.T) {
	live := jsonCopy(map[string]any{"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "admin-server", "namespace": "control-plane-prod", "resourceVersion": "41"},
		"spec":     podTemplate("tmp", "scratch")})
	trimmed := jsonCopy(map[string]any{"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "admin-server", "namespace": "control-plane-prod"}, "spec": podTemplate("tmp")})
	f := &fakeOwnershipKubectl{live: []map[string]any{live}, probe: adminServerFields, trimmed: trimmed}
	var out bytes.Buffer
	if err := convergeFieldOwnership(context.Background(), f.run, "control-plane-prod", adminServerDoc, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.replaces) != 1 {
		t.Fatalf("want one dry-run replace, calls: %v", f.calls)
	}
	var list struct{ Items []map[string]any }
	if err := json.Unmarshal([]byte(f.replaces[0]), &list); err != nil || len(list.Items) != 1 {
		t.Fatalf("replace stdin: %s (%v)", f.replaces[0], err)
	}
	md := list.Items[0]["metadata"].(map[string]any)
	if spec, _ := json.Marshal(list.Items[0]["spec"]); string(spec) != "{}" || md["resourceVersion"] != "41" || md["namespace"] != "control-plane-prod" {
		t.Errorf("the replacement must be the declaration at the live revision and namespace: %s", f.replaces[0])
	}
	if len(f.patches) != 1 {
		t.Fatalf("the ownerless scratch volume of an object with no managedFields was left: calls %v", f.calls)
	}
	rv, entries := patchedEntries(t, f.patches[0])
	if rv != "41" || strings.Join(managerNames(entries), ",") != "kubectl/Apply" {
		t.Fatalf("rv=%v entries=%v, want forge's entry alone at 41", rv, managerNames(entries))
	}
	forge := entries[0].FieldsV1.GetRawString()
	if !strings.Contains(forge, "scratch") || !strings.Contains(forge, "PPROF_ENABLED") {
		t.Errorf("forge's entry lacks the orphans: %s", forge)
	}
	if strings.Contains(forge, "dnsPolicy") || strings.Contains(forge, "terminationMessagePath") || strings.Contains(forge, "revisionHistoryLimit") {
		t.Errorf("forge claimed a default the server puts back: %s", forge)
	}
	if !strings.Contains(out.String(), "no owner") {
		t.Errorf("report: %q", out.String())
	}
}

// After a lost race the claim is re-planned on the fresh revision with the
// server's answers for THAT revision, not the stale ones: here the winning
// writer dropped serviceAccountName and its owner, so the serviceAccount
// alias the server used to re-derive is now an orphan forge must claim.
func TestConvergeFieldOwnership_RetryRejudgesTheFreshRevision(t *testing.T) {
	saOwner := `,{"manager":"legacy-applier","operation":"Apply","apiVersion":"apps/v1","fieldsType":"FieldsV1","fieldsV1":
  {"f:spec":{"f:template":{"f:spec":{"f:serviceAccountName":{}}}}}}`
	managed := func(withSAOwner bool) string {
		m := `[{"manager":"kubectl","operation":"Apply","apiVersion":"apps/v1","fieldsType":"FieldsV1","fieldsV1":{"f:spec":{"f:replicas":{}}}},
 {"manager":"kubectl-label","operation":"Update","apiVersion":"apps/v1","fieldsType":"FieldsV1","fieldsV1":{"f:metadata":{"f:labels":{"f:hand":{}}}}}`
		if withSAOwner {
			m += saOwner
		}
		return m + "]"
	}
	obj := func(rv string, spec map[string]any) map[string]any {
		return jsonCopy(map[string]any{"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]any{"name": "admin-server", "namespace": "control-plane-prod", "resourceVersion": rv}, "spec": spec})
	}
	both := map[string]any{"replicas": 1, "template": map[string]any{"spec": map[string]any{"serviceAccountName": "zitadel", "serviceAccount": "zitadel"}}}
	aliasOnly := map[string]any{"replicas": 1, "template": map[string]any{"spec": map[string]any{"serviceAccount": "zitadel"}}}
	noSA := map[string]any{"replicas": 1, "template": map[string]any{"spec": map[string]any{}}}

	before := liveFixture(t, managed(true), both).raw
	after := liveFixture(t, managed(false), aliasOnly).raw
	after["metadata"].(map[string]any)["resourceVersion"] = "42"
	f := &fakeOwnershipKubectl{
		live:    []map[string]any{before},
		probe:   `{"f:metadata":{"f:labels":{"f:hand":{}}},"f:spec":{"f:replicas":{},"f:template":{"f:spec":{"f:serviceAccountName":{},"f:serviceAccount":{}}}}}`,
		trimmed: obj("41", both),
		lose:    1,
		raced: &fakeOwnershipKubectl{
			live:    []map[string]any{after},
			probe:   `{"f:metadata":{"f:labels":{"f:hand":{}}},"f:spec":{"f:replicas":{},"f:template":{"f:spec":{"f:serviceAccount":{}}}}}`,
			trimmed: obj("42", noSA),
		},
	}
	if err := convergeFieldOwnership(context.Background(), f.run, "control-plane-prod", adminServerDoc, nil); err != nil {
		t.Fatal(err)
	}
	if len(f.patches) != 2 {
		t.Fatalf("want the lost patch and its retry, calls: %v", f.calls)
	}
	_, first := patchedEntries(t, f.patches[0])
	if fields := first[0].FieldsV1.GetRawString(); strings.Contains(fields, "serviceAccount") {
		t.Errorf("the first claim took the alias the server re-derived then: %s", fields)
	}
	rv, retry := patchedEntries(t, f.patches[1])
	if rv != "42" {
		t.Errorf("the retry must be guarded by the fresh revision, got %v", rv)
	}
	fields := retry[0].FieldsV1.GetRawString()
	if !strings.Contains(fields, `"f:serviceAccount"`) || !strings.Contains(fields, `"f:hand"`) {
		t.Errorf("the retry did not re-judge the fresh revision: forge's entry %s", fields)
	}
	if len(f.replaces) != 2 || !strings.Contains(f.replaces[1], `"resourceVersion":"42"`) {
		t.Errorf("the retry must ask the server about the fresh revision: %v", f.replaces)
	}
}

// A write between the read and the patch fails the patch's resourceVersion
// test; the object is re-read and the claim re-planned, a bounded number of
// times.
func TestConvergeFieldOwnership_RetriesALostRace(t *testing.T) {
	live := liveFixture(t, adminServerManagedFields, podTemplate("tmp", "scratch")).raw
	trimmed := jsonCopy(map[string]any{"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "admin-server", "namespace": "control-plane-prod"}, "spec": podTemplate("tmp")})
	f := &fakeOwnershipKubectl{live: []map[string]any{live}, probe: adminServerFields, trimmed: trimmed, lose: 1}
	if err := convergeFieldOwnership(context.Background(), f.run, "control-plane-prod", adminServerDoc, nil); err != nil {
		t.Fatalf("a lost race must be retried: %v", err)
	}
	if len(f.patches) != 2 {
		t.Errorf("want the patch retried once, calls: %v", f.calls)
	}
	f = &fakeOwnershipKubectl{live: []map[string]any{live}, probe: adminServerFields, trimmed: trimmed, lose: ownershipClaimAttempts}
	if err := convergeFieldOwnership(context.Background(), f.run, "control-plane-prod", adminServerDoc, nil); err == nil {
		t.Error("a race lost on every attempt must fail the converge")
	}
	if len(f.patches) != ownershipClaimAttempts {
		t.Errorf("patched %d times, want %d", len(f.patches), ownershipClaimAttempts)
	}
}

func TestConvergeFieldOwnership_ConvergedStreamWritesNothing(t *testing.T) {
	managed := `[{"manager":"kubectl","operation":"Apply","apiVersion":"apps/v1","fieldsType":"FieldsV1","fieldsV1":
  {"f:spec":{"f:template":{"f:spec":{"f:volumes":{"k:{\"name\":\"tmp\"}":{".":{},"f:name":{},"f:emptyDir":{}}}}}}}}]`
	live := liveFixture(t, managed, podTemplate("tmp")).raw
	f := &fakeOwnershipKubectl{live: []map[string]any{live},
		probe:   `{"f:spec":{"f:template":{"f:spec":{"f:dnsPolicy":{},"f:volumes":{"k:{\"name\":\"tmp\"}":{".":{},"f:name":{},"f:emptyDir":{}}}}}}}`,
		trimmed: jsonCopy(map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "admin-server", "namespace": "control-plane-prod"}, "spec": podTemplate("tmp")})}
	var out bytes.Buffer
	if err := convergeFieldOwnership(context.Background(), f.run, "control-plane-prod", adminServerDoc, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.patches) != 0 || out.Len() != 0 {
		t.Errorf("a converged object was written: patches=%v report=%q", f.patches, out.String())
	}
}

func TestConvergeFieldOwnership_SharedObjectIsNotRead(t *testing.T) {
	doc := strings.Replace(adminServerDoc, "  name: admin-server\n", "  name: admin-server\n  annotations: {forge.dev/field-ownership: shared}\n", 1)
	f := &fakeOwnershipKubectl{}
	if err := convergeFieldOwnership(context.Background(), f.run, "ns", doc, nil); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 {
		t.Errorf("a shared object was read: %v", f.calls)
	}
}

// A kind the server does not serve yet (its CRD rides the same stream) has no
// live object to converge; any other read failure stops the deploy rather than
// applying without converging.
func TestConvergeFieldOwnership_GetErrors(t *testing.T) {
	f := &fakeOwnershipKubectl{getErr: `error: resource mapping not found for name: "w" namespace: "" from "STDIN": no matches for kind "Widget" in version "example.com/v1"`}
	if err := convergeFieldOwnership(context.Background(), f.run, "ns", adminServerDoc, nil); err != nil {
		t.Errorf("an unserved kind must not fail the converge: %v", err)
	}
	f = &fakeOwnershipKubectl{getErr: "Unable to connect to the server: dial tcp: i/o timeout"}
	if err := convergeFieldOwnership(context.Background(), f.run, "ns", adminServerDoc, nil); err == nil {
		t.Error("a failed read must fail the converge")
	}
}

// The apply path converges before it applies: the live objects are read first.
func TestKubectlApply_ConvergesFieldOwnershipFirst(t *testing.T) {
	if testing.Short() {
		t.Skip("execs a fake kubectl script; runs in task test")
	}
	calls := fakeKubectlRecorder(t)
	if err := KubectlApplyNamespaced(context.Background(), "ctx-a", "prod", adminServerDoc); err != nil {
		t.Fatal(err)
	}
	got := calls()
	if len(got) < 2 || !strings.Contains(got[0].Args, "get -f - -o json --ignore-not-found --show-managed-fields") ||
		!strings.Contains(got[1].Args, "apply --server-side") {
		var args []string
		for _, c := range got {
			args = append(args, c.Args)
		}
		t.Fatalf("want the ownership read before the apply, got:\n%s", strings.Join(args, "\n"))
	}
}
