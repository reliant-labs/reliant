// Copyright (c) 2025 Reliant Labs
package commands

import (
	"fmt"
	"net/url"
	"os"
	"strings"

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

	// Global persistent flags available to all subcommands
	root.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose output")
	root.PersistentFlags().StringVar(&serverURL, "server", envOrDefault("RELIANT_SERVER_URL", "https://reliantapi.com"), "Cloud API server URL")
	root.PersistentFlags().StringVar(&gatewayURL, "gateway", envOrDefault("RELIANT_GATEWAY_URL", ""), "Daemon gateway URL (defaults to gateway subdomain of --server)")

	// Register subcommand groups
	root.AddCommand(newMonolithCmd())
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
	// Without RELIANT_GATEWAY_URL, fall back to the server URL itself
	// (the API server also hosts the ConnectDaemon endpoint in dev).
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
