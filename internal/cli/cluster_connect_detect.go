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

// The ONE way the hub authenticates to a connected cluster: a scoped
// ServiceAccount token forge mints in the owner's cluster, uploaded write-only.
// There is no cloud identity, hub-side or otherwise: a connected cluster is
// somebody else's, and nothing of ours is trusted there. So the request names
// no auth mode: the control plane deleted the enum and reserved the field.

// connectTarget is everything forge learns about a cluster from the
// operator's kubeconfig plus the chosen auth: the whole ConnectCluster
// request, decided before any call is made.
type connectTarget struct {
	// Context is the kubectl context forge read this from, and the context
	// the bootstrap RBAC is applied THROUGH. It is also the key a bundle
	// renders under (release.BundleClusterPath), which is what makes a
	// cluster_bindings entry addressable.
	Context string
	// Address is the API server URL the HUB will dial. https only, port 443
	// or 6443.
	Address string
	// CAPEM is the PEM bundle that signed the API server's certificate.
	CAPEM string
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

// readConnectTarget assembles the ConnectCluster request from the operator's
// kubeconfig entry for kctx, and refuses an address the hub cannot use.
//
// WHERE THE ADDRESS COMES FROM, in order:
//
//  1. --address, when given.
//  2. On a GKE context, the cluster's PUBLIC endpoint from GKE's describe. A
//     GKE kubeconfig often holds a DNS endpoint (*.gke.goog) instead, which is
//     IAM-gated: a ServiceAccount token cannot pass it. The private endpoint is
//     never chosen by default — the hub reaches a private one only if the
//     operator says so with --address.
//  3. The kubeconfig's cluster.server.
//
// The CA comes from --ca-file, else GKE's describe (masterAuth), else the
// kubeconfig. The credential beside the kubeconfig entry is never read.
func readConnectTarget(kctx, addressFlag, caFile string) (connectTarget, error) {
	kctx = strings.TrimSpace(kctx)
	if kctx == "" {
		return connectTarget{}, fmt.Errorf("--context is required: it names the kubectl context forge reads the " +
			"cluster's address and CA from, and applies the bootstrap RBAC through")
	}
	address, caPEM, err := kubeconfigClusterOf(kctx)
	if err != nil {
		return connectTarget{}, err
	}
	target := connectTarget{Context: kctx}
	addressFlag = strings.TrimRight(strings.TrimSpace(addressFlag), "/")
	if addressFlag != "" {
		address = addressFlag
		target.AddressNote = "the address is --address (the context's server is not what the hub dials)"
	}
	if gke, ok := parseGKEContext(kctx); ok {
		// The hub dials from its own pods, so the address and CA it registers
		// come from GKE's describe, not the operator's kubeconfig (which may
		// hold a DNS endpoint with a publicly trusted cert and no CA).
		if d, ok := gkeDescribeOf(gke); ok {
			if addressFlag == "" && d.Endpoint != "" {
				address = "https://" + d.Endpoint
				target.AddressNote = "the address is the cluster's public endpoint from GKE (the context's server is not what the hub dials)"
			}
			if d.CAPEM != "" {
				caPEM = d.CAPEM
			}
		}
	}
	if caFile != "" {
		b, err := os.ReadFile(caFile)
		if err != nil {
			return connectTarget{}, fmt.Errorf("read --ca-file: %w", err)
		}
		if !strings.Contains(string(b), "-----BEGIN CERTIFICATE-----") {
			return connectTarget{}, fmt.Errorf("--ca-file %q holds no PEM certificate\nfix: pass the PEM bundle that signed the API server's certificate", caFile)
		}
		caPEM = strings.TrimSpace(string(b))
	}
	if err := refuseUnusableAddress(address); err != nil {
		return connectTarget{}, err
	}
	if caPEM == "" {
		// Never degrade to skipping verification. A target we cannot verify
		// is one we decline to deploy to — the alternative is sending a
		// standing credential to whatever answers that address.
		return connectTarget{}, fmt.Errorf("no certificate authority for %s (context %q), and forge "+
			"will not connect a cluster whose API server it cannot verify\n"+
			"fix: --ca-file <pem>, or refresh the kubeconfig entry so it carries certificate-authority-data "+
			"(re-run your provider's get-credentials)", address, kctx)
	}
	target.Address, target.CAPEM = address, caPEM
	return target, nil
}

// gkeDescription is the part of `gcloud container clusters describe` the hub's
// registration needs.
type gkeDescription struct {
	Endpoint string
	CAPEM    string // decoded PEM
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
		Endpoint   string `json:"endpoint"`
		MasterAuth struct {
			ClusterCaCertificate string `json:"clusterCaCertificate"`
		} `json:"masterAuth"`
	}
	if json.Unmarshal(out, &raw) != nil {
		return gkeDescription{}, false
	}
	d := gkeDescription{Endpoint: raw.Endpoint}
	if pem, err := base64.StdEncoding.DecodeString(raw.MasterAuth.ClusterCaCertificate); err == nil {
		d.CAPEM = string(pem)
	}
	return d, true
}

