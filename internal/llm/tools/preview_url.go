// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"os"
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

// isPubliclyBoundPort reports whether a detected listening port is reachable
// from outside the daemon's loopback — i.e. proxyable for preview. Only such
// ports get a preview URL surfaced to the agent; a 127.0.0.1-only dev server is
// NOT previewable through the proxy, so the agent must rebind it to 0.0.0.0.
func isPubliclyBoundPort(p daemon.PortInfo) bool {
	// getProcessPorts already keeps LISTEN sockets only, but be defensive:
	// never surface an outbound/established connection as a preview target.
	if p.State != "" && !strings.EqualFold(p.State, "LISTEN") {
		return false
	}
	switch strings.TrimSpace(p.Address) {
	case "0.0.0.0", "::", "[::]", "*", "":
		// Wildcard binds are reachable via the proxy. "" covers platforms whose
		// port scraping omits an explicit wildcard address.
		return true
	default:
		return false
	}
}

// proxyPreviewURL returns the env-aware, externally-reachable URL for a port on
// a REMOTE daemon, or "" when there is no proxy to build one for (local daemon
// or unknown identity) — in which case the caller falls back to the loopback.
//
// Formats mirror control-plane/internal/proxy/urlbuilder.go:
//
//	dev/test: http://<proxyHost>/proxy/<daemonID>/<port>/   (path-based; local wildcard DNS absent)
//	other:    https://<port>-<daemonID>.<baseDomain>        (subdomain-based)
func proxyPreviewURL(env config.Environment, daemonID string, port int) string {
	if daemonID == "" || port <= 0 {
		return ""
	}
	switch env {
	case config.EnvironmentDev, config.EnvironmentTest:
		host := os.Getenv("RELIANT_PROXY_HOST")
		if host == "" {
			host = defaultDevProxyHost
		}
		return fmt.Sprintf("http://%s/proxy/%s/%d/", host, daemonID, port)
	default:
		base := os.Getenv("WORKSPACE_BASE_DOMAIN")
		if base == "" {
			base = defaultWorkspaceBaseDomain
		}
		return fmt.Sprintf("https://%d-%s.%s", port, daemonID, base)
	}
}

// previewURLsForProcess returns, for each publicly-bound listening port of a
// RUNNING process, a line the agent can read and post as the preview link.
// Empty when the process is not running or exposes only loopback ports.
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
		if !isPubliclyBoundPort(port) || seen[port.Port] {
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
