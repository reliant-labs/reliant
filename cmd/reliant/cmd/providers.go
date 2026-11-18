// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/spf13/cobra"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/orgfunding"
)

// `reliant providers` shows the AI accounts a person's chats run on, and who
// pays for each (control-plane docs/design/org-provider-accounts.md §7).
//
// It READS only. Everything §7 lists beside `list` — choosing whose accounts an
// org's chats use first (`prefer`), and an admin adding, rotating, pausing,
// granting or removing an org's API keys and team workspaces (`org …`) — is a
// person's decision: the server takes it only from a person signed in to the
// app (auth.RequirePerson, reason person_required / human_only), and the CLI
// login is an access token every agent shell inherits. A command the server
// would refuse every time is not offered; `list` says where each of those
// choices is made instead.

// providersSettingsPlace and orgAccountsSettingsPlace are where the app makes
// the choices this CLI does not.
const (
	providersSettingsPlace   = "Settings → AI providers"
	orgAccountsSettingsPlace = "Settings → Organization → AI accounts"
)

// providersClient is the slice of the ProviderAccountService, the
// OrgProviderAccountService and the SettingsService `providers list` reads.
type providersClient interface {
	ListProviderAccounts(context.Context, *connect.Request[reliantv1.ListProviderAccountsRequest]) (*connect.Response[reliantv1.ListProviderAccountsResponse], error)
	ListOrgProviderAccounts(context.Context, *connect.Request[reliantv1.ListOrgProviderAccountsRequest]) (*connect.Response[reliantv1.ListOrgProviderAccountsResponse], error)
	GetSetting(context.Context, *connect.Request[reliantv1.GetSettingRequest]) (*connect.Response[reliantv1.GetSettingResponse], error)
}

type providersRPC struct {
	reliantv1connect.ProviderAccountServiceClient
	org      reliantv1connect.OrgProviderAccountServiceClient
	settings reliantv1connect.SettingsServiceClient
}

func (c providersRPC) ListOrgProviderAccounts(ctx context.Context, req *connect.Request[reliantv1.ListOrgProviderAccountsRequest]) (*connect.Response[reliantv1.ListOrgProviderAccountsResponse], error) {
	return c.org.ListOrgProviderAccounts(ctx, req)
}

func (c providersRPC) GetSetting(ctx context.Context, req *connect.Request[reliantv1.GetSettingRequest]) (*connect.Response[reliantv1.GetSettingResponse], error) {
	return c.settings.GetSetting(ctx, req)
}

func newProvidersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "providers",
		Short: "Show the AI accounts your chats run on, and who pays for each",
		Long: `Shows the AI accounts your chats run on: your own subscriptions and API keys,
the seats an organization pays for, and, in an organization, its own API keys.

Choosing whose accounts an organization's chats use first, and managing an
organization's keys and team workspaces, is done in the Reliant app (web or
desktop): ` + providersSettingsPlace + ` and ` + orgAccountsSettingsPlace + `. The
server takes those only from a person signed in to the app, never from the CLI
login, which any agent's shell can use.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newProvidersListCmd())
	return cmd
}

func newProvidersListCmd() *cobra.Command {
	var (
		orgID   string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List your AI accounts and who pays for each",
		Long: `Lists your AI accounts, grouped by who pays: your own, and each organization
that pays for one of your sign-ins (a team seat, or its Reliant credits).

With --org, it also shows your choices for that organization (whose accounts
its chats use first, and what happens when its accounts can't serve a chat) and
the organization's own API keys you may use. The organization's keys are listed
only to a person signed in to the app: from the CLI it says where to see them.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}
			client := providersRPC{
				ProviderAccountServiceClient: reliantv1connect.NewProviderAccountServiceClient(conn.httpClient(), conn.ServerURL),
				org:                          reliantv1connect.NewOrgProviderAccountServiceClient(conn.httpClient(), conn.ServerURL),
				settings:                     reliantv1connect.NewSettingsServiceClient(conn.httpClient(), conn.ServerURL),
			}
			view, err := loadProvidersView(cmd.Context(), client, strings.TrimSpace(orgID))
			if err != nil {
				return conn.annotate(err)
			}
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(view)
			}
			writeProvidersView(cmd.OutOrStdout(), view)
			return nil
		},
	}
	cmd.Flags().StringVar(&orgID, "org", "", "An organization's id: also show your choices for it and its own API keys")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print JSON")
	return cmd
}

