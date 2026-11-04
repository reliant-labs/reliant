package cli

import (
	"context"
	"fmt"
	"io"
	"net/mail"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/deploytarget"
)

// Only the public, membership-checked IdentityService belongs here. The
// reconciler's IdentityInternalService and its credentials are never CLI inputs.
const (
	procIdentityStatus = "controlplane.v1.IdentityService/GetIdentityStatus"
	procIdentityUsers  = "controlplane.v1.IdentityService/ListUsers"
	procIdentityInvite = "controlplane.v1.IdentityService/InviteUser"
	procIdentityExport = "controlplane.v1.IdentityService/RequestExport"
)

func newCloudIdentityCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "identity", Short: "Inspect managed identity and administer its users",
		Long: `Manage the identity an environment declares in KCL, using its control plane.

Reads (status, users) need identity:read; every verb that changes a user
(invite, set-roles, reset, lock, unlock, delete, revoke-sessions) and export
need identity:manage and an org admin. Under Reliant, or after forge login, nothing
extra is needed: when the control plane answers that the credential lacks one of
those scopes, forge trades it for the scope on demand (an hour, and only while
the person behind the session holds the grant), as deploy does for deploy:read.
A --token or the token env var is used exactly as given and must carry the scope.
The organization is resolved from the authenticated server, not a token claim or
a flag. The environment must already exist in this project.

--user takes a user id or an exact email address (see 'users').

Inviting creates the account (or grants roles to an existing one) and, for an
account with no credential yet, issues a one-time password-set code; reset
issues one for any account. The code is emailed only where the environment
declares smtp in KCL ('status' shows "Mail:"). Without smtp the control plane
refuses both rather than send nothing: pass --return-code to have the code
handed back, and deliver it yourself through a channel you control. The code is
a credential. There is no password option, seed account or implicit recipient.
Roles managed in deploy/kcl/identity.k (members_from = "declared") are
read-only here.`,
	}
	cmd.AddCommand(newCloudIdentityStatusCmd(), newCloudIdentityUsersCmd(), newCloudIdentityInviteCmd(), newCloudIdentityExportCmd())
	cmd.AddCommand(newCloudIdentityUserVerbs()...)
	return cmdutil.StrictGroup(cmd)
}

// Keep KCL rendering outside the HTTP contract tests; production always uses
// the same per-environment endpoint and credential resolution as the rest of `forge cloud`.
var resolveIdentityCloudTarget = resolveCloudTarget

type cloudIdentityFlags struct {
	token string
	json  bool
}

func (f *cloudIdentityFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.token, "token", "", "Credential to use, ahead of the env var, stored login and session helper")
	cmd.Flags().BoolVar(&f.json, "json", false, "Emit machine-readable JSON (never credentials)")
}

func (f *cloudIdentityFlags) target(ctx context.Context, env string) (cloudCaller, map[string]any, error) {
	ep, cred, err := resolveIdentityCloudTarget(ctx, env, f.token)
	if err != nil {
		return nil, nil, err
	}
	client := cloud.NewClient(ep, cred)
	org, err := cloud.ResolveOrganization(ctx, client)
	if err != nil {
		return nil, nil, err
	}
	id, err := deploytarget.LookupHostedEnvironment(ctx, client, hostedProjectName(), env)
	if err != nil {
		return nil, nil, err
	}
	return client, map[string]any{"orgId": org, "environmentId": id}, nil
}

// Narrow proto3 JSON projections of controlplane.v1's identity messages.
// Unknown response fields are deliberately not forwarded to stdout.
type identityAppWire struct {
	Name            string `json:"name"`
	ClientID        string `json:"clientId"`
	BrokerExpiresAt string `json:"brokerExpiresAt,omitempty"`
}

type identityDeliveryWire struct {
	Kind           string `json:"kind"`
	Cluster        string `json:"cluster"`
	Namespace      string `json:"namespace"`
	Name           string `json:"name"`
	State          string `json:"state"`
	LastError      string `json:"lastError,omitempty"`
	LastVerifiedAt string `json:"lastVerifiedAt,omitempty"`
	// LastWrittenAt moves only when the delivered content changed.
	LastWrittenAt string `json:"lastWrittenAt,omitempty"`
}

// identityEnvironmentWire is controlplane.v1.IdentityEnvironment: an env id
// with its name, as the control plane resolved it.
type identityEnvironmentWire struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Project string `json:"project,omitempty"`
}

// label is the env's name (with its project), else its id.
func (e identityEnvironmentWire) label() string {
	switch {
	case e.Name == "":
		return e.ID
	case e.Project != "":
		return e.Project + "/" + e.Name
	}
	return e.Name
}

