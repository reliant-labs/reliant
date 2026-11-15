package cli

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// TestParseKCLEntities_KubeconfigSecrets pins that the declared
// kubeconfig_secrets block parses into KubeconfigSecretEntity with
// defaults preserved as the renderer emits them.
func TestParseKCLEntities_KubeconfigSecrets(t *testing.T) {
	const js = `{
  "output": {
    "kubeconfig_secrets": [
      {
        "name": "workload-kubeconfig",
        "in_cluster": "k3d-cp",
        "target_cluster": "workload",
        "context_name": "workload",
        "key": "kubeconfig",
        "reachability": "in-network"
      },
      {
        "name": "prod-kubeconfig",
        "in_cluster": "k3d-cp",
        "target_cluster": "prod-workload",
        "context_name": "prod-workload",
        "key": "config",
        "namespace": "system",
        "reachability": "endpoint"
      }
    ],
    "workloads": []
  }
}`
	entities, err := parseKCLEntities([]byte(js))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}
	if len(entities.KubeconfigSecrets) != 2 {
		t.Fatalf("kubeconfig_secrets: got %d want 2", len(entities.KubeconfigSecrets))
	}
	k0 := entities.KubeconfigSecrets[0]
	if k0.InCluster != "k3d-cp" || k0.TargetCluster != "workload" || k0.ContextName != "workload" {
		t.Errorf("k0 fields wrong: %+v", k0)
	}
	if k0.Reachability != "in-network" {
		t.Errorf("k0.Reachability = %q want in-network", k0.Reachability)
	}
	k1 := entities.KubeconfigSecrets[1]
	if k1.Namespace != "system" || k1.Key != "config" || k1.Reachability != "endpoint" {
		t.Errorf("k1 fields wrong: %+v", k1)
	}
}

// TestMintKubeconfigSecrets_EmptyIsNoop confirms the mint phase is a
// no-op for an env declaring no kubeconfig secrets (never shells out).
func TestMintKubeconfigSecrets_EmptyIsNoop(t *testing.T) {
	if err := mintKubeconfigSecrets(t.Context(), nil, "k3d-cp", "dev"); err != nil {
		t.Errorf("empty mint should be a no-op, got %v", err)
	}
}

// TestOwnerNetworkFromClusters covers the implicit-ownership network
// resolution: the first cluster that DECLARES a network (a secondary
// pointing at the owner) is the shared network; absent that, a lone
// cluster's own k3d network; otherwise empty (no cross-cluster wiring).
// There is no "primary" notion — the value is exactly what the clusters
// declare.
func TestOwnerNetworkFromClusters(t *testing.T) {
	cases := []struct {
		name string
		in   []ClusterEntity
		want string
	}{
		{
			name: "secondary declares owner network",
			in: []ClusterEntity{
				{Name: "cp"},
				{Name: "workload", Network: "k3d-cp", RegistryInherit: true},
			},
			want: "k3d-cp",
		},
		{
			name: "lone cluster falls back to its own network",
			in:   []ClusterEntity{{Name: "dev"}},
			want: "k3d-dev",
		},
		{
			name: "multi-cluster with no declared network => empty",
			in:   []ClusterEntity{{Name: "a"}, {Name: "b"}},
			want: "",
		},
		{
			name: "no clusters => empty",
			in:   nil,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ownerNetworkFromClusters(tc.in); got != tc.want {
				t.Errorf("ownerNetworkFromClusters = %q want %q", got, tc.want)
			}
		})
	}
}

