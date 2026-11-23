package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/mail"
	"net/url"
	"slices"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
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
extra is needed: forge trades the credential for the scope a verb needs before
calling it (an hour, and only while the person behind the session holds the
grant), as obs does for obs:read.
A --token or the token env var is used exactly as given and must carry the scope.
The organization is resolved from the authenticated server, not a token claim or
a flag. The environment must already exist in this project.

--user takes a user id or an exact email address (see 'users').

An environment may declare several identities (KCL Bundle.identities): name
the one a verb acts on with --identity <name>. It defaults to the env's only
identity; with several, every verb but 'status' refuses to guess and lists
them, and 'status' shows each. A control plane that serves one identity per
environment is never sent a name it would ignore: a verb that needs one is
refused there, and 'status' shows the one identity it serves and says so.

Inviting creates the account (or grants roles to an existing one) and, for an
account with no credential yet, issues a one-time password-set code; reset
issues one for any account. The code is emailed where the instance can send
mail ('status' shows "Mail:"): the environment declares smtp in KCL, or the
control plane's platform delivery is on. Without mail the control plane
refuses both rather than send nothing: pass --return-code to have the code
handed back, and deliver it yourself through a channel you control. The code is
a credential. There is no password option, seed account or implicit recipient.
Roles managed in deploy/kcl/identity.k (members_from = "declared") are
read-only here.`,
	}
	cmd.AddCommand(newCloudIdentityStatusCmd(), newCloudIdentityUsersCmd(), newCloudIdentityInviteCmd(), newCloudIdentityExportCmd(), newCloudIdentityImportCmd())
	cmd.AddCommand(newCloudIdentityUserVerbs()...)
	return cmdutil.StrictGroup(cmd)
}

// Keep KCL rendering outside the HTTP contract tests; production always uses
// the same per-environment endpoint and credential resolution as the rest of
// `forge cloud` (resolveCloudTarget), from ONE render that also names the
// managed identities the env declares.
var resolveIdentityCloudTarget = func(ctx context.Context, env, token string) (cloud.Endpoint, cloud.Credential, []string, error) {
	entities, err := RenderKCL(ctx, projectDirForKCL(), env)
	if err != nil {
		return cloud.Endpoint{}, cloud.Credential{}, nil, fmt.Errorf("render KCL for env %q: %w", env, err)
	}
	ep, cred, err := resolveCloudTargetFor(env, declarationFromEntities(entities), token)
	if err != nil {
		return cloud.Endpoint{}, cloud.Credential{}, nil, err
	}
	var names []string
	for _, id := range entities.AllIdentities() {
		names = append(names, id.Name)
	}
	return ep, cred, names, nil
}

type cloudIdentityFlags struct {
	token    string
	json     bool
	identity string
}

func (f *cloudIdentityFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.token, "token", "", "Credential to use, ahead of the env var, stored login and session helper")
	cmd.Flags().BoolVar(&f.json, "json", false, "Emit machine-readable JSON (never credentials)")
	cmd.Flags().StringVar(&f.identity, "identity", "", "The managed identity to act on, by ManagedIdentity.name (default: the env's only identity; required when it declares several)")
}

// identityBase is what every identity verb resolves before naming an
// identity: the client, the request it starts from, and the identity names
// the env declares in KCL.
func (f *cloudIdentityFlags) identityBase(ctx context.Context, env string) (cloudCaller, map[string]any, []string, error) {
	ep, cred, declared, err := resolveIdentityCloudTarget(ctx, env, f.token)
	if err != nil {
		return nil, nil, nil, err
	}
	client := cloud.NewClient(ep, cred)
	declareIdentityScopes(client)
	org, err := cloud.ResolveOrganization(ctx, client)
	if err != nil {
		return nil, nil, nil, err
	}
	id, err := deploytarget.LookupHostedEnvironment(ctx, client, hostedProjectName(), env)
	if err != nil {
		return nil, nil, nil, err
	}
	return client, map[string]any{"orgId": org, "environmentId": id}, declared, nil
}

// target is identityBase with the identity this verb acts on named in the
// request: --identity, else the env's only one. Several and no --identity is
// refused, listing them.
func (f *cloudIdentityFlags) target(ctx context.Context, env string) (cloudCaller, map[string]any, error) {
	client, req, declared, err := f.identityBase(ctx, env)
	if err != nil {
		return nil, nil, err
	}
	name, err := pickIdentity(env, f.identity, declared)
	if err != nil {
		return nil, nil, err
	}
	sel, err := identitySelector(ctx, client, env, name, declared)
	if err != nil {
		return nil, nil, err
	}
	if sel != "" {
		req["identity"] = sel
	}
	return client, req, nil
}

// pickIdentity is the identity a verb acts on: flag when given (it must be
// declared), else the env's only identity ("" when it declares none).
func pickIdentity(env, flag string, declared []string) (string, error) {
	if flag != "" {
		if slices.Contains(declared, flag) {
			return flag, nil
		}
		if len(declared) == 0 {
			return "", fmt.Errorf("--identity %q: env %q declares no managed identity", flag, env)
		}
		return "", fmt.Errorf("--identity %q: env %q declares no identity of that name (declared: %s)", flag, env, strings.Join(declared, ", "))
	}
	switch len(declared) {
	case 0:
		return "", nil
	case 1:
		return declared[0], nil
	}
	return "", fmt.Errorf("env %q declares %d managed identities (%s): name one with --identity <name>", env, len(declared), strings.Join(declared, ", "))
}

// identitySelector is the `identity` a request carries, "" for none. A
// request names its identity only where that tells the control plane
// something: the env declares several, or its one identity is not the
// default name. And only to a control plane that addresses identities by
// name (release.CapabilityConvergesSeveralIdentities): an older one ignores
// the field and acts on the env's one identity, so a request whose meaning
// depends on the name is refused there before anything is written. An env
// whose one identity has another name is served the old request, which an
// older control plane answers for that one identity.
func identitySelector(ctx context.Context, client cloudCaller, env, name string, declared []string) (string, error) {
	if name == "" || (len(declared) <= 1 && name == release.DefaultIdentityName) {
		return "", nil
	}
	reader, err := deploytarget.HostedBundleReader(ctx, client)
	if err != nil {
		return "", fmt.Errorf("identity %q: %w", name, err)
	}
	if reader.Supports(release.CapabilityConvergesSeveralIdentities) {
		return name, nil
	}
	if len(declared) <= 1 {
		return "", nil
	}
	return "", errIdentitiesUnaddressable(env, declared)
}

func errIdentitiesUnaddressable(env string, declared []string) error {
	return fmt.Errorf("env %q declares managed identities %s, and this control plane addresses one identity per environment "+
		"(it does not advertise %q): it cannot tell one from another, so nothing was sent. Upgrade the control plane",
		env, strings.Join(declared, ", "), release.CapabilityConvergesSeveralIdentities)
}

// declareIdentityScopes tells the client which identity scope each procedure
// needs, so a login or session credential is elevated for it before the call
// instead of after a refusal. Neither scope is one a login carries. The
// control plane's identity.scopeFor maps the read level (GetIdentityStatus,
// ListUsers) to identity:read and manage, codes and exports (every other
// procedure here) to identity:manage. The social sign-in check starts intents
// that are never completed and changes nothing a person sees, so it is read.
func declareIdentityScopes(c *cloud.Client) {
	c.DeclareScope("identity:read", procIdentityStatus, procIdentityUsers, procIdentityCheckSocial)
	c.DeclareScope("identity:manage", procIdentityInvite, procIdentityExport, procIdentityImport,
		procIdentitySetRoles, procIdentityResetPassword, procIdentityLockUser,
		procIdentityUnlockUser, procIdentityDeleteUser, procIdentityRevokeSessions)
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
	// SocialCallbackURL is the redirect URI to register with each social
	// provider (KCL ManagedIdentity.social): <issuer>/idps/callback.
	SocialCallbackURL string `json:"socialCallbackUrl,omitempty"`
	// Social is each declared social provider as the control plane applied
	// it.
	Social []identitySocialStatusWire `json:"social,omitempty"`
	// OwnersPending are declared owners with no credential yet who were not
	// invited on the last pass (no mail, or invited recently).
	OwnersPending []string `json:"ownersPending,omitempty"`
	// Identity is the ManagedIdentity.name this answer is for, as the
	// request named it: set by forge (never read from the response), and
	// only when the request named one.
	Identity string `json:"identity,omitempty"`
	// SocialChecks is --check-social's verdict per provider; absent without
	// the flag.
	SocialChecks []socialCheck `json:"socialChecks,omitempty"`
}

// identitySocialStatusWire is controlplane.v1.IdentitySocialStatus.
type identitySocialStatusWire struct {
	Type      string `json:"type"`
	State     string `json:"state,omitempty"`
	AppliedAt string `json:"appliedAt,omitempty"`
}

// mailDeliveryText says what an invitation or a reset does on this instance.
func mailDeliveryText(v string) string {
	switch v {
	case "IDENTITY_MAIL_DELIVERY_PLATFORM":
		return "platform (invitation, verification, reset and sign-in links are emailed by the platform's identity mail relay)"
	case "IDENTITY_MAIL_DELIVERY_SANDBOX":
		return "sandbox (the organization is unclaimed: mail is captured, not sent; org admins can read it)"
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
// identity declares one to build it from, else the bare code. Its lifetime
// is the env's (ManagedIdentity.code_lifetime, an hour by default) and no
// flag extends it, so it is said up front, with the command that issues a fresh code: a link an
// operator hands over by chat is routinely opened after it lapsed
// (forge#2265, "Code is expired (CODE-QvUQ4P)"). reissue is that command.
func writePasswordCode(w io.Writer, c *identityPasswordCodeWire, reissue string, now time.Time) {
	expires := "within the hour"
	if c.ExpiresAt != "" {
		expires = "at " + c.ExpiresAt
		if at, err := time.Parse(time.RFC3339, c.ExpiresAt); err == nil && at.After(now) {
			expires += fmt.Sprintf(" (in %s)", at.Sub(now).Round(time.Minute))
		}
	}
	fmt.Fprintf(w, "Password-set code — a credential: give it only to this person, over a channel you control. Single use.\n")
	fmt.Fprintf(w, "⚠️  It expires %s, and no flag extends it (the env's ManagedIdentity.code_lifetime sets how long codes live): deliver it now.\n", expires)
	if c.ResetURL != "" {
		fmt.Fprintf(w, "  %s\n", c.ResetURL)
	} else {
		fmt.Fprintf(w, "  code: %s\n  (the identity declares no login URI or domain, so there is no reset link to fill in)\n", c.Code)
	}
	if reissue != "" {
		fmt.Fprintf(w, "If it lapses before it is used, issue a fresh one (the old one stops mattering):\n  %s\n", reissue)
	}
}

// reissueCodeCommand is the command that issues a fresh password-set code to
// user (an email or a user id) in env.
func reissueCodeCommand(env, user string) string {
	return Name() + " cloud identity reset " + env + " --user " + user + " --return-code"
}

func newCloudIdentityStatusCmd() *cobra.Command {
	var f cloudIdentityFlags
	var checkSocialFlag bool
	var app string
	cmd := &cobra.Command{
		Use: "status <env>", Short: "Show managed-identity state, issuer and delivery metadata",
		Long: `Show the environment's managed-identity state, issuer, applications,
