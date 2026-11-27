package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func covAKube(objs ...runtime.Object) (*kubeClients, *fake.Clientset) {
	cs := fake.NewSimpleClientset(objs...)
	return &kubeClients{cs: cs}, cs
}

func TestKubeClientsGetSecret(t *testing.T) {
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns", Name: "s",
			Annotations:     map[string]string{"a": "b"},
			OwnerReferences: []metav1.OwnerReference{{Name: "o1"}, {Name: "o2"}},
		},
		Data: map[string][]byte{"k": []byte("v")},
	}
	k, cs := covAKube(sec)

	got, err := k.GetSecret(context.Background(), "ns", "s")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Data["k"]) != "v" || got.Annotations["a"] != "b" || got.OwnerRefs != 2 {
		t.Errorf("got %+v", got)
	}

	_, err = k.GetSecret(context.Background(), "ns", "missing")
	if !errors.Is(err, errKubeSecretNotFound) || !strings.Contains(err.Error(), "ns/missing") {
		t.Errorf("a missing secret must wrap errKubeSecretNotFound with its name: %v", err)
	}

	cs.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "s", errors.New("denied by policy"))
	})
	_, err = k.GetSecret(context.Background(), "ns", "s")
	if err == nil || errors.Is(err, errKubeSecretNotFound) || !strings.Contains(err.Error(), "read secret ns/s") {
		t.Errorf("a forbidden read is not 'not found': %v", err)
	}
}

func TestKubeClientsRemoveAnnotationsSendsMergePatch(t *testing.T) {
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "s", Annotations: map[string]string{lastAppliedAnnotation: "{}", "keep": "1"}}}
	k, cs := covAKube(sec)
	var patch k8stesting.PatchAction
	cs.PrependReactor("patch", "secrets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		patch = a.(k8stesting.PatchAction)
		return false, nil, nil
	})
	if err := k.RemoveAnnotations(context.Background(), "ns", "s", []string{lastAppliedAnnotation}); err != nil {
		t.Fatal(err)
	}
	if patch == nil || patch.GetName() != "s" || patch.GetPatchType() != "application/merge-patch+json" {
		t.Fatalf("patch = %+v", patch)
	}
	if want := `{"metadata":{"annotations":{"kubectl.kubernetes.io/last-applied-configuration":null}}}`; string(patch.GetPatch()) != want {
		t.Errorf("patch body = %s, want %s", patch.GetPatch(), want)
	}
	if strings.Contains(string(patch.GetPatch()), `"data"`) {
		t.Error("the patch must never touch data")
	}

	cs.PrependReactor("patch", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("conflict")
	})
	if err := k.RemoveAnnotations(context.Background(), "ns", "s", []string{"x"}); err == nil || !strings.Contains(err.Error(), "patch secret ns/s: conflict") {
		t.Errorf("err = %v", err)
	}
}

func TestKubeClientsMintToken(t *testing.T) {
	k, cs := covAKube(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "sa"}})
	var req *authenticationv1.TokenRequest
	reply := "tok"
	cs.PrependReactor("create", "serviceaccounts", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() != "token" {
			return false, nil, nil
		}
		req = a.(k8stesting.CreateAction).GetObject().(*authenticationv1.TokenRequest)
		return true, &authenticationv1.TokenRequest{Status: authenticationv1.TokenRequestStatus{Token: reply}}, nil
	})
	tok, err := k.MintToken(context.Background(), "ns", "sa", "openbao", 10*time.Minute)
	if err != nil || tok != "tok" {
		t.Fatalf("tok=%q err=%v", tok, err)
	}
	if req == nil || req.Spec.Audiences[0] != "openbao" || *req.Spec.ExpirationSeconds != 600 {
		t.Errorf("token request = %+v", req)
	}

	reply = ""
	if _, err := k.MintToken(context.Background(), "ns", "sa", "a", time.Minute); err == nil || !strings.Contains(err.Error(), "empty token for serviceaccount ns/sa") {
		t.Errorf("an empty token must be refused: %v", err)
	}

	cs.PrependReactor("create", "serviceaccounts", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("denied")
	})
	if _, err := k.MintToken(context.Background(), "ns", "sa", "a", time.Minute); err == nil || !strings.Contains(err.Error(), "request a token for serviceaccount ns/sa: denied") {
		t.Errorf("err = %v", err)
	}
}

