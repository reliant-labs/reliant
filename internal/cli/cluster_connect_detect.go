package cli

// Reading a cluster's ADDRESS out of the operator's kubeconfig, and deciding
// how the hub should authenticate to it. Every function here is pure or reads
// only the kubeconfig, so the refusals and the auto-detect table are testable
// without a cluster.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"k8s.io/client-go/tools/clientcmd"
)

// The two ways the hub authenticates to a connected cluster, as the
// controlplane.v1.ClusterAuth value NAMES protojson encodes.
//
// TWO VALUES, AND THEY ARE THE TWO ENDS OF A TRADE-OFF rather than two
// clouds. GCP workload identity is the good one where it applies — no secret
// crosses the boundary, so there is nothing in anyone's database for a read to
// leak. The ServiceAccount token is the one that works ANYWHERE, and it is
// strictly worse: a replayable bearer token at rest, scoped down by RBAC.
//
// THERE IS NO aws OR azure, deliberately. Flux's configMapRef supports both
// providers and the control plane reserves the enum tags for them, but neither
// path is built or tested — and an untested code path that LOOKS supported is
// worse than an absent one, because it gets chosen, fails inside a cloud
// client, and reports a problem with the customer's cluster. An EKS or AKS
// cluster is a target TODAY through authToken, which needs no cloud identity.
const (
	authGCP   = "CLUSTER_AUTH_WORKLOAD_IDENTITY_GCP"
	authToken = "CLUSTER_AUTH_SERVICE_ACCOUNT_TOKEN"
)

// connectTarget is everything forge learns about a cluster from the
// operator's kubeconfig plus the chosen auth: the whole ConnectCluster
// request, decided before any call is made.
type connectTarget struct {
	// Context is the kubectl context forge read this from, and the context
	// the bootstrap RBAC is applied THROUGH. It is also the key a bundle
	// renders under (release.BundleClusterPath), which is what makes a
	// cluster_bindings entry addressable.
	Context string
	// Auth is one of the two constants above.
	Auth string
	// Address is the API server URL the HUB will dial. https only.
	Address string
	// CAPEM is the PEM bundle that signed the API server's certificate.
	CAPEM string
	// CloudCluster is the provider's own resource name, set iff Auth is a
	// workload-identity auth. The control plane REFUSES it on the token
	// auth, which does not use it.
	CloudCluster string
	// AddressNote says why Address is not the context's server, or "".
	AddressNote string
}

// gkeContext is a parsed GKE kubectl context: `gke_<project>_<location>_<cluster>`,
// which is the name `gcloud container clusters get-credentials` writes.
type gkeContext struct {
	Project  string
	Location string
	Cluster  string
}

// CloudCluster is the resource name Flux exchanges the hub's identity
// against. Its format is validated server-side, because a malformed path
// produces a Flux reconcile error naming a cloud API two layers from the
// typo.
func (g gkeContext) CloudCluster() string {
	return fmt.Sprintf("projects/%s/locations/%s/clusters/%s", g.Project, g.Location, g.Cluster)
}

// parseGKEContext recognises a GKE context name.
//
// EXACTLY FOUR UNDERSCORE-SEPARATED FIELDS, and none of the three values may
// be empty. A GCP project id, a GCP location and an RFC-1123 cluster name all
// forbid `_`, so a context carrying five fields is not a GKE context with an
// odd name — it is something else whose shape this must not guess at.
func parseGKEContext(kctx string) (gkeContext, bool) {
	parts := strings.Split(strings.TrimSpace(kctx), "_")
	if len(parts) != 4 || parts[0] != "gke" {
		return gkeContext{}, false
	}
	for _, p := range parts[1:] {
		if p == "" {
			return gkeContext{}, false
		}
	}
	return gkeContext{Project: parts[1], Location: parts[2], Cluster: parts[3]}, true
}

