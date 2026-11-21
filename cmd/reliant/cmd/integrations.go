// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

// `reliant integrations` shows the connections — the accounts at GitHub,
// Slack, Gmail and the rest — that integration steps and integration
// triggers act through (ConnectionService). It READS only: connecting an
// account is an OAuth consent in a browser, made in the app (Settings →
// Integrations), and an automation names the connection it listens through
// with `trigger activate --connection integration=<id>`, so the id this
// prints is what that flag takes.

// integrationsSettingsPlace is where the app connects and manages accounts.
const integrationsSettingsPlace = "Settings → Integrations"

func newConnectionServiceClient(conn *connection) reliantv1connect.ConnectionServiceClient {
	return reliantv1connect.NewConnectionServiceClient(conn.httpClient(), conn.ServerURL)
}

func newIntegrationsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "integrations",
		Short: "Show the integration connections your steps and automations act through",
		Long: `Shows your integration connections: the accounts at GitHub, Slack, Gmail and
the rest that integration steps and integration triggers act through.

Connecting or removing an account is done in the Reliant app (web or desktop):
` + integrationsSettingsPlace + `. An automation names the connection it listens
through with ` + "`reliant trigger activate --connection <integration>=<id>`" + `, and
` + "`reliant trigger get`" + ` shows the one it uses.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newIntegrationsListCmd())
	return cmd
}

// connectionRow is one connection as `integrations list` shows it; its JSON
// is the --json shape.
type connectionRow struct {
	ID          string `json:"id"`
	Integration string `json:"integration"`
	Name        string `json:"name"`
	// Account is the provider-side identity the connection acts as
	// ("octocat"), empty when the integration does not say.
	Account string `json:"account,omitempty"`
	// Status is active, needs_reauth or revoked.
	Status       string `json:"status"`
	StatusReason string `json:"status_reason,omitempty"`
	Default      bool   `json:"default"`
	Owner        string `json:"owner"` // you, or org
	OrgID        string `json:"org_id,omitempty"`
	LastUsedAt   string `json:"last_used_at,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
	AuthKind     string `json:"auth_kind,omitempty"`
}

func newIntegrationsListCmd() *cobra.Command {
	var (
		integration string
		orgID       string
		jsonOut     bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List your integration connections",
		Long: `Lists your integration connections: the integration, the account each acts as,
its status, whether it is that integration's default, and its id.

The default connection is the one an integration trigger listens through when
its activation names none. With --org it also lists that organization's own
connections (its Slack bot, its API keys), which its members may use by id.`,
		Example: `  reliant integrations list
  reliant integrations list --integration github
  reliant integrations list --org org_123 --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}
			req := &reliantv1.ListConnectionsRequest{}
			if v := strings.TrimSpace(integration); v != "" {
				req.IntegrationId = &v
			}
			if v := strings.TrimSpace(orgID); v != "" {
				req.OrgId = &v
			}
			resp, err := newConnectionServiceClient(conn).ListConnections(cmd.Context(), connect.NewRequest(req))
			if err != nil {
				return conn.annotate(fmt.Errorf("listing integration connections: %w", err))
			}
			rows := make([]connectionRow, 0, len(resp.Msg.GetConnections()))
			for _, c := range resp.Msg.GetConnections() {
				rows = append(rows, connectionRowOf(c))
			}
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(rows)
			}
			writeConnectionTable(cmd.OutOrStdout(), rows)
			return nil
		},
	}
	cmd.Flags().StringVar(&integration, "integration", "", "Only this integration's connections (e.g. github)")
	cmd.Flags().StringVar(&orgID, "org", "", "Also list this organization's own connections")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print JSON")
	return cmd
}

func connectionRowOf(c *reliantv1.Connection) connectionRow {
	row := connectionRow{
		ID:           c.GetId(),
		Integration:  c.GetIntegrationId(),
		Name:         c.GetName(),
		Account:      c.GetAccountLabel(),
		Status:       connectionStatusLabel(c.GetStatus()),
		StatusReason: c.GetStatusReason(),
		Default:      c.GetIsDefault(),
		Owner:        "you",
		LastUsedAt:   c.GetLastUsedAt(),
		CreatedAt:    c.GetCreatedAt(),
		AuthKind:     enumWord(c.GetAuthKind().String(), "CONNECTION_AUTH_KIND_"),
	}
	if c.GetOwnerKind() == reliantv1.ConnectionOwnerKind_CONNECTION_OWNER_KIND_ORG {
		row.Owner, row.OrgID = "org", c.GetOrgId()
	}
	return row
}

// connectionStatusLabel is ConnectionStatus in lower-case words; an older
// server's UNSPECIFIED reads as active, which is what it stored.
func connectionStatusLabel(s reliantv1.ConnectionStatus) string {
	if s == reliantv1.ConnectionStatus_CONNECTION_STATUS_UNSPECIFIED {
		return "active"
	}
	return enumWord(s.String(), "CONNECTION_STATUS_")
}

// enumWord is a proto enum name without its prefix, in lower case;
// "" for the UNSPECIFIED value.
func enumWord(name, prefix string) string {
	word := strings.ToLower(strings.TrimPrefix(name, prefix))
	if word == "unspecified" {
		return ""
	}
	return word
}

func writeConnectionTable(out io.Writer, rows []connectionRow) {
	if len(rows) == 0 {
		fmt.Fprintf(out, "No integration connections. Connect an account in the app: %s.\n", integrationsSettingsPlace)
		return
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "INTEGRATION\tACCOUNT\tNAME\tSTATUS\tDEFAULT\tOWNER\tID")
	for _, r := range rows {
		status := r.Status
		if r.StatusReason != "" {
			status += " (" + r.StatusReason + ")"
		}
		owner := r.Owner
		if r.OrgID != "" {
			owner = "org " + r.OrgID
		}
		def := "-"
		if r.Default {
			def = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Integration, orDash(r.Account), orDash(r.Name), status, def, owner, r.ID)
	}
	_ = w.Flush()
}
