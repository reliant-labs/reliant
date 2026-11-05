package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	streamhttp "k8s.io/streaming/pkg/httpstream"
)

// The operator's kube context is the one credential the store-source commands
// lean on: `secret import` and `env secrets verify|adopt` read and patch
// Secrets with it, and `secret set` against a self-run OpenBao mints its
// ten-minute writer token with it, reads the CA the component publishes, and
// port-forwards to the store with it. This file is the only place they touch the
// cluster, behind a seam so no unit test dials one.

// errKubeSecretNotFound is the one failure callers branch on.
var errKubeSecretNotFound = errors.New("secret not found")

// errKubeConfigMapNotFound is a ConfigMap (or its key) that is not there.
var errKubeConfigMapNotFound = errors.New("configmap key not found")

// lastAppliedAnnotation is the copy of a manifest `kubectl apply` keeps on the
// object. For a Secret it holds every value in clear, which is why adoption
// strips it.
const lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// kubeSecret is what the commands read of a Secret: the data and the two
// facts adoption asserts. Held in memory only; no command prints Data.
type kubeSecret struct {
	Data        map[string][]byte
	Annotations map[string]string
	OwnerRefs   int
}

// kubeAccess is the narrow slice of the Kubernetes API the commands use.
type kubeAccess interface {
	GetSecret(ctx context.Context, namespace, name string) (kubeSecret, error)
	// RemoveAnnotations removes annotations with one JSON merge patch.
	// Removing an absent annotation is not an error. The patch never touches
	// data.
	RemoveAnnotations(ctx context.Context, namespace, name string, annotations []string) error
	// MintToken requests a short-lived token for a service account, bound to
	// one audience (the TokenRequest API).
	MintToken(ctx context.Context, namespace, serviceAccount, audience string, ttl time.Duration) (string, error)
	// ConfigMapValue reads one key of a ConfigMap. It is how the CLI gets the CA
	// the OpenBao component publishes; errKubeConfigMapNotFound when absent.
	ConfigMapValue(ctx context.Context, namespace, name, key string) (string, error)
	// ForwardService opens a local, loopback-only tunnel to a ready pod behind a
	// Service port (the kubelet's port-forward). The tunnel is an opaque byte
	// stream: whatever speaks across it, TLS included, is end to end between
	// the CLI and the pod. Close ends it.
	ForwardService(ctx context.Context, namespace, service string, port int) (podForward, error)
}

// podForward is a live tunnel to a pod.
type podForward interface {
	// Addr is the loopback host:port that reaches the pod.
	Addr() string
	io.Closer
}

// newKubeAccess builds the access for a kube context. It is a seam: tests
// substitute a fake, and TestMain's tripwire never needs to know.
//
// An empty context is refused rather than read as "the current context": a
// secret aimed at whichever cluster kubectl last pointed at is the mistake the
// required --env flag exists to prevent.
var newKubeAccess = func(kubeContext string) (kubeAccess, error) {
	if kubeContext == "" {
		return nil, errors.New("no kube context: pass --kube-context, or declare a cluster_target in the env's KCL")
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{CurrentContext: kubeContext}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kube context %q: %w", kubeContext, err)
	}
	return newKubeClients(cfg)
}

type kubeClients struct {
	cs  kubernetes.Interface
	cfg *rest.Config
}

func newKubeClients(cfg *rest.Config) (*kubeClients, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &kubeClients{cs: cs, cfg: cfg}, nil
}

func (k *kubeClients) GetSecret(ctx context.Context, namespace, name string) (kubeSecret, error) {
	s, err := k.cs.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return kubeSecret{}, fmt.Errorf("%s/%s: %w", namespace, name, errKubeSecretNotFound)
	}
	if err != nil {
		return kubeSecret{}, fmt.Errorf("read secret %s/%s: %w", namespace, name, err)
	}
	return kubeSecret{Data: s.Data, Annotations: s.Annotations, OwnerRefs: len(s.OwnerReferences)}, nil
}

func (k *kubeClients) RemoveAnnotations(ctx context.Context, namespace, name string, annotations []string) error {
	ann := make(map[string]any, len(annotations))
	for _, a := range annotations {
		ann[a] = nil
	}
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": ann}})
	if err != nil {
		return err
	}
	if _, err := k.cs.CoreV1().Secrets(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("patch secret %s/%s: %w", namespace, name, err)
	}
	return nil
}

func (k *kubeClients) MintToken(ctx context.Context, namespace, serviceAccount, audience string, ttl time.Duration) (string, error) {
	secs := int64(ttl.Seconds())
	tr, err := k.cs.CoreV1().ServiceAccounts(namespace).CreateToken(ctx, serviceAccount, &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{Audiences: []string{audience}, ExpirationSeconds: &secs},
	}, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("request a token for serviceaccount %s/%s: %w", namespace, serviceAccount, err)
	}
	if tr.Status.Token == "" {
		return "", fmt.Errorf("the cluster issued an empty token for serviceaccount %s/%s", namespace, serviceAccount)
	}
	return tr.Status.Token, nil
}

func (k *kubeClients) ConfigMapValue(ctx context.Context, namespace, name, key string) (string, error) {
	cm, err := k.cs.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", fmt.Errorf("configmap %s/%s: %w", namespace, name, errKubeConfigMapNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("read configmap %s/%s: %w", namespace, name, err)
	}
	v, ok := cm.Data[key]
	if !ok || v == "" {
		return "", fmt.Errorf("configmap %s/%s has no %q: %w", namespace, name, key, errKubeConfigMapNotFound)
	}
	return v, nil
}

