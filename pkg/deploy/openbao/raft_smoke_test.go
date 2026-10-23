package openbao

// P6, as far as it can be settled without a cluster: three REAL OpenBao 2.6.2
// nodes started from the config this package renders for the default (Raft x3)
// store. Docker network aliases stand in for the headless Service's per-pod DNS
// names (openbao-N.openbao-internal.<ns>.svc), so retry_join, the TLS
// verification of the peer's certificate (the SAN list the cert-manager
// Certificate requests) and the static auto-unseal run for real. What is left
// for k3d is everything Kubernetes does around the pods: cert-manager issuing,
// PVCs, DNS, PSA admission.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSmoke_RaftAutoJoinThreeNodes(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: boots three OpenBao containers; runs in task test")
	}
	if exec.Command("docker", "version").Run() != nil {
		t.Skip("docker is not available")
	}
	if exec.Command("docker", "image", "inspect", smokeOpenBaoImage).Run() != nil {
		if out, err := exec.Command("docker", "pull", smokeOpenBaoImage).CombinedOutput(); err != nil {
			t.Skipf("cannot pull %s: %v\n%s", smokeOpenBaoImage, err, out)
		}
	}

	spec := raftSpec()
	objs, err := Render(spec)
	if err != nil {
		t.Fatal(err)
	}
	cfg := configOf(t, objs)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "config.hcl"), cfg, 0o644)

	// The server certificate is issued for exactly the names the rendered
	// Certificate asks cert-manager for.
	names := spec.dnsNames()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caT := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ca"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, _ := x509.CreateCertificate(rand.Reader, caT, caT, &caKey.PublicKey, caKey)
	ca, _ := x509.ParseCertificate(caDER)
	srvKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	srvT := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: spec.Name}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), DNSNames: names, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	srvDER, _ := x509.CreateCertificate(rand.Reader, srvT, ca, &srvKey.PublicKey, caKey)
	keyDER, _ := x509.MarshalECPrivateKey(srvKey)
	tlsDir := filepath.Join(dir, "tls")
	mustMkdir(t, tlsDir)
	pemOf := func(typ string, der []byte) string {
		return string(pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}))
	}
	mustWrite(t, filepath.Join(tlsDir, "tls.crt"), pemOf("CERTIFICATE", srvDER), 0o644)
	mustWrite(t, filepath.Join(tlsDir, "tls.key"), pemOf("EC PRIVATE KEY", keyDER), 0o644)
	mustWrite(t, filepath.Join(tlsDir, "ca.crt"), pemOf("CERTIFICATE", caDER), 0o644)
	pool := x509.NewCertPool()
	pool.AddCert(ca)

	kb := make([]byte, 32)
	_, _ = rand.Read(kb)
	key := hex.EncodeToString(kb)
	sfx := fmt.Sprintf("%d", time.Now().UnixNano()%1e9)
	network := "bao-raft-" + sfx
	if out, err := exec.Command("docker", "network", "create", network).CombinedOutput(); err != nil {
		t.Fatalf("docker network: %v\n%s", err, out)
	}
	var containers []string
	t.Cleanup(func() {
		for _, c := range containers {
			_ = exec.Command("docker", "rm", "-fv", c).Run()
		}
		_ = exec.Command("docker", "network", "rm", network).Run()
	})

	ports := make([]int, spec.Replicas)
	for i := range ports {
		pod := fmt.Sprintf("%s-%d", spec.Name, i)
		fqdn := fmt.Sprintf("%s.%s.%s.svc", pod, spec.internalName(), spec.Namespace)
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		ports[i] = l.Addr().(*net.TCPAddr).Port
		_ = l.Close()
		name := fmt.Sprintf("%s-%s", pod, sfx)
		containers = append(containers, name)
		out, err := exec.Command("docker", "run", "-d", "--name", name, "--network", network, "--network-alias", fqdn,
			"-p", fmt.Sprintf("127.0.0.1:%d:8200", ports[i]),
			"--tmpfs", "/openbao/data:uid=100,gid=1000", "--tmpfs", "/openbao/audit:uid=100,gid=1000",
			"-v", tlsDir+":/openbao/tls:ro", "-v", filepath.Join(dir, "config.hcl")+":/etc/bao/config.hcl:ro",
			"-e", "BAO_STATIC_KEY="+key, "-e", "BAO_RAFT_NODE_ID="+pod,
			"-e", "BAO_API_ADDR=https://"+fqdn+":8200", "-e", "BAO_CLUSTER_ADDR=https://"+fqdn+":8201",
			smokeOpenBaoImage, "server", "-config=/etc/bao/config.hcl").CombinedOutput()
		if err != nil {
			t.Fatalf("docker run %s: %v\n%s", pod, err, out)
		}
	}

	get := func(i int, token, path string) (int, map[string]any) {
		req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("https://127.0.0.1:%d/v1/%s", ports[i], path), nil)
		if token != "" {
			req.Header.Set("X-Vault-Token", token)
		}
		cl := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost"}}}
		resp, err := cl.Do(req)
		if err != nil {
			return 0, nil
		}
		defer func() { _ = resp.Body.Close() }()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	wait := func(what string, d time.Duration, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(d)
		for !ok() {
			if time.Now().After(deadline) {
				var logs []string
				for _, c := range containers {
					b, _ := exec.Command("docker", "logs", "--tail", "15", c).CombinedOutput()
					logs = append(logs, c+":\n"+string(b))
				}
				t.Fatalf("timed out waiting for %s\n%s", what, strings.Join(logs, "\n"))
			}
			time.Sleep(500 * time.Millisecond)
		}
	}

	// All three listen with the same cert, and none is initialised.
	for i := range ports {
		wait(fmt.Sprintf("pod %d to answer over TLS", i), 60*time.Second, func() bool {
			code, _ := get(i, "", "sys/health?uninitcode=200&sealedcode=200")
			return code == 200
		})
	}

	// Initialise pod 0 only, as converge.sh does.
	raw := []byte(`{"recovery_shares":1,"recovery_threshold":1}`)
	req, _ := http.NewRequest(http.MethodPut, fmt.Sprintf("https://127.0.0.1:%d/v1/sys/init", ports[0]), strings.NewReader(string(raw)))
	cl := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost"}}}
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var init struct {
		RootToken string `json:"root_token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&init)
	_ = resp.Body.Close()
	if init.RootToken == "" {
		t.Fatalf("init answered %d", resp.StatusCode)
	}

	// The other two join by retry_join, verify the peer's certificate against
	// the CA, and open themselves with the static key: no operator step.
	for i := 1; i < len(ports); i++ {
		wait(fmt.Sprintf("pod %d to join and unseal", i), 90*time.Second, func() bool {
			code, out := get(i, "", "sys/health?standbyok=true&sealedcode=200&uninitcode=200")
			return code == 200 && out["initialized"] == true && out["sealed"] == false
		})
	}
	wait("three raft voters", 60*time.Second, func() bool {
		code, out := get(0, init.RootToken, "sys/storage/raft/configuration")
		if code != 200 {
			return false
		}
		data, _ := out["data"].(map[string]any)
		cfgm, _ := data["config"].(map[string]any)
		servers, _ := cfgm["servers"].([]any)
		voters := 0
		for _, s := range servers {
			if m, _ := s.(map[string]any); m["voter"] == true {
				voters++
			}
		}
		return voters == 3
	})

	// Exactly one active node; the others are standbys that forward.
	active := 0
	for i := range ports {
		if code, _ := get(i, "", "sys/health"); code == 200 {
			active++
		}
	}
	if active != 1 {
		t.Errorf("%d active nodes, want 1", active)
	}
}