deliveries and, when it declares social sign-in, the callback to register with
each provider and each provider's state.

--check-social starts a sign-in with each applied social provider and checks
the authorize URL the issuer answers with: the provider's own endpoint, a
client id, scopes, and a redirect_uri EQUAL to the printed callback — the URL
registered on the provider's OAuth app, which the provider refuses to differ.
The sign-ins are never completed. They are started by the control plane, or
with --app <url> by your app's own login broker (POST <url>/auth/social/start,
the route every sign-in button calls), which also proves the deployed broker
offers each provider. It exits non-zero when any provider fails or is not
applied yet.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if app != "" && !checkSocialFlag {
				return fmt.Errorf("--app goes with --check-social")
			}
			if app != "" {
				if u, err := url.Parse(app); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
					return fmt.Errorf("--app %q must be an http(s) URL: where your app's login broker answers", app)
				}
			}
			ctx := cmd.Context()
			client, req, declared, err := f.identityBase(ctx, args[0])
			if err != nil {
				return err
			}
			names := []string{""}
			switch {
			case f.identity != "" || len(declared) <= 1:
				name, err := pickIdentity(args[0], f.identity, declared)
				if err != nil {
					return err
				}
				sel, err := identitySelector(ctx, client, args[0], name, declared)
				if err != nil {
					return err
				}
				names = []string{sel}
			default:
				// Several, and none named: every one of them, where the
				// control plane can tell them apart.
				reader, err := deploytarget.HostedBundleReader(ctx, client)
				if err != nil {
					return fmt.Errorf("identity status: %w", err)
				}
				if reader.Supports(release.CapabilityConvergesSeveralIdentities) {
					names = declared
				} else {
					fmt.Fprintf(cmd.ErrOrStderr(), "Note: env %s declares identities %s, but this control plane serves one identity per environment "+
						"(it does not advertise %q); showing the one it serves.\n", args[0], strings.Join(declared, ", "), release.CapabilityConvergesSeveralIdentities)
				}
			}
			docs, checkErrs, err := identityStatuses(ctx, client, req, names, checkSocialFlag, app)
			if err != nil {
				return err
			}
			checkErr := errors.Join(checkErrs...)
			if f.json {
				if len(docs) == 1 {
					if err := writeJSONDocument(cmd.OutOrStdout(), docs[0]); err != nil {
						return err
					}
					return checkErr
				}
				all := identityStatusesDocument{Identities: docs}
				all.stamp(checkErr)
				if err := writeJSONDocument(cmd.OutOrStdout(), all); err != nil {
					return err
				}
				return checkErr
			}
			out := cmd.OutOrStdout()
			for i, doc := range docs {
				if len(docs) > 1 {
					if i > 0 {
						fmt.Fprintln(out)
					}
					fmt.Fprintf(out, "Identity: %s\n", doc.Identity)
				}
				writeIdentityStatus(out, doc, fmt.Sprint(req["environmentId"]))
				if checkSocialFlag && doc.HasInstance && (checkErrs[i] == nil || doc.SocialChecks != nil) {
					writeSocialChecks(out, socialCallbackFor(doc), doc.SocialChecks)
				}
			}
			return checkErr
		},
	}
	f.register(cmd)
	cmd.Flags().BoolVar(&checkSocialFlag, "check-social", false, "Start a sign-in with each social provider and check the authorize URL's redirect_uri equals the printed callback")
	cmd.Flags().StringVar(&app, "app", "", "With --check-social: start each sign-in through your app's login broker at this URL (POST <url>/auth/social/start) instead of the control plane")
	return cmd
}

