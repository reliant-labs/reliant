// Copyright (c) 2025 Reliant Labs
package commands

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/reliant-labs/reliant/internal/builddefaults"
	"github.com/spf13/cobra"
)

var (
	// Global persistent flags
	verbose    bool
	serverURL  string
	gatewayURL string
)

// NewRootCmd creates and returns the root Cobra command with all subcommands registered.
// Exported so tools/docgen/cli can walk the command tree for documentation generation.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "reliant",
		Short: "Reliant — AI-powered software engineering platform",
		Long: `Reliant is a multi-workflow, multi-workspace agentic assistant for
software engineering. This CLI provides commands for authentication,
project management, daemon control, workflow operations, and running
Reliant server components.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	defaultServerURL := builddefaults.Value("RELIANT_SERVER_URL", builddefaults.ServerURL, builddefaults.ProductionServerURL)
	defaultGatewayURL := builddefaults.Value("RELIANT_GATEWAY_URL", builddefaults.GatewayURL, "")

	// Global persistent flags available to all subcommands
	root.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose output")
	root.PersistentFlags().StringVar(&serverURL, "server", defaultServerURL, "Cloud API server URL")
	root.PersistentFlags().StringVar(&gatewayURL, "gateway", defaultGatewayURL, "Daemon gateway URL (defaults to gateway subdomain of --server)")

	// Register subcommand groups
	root.AddCommand(newServerCmd())
	root.AddCommand(newDaemonCmd())
	root.AddCommand(newAuthCmd())
	root.AddCommand(newOpenCmd())
	root.AddCommand(newWorkflowCmd())
	root.AddCommand(newVersionCmd())
	root.AddCommand(newForgeCmd())

	return root
}

// Execute runs the root command.
func Execute() error {
	root := NewRootCmd()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	return nil
}

// envOrDefault returns the environment variable value or the default.
func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// envOrDefaultInt returns the environment variable as an int, or the default.
func envOrDefaultInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return defaultVal
}

// resolveGatewayURL returns the gateway URL, deriving it from the server URL if not set.
// e.g. https://staging.reliantapi.com -> https://gateway-staging.reliantapi.com
//
//	https://reliantapi.com -> https://gateway.reliantapi.com
//	https://localhost:3110 -> https://localhost:3110 (localhost is kept as-is)
func resolveGatewayURL() string {
	if gatewayURL != "" {
		return gatewayURL
	}

	// Derive from server URL by adding "gateway" prefix to the host
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return serverURL
	}

	host := parsed.Hostname()
	port := parsed.Port()

	// For localhost/loopback addresses, don't transform the hostname.
	// In local dev the gateway runs on a different port on the same host.
	// Without RELIANT_GATEWAY_URL, fall back to the server URL itself —
	// the caller's connect logic will reach the daemon-gateway via the
	// port the user actually has running.
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return serverURL
	}

	// Count dots to determine if there's a subdomain.
	// "staging.reliantapi.com" has 2 dots -> has subdomain -> gateway-staging.reliantapi.com
	// "reliantapi.com" has 1 dot -> no subdomain -> gateway.reliantapi.com
	dotCount := strings.Count(host, ".")
	if dotCount >= 2 {
		// Has a subdomain: staging.reliantapi.com -> gateway-staging.reliantapi.com
		parts := strings.SplitN(host, ".", 2)
		host = "gateway-" + parts[0] + "." + parts[1]
	} else {
		// No subdomain: reliantapi.com -> gateway.reliantapi.com
		host = "gateway." + host
	}

	if port != "" {
		host = host + ":" + port
	}

	parsed.Host = host
	return parsed.String()
}
