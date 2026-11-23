package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/reliant-labs/forge/internal/testcollect"
	"github.com/reliant-labs/forge/pkg/dbquery/kubedial"
)

// collectKube is the testcollect.Kube a kubeconfig reaches: one client per
// context, built on first use. Pods are reached through the port-forward
// tunnel `forge db query` uses (pkg/dbquery/kubedial), one stream per
// request and no local port.
type collectKube struct {
	mu        sync.Mutex
	byContext map[string]*collectKubeClient
}

type collectKubeClient struct {
	cs   kubernetes.Interface
	dial *kubedial.Client
}

func (k *collectKube) client(kubeContext string) (*collectKubeClient, error) {
	if kubeContext == "" {
		// Never the kubeconfig's current context: the env names its cluster.
		return nil, errors.New("the env declares no cluster (kube context) for this workload")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if c, ok := k.byContext[kubeContext]; ok {
		return c, nil
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{CurrentContext: kubeContext}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kube context %q: %w", kubeContext, err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kube context %q: %w", kubeContext, err)
	}
	dial, err := kubedial.New(cfg)
	if err != nil {
		return nil, err
	}
	if k.byContext == nil {
		k.byContext = map[string]*collectKubeClient{}
	}
	c := &collectKubeClient{cs: cs, dial: dial}
	k.byContext[kubeContext] = c
	return c, nil
}

// Pods lists the running pods selector picks, oldest first.
func (k *collectKube) Pods(ctx context.Context, kubeContext, namespace, selector string) ([]testcollect.Pod, error) {
	c, err := k.client(kubeContext)
	if err != nil {
		return nil, err
	}
	list, err := c.cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("list pods %s in %s on %s: %w", selector, namespace, kubeContext, err)
	}
	return runningPods(list.Items), nil
}

func runningPods(items []corev1.Pod) []testcollect.Pod {
	var out []testcollect.Pod
	for _, p := range items {
		if p.Status.Phase != corev1.PodRunning || p.DeletionTimestamp != nil {
			continue
		}
		started := p.CreationTimestamp.Time
		if p.Status.StartTime != nil {
			started = p.Status.StartTime.Time
		}
		out = append(out, testcollect.Pod{Name: p.Name, Started: started})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Started.Equal(out[j].Started) {
			return out[i].Started.Before(out[j].Started)
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Do sends req to port inside the pod, over its own tunnel.
func (k *collectKube) Do(ctx context.Context, kubeContext, namespace, pod string, port int, req *http.Request) (*http.Response, error) {
	c, err := k.client(kubeContext)
	if err != nil {
		return nil, err
	}
	hc := &http.Client{
		Timeout: 2 * time.Minute,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return c.dial.DialPod(ctx, namespace, pod, port)
			},
		},
	}
	return hc.Do(req.WithContext(ctx))
}