// providerAccountRow is one account as `providers list` shows it.
type providerAccountRow struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Label    string `json:"label"`
	Plan     string `json:"plan,omitempty"`
	Status   string `json:"status"`
	// Funding is personal, org_key, org_seat or org_wallet.
	Funding string `json:"funding"`
	OrgID   string `json:"org_id,omitempty"`
}

// providersOrgView is what --org adds: the person's choices for the org, and
// the org's own keys (or why they are not listed).
type providersOrgView struct {
	OrgID      string               `json:"org_id"`
	Order      string               `json:"order"`
	OnOrgLimit string               `json:"on_org_limit"`
	Keys       []providerAccountRow `json:"keys"`
	// KeysHidden says why the org's keys are not listed ("" when they are).
	KeysHidden string `json:"keys_hidden,omitempty"`
}

type providersView struct {
	Accounts []providerAccountRow `json:"accounts"`
	Org      *providersOrgView    `json:"org,omitempty"`
}

func loadProvidersView(ctx context.Context, c providersClient, orgID string) (providersView, error) {
	resp, err := c.ListProviderAccounts(ctx, connect.NewRequest(&reliantv1.ListProviderAccountsRequest{}))
	if err != nil {
		return providersView{}, fmt.Errorf("listing your AI accounts: %w", err)
	}
	view := providersView{Accounts: make([]providerAccountRow, 0, len(resp.Msg.GetAccounts()))}
	for _, a := range resp.Msg.GetAccounts() {
		view.Accounts = append(view.Accounts, accountRow(a))
	}
	if orgID == "" {
		return view, nil
	}

	org := &providersOrgView{OrgID: orgID, Keys: []providerAccountRow{}}
	pref := orgfunding.Default()
	if s, err := c.GetSetting(ctx, connect.NewRequest(&reliantv1.GetSettingRequest{Key: orgfunding.SettingKey})); err == nil {
		pref = orgfunding.Parse(s.Msg.GetSetting().GetValue()).For(orgID)
	} else if connect.CodeOf(err) != connect.CodeNotFound {
		return providersView{}, fmt.Errorf("reading your choices for the organization: %w", err)
	}
	org.Order, org.OnOrgLimit = string(pref.Order), string(pref.OnOrgLimit)

	keys, err := c.ListOrgProviderAccounts(ctx, connect.NewRequest(&reliantv1.ListOrgProviderAccountsRequest{OrgId: orgID}))
	switch {
	case err == nil:
		for _, k := range keys.Msg.GetAccounts() {
			if row := accountRow(k.GetAccount()); row.ID != "" {
				org.Keys = append(org.Keys, row)
			}
		}
	case isPersonOnlyRefusal(err):
		org.KeysHidden = "person_required"
	case connect.CodeOf(err) == connect.CodePermissionDenied || connect.CodeOf(err) == connect.CodeNotFound:
		org.KeysHidden = "not_member"
	default:
		return providersView{}, fmt.Errorf("listing the organization's AI accounts: %w", err)
	}
	view.Org = org
	return view, nil
}

// isPersonOnlyRefusal reports a refusal of an act only a person signed in to
// the app may do: the CLI login is software.
func isPersonOnlyRefusal(err error) bool {
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodePermissionDenied {
		return false
	}
	switch ce.Meta().Get(svcerr.ReasonHeader) {
	case "person_required", "human_only":
		return true
	}
	return false
}

func accountRow(a *reliantv1.ProviderAccount) providerAccountRow {
	if a == nil {
		return providerAccountRow{}
	}
	row := providerAccountRow{
		ID: a.GetId(), Provider: a.GetProvider(), Label: a.GetLabel(), Plan: a.GetPlanDisplay(),
		Status: accountStatusWord(a), Funding: "personal", OrgID: a.GetFundingOrgId(),
	}
	switch a.GetFunding() {
	case reliantv1.ProviderAccountFunding_PROVIDER_ACCOUNT_FUNDING_ORG_KEY:
		row.Funding = "org_key"
	case reliantv1.ProviderAccountFunding_PROVIDER_ACCOUNT_FUNDING_ORG_SEAT:
		row.Funding = "org_seat"
	case reliantv1.ProviderAccountFunding_PROVIDER_ACCOUNT_FUNDING_ORG_WALLET:
		row.Funding = "org_wallet"
	}
	return row
}

