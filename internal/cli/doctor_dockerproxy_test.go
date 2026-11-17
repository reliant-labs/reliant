package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/doctor"
)

// TestDockerProxyCheck covers every branch of the pure decision core. The
// load-bearing case is "proxy present": a non-empty HTTPProxy/HTTPSProxy
// must WARN (not fail — a proxy can be legitimate) with an actionable
// message that names the ImagePullBackOff symptom and the fix.
func TestDockerProxyCheck(t *testing.T) {
	cases := []struct {
		name       string
		httpProxy  string
		httpsProxy string
		wantStatus doctor.Status
		wantInMsg  string
		wantInEv   string
	}{
		{
			name:       "no proxy: pass",
			wantStatus: doctor.StatusPass,
			wantInMsg:  "no HTTP(S) proxy",
		},
		{
			name:       "whitespace-only proxy: pass",
			httpProxy:  "   ",
			httpsProxy: "\t",
			wantStatus: doctor.StatusPass,
			wantInMsg:  "no HTTP(S) proxy",
		},
		{
			name:       "Docker Desktop system proxy: warn",
			httpProxy:  "http://http.docker.internal:3128",
			httpsProxy: "http://http.docker.internal:3128",
			wantStatus: doctor.StatusWarn,
			wantInMsg:  "http://http.docker.internal:3128",
			wantInEv:   "ImagePullBackOff",
		},
		{
			name:       "https-only proxy: warn",
			httpsProxy: "http://127.0.0.1:9090",
			wantStatus: doctor.StatusWarn,
			wantInMsg:  "http://127.0.0.1:9090",
			wantInEv:   "CONNECT",
		},
		{
			name:       "distinct http and https proxies: both listed",
			httpProxy:  "http://proxy-a:3128",
			httpsProxy: "http://proxy-b:3129",
			wantStatus: doctor.StatusWarn,
			wantInMsg:  "http://proxy-a:3128, http://proxy-b:3129",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := dockerProxyCheck(c.httpProxy, c.httpsProxy)
			if res.Name != dockerProxyCheckName {
				t.Errorf("Name = %q, want %q", res.Name, dockerProxyCheckName)
			}
			if res.Status != c.wantStatus {
				t.Errorf("Status = %q, want %q (message: %s)", res.Status, c.wantStatus, res.Message)
			}
			if c.wantInMsg != "" && !strings.Contains(res.Message, c.wantInMsg) {
				t.Errorf("Message %q missing %q", res.Message, c.wantInMsg)
			}
			if c.wantInEv != "" && !strings.Contains(res.Evidence, c.wantInEv) {
				t.Errorf("Evidence %q missing %q", res.Evidence, c.wantInEv)
			}
		})
	}
}

// TestDockerProxyValuesDedup pins the dedup/collapse behavior the warning
// message relies on: Docker Desktop populates HTTP and HTTPS from one
// system proxy, so the identical value must appear once, not twice.
func TestDockerProxyValuesDedup(t *testing.T) {
	got := dockerProxyValues("http://p:3128", "http://p:3128")
	if len(got) != 1 || got[0] != "http://p:3128" {
		t.Errorf("dockerProxyValues dedup = %v, want one entry", got)
	}
	if got := dockerProxyValues("", ""); got != nil {
		t.Errorf("dockerProxyValues empty = %v, want nil", got)
	}
}
