package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// kubectlUntrustedCertificate is what kubectl printed for forge bug report
// 523c6f47: after a Docker Desktop restart reshuffled the k3d network's IPs,
// k3d-control-plane-v2-serverlb kept forwarding 6443 to an address that now
// belonged to another cluster's server.
const kubectlUntrustedCertificate = "Unable to connect to the server: tls: failed to verify certificate: x509: certificate signed by unknown authority"

// kubectlProxyUntrustedCertificate and kubectlWebhookUntrustedCertificate
// carry the same x509 verdict for TLS failures that say nothing about which
// server the API port reaches: kubectl could not verify its HTTPS proxy, or
// the API server relayed one of its own clients' failures.
const (
	kubectlProxyUntrustedCertificate   = "Unable to connect to the server: proxyconnect tcp: tls: failed to verify certificate: x509: certificate signed by unknown authority"
	kubectlWebhookUntrustedCertificate = `Error from server (InternalError): Internal error occurred: failed calling webhook "validate.example.io": failed to call webhook: Post "https://webhook.default.svc:443/validate": tls: failed to verify certificate: x509: certificate signed by unknown authority`
)

type nodeReadyAttempt struct {
	out string
	err error
}

var (
	attemptReady    = nodeReadyAttempt{out: "node/k3d-control-plane-v2-server-0 condition met"}
	attemptRefused  = nodeReadyAttempt{out: "The connection to the server 0.0.0.0:6445 was refused - did you specify the right host or port?", err: errors.New("exit status 1")}
	attemptEOF      = nodeReadyAttempt{out: "Unable to connect to the server: EOF", err: errors.New("exit status 1")}
	attemptUntrusts = nodeReadyAttempt{out: kubectlUntrustedCertificate, err: errors.New("exit status 1")}
)

// stubNodeReadyAttempts replays attempts in order (the last one repeats) and
// records each as "attempt" in events. The wait budget is long enough that a
// case which waits it out is unmistakable, and the retry interval short
// enough that a case which retries a few times is instant.
func stubNodeReadyAttempts(t *testing.T, events *[]string, attempts ...nodeReadyAttempt) {
	t.Helper()
	origAttempt := nodeReadyAttemptFn
	origBudget := nodeReadyWaitBudget
	origInterval := nodeReadyRetryInterval
	t.Cleanup(func() {
		nodeReadyAttemptFn = origAttempt
		nodeReadyWaitBudget = origBudget
		nodeReadyRetryInterval = origInterval
	})
	nodeReadyWaitBudget = 3 * time.Second
	nodeReadyRetryInterval = time.Millisecond
	n := 0
	nodeReadyAttemptFn = func(context.Context, string, time.Duration) (string, error) {
		*events = append(*events, "attempt")
		if len(attempts) == 0 {
			return "", errors.New("no readiness attempt was expected")
		}
		a := attempts[min(n, len(attempts)-1)]
		n++
		return a.out, a.err
	}
}

// firstEvents keeps a failure message readable when a case that should
// settle at once instead retried for the whole budget.
func firstEvents(events []string) string {
	if len(events) > 6 {
		return strings.Join(events[:6], ", ") + fmt.Sprintf(", ... (%d in all)", len(events))
	}
	return strings.Join(events, ", ")
}

