// Package kubedial reaches a CloudNativePG database in a Kubernetes namespace
// through the kube API server, with no network path of its own: it resolves a
// ready pod behind the database's Service, opens a pods/portforward stream to
// it and hands the stream to pgx as a net.Conn.
//
// It is the transport under [dbquery.Run] wherever the database is not
// reachable by DNS and IP: `forge db query` against a developer's k3d, and the
// control plane's QueryDatabase against a customer environment's namespace on
// another cluster. It lives beside pkg/dbquery rather than in it so that dbquery's
// core stays free of client-go.
//
// # What a connection is
//
//	rest.Config ──► EndpointSlices of <db>-ro (or <db>-rw)
//	                  └─ a ready pod, chosen per connection
//	            ──► pods/portforward to that pod, port 5432
//	                  └─ one SPDY-over-websocket (plain SPDY as fallback)
//	                     connection and one stream pair per pgx connection
//	            ──► a net.Conn (pgx's DialFunc)
//
// There is no local listener: the stream is the connection. A pool therefore
// holds one API-server connection per database connection, and the pod is
// re-resolved on every dial, so a failover is followed by the next connection.
// A port-forward enters the pod's network namespace through the kubelet, so a
// NetworkPolicy that admits only same-namespace pods does not stand in the
// way, which is also why the Kubernetes Role below is the access boundary.
//
// # Choosing the Service
//
// [ServiceName] mirrors the rule `forge db query` has always used: <name>-ro
// when the Cluster runs standbys (instances > 1), otherwise <name>-rw, so a
// read never loads the primary when a replica exists.
//
// # TLS
//
// CNPG serves TLS and its pg_hba admits remote clients over hostssl only.
// [Credentials.ConnConfig] verifies the server certificate against the
// cluster CA ([CASecretName]) with ServerName <service>.<namespace>.svc, a
// name CNPG's server certificate carries, so sslmode=verify-full holds inside
// the tunnel and the API server's own TLS is not what protects the session.
//
// # Kubernetes Role rules the caller needs
//
// [PolicyRules] returns the exact rules, in a namespaced Role, for the
// databases the caller may reach. Declare them from that one source:
//
//	pods/portforward   get, create   a port-forward is a GET upgrade to
//	                                 websocket and a POST for SPDY; since
//	                                 Kubernetes 1.35 the websocket upgrade also
//	                                 needs create (the API server's pod-websocket-upgrade create-permission gate)
//	endpointslices     get, list     (discovery.k8s.io) to find ready pods
//	secrets            get           on exactly <db>-forge-ro and <db>-ca
//
// pods/portforward cannot be narrowed to a port or a pod by a Role, so it admits
// any port of any pod in the namespace. Give the identity that dials its own
// Role and ServiceAccount (or impersonation) rather than widening a deploy
// identity, and rely on the API server's audit log as the second trail.
//
// The <db>-ca Secret holds the CA's private key beside its certificate. This
// package reads only ca.crt and keeps nothing else, but the Secret's get
// permission is a real grant: scope it to the names, never to all Secrets.
package kubedial

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"math/rand/v2"

	discoveryv1 "k8s.io/api/discovery/v1"
)

// PostgresPort is the port a CNPG instance serves on.
const PostgresPort = 5432

// ErrNoReadyEndpoint means the Service has no ready pod to forward to: the
// Cluster is starting, scaled to zero, or the Service does not exist.
var ErrNoReadyEndpoint = errors.New("no ready endpoint")

// ServiceName is the CNPG Service a read goes to for the Cluster called name:
// name-ro when it runs standbys, name-rw otherwise.
func ServiceName(name string, instances int32) string {
	if instances > 1 {
		return name + "-ro"
	}
	return name + "-rw"
}

// CASecretName is the Secret CNPG keeps the cluster CA in (key ca.crt) for the
// Cluster called name.
func CASecretName(name string) string { return name + "-ca" }

// Target is a Service whose ready pods a connection can land on.
type Target struct {
	Namespace string
	Service   string
}

// Host is the in-cluster name a TLS certificate is verified against, and the
// host a DSN built for this target carries: <service>.<namespace>.svc.
func (t Target) Host() string { return t.Service + "." + t.Namespace + ".svc" }

// Database is one ManagedDatabase: the CNPG Cluster's name, the namespace it
// runs in and how many instances it has (which picks -ro or -rw).
type Database struct {
	Namespace string
	Name      string
	Instances int32
}

// Target is the Service a read of this database goes to.
func (d Database) Target() Target {
	return Target{Namespace: d.Namespace, Service: ServiceName(d.Name, d.Instances)}
}

// PodDialer opens one stream to a port of a pod. The default speaks the
// Kubernetes port-forward protocol through the API server; a test or a
// different tunnel substitutes its own through [NewWithClientset].
type PodDialer interface {
	// DialPod returns a stream to port inside the pod. Closing it tears the
	// forward down. ctx bounds the setup only; the stream outlives it.
	DialPod(ctx context.Context, namespace, pod string, port int) (io.ReadWriteCloser, error)
}