// resolveAuth turns the --auth flag into one of the two wire values.
//
// `auto` picks gcp for a GKE context and token for everything else. That
// default is the honest one: GKE is the single cloud whose workload identity
// the hub can present, and a token works on any Kubernetes cluster — so the
// fallback is never "unsupported", it is the universal path.
func resolveAuth(flag, kctx string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(flag)) {
	case "", "auto":
		if _, ok := parseGKEContext(kctx); ok {
			return authGCP, nil
		}
		return authToken, nil
	case "gcp":
		if _, ok := parseGKEContext(kctx); !ok {
			return "", fmt.Errorf("--auth gcp needs a GKE context, and %q is not one "+
				"(gcloud writes them as gke_<project>_<location>_<cluster>)\n"+
				"fix: --auth token, which authenticates with a scoped ServiceAccount token and works on any cluster", kctx)
		}
		return authGCP, nil
	case "token":
		return authToken, nil
	default:
		return "", fmt.Errorf("--auth %q is not a known value: auto, gcp or token\n"+
			"gcp is GKE workload identity (no secret crosses the boundary); token is a scoped "+
			"ServiceAccount token, which works on any Kubernetes cluster", flag)
	}
}

// readConnectTarget assembles the ConnectCluster request from the operator's
// kubeconfig entry for kctx, and refuses an address the hub cannot use.
func readConnectTarget(kctx, authFlag string) (connectTarget, error) {
	kctx = strings.TrimSpace(kctx)
	if kctx == "" {
		return connectTarget{}, fmt.Errorf("--context is required: it names the kubectl context forge reads the " +
			"cluster's address and CA from, and applies the bootstrap RBAC through")
	}
	auth, err := resolveAuth(authFlag, kctx)
	if err != nil {
		return connectTarget{}, err
	}
	address, caPEM, err := kubeconfigClusterOf(kctx)
	if err != nil {
		return connectTarget{}, err
	}
	target := connectTarget{Context: kctx, Auth: auth}
	if auth == authGCP {
		gke, _ := parseGKEContext(kctx)
		target.CloudCluster = gke.CloudCluster()
		// The hub dials from its own pods, so the address and CA it registers
		// come from GKE's describe, not the operator's kubeconfig (which may
		// hold a DNS endpoint with a publicly trusted cert and no CA).
		if d, ok := gkeDescribeOf(gke); ok {
			switch {
			case d.PrivateEndpoint != "":
				address = "https://" + d.PrivateEndpoint
				target.AddressNote = "the cluster has a private endpoint, which is what the hub can reach (the context's server is not what the hub dials)"
			case d.Endpoint != "":
				address = "https://" + d.Endpoint
				target.AddressNote = "the address is the cluster's endpoint from GKE (the context's server is not what the hub dials)"
			}
			if d.CAPEM != "" {
				caPEM = d.CAPEM
			}
		}
	}
	if err := refuseUnusableAddress(address); err != nil {
		return connectTarget{}, err
	}
	if caPEM == "" {
		// Never degrade to skipping verification. A target we cannot verify
		// is one we decline to deploy to — the alternative is sending a
		// standing credential to whatever answers that address.
		if auth == authGCP {
			return connectTarget{}, fmt.Errorf("GKE's describe of cluster %q returned no masterAuth.clusterCaCertificate, and forge "+
				"will not connect a cluster whose API server it cannot verify", target.CloudCluster)
		}
		return connectTarget{}, fmt.Errorf("context %q carries no certificate authority for its cluster, and forge "+
			"will not connect a cluster whose API server it cannot verify\n"+
			"fix: the kubeconfig entry needs certificate-authority-data or certificate-authority "+
			"(re-run your provider's get-credentials to refresh it)", kctx)
	}
	target.Address, target.CAPEM = address, caPEM
	return target, nil
}

// gkeDescription is the part of `gcloud container clusters describe` the hub's
// registration needs.
type gkeDescription struct {
	Endpoint        string
	PrivateEndpoint string
	CAPEM           string // decoded PEM
}

// gkeDescribeOf reads the cluster's endpoints and CA from GKE. Read-only, with
// the operator's own gcloud credentials; ok is false when it cannot be read.
// A var so tests supply a fake describe.
var gkeDescribeOf = func(g gkeContext) (gkeDescription, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gcloud", "container", "clusters", "describe", g.Cluster,
		"--project", g.Project, "--location", g.Location, "--format=json").Output()
	if err != nil {
		return gkeDescription{}, false
	}
	var raw struct {
		Endpoint             string `json:"endpoint"`
		PrivateClusterConfig struct {
			PrivateEndpoint string `json:"privateEndpoint"`
		} `json:"privateClusterConfig"`
		MasterAuth struct {
			ClusterCaCertificate string `json:"clusterCaCertificate"`
		} `json:"masterAuth"`
	}
	if json.Unmarshal(out, &raw) != nil {
		return gkeDescription{}, false
	}
	d := gkeDescription{Endpoint: raw.Endpoint, PrivateEndpoint: raw.PrivateClusterConfig.PrivateEndpoint}
	if pem, err := base64.StdEncoding.DecodeString(raw.MasterAuth.ClusterCaCertificate); err == nil {
		d.CAPEM = string(pem)
	}
	return d, true
}

