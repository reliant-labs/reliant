package cli

// telemetry.go — forge's own Events (pkg/observe/cliev): the anonymous
// forge_cli_first_run event a release build sends once per install, and
// `forge telemetry on|off|status`.
//
// On by default with a one-time notice on stderr; off with DO_NOT_TRACK=1,
// FORGE_TELEMETRY=off or `forge telemetry off`, and always on a CI runner
// (cliev.InCI). Only a release build sends anything: the endpoint (the platform's
// keyed ingest, with the forge CLI's native key in Reliant's telemetry org) is
// stamped by scripts/build-release-assets.sh; a dev build has none. An
// embedded forge (`reliant forge`) never calls Execute, so it sends nothing.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/bugreport"
	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/pkg/observe/cliev"
)

// telemetryEndpoint is the OTLP logs URL a release build sends Events to;
// "" (every dev build) sends nothing. cmd/forge sets it from its ldflags.
var telemetryEndpoint string

// SetTelemetryEndpoint sets where forge's Events go (cmd/forge's
// TelemetryEndpoint, stamped at release).
func SetTelemetryEndpoint(endpoint string) { telemetryEndpoint = endpoint }

// telemetryConfig is forge's cliev configuration. Its state file sits beside
// the bug reporter's, whose install id it adopts so a machine has one id.
func telemetryConfig(args []string) cliev.Config {
	dir := forgeStateDir()
	return cliev.Config{
		App:           "forge",
		Version:       buildinfo.Version(),
		Endpoint:      telemetryEndpoint,
		ConfigDir:     dir,
		Quiet:         wantsMachineOutput(args),
		SeedInstallID: func() string { return bugReportInstallID(dir) },
	}
}

// wantsMachineOutput reports a run whose stdout or stderr a program reads
// (--json, --quiet): the notice must not land in it.
func wantsMachineOutput(args []string) bool {
	for _, a := range args {
		switch a {
		case "--json", "--quiet", "-q", "--output=json", "-o=json":
			return true
		}
	}
	return false
}

// bugReportInstallID is the install id the bug reporter minted, "" when it
// never ran.
func bugReportInstallID(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, bugreport.StateFile))
	if err != nil {
		return ""
	}
	var doc struct {
		InstallID string `json:"install_id"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return ""
	}
	return doc.InstallID
}

// startTelemetry sets up this run's Events and records the first-run event
// when this install has not sent it. The returned func sends the batch; call
// it as the process exits.
func startTelemetry(args []string) func() {
	c := cliev.Init(telemetryConfig(args))
	if !c.Enabled() {
		return func() {}
	}
	if c.FirstRun() {
		harness, _ := buildinfo.DetectAgent(os.Getenv)
		c.Track(context.Background(), cliev.FirstRunEvent("forge"), c.FirstRunAttrs(harness)...)
	}
	return func() { c.Flush(context.Background()) }
}

func newTelemetryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "telemetry",
		Short: "Show or change whether forge sends anonymous usage events",
		Long: `forge sends anonymous usage events (the first run of an install: version, OS,
architecture, the coding agent running it) so the Reliant team can see how
forge is used. Events carry a random install id and never code, paths, names
or personal data.

It is off when DO_NOT_TRACK=1 or FORGE_TELEMETRY=off, always on a CI runner
(CI=true, GITHUB_ACTIONS and the like), and after
` + "`forge telemetry off`" + `. Development builds never send anything.`,
	}
	status := func(cmd *cobra.Command, c *cliev.Client) {
		st := c.Status()
		state := "on"
		if !st.Enabled {
			state = "off (" + st.Reason + ")"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "telemetry: %s\n", state)
		if st.InstallID != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "install id: %s\n", st.InstallID)
		}
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Say whether usage events are sent, and why",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			status(cmd, telemetryClient())
			return nil
		},
	})
	// Hidden: install.sh and install.ps1 record where the install came from
	// (FORGE_INSTALL_ATTRIBUTION, an install page's rid and utm_*).
	cmd.AddCommand(&cobra.Command{
		Use:    "attribution <query>",
		Short:  "Record where this install came from (used by the installer)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return telemetryClient().SetInstallAttribution(args[0])
		},
	})
	for _, on := range []bool{true, false} {
		use, short := "off", "Stop sending usage events from this machine"
		if on {
			use, short = "on", "Send anonymous usage events from this machine"
		}
		cmd.AddCommand(&cobra.Command{
			Use:   use,
			Short: short,
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				c := telemetryClient()
				if err := c.SetEnabled(on); err != nil {
					return fmt.Errorf("record the telemetry choice: %w", err)
				}
				status(cmd, c)
				return nil
			},
		})
	}
	return cmdutil.StrictGroup(cmd)
}

// telemetryClient is the run's client: the one Execute started, or a fresh
// one for an embedded forge.
func telemetryClient() *cliev.Client {
	if c := cliev.Default(); c != nil {
		return c
	}
	return cliev.New(telemetryConfig([]string{"--quiet"}))
}