// TestKubeconfigSecretYAML pins the minted Secret manifest shape: a
// base64 `data` entry under the declared key, namespace + name, and the
// forge managed-by label.
// TestRewriteInNetworkKubeconfig_VerifiesTLSByName pins the in-network mint's
// TLS policy: the minted kubeconfig reaches the target by its container IP,
// KEEPS the cluster CA, and names the certificate's DNS SAN in
// tls-server-name. It must NOT fall back to insecure-skip-tls-verify.
//
// WHY THIS IS A CORRECTNESS BUG AND NOT HYGIENE. Flux's kustomize-controller
// refuses to honour insecure-skip-tls-verify in a Kustomization's kubeConfig
// Secret (it would let whoever writes that Secret disable verification), so it verifies
// against the system roots and every apply fails with "x509: certificate
// signed by unknown authority" — which is how the hosted-deploy e2e proof
// found it. The premise the old code stated ("the serverlb cert doesn't cover
// the container IP") is true of the IP and false of the NAME: k3s stamps
// k3d-<name>-serverlb and k3d-<name>-server-0 into the SANs.
//
// The property is checked end to end: a real TLS server whose certificate is
// signed by the kubeconfig's CA and carries ONLY the DNS SAN is reached by IP
// through client-go's own transport built from the minted file.
//
// MUTATION VERIFIED RED: dropping the tls-server-name → "certificate is valid
// for k3d-x-serverlb, not 127.0.0.1"; restoring the CA strip + insecure flag
// → the insecure/CA assertions.
func TestRewriteInNetworkKubeconfig_VerifiesTLSByName(t *testing.T) {
	const serverName = "k3d-x-serverlb"
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	caPEM, cert := testCAAndLeaf(t, serverName)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	ip, port := u.Hostname(), u.Port()

	raw := []byte(`apiVersion: v1
kind: Config
clusters:
- name: k3d-x
  cluster:
    server: https://0.0.0.0:6445
    certificate-authority-data: ` + base64.StdEncoding.EncodeToString(caPEM) + `
contexts:
- name: k3d-x
  context: {cluster: k3d-x, user: admin@k3d-x}
current-context: k3d-x
users:
- name: admin@k3d-x
  user: {token: t}
`)
	out, err := rewriteInNetworkKubeconfig(raw, "k3d-x", "https://"+ip+":"+port, serverName)
	if err != nil {
		t.Fatalf("rewriteInNetworkKubeconfig: %v", err)
	}
	cfg, err := clientcmd.Load(out)
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.Clusters["k3d-x"]
	if c.InsecureSkipTLSVerify {
		t.Fatal("the minted kubeconfig disables TLS verification: Flux's kustomize-controller ignores that flag and fails x509")
	}
	if len(c.CertificateAuthorityData) == 0 {
		t.Fatal("the minted kubeconfig dropped the cluster CA")
	}
	if c.Server != "https://"+ip+":"+port || c.TLSServerName != serverName {
		t.Fatalf("server=%q tls-server-name=%q, want the container address verified as %q", c.Server, c.TLSServerName, serverName)
	}

	restCfg, err := clientcmd.RESTConfigFromKubeConfig(out)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := rest.TransportFor(restCfg)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Transport: rt}).Get(restCfg.Host + "/")
	if err != nil {
		t.Fatalf("a client built from the minted kubeconfig cannot verify the API server by name: %v", err)
	}
	_ = resp.Body.Close()
}

// TestInNetworkServer_DialsTheContainerNameNotAnIP pins the in-network
// kubeconfig to a docker container NAME. Docker reassigns container IPs when
// the network is re-established; after a host sleep the live e2e env's minted
// "hub" kubeconfig (172.20.0.2) was reaching the cp-daemon cluster, which now
// held that IP — under the old insecure policy that would have been a silent
// write to the wrong cluster. The name is stable and is a cert SAN.
//
// MUTATION VERIFIED RED: returning an IP-based URL from the call site's
// resolved IP.
func TestInNetworkServer_DialsTheContainerNameNotAnIP(t *testing.T) {
	got := inNetworkServer("k3d-cp-daemon-v2-serverlb")
	if got != "https://k3d-cp-daemon-v2-serverlb:6443" {
		t.Fatalf("inNetworkServer = %q", got)
	}
	u, _ := url.Parse(got)
	if net.ParseIP(u.Hostname()) != nil {
		t.Fatalf("in-network server %q is an IP: it goes stale when docker reassigns addresses", got)
	}
}

// testCAAndLeaf returns a self-signed CA (PEM) and a leaf for serverName
// signed by it — with NO IP SAN, like a k3s serving cert seen by container IP.
func testCAAndLeaf(t *testing.T, serverName string) ([]byte, tls.Certificate) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "k3s-server-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "k3s"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{serverName}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		KeyUsage: x509.KeyUsageDigitalSignature,
	}, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}
}

func TestKubeconfigSecretYAML(t *testing.T) {
	payload := []byte("apiVersion: v1\nkind: Config\n")
	got := kubeconfigSecretYAML("workload-kubeconfig", "system", "config", payload)

	for _, want := range []string{
		"kind: Secret",
		"name: workload-kubeconfig",
		"namespace: system",
		"app.kubernetes.io/managed-by: forge",
		"type: Opaque",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Secret YAML missing %q:\n%s", want, got)
		}
	}
	// The kubeconfig must be base64-encoded under the declared key.
	wantData := "config: " + base64.StdEncoding.EncodeToString(payload)
	if !strings.Contains(got, wantData) {
		t.Errorf("Secret YAML missing base64 data %q:\n%s", wantData, got)
	}
	// Never inline the raw kubeconfig (it would be stringData, not data).
	if strings.Contains(got, "stringData") {
		t.Errorf("kubeconfig Secret should use base64 data, not stringData:\n%s", got)
	}
}