type identityStatusDocument struct {
	jsonEnvelope
	HasInstance          bool                   `json:"hasInstance"`
	State                string                 `json:"state"`
	Issuer               string                 `json:"issuer"`
	Domain               string                 `json:"domain"`
	Public               bool                   `json:"public"`
	Adopted              bool                   `json:"adopted"`
	Apps                 []identityAppWire      `json:"apps"`
	Deliveries           []identityDeliveryWire `json:"deliveries"`
	OwnerEnvironmentID   string                 `json:"ownerEnvironmentId"`
	SharerEnvironmentIDs []string               `json:"sharerEnvironmentIds"`
	LastReconciledAt     string                 `json:"lastReconciledAt,omitempty"`
	NextAttemptAt        string                 `json:"nextAttemptAt,omitempty"`
	LastError            string                 `json:"lastError,omitempty"`
	Failures             int32                  `json:"failures"`
	GraceUntil           string                 `json:"graceUntil,omitempty"`
	ExportURLAvailable   bool                   `json:"exportUrlAvailable"`
	Refused              string                 `json:"refused,omitempty"`
	// MailDelivery is controlplane.v1.IdentityMailDelivery: whether the
	// instance can email invitations and reset links.
	MailDelivery string `json:"mailDelivery,omitempty"`
	// NotEnabledReason is controlplane.v1.IdentityNotEnabledReason, and
	// NotEnabledMessage the same as a sentence: why there is no instance.
	NotEnabledReason  string `json:"notEnabledReason,omitempty"`
	NotEnabledMessage string `json:"notEnabledMessage,omitempty"`
	// OwnerEnvironment and SharerEnvironments are the id fields above with
	// their names.
	OwnerEnvironment   *identityEnvironmentWire  `json:"ownerEnvironment,omitempty"`
	SharerEnvironments []identityEnvironmentWire `json:"sharerEnvironments,omitempty"`
}

// mailDeliveryText says what an invitation or a reset does on this instance.
func mailDeliveryText(v string) string {
	switch v {
	case "IDENTITY_MAIL_DELIVERY_SMTP":
		return "smtp (invitation and reset codes are emailed)"
	case "IDENTITY_MAIL_DELIVERY_NONE":
		return "none (invite and reset need --return-code; you deliver the code)"
	case "", "IDENTITY_MAIL_DELIVERY_UNSPECIFIED":
		return "not reported by this control plane"
	}
	return v
}

// identityPasswordCodeWire is controlplane.v1.IdentityPasswordCode: the
// one-time password-set code --return-code hands back. It is a credential.
type identityPasswordCodeWire struct {
	Code      string `json:"code"`
	ExpiresAt string `json:"expiresAt,omitempty"`
	ResetURL  string `json:"resetUrl,omitempty"`
}

// writePasswordCode prints a returned code: the env's reset link when the
// identity declares one to build it from, else the bare code.
func writePasswordCode(w io.Writer, c *identityPasswordCodeWire) {
	expires := "within the hour"
	if c.ExpiresAt != "" {
		expires = "at " + c.ExpiresAt
	}
	fmt.Fprintf(w, "Password-set code — a credential: give it only to this person, over a channel you control. Single use; it expires %s.\n", expires)
	if c.ResetURL != "" {
		fmt.Fprintf(w, "  %s\n", c.ResetURL)
		return
	}
	fmt.Fprintf(w, "  code: %s\n  (the identity declares no login URI or domain, so there is no reset link to fill in)\n", c.Code)
}

func newCloudIdentityStatusCmd() *cobra.Command {
	var f cloudIdentityFlags
	cmd := &cobra.Command{
		Use: "status <env>", Short: "Show managed-identity state, issuer and delivery metadata",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, req, err := f.target(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			var doc identityStatusDocument
			if err := client.Call(cmd.Context(), procIdentityStatus, req, &doc); err != nil {
				return fmt.Errorf("identity status: %w", err)
			}
			doc.stamp(nil)
			if f.json {
				return writeJSONDocument(cmd.OutOrStdout(), doc)
			}
			out := cmd.OutOrStdout()
			if !doc.HasInstance {
				fmt.Fprintln(out, "No managed identity instance.")
				if doc.NotEnabledMessage != "" {
					fmt.Fprintln(out, doc.NotEnabledMessage)
				}
			} else {
				fmt.Fprintf(out, "State: %s\nIssuer: %s\nMail: %s\n", doc.State, doc.Issuer, mailDeliveryText(doc.MailDelivery))
				if o := doc.OwnerEnvironment; o != nil && o.ID != fmt.Sprint(req["environmentId"]) {
					fmt.Fprintf(out, "Owner: %s (this environment shares its identity)\n", o.label())
				}
				for _, sh := range doc.SharerEnvironments {
					fmt.Fprintf(out, "Shared with: %s\n", sh.label())
				}
				for _, app := range doc.Apps {
					fmt.Fprintf(out, "App: %s (client %s)\n", app.Name, app.ClientID)
				}
				for _, d := range doc.Deliveries {
					written := ""
					if d.LastWrittenAt != "" {
						written = " written " + d.LastWrittenAt
					}
					fmt.Fprintf(out, "Delivery: %s %s/%s/%s %s%s %s\n", d.Kind, d.Cluster, d.Namespace, d.Name, d.State, written, d.LastError)
				}
			}
			if doc.Refused != "" {
				fmt.Fprintf(out, "Refused: %s\n", doc.Refused)
			}
			if doc.LastError != "" {
				fmt.Fprintf(out, "Last error: %s\n", doc.LastError)
			}
			return nil
		},
	}
	f.register(cmd)
	return cmd
}

