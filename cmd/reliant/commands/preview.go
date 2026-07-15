// Copyright (c) 2025 Reliant Labs
package commands

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/reliant-labs/reliant/internal/netports"
	"github.com/spf13/cobra"
)

// previewURLTemplateEnv is the env var the control-plane workspace-controller
// injects into the daemon container: the fully-resolved preview URL for THIS
// daemon with a literal "{port}" placeholder, e.g.
//
//	http://{port}-<daemonID>.preview.reliant.test:28080   (dev, subdomain)
//	http://<proxyHost>/proxy/<daemonID>/{port}/            (dev, path-based)
//	https://{port}-<daemonID>.workspaces.reliantapi.com    (prod)
//
// See control-plane/internal/operators/workspace's buildWorkspacePod. The value
// is built from the proxy's own URL builder, so it can't drift from what the
// gateway serves. Absent on a local (non-managed) daemon — then the CLI falls
// back to the loopback, since the port is on the user's own machine.
const previewURLTemplateEnv = "RELIANT_PREVIEW_URL_TEMPLATE"

const previewPortPlaceholder = "{port}"

// previewURLResult is the structured deliverable shape a handoff/terminal node
// (or the agent) can emit so "we built X, run via Y, open it at Z" stays
// consistent and a UI could later render an "Open your app" affordance. The
// CLI fills the URL-derived fields (Port/URL/Listening/AccessLevel); Built and
// RunCommand are supplied by the caller (agent/workflow) via --built/--run.
type previewURLResult struct {
	Built       string `json:"built,omitempty"`
	RunCommand  string `json:"run_command,omitempty"`
	Port        int    `json:"port"`
	URL         string `json:"url"`
	Listening   bool   `json:"listening"`
	AccessLevel string `json:"access_level"`
}

func newPreviewURLCmd() *cobra.Command {
	var (
		jsonOutput       bool
		built            string
		runCommand       string
		requireListening bool
	)

	cmd := &cobra.Command{
		Use:   "preview-url <port>",
		Short: "Print the shareable preview URL for a local port",
		Long: `Deterministically construct the workspace preview URL for a listening port.

It reads the ` + previewURLTemplateEnv + ` template the workspace-controller injects
into the daemon container and substitutes the given port — no round-trip to the
control plane. On a local (non-managed) daemon, where no template is present, it
falls back to the loopback URL for the port.

The port is checked against the daemon's listening sockets; if it is not up yet
the URL is still printed (a warning goes to stderr) unless --require-listening
is set. The URL is openable by the workspace OWNER under the authenticated
default; sharing it beyond the owner requires the port to be made public.

With --json, emits {built, run_command, port, url, listening, access_level} —
the deliverable shape a handoff node can post.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			port, err := strconv.Atoi(strings.TrimSpace(args[0]))
			if err != nil || port <= 0 || port > 65535 {
				return fmt.Errorf("invalid port %q: must be an integer in 1..65535", args[0])
			}

			url, remote := previewURLForPort(port)

			// Listening check is advisory (mirrors netports' "affordance, not a
			// gate" stance). ok=false on non-Linux hosts where /proc is absent —
			// treat as unknown and never gate on it.
			ports, ok := netports.ListeningLoopbackPorts(nil)
			listening := ok && slices.Contains(ports, uint32(port))
			if ok && !listening {
				if requireListening {
					return fmt.Errorf("nothing is listening on port %d yet", port)
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: nothing detected listening on port %d yet (URL printed anyway)\n", port)
			}

			// Access level: a managed (remote) daemon's preview is behind the
			// gateway's authenticated default — the OWNER can open it. A local
			// daemon's loopback is the user's own machine.
			accessLevel := "local"
			if remote {
				accessLevel = "owner"
			}

			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(previewURLResult{
					Built:       built,
					RunCommand:  runCommand,
					Port:        port,
					URL:         url,
					Listening:   listening,
					AccessLevel: accessLevel,
				})
			}

			// Human/agent path: URL on stdout (easy to capture), notes on stderr.
			fmt.Fprintln(cmd.OutOrStdout(), url)
			if remote {
				fmt.Fprintln(cmd.ErrOrStderr(), "access: openable by the workspace owner (authenticated); make the port public to share beyond the owner")
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit the structured deliverable shape {built, run_command, port, url, listening, access_level}")
	cmd.Flags().StringVar(&built, "built", "", "What was built (included in --json output)")
	cmd.Flags().StringVar(&runCommand, "run", "", "The command that runs it (included in --json output)")
	cmd.Flags().BoolVar(&requireListening, "require-listening", false, "Fail if nothing is listening on the port")

	return cmd
}

// previewURLForPort returns the preview URL for a port and whether it is a
// REMOTE (managed-daemon) URL. When the injected template is present and valid
// it is authoritative; otherwise the port is on the local machine (loopback).
func previewURLForPort(port int) (url string, remote bool) {
	tmpl := envOrDefault(previewURLTemplateEnv, "")
	if tmpl != "" && strings.Contains(tmpl, previewPortPlaceholder) {
		return strings.ReplaceAll(tmpl, previewPortPlaceholder, strconv.Itoa(port)), true
	}
	return fmt.Sprintf("http://localhost:%d/", port), false
}