func accountStatusWord(a *reliantv1.ProviderAccount) string {
	switch a.GetStatus() {
	case reliantv1.ProviderAccountStatus_PROVIDER_ACCOUNT_STATUS_LIMITED:
		if until := a.GetLimitedUntil(); until != "" {
			return "limited until " + until
		}
		return "limited"
	case reliantv1.ProviderAccountStatus_PROVIDER_ACCOUNT_STATUS_PAUSED:
		return "paused"
	case reliantv1.ProviderAccountStatus_PROVIDER_ACCOUNT_STATUS_NEEDS_SIGN_IN:
		return "needs sign-in"
	}
	return "available"
}

// orderWords and onOrgLimitWords are the choices as the app words them.
var (
	orderWords = map[string]string{
		string(orgfunding.OrderOrgFirst):      "the organization's accounts first",
		string(orgfunding.OrderPersonalFirst): "your own accounts first",
		string(orgfunding.OrderOrgOnly):       "only the organization's accounts",
	}
	onOrgLimitWords = map[string]string{
		string(orgfunding.OnOrgLimitAsk):         "ask you",
		string(orgfunding.OnOrgLimitUsePersonal): "use your own accounts",
		string(orgfunding.OnOrgLimitWait):        "wait",
	}
)

func writeProvidersView(w io.Writer, v providersView) {
	groups := map[string][]providerAccountRow{}
	var orgs []string
	for _, a := range v.Accounts {
		key := ""
		if a.Funding != "personal" {
			key = a.OrgID
		}
		if _, seen := groups[key]; !seen && key != "" {
			orgs = append(orgs, key)
		}
		groups[key] = append(groups[key], a)
	}
	sort.Strings(orgs)

	writeGroup := func(title string, rows []providerAccountRow) {
		fmt.Fprintln(w, title)
		if len(rows) == 0 {
			fmt.Fprintln(w, "  (none)")
			return
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, r := range rows {
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", r.Provider, r.Label, fundingWord(r), r.Status)
		}
		_ = tw.Flush()
	}

	writeGroup("Yours", groups[""])
	for _, org := range orgs {
		fmt.Fprintln(w)
		writeGroup("Paid by organization "+org, groups[org])
	}

	if o := v.Org; o != nil {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "In organization %s\n", o.OrgID)
		fmt.Fprintf(w, "  Use:  %s\n", orderWords[o.Order])
		if o.Order == string(orgfunding.OrderOrgOnly) {
			fmt.Fprintln(w, "  When its accounts can't serve a chat:  wait (your own accounts never serve its chats)")
		} else {
			fmt.Fprintf(w, "  When its accounts can't serve a chat:  %s\n", onOrgLimitWords[o.OnOrgLimit])
		}
		fmt.Fprintf(w, "  Change these in the Reliant app: %s. Only a person signed in to the app can.\n", providersSettingsPlace)
		fmt.Fprintln(w)
		switch o.KeysHidden {
		case "person_required":
			fmt.Fprintf(w, "Its own API keys are listed only to a person signed in to the app: see %s.\n", orgAccountsSettingsPlace)
		case "not_member":
			fmt.Fprintln(w, "You are not a member of this organization, so it gives you no AI accounts.")
		default:
			writeGroup("Its own API keys", o.Keys)
			fmt.Fprintf(w, "Admins manage them in the Reliant app: %s.\n", orgAccountsSettingsPlace)
		}
	}
}

func fundingWord(r providerAccountRow) string {
	switch r.Funding {
	case "org_seat":
		return "team seat"
	case "org_wallet":
		return "Reliant credits"
	case "org_key":
		return "organization key"
	}
	if r.Plan != "" {
		return r.Plan
	}
	return "-"
}