// TestEnsureRunningK3dClusterHealthyUntrustedCertificate pins forge bug report
// 523c6f47. An API port answering with a certificate the kubeconfig does not
// trust is not a slow start, and forge used to wait 90s on it before
// reporting "nodes not Ready" — pointing the operator at the nodes, which
// were fine. It must instead refresh THIS cluster's load balancer once, and
// if that does not help, fail at once naming the remaining cause. Genuine
// not-yet answers keep the retry path.
func TestEnsureRunningK3dClusterHealthyUntrustedCertificate(t *testing.T) {
	cases := []struct {
		name       string
		probe      string
		attempts   []nodeReadyAttempt
		refreshErr error
		wantEvents []string
		wantErr    []string // substrings; nil means success
		wantOut    []string
		notOut     []string
	}{
		{
			name:       "untrusted certificate refreshes the load balancer before any wait",
			probe:      kubectlUntrustedCertificate,
			attempts:   []nodeReadyAttempt{attemptReady},
			wantEvents: []string{"refresh k3d-control-plane-v2-serverlb", "attempt"},
			wantOut: []string{
				"certificate its kubeconfig does not trust",
				"k3d-control-plane-v2-serverlb is most likely forwarding to a server IP Docker has since given another cluster",
			},
			notOut: []string{"not-yet"},
		},
		{
			name:       "untrusted certificate surviving the refresh fails fast naming the kubeconfig",
			probe:      kubectlUntrustedCertificate,
			attempts:   []nodeReadyAttempt{attemptUntrusts},
			wantEvents: []string{"refresh k3d-control-plane-v2-serverlb", "attempt"},
			wantErr: []string{
				"answered with a certificate the kubeconfig does not trust",
				"even after k3d-control-plane-v2-serverlb was restarted",
				"k3d kubeconfig merge control-plane-v2 --kubeconfig-merge-default --kubeconfig-switch-context=false",
			},
		},
		{
			name:       "load balancer refresh failure still names both remedies",
			probe:      kubectlUntrustedCertificate,
			refreshErr: errors.New("docker: daemon unreachable"),
			wantEvents: []string{"refresh k3d-control-plane-v2-serverlb"},
			wantErr: []string{
				"docker: daemon unreachable",
				"docker restart k3d-control-plane-v2-serverlb",
				"k3d kubeconfig merge control-plane-v2",
			},
		},
		{
			name:       "untrusted certificate seen during the wait is healed the same way",
			probe:      "Unable to connect to the server: EOF",
			attempts:   []nodeReadyAttempt{attemptEOF, attemptUntrusts, attemptReady},
			wantEvents: []string{"attempt", "attempt", "refresh k3d-control-plane-v2-serverlb", "attempt"},
			wantOut:    []string{"not-yet", "k3d-control-plane-v2-serverlb is most likely forwarding"},
		},
		{
			name:       "an HTTPS proxy's untrusted certificate never restarts the load balancer",
			probe:      kubectlProxyUntrustedCertificate,
			attempts:   []nodeReadyAttempt{{out: kubectlProxyUntrustedCertificate, err: errors.New("exit status 1")}, attemptReady},
			wantEvents: []string{"attempt", "attempt"},
			wantOut:    []string{"not-yet"},
			notOut:     []string{"serverlb"},
		},
		{
			name:       "a webhook's untrusted certificate relayed by the API server never restarts the load balancer",
			probe:      kubectlWebhookUntrustedCertificate,
			attempts:   []nodeReadyAttempt{{out: kubectlWebhookUntrustedCertificate, err: errors.New("exit status 1")}, attemptReady},
			wantEvents: []string{"attempt", "attempt"},
			wantOut:    []string{"not-yet"},
			notOut:     []string{"serverlb"},
		},
		{
			name:       "connection refused is still a not-yet that retries until Ready",
			probe:      attemptRefused.out,
			attempts:   []nodeReadyAttempt{attemptRefused, attemptRefused, attemptReady},
			wantEvents: []string{"attempt", "attempt", "attempt"},
			wantOut:    []string{"not-yet"},
			notOut:     []string{"serverlb"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origReady := runningClusterNodesReadyFn
			origProcess := containerHasK3sProcessFn
			origRefresh := refreshLoadBalancerFn
			t.Cleanup(func() {
				runningClusterNodesReadyFn = origReady
				containerHasK3sProcessFn = origProcess
				refreshLoadBalancerFn = origRefresh
			})
			var events []string
			runningClusterNodesReadyFn = func(context.Context, string) (bool, error) {
				return false, errors.New("exit status 1\n  " + tc.probe)
			}
			containerHasK3sProcessFn = func(context.Context, string) (bool, error) { return true, nil }
			refreshLoadBalancerFn = func(_ context.Context, name string) error {
				events = append(events, "refresh k3d-"+name+"-serverlb")
				return tc.refreshErr
			}
			stubNodeReadyAttempts(t, &events, tc.attempts...)

			var err error
			out := captureStdout(t, func() {
				err = ensureRunningK3dClusterHealthy(t.Context(), ClusterEntity{Name: "control-plane-v2", Servers: 1})
			})
			if got, want := firstEvents(events), firstEvents(tc.wantEvents); got != want {
				t.Errorf("events = [%s]; want [%s]", got, want)
			}
			if tc.wantErr == nil && err != nil {
				t.Errorf("ensureRunningK3dClusterHealthy: %v", err)
			}
			if tc.wantErr != nil && err == nil {
				t.Errorf("ensureRunningK3dClusterHealthy succeeded; want an error containing %q", tc.wantErr)
			}
			for _, want := range tc.wantErr {
				if err != nil && !strings.Contains(err.Error(), want) {
					t.Errorf("error does not mention %q:\n%v", want, err)
				}
			}
			for _, want := range tc.wantOut {
				if !strings.Contains(out, want) {
					t.Errorf("output does not mention %q:\n%s", want, out)
				}
			}
			for _, unwanted := range tc.notOut {
				if strings.Contains(out, unwanted) {
					t.Errorf("output mentions %q:\n%s", unwanted, out)
				}
			}
		})
	}
}