// identityExportDocument is RequestIdentityExportResponse. The URL is a
// bearer credential for every user's password hash.
type identityExportDocument struct {
	jsonEnvelope
	ExportURL string `json:"exportUrl"`
	ExpiresAt string `json:"expiresAt,omitempty"`
}

func newCloudIdentityExportCmd() *cobra.Command {
	var f cloudIdentityFlags
	cmd := &cobra.Command{
		Use: "export <env>", Short: "Export the identity's users now and print a short-lived download URL (identity:manage)",
		Long: `Ask the control plane to export the environment's identity instance — every
user, password hashes included, in Zitadel's import format — and print a signed
download URL valid for minutes. Requests within the same 15 minutes share one
export. For a withdrawn identity the URL is for the export written when it was
withdrawn.

The URL is a credential: anyone holding it can download every user's password
hash until it expires. It is printed once and never logged; do not paste it
into a ticket or a chat. Needs identity:manage and, for a person, an org admin
holding the identity:manage grant. Refused where export is not enabled for the
control plane, or the instance is not ready.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, req, err := f.target(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			var doc identityExportDocument
			if err := client.Call(cmd.Context(), procIdentityExport, req, &doc); err != nil {
				return fmt.Errorf("identity export: %w", err)
			}
			if strings.TrimSpace(doc.ExportURL) == "" {
				return fmt.Errorf("identity export: the control plane answered with no download URL")
			}
			doc.stamp(nil)
			if f.json {
				return writeJSONDocument(cmd.OutOrStdout(), doc)
			}
			out := cmd.OutOrStdout()
			expires := "within minutes"
			if doc.ExpiresAt != "" {
				expires = "at " + doc.ExpiresAt
			}
			fmt.Fprintf(out, "Identity export — a credential: it holds every user's password hash. The link expires %s.\n  %s\n", expires, doc.ExportURL)
			return nil
		},
	}
	f.register(cmd)
	return cmd
}

type identityUserWire struct {
	UserID      string   `json:"userId"`
	Email       string   `json:"email"`
	DisplayName string   `json:"displayName"`
	State       string   `json:"state"`
	Roles       []string `json:"roles"`
	CreatedAt   string   `json:"createdAt,omitempty"`
	// LastSignInAt is unset for a user who never signed in.
	LastSignInAt string `json:"lastSignInAt,omitempty"`
}

type identityRoleWire struct {
	Key         string `json:"key"`
	DisplayName string `json:"displayName"`
	Group       string `json:"group"`
	Declared    bool   `json:"declared"`
}

type identityUsersDocument struct {
	jsonEnvelope
	Users         []identityUserWire `json:"users"`
	NextPageToken string             `json:"nextPageToken"`
	Roles         []identityRoleWire `json:"roles"`
	// TotalSize is how many users match across every page.
	TotalSize int32 `json:"totalSize,omitempty"`
}

func newCloudIdentityUsersCmd() *cobra.Command {
	var f cloudIdentityFlags
	var query, pageToken string
	var pageSize int
	cmd := &cobra.Command{
		Use: "users <env>", Short: "List one page of users and declared roles (no credentials)",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if pageSize < 0 || pageSize > 100 || utf8.RuneCountInString(query) > 200 || utf8.RuneCountInString(pageToken) > 64 {
				return fmt.Errorf("--page-size must be 0..100, --query at most 200 characters and --page-token at most 64 characters")
			}
			client, req, err := f.target(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			req["pageSize"], req["pageToken"] = pageSize, pageToken
			if cmd.Flags().Changed("query") {
				req["query"] = query
			}
			doc := identityUsersDocument{Users: []identityUserWire{}, Roles: []identityRoleWire{}}
			if err := client.Call(cmd.Context(), procIdentityUsers, req, &doc); err != nil {
				return fmt.Errorf("identity users: %w", err)
			}
			doc.stamp(nil)
			if f.json {
				return writeJSONDocument(cmd.OutOrStdout(), doc)
			}
			out := cmd.OutOrStdout()
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "EMAIL\tUSER ID\tSTATE\tROLES\tLAST SIGN-IN")
			for _, u := range doc.Users {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", u.Email, u.UserID, u.State, strings.Join(u.Roles, ","), emptyOr(u.LastSignInAt, "-"))
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			if doc.TotalSize > 0 {
				fmt.Fprintf(out, "%d of %d user(s)\n", len(doc.Users), doc.TotalSize)
			}
			for _, r := range doc.Roles {
				fmt.Fprintf(out, "Role: %s (declared/read-only: %t)\n", r.Key, r.Declared)
			}
			if doc.NextPageToken != "" {
				fmt.Fprintf(out, "Next page: --page-token %s\n", doc.NextPageToken)
			}
			return nil
		},
	}
	f.register(cmd)
	cmd.Flags().StringVar(&query, "query", "", "Case-insensitive email substring (max 200 characters)")
	cmd.Flags().IntVar(&pageSize, "page-size", 20, "Maximum users in this page (0 uses server default, max 100)")
	cmd.Flags().StringVar(&pageToken, "page-token", "", "Continuation from the preceding response")
	return cmd
}

type identityInviteDocument struct {
	jsonEnvelope
	UserID  string                    `json:"userId"`
	Created bool                      `json:"created"`
	Code    *identityPasswordCodeWire `json:"code,omitempty"`
}

func newCloudIdentityInviteCmd() *cobra.Command {
	var f cloudIdentityFlags
	var email string
	var roles []string
	var returnCode bool
	cmd := &cobra.Command{
		Use: "invite <env> --email <address> --role <role>", Short: "Create an account (or grant roles) and issue a password-set code (org admin)",
		Long: `Invite this explicit email address; repeat --role for multiple roles.
An existing account receives the requested roles (created=false). An account
with no credential yet is issued a one-time password-set code: emailed where
the environment declares smtp, or, with --return-code, printed here for you to
deliver. Without smtp and without --return-code the control plane refuses the
invitation before writing anything. The server also refuses unknown or
declared/read-only roles, a credential without identity:manage and an identity
that is not ready. A refusal is an error, never a fallback to a seeded password.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			address, err := mail.ParseAddress(email)
			if err != nil || address.Address != email || len(email) > 254 {
				return fmt.Errorf("--email must be an explicit email address, without a display name")
			}
			if len(roles) == 0 || len(roles) > 20 {
				return fmt.Errorf("provide 1..20 --role flags")
			}
			for _, role := range roles {
				if strings.TrimSpace(role) == "" || utf8.RuneCountInString(role) > 200 {
					return fmt.Errorf("each --role must be nonempty and at most 200 characters")
				}
			}
			client, req, err := f.target(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			req["email"], req["roles"] = email, roles
			if returnCode {
				req["returnCode"] = true
			}
			var doc identityInviteDocument
			if err := client.Call(cmd.Context(), procIdentityInvite, req, &doc); err != nil {
				return fmt.Errorf("invite identity user: %w", err)
			}
			if strings.TrimSpace(doc.UserID) == "" {
				return fmt.Errorf("invite identity user: the control plane answered with no user id")
			}
			doc.stamp(nil)
			if f.json {
				return writeJSONDocument(cmd.OutOrStdout(), doc)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Invited %s (user %s, created=%t).\n", email, doc.UserID, doc.Created)
			switch {
			case doc.Code != nil:
				writePasswordCode(out, doc.Code)
			case returnCode:
				fmt.Fprintln(out, "The account already has a credential, so no code was issued (`forge cloud identity reset --return-code` issues one).")
			}
			return nil
		},
	}
	f.register(cmd)
	cmd.Flags().BoolVar(&returnCode, "return-code", false, "Print the password-set code instead of emailing it (it is a credential; needed where the environment declares no smtp)")
	cmd.Flags().StringVar(&email, "email", "", "Explicit recipient email address (required; never inferred)")
	cmd.Flags().StringArrayVar(&roles, "role", nil, "Console-managed role to grant (required; repeat for multiple roles)")
	_ = cmd.MarkFlagRequired("email")
	_ = cmd.MarkFlagRequired("role")
	return cmd
}