// identityStatuses reads GetIdentityStatus for each name ("" is the env's one
// identity), and with checkSocial runs --check-social on each through the
// same identity-scoped request. checkErrs[i] is docs[i]'s check verdict; err
// is a status read that failed.
func identityStatuses(ctx context.Context, client cloudCaller, req map[string]any, names []string, checkSocialFlag bool, app string) (docs []identityStatusDocument, checkErrs []error, err error) {
	for _, name := range names {
		r := maps.Clone(req)
		if name != "" {
			r["identity"] = name
		}
		var doc identityStatusDocument
		if err := client.Call(ctx, procIdentityStatus, r, &doc); err != nil {
			if name != "" {
				return nil, nil, fmt.Errorf("identity status (%s): %w", name, err)
			}
			return nil, nil, fmt.Errorf("identity status: %w", err)
		}
		doc.Identity = name
		var checkErr error
		if checkSocialFlag {
			checkErr = checkIdentitySocial(ctx, client, r, &doc, app)
			if checkErr != nil && name != "" {
				checkErr = fmt.Errorf("identity %s: %w", name, checkErr)
			}
		}
		doc.stamp(checkErr)
		docs = append(docs, doc)
		checkErrs = append(checkErrs, checkErr)
	}
	return docs, checkErrs, nil
}