// forwardReadyTimeout bounds how long opening the tunnel may take.
const forwardReadyTimeout = 30 * time.Second

func (k *kubeClients) ForwardService(ctx context.Context, namespace, service string, port int) (podForward, error) {
	pod, remote, err := pickForwardTarget(ctx, k.cs, namespace, service, port)
	if err != nil {
		return nil, err
	}
	u := k.cs.CoreV1().RESTClient().Post().Namespace(namespace).Resource("pods").Name(pod).SubResource("portforward").URL()

	// What kubectl does: tunnel SPDY over a websocket, and fall back to plain
	// SPDY for an API server or proxy that refuses the upgrade.
	rt, upgrader, err := spdy.RoundTripperFor(k.cfg)
	if err != nil {
		return nil, fmt.Errorf("port-forward to %s/%s: %w", namespace, pod, err)
	}
	dialer := spdy.NewDialerForStreaming(upgrader, &http.Client{Transport: rt}, http.MethodPost, u)
	if ws, err := portforward.NewSPDYOverWebsocketDialerForStreaming(u, k.cfg); err == nil {
		dialer = portforward.NewFallbackDialerForStreaming(ws, dialer, func(err error) bool {
			return streamhttp.IsUpgradeFailure(err) || streamhttp.IsHTTPSProxyError(err)
		})
	}

	fctx, cancel := context.WithCancel(ctx)
	ready := make(chan struct{})
	pf, err := portforward.NewOnAddressesForStreamingWithContext(fctx, dialer, []string{"127.0.0.1"}, []string{"0:" + strconv.Itoa(remote)}, ready, io.Discard, io.Discard)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("port-forward to %s/%s: %w", namespace, pod, err)
	}
	done := make(chan error, 1)
	go func() { done <- pf.ForwardPorts() }()
	select {
	case <-ready:
	case err := <-done:
		cancel()
		return nil, fmt.Errorf("port-forward to %s/%s: %w\nfix: the operator needs `create` on pods/portforward and `get`/`list` on pods and services in %s", namespace, pod, err, namespace)
	case <-time.After(forwardReadyTimeout):
		cancel()
		return nil, fmt.Errorf("port-forward to %s/%s: not ready after %s", namespace, pod, forwardReadyTimeout)
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	}
	ports, err := pf.GetPorts()
	if err != nil || len(ports) == 0 {
		cancel()
		return nil, fmt.Errorf("port-forward to %s/%s: no local port bound (%v)", namespace, pod, err)
	}
	return &kubePodForward{addr: "127.0.0.1:" + strconv.Itoa(int(ports[0].Local)), cancel: cancel, done: done}, nil
}

type kubePodForward struct {
	addr   string
	cancel context.CancelFunc
	done   <-chan error
}

func (f *kubePodForward) Addr() string { return f.addr }

func (f *kubePodForward) Close() error {
	f.cancel()
	select {
	case <-f.done:
	case <-time.After(5 * time.Second):
	}
	return nil
}

// pickForwardTarget chooses the pod a tunnel to a Service port ends at, and the
// container port it forwards to: a Running, Ready pod the Service selects.
// OpenBao's readiness accepts standbys, and a standby forwards a request to the
// active node, so any ready pod serves.
func pickForwardTarget(ctx context.Context, cs kubernetes.Interface, namespace, service string, port int) (pod string, remote int, err error) {
	svc, err := cs.CoreV1().Services(namespace).Get(ctx, service, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", 0, fmt.Errorf("service %s/%s: not found\nfix: the OpenBao component renders it; run `forge env deploy` for this env first", namespace, service)
	}
	if err != nil {
		return "", 0, fmt.Errorf("read service %s/%s: %w", namespace, service, err)
	}
	if len(svc.Spec.Selector) == 0 {
		return "", 0, fmt.Errorf("service %s/%s selects no pods", namespace, service)
	}
	var sp *corev1.ServicePort
	for i := range svc.Spec.Ports {
		if int(svc.Spec.Ports[i].Port) == port {
			sp = &svc.Spec.Ports[i]
		}
	}
	if sp == nil {
		return "", 0, fmt.Errorf("service %s/%s has no port %d", namespace, service, port)
	}
	pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: labels.SelectorFromSet(svc.Spec.Selector).String()})
	if err != nil {
		return "", 0, fmt.Errorf("list the pods of service %s/%s: %w", namespace, service, err)
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning || !podReady(p) {
			continue
		}
		if r, ok := containerPort(p, sp); ok {
			return p.Name, r, nil
		}
	}
	return "", 0, fmt.Errorf("service %s/%s has no ready pod (%d selected)\nfix: wait for the OpenBao pods to become ready, or check them with `kubectl -n %s get pods`", namespace, service, len(pods.Items), namespace)
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// containerPort resolves a Service port's target to a number on this pod.
func containerPort(p *corev1.Pod, sp *corev1.ServicePort) (int, bool) {
	t := sp.TargetPort
	if t.StrVal == "" {
		if t.IntVal == 0 {
			return int(sp.Port), true
		}
		return int(t.IntVal), true
	}
	for _, c := range p.Spec.Containers {
		for _, cp := range c.Ports {
			if cp.Name == t.StrVal {
				return int(cp.ContainerPort), true
			}
		}
	}
	return 0, false
}