func TestKubeClientsConfigMapValue(t *testing.T) {
	k, cs := covAKube(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "ca"},
		Data:       map[string]string{"ca.crt": "PEM", "empty": ""},
	})
	if v, err := k.ConfigMapValue(context.Background(), "ns", "ca", "ca.crt"); err != nil || v != "PEM" {
		t.Fatalf("v=%q err=%v", v, err)
	}
	for name, tc := range map[string]struct{ cm, key, want string }{
		"missing configmap": {"nope", "ca.crt", "configmap ns/nope"},
		"missing key":       {"ca", "other", `configmap ns/ca has no "other"`},
		"empty value":       {"ca", "empty", `configmap ns/ca has no "empty"`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := k.ConfigMapValue(context.Background(), "ns", tc.cm, tc.key)
			if !errors.Is(err, errKubeConfigMapNotFound) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want not-found containing %q", err, tc.want)
			}
		})
	}
	cs.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("boom")
	})
	if _, err := k.ConfigMapValue(context.Background(), "ns", "ca", "ca.crt"); err == nil || errors.Is(err, errKubeConfigMapNotFound) {
		t.Errorf("an API failure is not 'not found': %v", err)
	}
}

func TestNewKubeAccessRefusesAnEmptyContext(t *testing.T) {
	if _, err := newKubeAccess(""); err == nil || !strings.Contains(err.Error(), "no kube context") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewKubeAccessReportsAnUnknownContext(t *testing.T) {
	t.Setenv("KUBECONFIG", t.TempDir()+"/absent")
	_, err := newKubeAccess("ghost")
	if err == nil || !strings.Contains(err.Error(), `kube context "ghost"`) {
		t.Fatalf("err = %v", err)
	}
}

func covAPod(name string, phase corev1.PodPhase, ready bool, deleting bool, ports ...corev1.ContainerPort) *corev1.Pod {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: name, Labels: map[string]string{"app": "bao"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Ports: ports}}},
		Status:     corev1.PodStatus{Phase: phase, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: status}}},
	}
	if deleting {
		now := metav1.Now()
		p.DeletionTimestamp = &now
	}
	return p
}

func covASvc(selector map[string]string, ports ...corev1.ServicePort) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "bao"},
		Spec:       corev1.ServiceSpec{Selector: selector, Ports: ports},
	}
}