// connectPorts are the API-server ports the hub will dial: 443 (the https
// default) and 6443 (the k3s and kubeadm default). The control plane applies
// the same rule, so a refusal here is the same refusal without a round trip.
var connectPorts = map[string]bool{"": true, "443": true, "6443": true}

// refuseUnusableAddress rejects the addresses that cannot work, or must not be
// trusted with the credential.
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
		return fmt.Errorf("the cluster's API server address is %q, which is not a URL", address)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("the cluster's API server address is %q, whose scheme is %q\n"+
			"forge connects over https only: any other scheme would carry the hub's credential in clear text",
			address, u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("the cluster's API server address %q carries credentials in the URL\n"+
			"fix: pass the bare https://host[:port] address", address)
	}
	host := strings.ToLower(u.Hostname())
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsUnspecified()) {
		return fmt.Errorf("the cluster's API server address is %q, whose host %q is a loopback or bind address.\n"+
			"The hub dials it from a POD, where %q is that pod — so the deploy would fail as "+
			"\"connection refused\" against a cluster that is perfectly healthy.\n"+
			"fix: connect this cluster by an address reachable from outside it (--address, its public or "+
			"peered API endpoint); a local k3d cluster is not a connect target, it is applied "+
			"to directly", address, host, host)
	}
	if host == "localhost" {
		return fmt.Errorf("the cluster's API server address is %q, whose host is localhost.\n"+
			"The hub dials it from a POD, where localhost is that pod.\n"+
			"fix: connect this cluster by an address reachable from outside it (--address)", address)
	}
	if strings.HasSuffix(host, ".gke.goog") {
		return fmt.Errorf("the cluster's API server address is %q, a GKE DNS endpoint (*.gke.goog).\n"+
			"It is IAM-gated: it authenticates a Google identity, and a ServiceAccount token cannot pass it.\n"+
			"fix: --address https://<the cluster's public endpoint> "+
			"(gcloud container clusters describe <cluster> --format='value(endpoint)')", address)
	}
	if !connectPorts[u.Port()] {
		return fmt.Errorf("the cluster's API server address is %q, on port %s\n"+
			"the hub dials the API server on port 443 or 6443 only (6443 is the k3s and kubeadm default)\n"+
			"fix: expose the API server on 443 or 6443 (a load balancer or port-forward in front of it), "+
			"then pass it with --address", address, u.Port())
	}
	return nil
}

// kubeconfigClusterOf reads ONE context's cluster.server and CA out of the
// operator's kubeconfig.
//
// ONLY THE ADDRESS AND THE CA ARE TAKEN, never the credential beside them. A
// GKE kubeconfig authenticates through an `exec` plugin — an instruction to
// run a binary on the operator's machine with the operator's own credentials —
// which is inert anywhere else. The hub's credential is the minted token;
// this function is reading a public address and a public certificate.
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
