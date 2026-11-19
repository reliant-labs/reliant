// Package cli — `forge doctor` Docker daemon HTTP(S)-proxy check.
//
// When the Docker daemon is configured with an HTTP/HTTPS proxy, ALL
// container egress — including image pulls and registry TLS handshakes —
// is routed through that proxy. A well-behaved forward proxy tunnels the
// registry's CONNECT request straight through and pulls work fine. But a
// TLS-intercepting proxy (Proxyman, Charles, mitmproxy, a corporate MITM
// box) terminates the CONNECT and re-originates TLS with its own cert.
// The container runtime, which has no trust path to that cert, sees the
// pull fail mid-stream — surfacing as ImagePullBackOff, "unexpected EOF",
// or "connection reset by peer" on the image layer. The proxy is invisible
// to the user (Docker Desktop silently inherits the macOS system proxy,
// commonly `http://http.docker.internal:3128`), so the failure is
// notoriously hard to trace back to its cause.
//
// Doctor surfaces the proxy as a WARN, not a fail: a proxy can be
// perfectly legitimate (a transparent corporate egress proxy, a pull-
// through cache). We can't tell from `docker info` alone whether it
// intercepts TLS — only that one is present and that, IF it does, image
// pulls will break in a way that looks like everything except a proxy.
// The message names the symptom and the fix so a user staring at an
// ImagePullBackOff can connect the two.
//
// The decision core (dockerProxyCheck) is pure — it takes the already-
// extracted proxy strings — so unit tests cover every branch without
// shelling out to docker.
package cli

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/doctor"
)

// dockerProxyCheckName is the check's display name in the doctor report.
const dockerProxyCheckName = "Docker daemon proxy"

// dockerProxyCheck is the pure decision core. httpProxy / httpsProxy are
// the values `docker info` reports for the daemon's HTTPProxy / HTTPSProxy
// fields (either may be empty). A non-empty value warns; both empty passes.
func dockerProxyCheck(httpProxy, httpsProxy string) doctor.CheckResult {
	res := doctor.CheckResult{Name: dockerProxyCheckName}

	proxies := dockerProxyValues(httpProxy, httpsProxy)
	if len(proxies) == 0 {
		res.Status = doctor.StatusPass
		res.Message = "no HTTP(S) proxy configured on the Docker daemon"
		return res
	}

	res.Status = doctor.StatusWarn
	res.Message = fmt.Sprintf("Docker daemon is using an HTTP(S) proxy (%s)", strings.Join(proxies, ", "))
	res.Evidence = strings.Join([]string{
		"The Docker daemon routes container egress — including image pulls and",
		"registry TLS — through this proxy. If it intercepts the registry",
		"CONNECT/TLS handshake (Proxyman, Charles, mitmproxy, corporate MITM),",
		"image pulls fail mid-stream and surface as ImagePullBackOff, \"unexpected",
		"EOF\", or \"connection reset by peer\" — with no mention of a proxy.",
		"",
		"If pulls are failing: bypass your registries in the proxy's no-proxy /",
		"exclusion list, or disable the proxy for local cluster work.",
		"A legitimate, well-behaved proxy can be ignored.",
	}, "\n")
	return res
}

// dockerProxyValues normalises the two proxy fields into a deduplicated,
// non-empty list for the warning message. Both fields commonly carry the
// same value (Docker Desktop populates HTTP and HTTPS from one system
// proxy), so we collapse the duplicate rather than printing it twice.
func dockerProxyValues(httpProxy, httpsProxy string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, p := range []string{httpProxy, httpsProxy} {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// runDockerProxyDoctorChecks is the side-effecting wrapper invoked from
// runDoctor. It is one cheap `docker info` call. Like the tool checks it
// is toolchain-shaped (not a telemetry signal), so it runs only when the
// signal filter is empty ("all checks") or "tools".
//
// It is gated on the build feature: a project that doesn't build images
// never pulls through the daemon, so the proxy is irrelevant. Docker not
// being installed/running is a clean skip — the tool checks already
// surface a missing/installed docker binary, and we don't want a second
// noisy line for the same root cause.
func runDockerProxyDoctorChecks(ctx context.Context, cfg *config.ProjectConfig, _, signal string) []doctor.CheckResult {
	// Only the unfiltered pass. `--signal` selects among the values
	// doctor.RunFiltered accepts (metrics/traces/logs/profiles/deploy); it
	// REJECTS anything else before a check runs, so a guard naming its own
	// signal here could never be true and advertised a filter that does not
	// exist.
	if signal != "" {
		return nil
	}
	if cfg == nil || !cfg.Features.BuildEnabled() {
		return nil
	}
	start := time.Now()

	httpProxy, httpsProxy, ok := readDockerProxy(ctx)
	if !ok {
		// docker not installed / daemon not running — skip cleanly.
		return []doctor.CheckResult{{
			Name:     dockerProxyCheckName,
			Status:   doctor.StatusSkip,
			Message:  "could not query the Docker daemon (docker not running?)",
			Duration: time.Since(start),
		}}
	}

	res := dockerProxyCheck(httpProxy, httpsProxy)
	res.Duration = time.Since(start)
	return []doctor.CheckResult{res}
}

// readDockerProxy runs `docker info` and returns the daemon's HTTPProxy
// and HTTPSProxy values. ok is false when the command can't run (docker
// missing, daemon down) so the caller can skip rather than warn. A short
// deadline keeps doctor snappy even when the daemon is wedged.
func readDockerProxy(ctx context.Context) (httpProxy, httpsProxy string, ok bool) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Tab-separate the two fields; a missing field renders empty, which
	// dockerProxyCheck treats as "no proxy" for that channel.
	out, err := exec.CommandContext(ctx, "docker", "info", "--format",
		"{{.HTTPProxy}}\t{{.HTTPSProxy}}").Output()
	if err != nil {
		return "", "", false
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "\t", 2)
	httpProxy = strings.TrimSpace(parts[0])
	if len(parts) > 1 {
		httpsProxy = strings.TrimSpace(parts[1])
	}
	return httpProxy, httpsProxy, true
}