func TestPickForwardTarget(t *testing.T) {
	sel := map[string]string{"app": "bao"}
	named := corev1.ServicePort{Port: 8200, TargetPort: intstr.FromString("api")}
	apiPort := corev1.ContainerPort{Name: "api", ContainerPort: 18200}
	tests := []struct {
		name       string
		objs       []runtime.Object
		port       int
		wantPod    string
		wantRemote int
		wantErr    string
	}{
		{
			name:    "numeric target port",
			objs:    []runtime.Object{covASvc(sel, corev1.ServicePort{Port: 8200, TargetPort: intstr.FromInt32(9200)}), covAPod("p0", corev1.PodRunning, true, false)},
			port:    8200,
			wantPod: "p0", wantRemote: 9200,
		},
		{
			name:    "no target port means the service port",
			objs:    []runtime.Object{covASvc(sel, corev1.ServicePort{Port: 8200}), covAPod("p0", corev1.PodRunning, true, false)},
			port:    8200,
			wantPod: "p0", wantRemote: 8200,
		},
		{
			name:    "named target port is resolved on the pod and unready, deleting and pending pods are skipped",
			objs:    []runtime.Object{covASvc(sel, named), covAPod("a-notready", corev1.PodRunning, false, false, apiPort), covAPod("b-deleting", corev1.PodRunning, true, true, apiPort), covAPod("c-pending", corev1.PodPending, true, false, apiPort), covAPod("d-good", corev1.PodRunning, true, false, apiPort)},
			port:    8200,
			wantPod: "d-good", wantRemote: 18200,
		},
		{
			name:    "a pod that does not expose the named port is not a target",
			objs:    []runtime.Object{covASvc(sel, named), covAPod("p0", corev1.PodRunning, true, false)},
			port:    8200,
			wantErr: "has no ready pod (1 selected)",
		},
		{
			name:    "service missing",
			port:    8200,
			wantErr: "service ns/bao: not found\nfix: the OpenBao component renders it",
		},
		{
			name:    "service with no selector",
			objs:    []runtime.Object{covASvc(nil, corev1.ServicePort{Port: 8200})},
			port:    8200,
			wantErr: "selects no pods",
		},
		{
			name:    "service lacks the port",
			objs:    []runtime.Object{covASvc(sel, corev1.ServicePort{Port: 8200})},
			port:    1234,
			wantErr: "has no port 1234",
		},
		{
			name:    "no pods at all",
			objs:    []runtime.Object{covASvc(sel, corev1.ServicePort{Port: 8200})},
			port:    8200,
			wantErr: "has no ready pod (0 selected)",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cs := fake.NewSimpleClientset(tc.objs...)
			pod, remote, err := pickForwardTarget(context.Background(), cs, "ns", "bao", tc.port)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || pod != tc.wantPod || remote != tc.wantRemote {
				t.Errorf("got (%q,%d,%v), want (%q,%d)", pod, remote, err, tc.wantPod, tc.wantRemote)
			}
		})
	}

	t.Run("API failures name the call that failed", func(t *testing.T) {
		cs := fake.NewSimpleClientset(covASvc(map[string]string{"app": "bao"}, corev1.ServicePort{Port: 8200}))
		cs.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("denied by policy")
		})
		if _, _, err := pickForwardTarget(context.Background(), cs, "ns", "bao", 8200); err == nil || !strings.Contains(err.Error(), "list the pods of service ns/bao: denied by policy") {
			t.Errorf("err = %v", err)
		}
		cs = fake.NewSimpleClientset()
		cs.PrependReactor("get", "services", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("denied by policy")
		})
		if _, _, err := pickForwardTarget(context.Background(), cs, "ns", "bao", 8200); err == nil || !strings.Contains(err.Error(), "read service ns/bao: denied by policy") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestForwardServiceFailsBeforeDialingWhenNoPodIsReady(t *testing.T) {
	cs := fake.NewSimpleClientset(covASvc(map[string]string{"app": "bao"}, corev1.ServicePort{Port: 8200}))
	k := &kubeClients{cs: cs}
	if _, err := k.ForwardService(context.Background(), "ns", "bao", 8200); err == nil || !strings.Contains(err.Error(), "has no ready pod") {
		t.Fatalf("err = %v", err)
	}
}

func TestPodReady(t *testing.T) {
	if podReady(&corev1.Pod{}) {
		t.Error("a pod with no Ready condition is not ready")
	}
	if !podReady(covAPod("p", corev1.PodRunning, true, false)) || podReady(covAPod("p", corev1.PodRunning, false, false)) {
		t.Error("podReady must follow the Ready condition's status")
	}
}

func TestKubePodForwardCloseCancelsAndWaits(t *testing.T) {
	done := make(chan error, 1)
	cancelled := make(chan struct{})
	f := &kubePodForward{addr: "127.0.0.1:1", cancel: func() { close(cancelled); done <- nil }, done: done}
	if f.Addr() != "127.0.0.1:1" {
		t.Errorf("addr = %q", f.Addr())
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	default:
		t.Error("Close must cancel the tunnel's context")
	}
}