// TestWaitNodeReadyUntrustedCertificateFailsFast covers every other caller of
// waitNodeReady — a cluster start, a create, a node restart: they get the
// diagnosis and both remedies after one attempt instead of a 90s timeout that
// reports the nodes as the problem.
func TestWaitNodeReadyUntrustedCertificateFailsFast(t *testing.T) {
	cases := []struct {
		name         string
		attempts     []nodeReadyAttempt
		wantAttempts int
		wantErr      []string
	}{
		{
			name:         "untrusted certificate ends the wait after one attempt",
			attempts:     []nodeReadyAttempt{attemptUntrusts, attemptReady},
			wantAttempts: 1,
			wantErr: []string{
				"answered with a certificate the kubeconfig does not trust (context k3d-cp)",
				"x509: certificate signed by unknown authority",
				"docker restart k3d-cp-serverlb",
				"k3d kubeconfig merge cp --kubeconfig-merge-default",
			},
		},
		{
			name:         "refused and EOF keep retrying until Ready",
			attempts:     []nodeReadyAttempt{attemptRefused, attemptEOF, attemptReady},
			wantAttempts: 3,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var events []string
			stubNodeReadyAttempts(t, &events, tc.attempts...)
			var err error
			_ = captureStdout(t, func() { err = waitNodeReady(t.Context(), "cp") })
			if len(events) != tc.wantAttempts {
				t.Errorf("attempts = %d; want %d", len(events), tc.wantAttempts)
			}
			if tc.wantErr == nil && err != nil {
				t.Errorf("waitNodeReady: %v", err)
			}
			if tc.wantErr != nil && err == nil {
				t.Errorf("waitNodeReady succeeded; want an error containing %q", tc.wantErr)
			}
			for _, want := range tc.wantErr {
				if err != nil && !strings.Contains(err.Error(), want) {
					t.Errorf("error does not mention %q:\n%v", want, err)
				}
			}
		})
	}
}

// TestServerCertificateUntrusted pins which kubectl outputs count as the API
// server's own certificate being refused — the only case a load balancer
// restart can cure.
func TestServerCertificateUntrusted(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want bool
	}{
		{"kubectl refused the server certificate", kubectlUntrustedCertificate, true},
		{"pre-Go 1.20 wording", "Unable to connect to the server: x509: certificate signed by unknown authority", true},
		{"discovery request refused the server certificate",
			`E1104 10:00:00.000000 1 memcache.go:265] couldn't get current server API group list: Get "https://0.0.0.0:6445/api?timeout=32s": tls: failed to verify certificate: x509: certificate signed by unknown authority`, true},
		{"https proxy", kubectlProxyUntrustedCertificate, false},
		{"webhook relayed by the API server", kubectlWebhookUntrustedCertificate, false},
		{"client certificate rejected by the server", "Unable to connect to the server: remote error: tls: bad certificate", false},
		{"connection refused", attemptRefused.out, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := serverCertificateUntrusted("exit status 1\n  " + tc.out); got != tc.want {
				t.Errorf("serverCertificateUntrusted(%q) = %v; want %v", tc.out, got, tc.want)
			}
		})
	}
}