// checkIdentitySocial is --check-social for one identity's status doc: it
// fills doc.SocialChecks and returns the verdict.
func checkIdentitySocial(ctx context.Context, client cloudCaller, req map[string]any, doc *identityStatusDocument, app string) error {
	if !doc.HasInstance {
		return fmt.Errorf("--check-social: the environment has no managed identity instance")
	}
	start := controlPlaneSocialStarter(client, req)
	if app != "" {
		start = brokerSocialStarter(socialCheckHTTPClient(), app)
	}
	var err error
	doc.SocialChecks, err = checkSocial(ctx, *doc, start)
	if err == nil {
		err = socialCheckErr(doc.SocialChecks)
	}
	return err
}

// identityStatusesDocument is `status --json` for an env with several
// identities: one GetIdentityStatus answer per identity, each naming it.
type identityStatusesDocument struct {
	jsonEnvelope
	Identities []identityStatusDocument `json:"identities"`
}

// writeIdentityStatus prints one GetIdentityStatus answer. envID is the env
// the request named, to tell an owner apart from a sharer.
func writeIdentityStatus(out io.Writer, doc identityStatusDocument, envID string) {
	if !doc.HasInstance {
		fmt.Fprintln(out, "No managed identity instance.")
		if doc.NotEnabledMessage != "" {
			fmt.Fprintln(out, doc.NotEnabledMessage)
		}
	} else {
		fmt.Fprintf(out, "State: %s\nIssuer: %s\nMail: %s\n", doc.State, doc.Issuer, mailDeliveryText(doc.MailDelivery))
		if o := doc.OwnerEnvironment; o != nil && o.ID != envID {
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
		if doc.SocialCallbackURL != "" {
			fmt.Fprintf(out, "Social callback: %s (register it as the redirect URI of each OAuth client)\n", doc.SocialCallbackURL)
		}
		for _, s := range doc.Social {
			applied := ""
			if s.AppliedAt != "" {
				applied = " applied " + s.AppliedAt
			}
			fmt.Fprintf(out, "Social: %s %s%s\n", s.Type, s.State, applied)
		}
		if len(doc.OwnersPending) > 0 {
			fmt.Fprintf(out, "Owners pending: %s (no credential yet and not invited on the last pass)\n", strings.Join(doc.OwnersPending, ", "))
		}
	}
	if doc.Refused != "" {
		fmt.Fprintf(out, "Refused: %s\n", doc.Refused)
	}
	if doc.LastError != "" {
		fmt.Fprintf(out, "Last error: %s\n", doc.LastError)
	}
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
				writePasswordCode(out, doc.Code, reissueCodeCommand(args[0], email), time.Now())
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