// refuseUnusableAddress rejects the two addresses that fail AGAINST A HEALTHY
// CLUSTER, which is why they are worth their own refusal.
//
// The hub's kustomize-controller dials this address from a POD, where
// `127.0.0.1` and `0.0.0.0` mean THAT POD. So a loopback address does not fail
// as "you gave me a loopback address" — it fails as "connection refused"
// against a target cluster that is perfectly healthy, which is the one place
// the fault is not. A non-https scheme would carry the credential in clear
// text.
func refuseUnusableAddress(address string) error {
	u, err := url.Parse(address)
	if err != nil || address == "" {
		return fmt.Errorf("the context's cluster.server is %q, which is not a URL", address)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("the cluster's API server address is %q, whose scheme is %q\n"+
			"forge connects over https only: any other scheme would carry the hub's credential in clear text",
			address, u.Scheme)
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsUnspecified()) {
		return fmt.Errorf("the cluster's API server address is %q, whose host %q is a loopback or bind address.\n"+
			"The hub dials it from a POD, where %q is that pod — so the deploy would fail as "+
			"\"connection refused\" against a cluster that is perfectly healthy.\n"+
			"fix: connect this cluster by an address reachable from outside it (its public or "+
			"peered API endpoint); a local k3d cluster is not a connect target, it is applied "+
			"to directly", address, host, host)
	}
	if host == "localhost" {
		return fmt.Errorf("the cluster's API server address is %q, whose host is localhost.\n"+
			"The hub dials it from a POD, where localhost is that pod.\n"+
			"fix: connect this cluster by an address reachable from outside it", address)
	}
	return nil
}

// kubeconfigClusterOf reads ONE context's cluster.server and CA out of the
// operator's kubeconfig.
//
// ONLY THE ADDRESS AND THE CA ARE TAKEN, never the credential beside them. A
// GKE kubeconfig authenticates through an `exec` plugin — an instruction to
// run a binary on the operator's machine with the operator's own credentials —
// which is inert anywhere else. The hub's credential is the workload identity
// or the minted token, decided by Auth; this function is reading a public
// address and a public certificate.
//
// A var so tests supply a kubeconfig without one on disk.
var kubeconfigClusterOf = func(kctx string) (address, caPEM string, err error) {
	raw, err := clientcmd.NewDefaultPathOptions().GetStartingConfig()
	if err != nil {
		return "", "", fmt.Errorf("read kubeconfig: %w", err)
	}
	ctxEntry, ok := raw.Contexts[kctx]
	if !ok {
		known := make([]string, 0, len(raw.Contexts))
		for name := range raw.Contexts {
			known = append(known, name)
		}
		return "", "", fmt.Errorf("no context %q in your kubeconfig (it has: %s)\n"+
			"fix: `kubectl config get-contexts` and name one of them, or run your provider's "+
			"get-credentials to add it", kctx, strings.Join(known, ", "))
	}
	clusterEntry, ok := raw.Clusters[ctxEntry.Cluster]
	if !ok {
		return "", "", fmt.Errorf("context %q names cluster %q, which your kubeconfig does not define",
			kctx, ctxEntry.Cluster)
	}
	ca := string(clusterEntry.CertificateAuthorityData)
	if ca == "" && clusterEntry.CertificateAuthority != "" {
		// An out-of-line CA is equally valid kubeconfig, and the control
		// plane needs the BYTES — so read the file rather than refusing a
		// kubeconfig that is merely spelled the other way.
		b, rerr := os.ReadFile(clusterEntry.CertificateAuthority)
		if rerr != nil {
			return "", "", fmt.Errorf("read certificate-authority %q for context %q: %w",
				clusterEntry.CertificateAuthority, kctx, rerr)
		}
		ca = string(b)
	}
	return strings.TrimSpace(clusterEntry.Server), ca, nil
}
