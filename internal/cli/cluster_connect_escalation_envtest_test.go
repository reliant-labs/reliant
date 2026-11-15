//go:build !windows

package cli

// escalate and bind pinned by resourceNames, enforced by a REAL kube-apiserver.
// The unit tests prove the rendered YAML carries the pin; this proves the pin
// does what it says: the binding's identity can apply exactly the Role (and
// RoleBinding to it) its render declares, via server-side apply as Flux does,
// and is Forbidden for any other, including a Role that grants more than it holds.
//
// Needs the envtest binaries (KUBEBUILDER_ASSETS): skipped without them and
// under -short.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

func TestGrant_EscalateAndBindArePinnedByNameOnARealAPIServer(t *testing.T) {
	cfg, admin, env := startIssuerEnvtest(t)
	_ = cfg
	ctx := context.Background()

	const ns = "acme-prod"
	if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}

	// The render declares one Role and one RoleBinding to it, in acme-prod.
	role := "apiVersion: rbac.authorization.k8s.io/v1\nkind: Role\nmetadata:\n  name: acme-reader\n  namespace: " + ns + "\nrules:\n- apiGroups: ['']\n  resources: [secrets]\n  verbs: [get]\n"
	binding := "apiVersion: rbac.authorization.k8s.io/v1\nkind: RoleBinding\nmetadata:\n  name: acme-reader\n  namespace: " + ns + "\n" +
		"roleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: Role\n  name: acme-reader\nsubjects:\n- kind: ServiceAccount\n  name: app\n  namespace: " + ns + "\n"
	for _, scope := range []applyScope{{Namespaces: []string{ns}}, {Cluster: true}} {
		t.Run(scope.String(), func(t *testing.T) {
			r := testRBAC(t, scope, onCtx(role), onCtx(binding))
			// The grant, applied as the owner (cluster-admin) would apply it.
			if err := applyManifestsAsAdmin(admin)(ctx, testCtx, r.bootstrapManifests()); err != nil {
				t.Fatal(err)
			}

			user := r.connectSubject().username()
			u, err := env.AddUser(envtest.User{Name: user}, nil)
			if err != nil {
				t.Fatal(err)
			}
			as, err := client.New(u.Config(), client.Options{})
			if err != nil {
				t.Fatal(err)
			}

			apply := func(doc string) error {
				obj := &unstructured.Unstructured{}
				if err := yaml.Unmarshal([]byte(doc), &obj.Object); err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(obj.Object)
				return as.Patch(ctx, obj, client.RawPatch(types.ApplyPatchType, raw), client.ForceOwnership, client.FieldOwner("kustomize-controller"))
			}
			named := func(doc, name string) string { return strings.ReplaceAll(doc, "acme-reader", name) }

			// The declared Role grants secrets/get, which the binding does NOT
			// hold: creating it needs `escalate`, and it is allowed by name.
			if err := apply(role); err != nil {
				t.Fatalf("the Role the render declares was refused: %v", err)
			}
			// The same shape under another name is not allowed to escalate.
			if err := apply(named(role, "other")); !apierrors.IsForbidden(err) {
				t.Errorf("a Role the render does not declare was not Forbidden: %v", err)
			}
			// Binding the declared Role is allowed by name ...
			if err := apply(binding); err != nil {
				t.Fatalf("the RoleBinding the render declares was refused: %v", err)
			}
			// ... and binding another Role is not.
			if err := admin.Create(ctx, mustUnstructured(t, named(role, "admin-made"))); err != nil && !apierrors.IsAlreadyExists(err) {
				t.Fatal(err)
			}
			other := strings.ReplaceAll(binding, "kind: Role\n  name: acme-reader", "kind: Role\n  name: admin-made")
			other = strings.Replace(other, "metadata:\n  name: acme-reader", "metadata:\n  name: other", 1)
			if err := apply(other); !apierrors.IsForbidden(err) {
				t.Errorf("a RoleBinding to a Role the render does not name was not Forbidden: %v", err)
			}
			// No escalate/bind on clusterroles when the render has none.
			cr := "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: grab\nrules:\n- apiGroups: ['']\n  resources: [secrets]\n  verbs: ['*']\n"
			if err := apply(cr); !apierrors.IsForbidden(err) {
				t.Errorf("a ClusterRole was not Forbidden: %v", err)
			}
			// Cleanup between scopes: the subject changes nothing, the objects are re-applied.
			_ = admin.Delete(ctx, mustUnstructured(t, binding))
			_ = admin.Delete(ctx, mustUnstructured(t, role))
		})
	}
}

func mustUnstructured(t *testing.T, doc string) *unstructured.Unstructured {
	t.Helper()
	obj := &unstructured.Unstructured{}
	if err := yaml.Unmarshal([]byte(doc), &obj.Object); err != nil {
		t.Fatal(err)
	}
	return obj
}
