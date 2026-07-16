// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/daemon"
)

// Defaults for the workspace proxy, matching control-plane's proxy URL
// builders. Overridable per-deploy via env vars so this stays correct if the
// proxy host / base domain ever changes without a reliant redeploy.
const (
	defaultDevProxyHost        = "localhost:28080"
	defaultWorkspaceBaseDomain = "workspaces.reliantapi.com"
)

// isPreviewablePort reports whether a detected listening port can be reached
// through the workspace preview path. Since the in-pod preview forwarder
// (internal/toolexec/daemonruntime/preview_forwarder.go) terminates preview
// traffic INSIDE the workspace network namespace and dials loopback itself,
// loopback-only binds (127.0.0.1, ::1, localhost — the default for vite,
// next dev, python -m http.server, ...) are previewable too. Only
// non-loopback/non-wildcard specific binds (e.g. a single ethernet IP) are
// excluded, plus anything that is not a LISTEN socket.
func isPreviewablePort(p daemon.PortInfo) bool {
	// getProcessPorts already keeps LISTEN sockets only, but be defensive:
	// never surface an outbound/established connection as a preview target.
	if p.State != "" && !strings.EqualFold(p.State, "LISTEN") {
		return false
	}
	switch strings.TrimSpace(p.Address) {
	case "0.0.0.0", "::", "[::]", "*", "":
		// Wildcard binds. "" covers platforms whose port scraping omits an
		// explicit wildcard address.
		return true
	case "127.0.0.1", "::1", "[::1]", "localhost":
		// Loopback binds — reachable via the in-pod preview forwarder.
		return true
	default:
		return false
	}
}

// PreviewURLTemplate returns the env-aware, externally-reachable preview URL for
// a REMOTE daemon with a literal "{port}" placeholder in the port position, or
// "" when there is no proxy to build one for (local daemon or unknown identity).
// Substituting a concrete port yields exactly what proxyPreviewURL produces.
//
// This is the reliant-side reconstruction of the template. The AUTHORITATIVE
// per-daemon template is injected by the control-plane workspace-controller into
// the daemon container as RELIANT_PREVIEW_URL_TEMPLATE (built from the proxy's
// own URL builder — see control-plane/internal/operators/workspace's
// buildWorkspacePod + internal/proxy/urlbuilder.go). Daemon-container code (the
// `reliant preview-url` CLI, `echo $RELIANT_PREVIEW_URL_TEMPLATE`) should prefer
// that env var. This function is the fallback used where that env is NOT
// reachable — most importantly the workflow engine, which runs in the worker
// process (not the daemon container) and so rebuilds the template from the same
// PROXY_HOST / WORKSPACE_BASE_DOMAIN config the proxy is configured with.
//
// It mirrors the URL-builder SELECTION in
// control-plane/internal/app/providers.go so the template can't drift from what
// the gateway actually serves:
//
//	PROXY_HOST set           -> path-based dev builder (DevPathURLBuilder):
//	                            http://<proxyHost>/proxy/<daemonID>/{port}/
//	WORKSPACE_BASE_DOMAIN set -> subdomain builder (SubdomainURLBuilder) with
//	                            optional WORKSPACE_URL_SCHEME (default https) and
//	                            WORKSPACE_URL_PORT (dev's host-mapped HTTP gateway):
//	                            <scheme>://{port}-<daemonID>.<baseDomain>[:<port>]
//
// When neither is configured it falls back by environment: dev/test to the
// loopback proxy path (no wildcard DNS locally), else the default prod
// subdomain. These defaults preserve the historical behavior byte-for-byte.
func PreviewURLTemplate(env config.Environment, daemonID string) string {
	if daemonID == "" {
		return ""
	}
	// PROXY_HOST wins → path-based routing (local k3d without wildcard DNS).
	if host := os.Getenv("PROXY_HOST"); host != "" {
		return fmt.Sprintf("http://%s/proxy/%s/{port}/", host, daemonID)
	}
	base := os.Getenv("WORKSPACE_BASE_DOMAIN")
	if base == "" {
		// Nothing configured: keep the historical env-derived defaults.
		switch env {
		case config.EnvironmentDev, config.EnvironmentTest:
			return fmt.Sprintf("http://%s/proxy/%s/{port}/", defaultDevProxyHost, daemonID)
		default:
			base = defaultWorkspaceBaseDomain
		}
	}
	scheme := os.Getenv("WORKSPACE_URL_SCHEME")
	if scheme == "" {
		scheme = "https"
	}
	portSuffix := ""
	if p := os.Getenv("WORKSPACE_URL_PORT"); p != "" {
		portSuffix = ":" + p
	}
	return fmt.Sprintf("%s://{port}-%s.%s%s", scheme, daemonID, base, portSuffix)
}

// proxyPreviewURL returns the env-aware, externally-reachable URL for a port on
// a REMOTE daemon, or "" when there is no proxy to build one for (local daemon
// or unknown identity) — in which case the caller falls back to the loopback.
// It is PreviewURLTemplate with the {port} placeholder substituted.
func proxyPreviewURL(env config.Environment, daemonID string, port int) string {
	if port <= 0 {
		return ""
	}
	tmpl := PreviewURLTemplate(env, daemonID)
	if tmpl == "" {
		return ""
	}
	return strings.ReplaceAll(tmpl, "{port}", strconv.Itoa(port))
}

// previewURLsForProcess returns, for each previewable listening port of a
// RUNNING process (wildcard OR loopback binds — the in-pod preview forwarder
// reaches both), a line the agent can read and post as the preview link.
// Empty when the process is not running or has no previewable ports.
//
// This is the runtime mechanism by which the launch_preview agent obtains the
// correct proxied URL: after starting the dev server in the background it calls
// bash_list (or bash_output), and these lines appear in the tool result.
func previewURLsForProcess(p *daemon.ProcessInfo) []string {
	if p == nil || p.Status != "running" || len(p.Ports) == 0 {
		return nil
	}
	env := config.GetEnvironment()
	var lines []string
	seen := make(map[int]bool)
	for _, port := range p.Ports {
		if !isPreviewablePort(port) || seen[port.Port] {
			continue
		}
		seen[port.Port] = true
		if url := proxyPreviewURL(env, p.DaemonID, port.Port); url != "" {
			lines = append(lines, fmt.Sprintf("Preview URL (port %d): %s", port.Port, url))
		} else {
			// Local/in-process daemon: the loopback is the user's own machine.
			lines = append(lines, fmt.Sprintf("Preview URL (port %d): http://localhost:%d/", port.Port, port.Port))
		}
	}
	return lines
}
