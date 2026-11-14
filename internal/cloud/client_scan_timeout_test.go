package cloud

import (
	"net/http"
	"testing"
	"time"
)

// The control plane content-scans uploads inside RecordBundle, Promote,
// CreateDeployment and UpdateDeployment within a 10m budget. A claimable
// deploy's RecordBundle exceeded 15s in prod on 2026-10-29. The default 30s
// client timeout would abandon such a scan the server is still running.
func TestHTTPClientFor_ContentScannedProceduresOutlastTheScanBudget(t *testing.T) {
	transport := &http.Transport{}
	c := &Client{HTTP: &http.Client{Timeout: 30 * time.Second, Transport: transport}}
	for _, proc := range []string{
		"controlplane.v1.DeployService/RecordBundle",
		"/controlplane.v1.DeployService/Promote",
		"controlplane.v1.DeployService/CreateDeployment",
		"controlplane.v1.DeployService/UpdateDeployment",
	} {
		got := c.httpClientFor(proc)
		if got.Timeout != ContentScannedTimeout || ContentScannedTimeout <= 11*time.Minute {
			t.Errorf("%s: timeout %s, want ContentScannedTimeout (%s), longer than the gateway's 11m", proc, got.Timeout, ContentScannedTimeout)
		}
		if got.Transport != transport {
			t.Errorf("%s: the configured transport was dropped", proc)
		}
	}
	if got := c.httpClientFor("controlplane.v1.DeployService/ListReleases"); got != c.HTTP {
		t.Errorf("an ordinary procedure got a different client (timeout %s); only scanned uploads need longer", got.Timeout)
	}
	unbounded := &Client{HTTP: &http.Client{}}
	if got := unbounded.httpClientFor("controlplane.v1.DeployService/RecordBundle"); got.Timeout != 0 {
		t.Errorf("a client with no timeout gained one (%s)", got.Timeout)
	}
	if c.HTTP.Timeout != 30*time.Second {
		t.Errorf("the shared client was mutated: timeout %s", c.HTTP.Timeout)
	}
}