// dialTimeout bounds resolving a pod and opening the stream when the caller's
// context carries no deadline of its own. A variable so a test can shrink it.
var dialTimeout = 30 * time.Second

// Client dials databases through one cluster's API server. It is safe for
// concurrent use.
type Client struct {
	cs   kubernetes.Interface
	pods PodDialer
}

// New builds a Client over the cluster cfg points at.
func New(cfg *rest.Config) (*Client, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kubedial: %w", err)
	}
	return &Client{cs: cs, pods: &spdyDialer{cfg: cfg, cs: cs}}, nil
}

// NewWithClientset builds a Client over an existing clientset (reads) and an
// explicit PodDialer (streams). With a fake clientset and a fake PodDialer no
// cluster is needed.
func NewWithClientset(cs kubernetes.Interface, pods PodDialer) *Client {
	return &Client{cs: cs, pods: pods}
}

// Dial opens one connection to a ready pod behind t: one resolve, one API
// server connection, one stream pair. The returned net.Conn honours read and
// write deadlines, which is what pgx cancels a query with. ctx bounds only the
// dial; cancelling it later does not close the connection.
func (c *Client) Dial(ctx context.Context, t Target) (net.Conn, error) {
	if t.Namespace == "" || t.Service == "" {
		return nil, errors.New("kubedial: a target needs a namespace and a service")
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, dialTimeout)
		defer cancel()
	}
	pod, port, err := c.resolve(ctx, t)
	if err != nil {
		return nil, err
	}
	rwc, err := c.pods.DialPod(ctx, t.Namespace, pod, port)
	if err != nil {
		return nil, fmt.Errorf("kubedial: port-forward to %s/%s (service %s): %w%s", t.Namespace, pod, t.Service, err, forbiddenHint(err, t.Namespace))
	}
	return newConn(rwc, fmt.Sprintf("%s/%s:%d", t.Namespace, pod, port)), nil
}

// DialFunc is [Client.Dial] for t as a pgx DialFunc (pgconn.Config.DialFunc).
// The address pgx passes is ignored: the target decides where the connection
// goes, the DSN's host only names the certificate to verify.
func (c *Client) DialFunc(t Target) pgconn.DialFunc {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		if !strings.HasPrefix(network, "tcp") {
			return nil, fmt.Errorf("kubedial: network %q is not supported; only tcp", network)
		}
		return c.Dial(ctx, t)
	}
}

func forbiddenHint(err error, namespace string) string {
	if apierrors.IsForbidden(err) || strings.Contains(strings.ToLower(err.Error()), "forbidden") {
		return fmt.Sprintf("\nfix: the identity needs the rules kubedial.PolicyRules returns in namespace %s (pods/portforward get+create, endpointslices get+list, secrets get on the database's two Secrets)", namespace)
	}
	return ""
}

var (
	randMu sync.Mutex
	// pick returns an index below n; a variable so a test can fix the choice.
	pick = func(n int) int {
		randMu.Lock()
		defer randMu.Unlock()
		return rand.IntN(n)
	}
)

// resolve picks a ready pod behind the Service from its EndpointSlices, and
// the port to forward to inside it. A ready pod is chosen at random per dial so
// a -ro Service spreads reads across its replicas.
func (c *Client) resolve(ctx context.Context, t Target) (pod string, port int, err error) {
	slices, err := c.cs.DiscoveryV1().EndpointSlices(t.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: discoveryv1.LabelServiceName + "=" + t.Service,
	})
	if err != nil {
		return "", 0, fmt.Errorf("kubedial: list endpointslices of %s/%s: %w%s", t.Namespace, t.Service, err, forbiddenHint(err, t.Namespace))
	}
	type candidate struct {
		pod  string
		port int
	}
	var ready []candidate
	for i := range slices.Items {
		s := &slices.Items[i]
		p, ok := slicePort(s)
		if !ok {
			continue
		}
		for _, ep := range s.Endpoints {
			if ep.Conditions.Ready != nil && !*ep.Conditions.Ready {
				continue
			}
			if ep.TargetRef == nil || ep.TargetRef.Kind != "Pod" || ep.TargetRef.Name == "" {
				continue
			}
			ready = append(ready, candidate{pod: ep.TargetRef.Name, port: p})
		}
	}
	if len(ready) == 0 {
		return "", 0, fmt.Errorf("kubedial: service %s/%s: %w", t.Namespace, t.Service, ErrNoReadyEndpoint)
	}
	c0 := ready[pick(len(ready))]
	return c0.pod, c0.port, nil
}

// slicePort is the pod-side port of the slice's Postgres port: the one numbered
// PostgresPort, or the only one there is.
func slicePort(s *discoveryv1.EndpointSlice) (int, bool) {
	var only int
	for _, p := range s.Ports {
		if p.Port == nil {
			continue
		}
		if int(*p.Port) == PostgresPort {
			return PostgresPort, true
		}
		only = int(*p.Port)
	}
	if len(s.Ports) == 1 && only != 0 {
		return only, true
	}
	return 0, false
}
