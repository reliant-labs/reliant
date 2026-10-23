package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/cloudcred"
)

// `forge domain` — custom hostnames as control-plane resources.
//
// A DOMAIN is org-scoped and has no environment. A BINDING is what makes it
// serve something: one environment, and one target inside it (or a redirect
// to another hostname). They are separate because their lifetimes are: a
// domain is acquired once, slowly, with a human editing a zone forge does
// not control, and then moved between environments freely.
//
// That is why none of this is spec. As a field on a workload, "point
// hounders.club at staging for an hour" would be a deploy, and replacing the
// workload would drop and re-acquire the domain — which costs a certificate
// against a rate limit that cannot be raised.
//
// WHY EVERY COMMAND TAKES --env WHEN A DOMAIN HAS NO ENVIRONMENT. The env is
// how forge knows WHICH CONTROL PLANE to talk to: the endpoint comes from
// that env's `forge.ControlPlane` declaration, exactly as `forge secret` and
// `forge cloud` resolve theirs. The org is never sent — it is resolved
// server-side from the token. So `--env` selects the control plane, and for
// `bind` it additionally selects the environment being bound to.

const (
	procCreateDomain        = "controlplane.v1.DomainService/CreateDomain"
	procGetDomain           = "controlplane.v1.DomainService/GetDomain"
	procListDomains         = "controlplane.v1.DomainService/ListDomains"
	procDeleteDomain        = "controlplane.v1.DomainService/DeleteDomain"
	procVerifyDomain        = "controlplane.v1.DomainService/VerifyDomain"
	procCreateDomainBinding = "controlplane.v1.DomainService/CreateDomainBinding"
	procDeleteDomainBinding = "controlplane.v1.DomainService/DeleteDomainBinding"
)

// The wire shapes forge reads, declared LOCALLY and narrowly.
//
// forge does not vendor the control plane's protos — that coupling is the
// thing the design avoids — so the shape forge depends on is written down
// here rather than inferred from a generated type. A field forge does not
// use is a field forge cannot break on. Mirrors controlplane.v1.Domain /
// DomainBinding / DeployDnsRecord as proto3 JSON (lowerCamelCase).

type domainWireRecord struct {
	Type  string `json:"type,omitempty"`
	Name  string `json:"name,omitempty"`
	Value string `json:"value,omitempty"`
	// Resolved and Detail are the control plane's verdict on THIS record
	// from its last verification pass.
	//
	// ⚠️ THESE MUST BE CARRIED, and forge dropping them is why they are
	// commented. forge re-encodes the response into this struct, so a
	// field absent here is a field absent from `--json` too — it does not
	// pass through. Without them, `forge domain show` on a domain stuck in
	// pending_dns said only "not verified yet" and the author had to guess
	// WHICH of their records was wrong, which is the exact question the
	// per-record checks were added to answer.
	//
	// Resolved=false with an empty Detail is a THIRD state, not a failure:
	// "not checked yet", which is every record on a brand-new domain. The
	// table below renders the three distinctly, because putting a red
	// cross on a correct record the verifier has not reached yet sends
	// someone to fix something that is not broken.
	Resolved bool   `json:"resolved,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type domainWireBinding struct {
	ID            string `json:"id,omitempty"`
	DomainID      string `json:"domainId,omitempty"`
	EnvironmentID string `json:"environmentId,omitempty"`
	Target        string `json:"target,omitempty"`
	RedirectTo    string `json:"redirectTo,omitempty"`
}

type domainWire struct {
	ID       string `json:"id,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	// State is the DeployCustomDomainState value NAME; decode it with
	// deploytarget.DomainStateName, never by trimming here, so the CLI and
	// `forge env status` speak one vocabulary.
	State           string             `json:"state,omitempty"`
	Source          string             `json:"source,omitempty"`
	RequiredRecords []domainWireRecord `json:"requiredRecords,omitempty"`
	LastError       string             `json:"lastError,omitempty"`
	VerifiedAt      string             `json:"verifiedAt,omitempty"`
	LiveSince       string             `json:"liveSince,omitempty"`
	Binding         *domainWireBinding `json:"binding,omitempty"`
}

// domainSourceName renders a DomainSource value name in forge's lower-case
// vocabulary. Unrecognised decodes as "unknown" rather than toward
// "platform": a domain forge calls platform-owned is one the control plane
// said is platform-owned.
func domainSourceName(wire string) string {
	switch v := strings.ToLower(strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(wire)), "DOMAIN_SOURCE_")); v {
	case "external", "platform":
		return v
	default:
		return "unknown"
	}
}

// bindingSummary is a binding as one line: where it points.
func bindingSummary(b *domainWireBinding) string {
	switch {
	case b == nil:
		return "(unbound)"
	case b.RedirectTo != "":
		return "→ " + b.RedirectTo + " (redirect)"
	default:
		return b.Target + " in " + b.EnvironmentID
	}
}

func newDomainCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "domain",
		Short: "Manage custom domains and what they serve",
		Long: `Manage custom hostnames as control-plane resources.

A DOMAIN is claimed once for your organization and is not tied to an
environment. A BINDING points it at one environment and one target inside
it. Adding a domain tells you the DNS to publish; once it verifies, the
platform issues its certificate and serves it.

  forge domain add hounders.club --env prod              # claim it, print the DNS
  forge domain ls --env prod
  forge domain show hounders.club --env prod
  forge domain verify hounders.club --env prod           # check DNS now
  forge domain bind hounders.club --env prod --target web
  forge domain bind www.hounders.club --env prod --redirect-to hounders.club
  forge domain unbind hounders.club --env prod
  forge domain rm hounders.club --env prod

Domains are NOT declared in KCL. A hostname you bring needs an action at
your registrar, ownership verification and a certificate — asynchronous and
human-gated, none of which a deploy converges — and it binds to ONE
environment while an env file renders to many. A hosted spec that carries a
` + "`domains`" + ` field is refused at render. (forge.OnCluster keeps
` + "`Port.domains`" + `: there you own the ingress.)

--env names WHICH CONTROL PLANE to talk to, from that env's
forge.ControlPlane declaration — a domain itself has no environment. Your
organization comes from the credential and is never sent, so there is
nothing to widen. The credential is --token, then the declared env var, then
the credentials file entry for that endpoint (` + "`forge login`" + `), then the
credential helper $` + cloudcred.HelperEnv + ` (a host application's session —
Reliant sets it, so a user signed in to Reliant needs no ` + "`forge login`" + `).`,
	}
	cmd.AddCommand(
		newDomainAddCmd(),
		newDomainListCmd(),
		newDomainShowCmd(),
		newDomainVerifyCmd(),
		newDomainRemoveCmd(),
		newDomainBindCmd(),
		newDomainUnbindCmd(),
	)
	return cmdutil.StrictGroup(cmd)
}

// domainFlags are the two every domain command takes.
func addDomainFlags(cmd *cobra.Command, env, token *string) {
	cmd.Flags().StringVar(env, "env", "", "Environment whose control plane to talk to (required; deploy/kcl/<env>/)")
	cmd.Flags().StringVar(token, "token", "", "Credential to use, ahead of the env var and the credentials file")
}

// domainClient resolves the endpoint and credential for env and builds the
// client. A var so tests point it at an httptest control plane.
var domainClient = func(ctx context.Context, envName, token string) (cloudCaller, error) {
	envName = strings.TrimSpace(envName)
	if envName == "" {
		return nil, fmt.Errorf("--env is required: it names which control plane to talk to, from that env's forge.ControlPlane declaration\n" +
			"fix: re-run with `--env <env>` (the environments under deploy/kcl/)")
	}
	ep, cred, err := resolveCloudTarget(ctx, envName, token)
	if err != nil {
		return nil, err
	}
	return cloud.NewClient(ep, cred), nil
}

// lookupDomain resolves a hostname to the domain the org holds, so every
// command can take the name a human knows rather than an id.
func lookupDomain(ctx context.Context, c cloudCaller, hostname string) (domainWire, error) {
	var resp struct {
		Domain domainWire `json:"domain"`
	}
	if err := c.Call(ctx, procGetDomain, map[string]any{"hostname": hostname}, &resp); err != nil {
		return domainWire{}, fmt.Errorf("look up domain %q: %w", hostname, err)
	}
	if resp.Domain.ID == "" {
		return domainWire{}, fmt.Errorf("the control plane returned no domain for %q", hostname)
	}
	return resp.Domain, nil
}

// writeDomainJSON is the --json form of every read: the decoded domain,
// indented.
func writeDomainJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// writeDNSTable prints the records to publish as a copyable table.
//
// THIS IS THE WHOLE OUTPUT THAT MATTERS. Until the author pastes these into
// their registrar nothing converges, and forge cannot do it for them — so
// the records are the answer, not a footnote under a status line. Printed
// for a live domain too: removing them breaks it.
func writeDNSTable(out io.Writer, records []domainWireRecord) {
	if len(records) == 0 {
		return
	}
	fmt.Fprintln(out, "\nSet these DNS records at your registrar:")
	fmt.Fprintln(out)
	tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "  TYPE\tNAME\tVALUE\tSTATUS")
	for _, r := range records {
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", r.Type, r.Name, r.Value, recordStatus(r))
	}
	_ = tw.Flush()
}

// recordStatus renders one record's verdict in the three states it actually
// has, rather than the two a boolean suggests.
//
// "not checked yet" is NOT a failure, and conflating it with one is the
// mistake worth avoiding: on a brand-new domain it describes every record,
// and a red cross there sends the author to re-check something correct. A
// failure prints the control plane's own detail ("resolves to 203.0.113.7,
// expected 34.63.203.181") because that names the fix; "failed" does not.
func recordStatus(r domainWireRecord) string {
	switch {
	case r.Resolved:
		return "ok"
	case strings.TrimSpace(r.Detail) != "":
		return "NOT YET — " + r.Detail
	default:
		return "not checked yet"
	}
}

// writeDomainDetail is the human form of one domain: what it is doing, what
// it serves, and what DNS it still needs.
func writeDomainDetail(out io.Writer, d domainWire) {
	fmt.Fprintf(out, "%s\n", d.Hostname)
	fmt.Fprintf(out, "  state:   %s\n", deploytarget.DomainStateName(d.State))
	fmt.Fprintf(out, "  source:  %s\n", domainSourceName(d.Source))
	fmt.Fprintf(out, "  serving: %s\n", bindingSummary(d.Binding))
	if d.LiveSince != "" {
		fmt.Fprintf(out, "  live:    since %s\n", d.LiveSince)
	}
	if d.VerifiedAt != "" {
		fmt.Fprintf(out, "  proven:  %s\n", d.VerifiedAt)
	}
	if d.LastError != "" {
		fmt.Fprintf(out, "  error:   %s\n", d.LastError)
	}
	writeDNSTable(out, d.RequiredRecords)
}

// ─── add ─────────────────────────────────────────────────────────────────────

func newDomainAddCmd() *cobra.Command {
	var env, token string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "add <hostname>",
		Short: "Claim a hostname and print the DNS records to publish",
		Long: `Claim a hostname for your organization and print the DNS to publish.

The domain starts in pending_dns: those records ARE the next step, and
nothing converges until they resolve. An apex and its www are two names —
add both, and bind one as a redirect to the other.

Claiming does not lock the name. Any number of organizations may add the
same hostname and sit in pending_dns; the first to PROVE ownership takes it,
and the others move to conflict. Locking at creation would let anyone who
merely types a domain deny it to its real owner.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := domainClient(cmd.Context(), env, token)
			if err != nil {
				return err
			}
			return runDomainAdd(cmd.Context(), c, args[0], jsonOutput, cmd.OutOrStdout())
		},
	}
	addDomainFlags(cmd, &env, &token)
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit the created domain as JSON")
	return cmd
}

func runDomainAdd(ctx context.Context, c cloudCaller, hostname string, jsonOutput bool, out io.Writer) error {
	var resp struct {
		Domain domainWire `json:"domain"`
	}
	if err := c.Call(ctx, procCreateDomain, map[string]any{"hostname": hostname}, &resp); err != nil {
		return fmt.Errorf("add domain %q: %w", hostname, err)
	}
	if jsonOutput {
		return writeDomainJSON(out, resp)
	}
	d := resp.Domain
	fmt.Fprintf(out, "Added %s (%s)\n", d.Hostname, deploytarget.DomainStateName(d.State))
	writeDNSTable(out, d.RequiredRecords)
	fmt.Fprintf(out, "\nOwnership is checked automatically. To check now:\n  forge domain verify %s --env <env>\n", d.Hostname)
	if d.Binding == nil {
		fmt.Fprintf(out, "\nIt serves nothing until you bind it:\n  forge domain bind %s --env <env> --target <workload|frontend>\n", d.Hostname)
	}
	return nil
}

// ─── ls ──────────────────────────────────────────────────────────────────────

func newDomainListCmd() *cobra.Command {
	var env, token string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List your organization's domains and what they serve",
		Long: `List every domain your organization holds, with its state and binding.

Every domain, not just this env's: a domain is org-scoped and has no
environment. The BINDING column names the environment each one serves, so
scoping by eye is possible without a second call.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := domainClient(cmd.Context(), env, token)
			if err != nil {
				return err
			}
			return runDomainList(cmd.Context(), c, jsonOutput, cmd.OutOrStdout())
		},
	}
	addDomainFlags(cmd, &env, &token)
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit the raw response as JSON")
	return cmd
}

func runDomainList(ctx context.Context, c cloudCaller, jsonOutput bool, out io.Writer) error {
	var resp struct {
		Domains []domainWire `json:"domains"`
	}
	if err := c.Call(ctx, procListDomains, map[string]any{}, &resp); err != nil {
		return err
	}
	if jsonOutput {
		return writeDomainJSON(out, resp)
	}
	if len(resp.Domains) == 0 {
		fmt.Fprintln(out, "No domains. Add one with `forge domain add <hostname> --env <env>`.")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOSTNAME\tSTATE\tSERVING")
	for _, d := range resp.Domains {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", d.Hostname, deploytarget.DomainStateName(d.State), bindingSummary(d.Binding))
	}
	return tw.Flush()
}

// ─── show ────────────────────────────────────────────────────────────────────

func newDomainShowCmd() *cobra.Command {
	var env, token string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "show <hostname>",
		Short: "Show one domain's state, binding and required DNS",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := domainClient(cmd.Context(), env, token)
			if err != nil {
				return err
			}
			return runDomainShow(cmd.Context(), c, args[0], jsonOutput, cmd.OutOrStdout())
		},
	}
	addDomainFlags(cmd, &env, &token)
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit the domain as JSON")
	return cmd
}

func runDomainShow(ctx context.Context, c cloudCaller, hostname string, jsonOutput bool, out io.Writer) error {
	d, err := lookupDomain(ctx, c, hostname)
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeDomainJSON(out, map[string]any{"domain": d})
	}
	writeDomainDetail(out, d)
	return nil
}

// ─── verify ──────────────────────────────────────────────────────────────────

func newDomainVerifyCmd() *cobra.Command {
	var env, token string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "verify <hostname>",
		Short: "Check a domain's DNS now, instead of waiting for the poller",
		Long: `Check DNS right now and print the resulting state.

A NUDGE, NOT THE MECHANISM. The control plane polls regardless, so this
changes only latency — it exists because someone who has just saved a record
wants an answer in a second rather than an hour.

Not yet verified is not a failure: DNS takes time to propagate, and the
records stay printed so you can confirm what you published.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := domainClient(cmd.Context(), env, token)
			if err != nil {
				return err
			}
			return runDomainVerify(cmd.Context(), c, args[0], jsonOutput, cmd.OutOrStdout())
		},
	}
	addDomainFlags(cmd, &env, &token)
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit the domain as JSON")
	return cmd
}

func runDomainVerify(ctx context.Context, c cloudCaller, hostname string, jsonOutput bool, out io.Writer) error {
	found, err := lookupDomain(ctx, c, hostname)
	if err != nil {
		return err
	}
	var resp struct {
		Domain domainWire `json:"domain"`
	}
	if err := c.Call(ctx, procVerifyDomain, map[string]any{"domainId": found.ID}, &resp); err != nil {
		return fmt.Errorf("verify domain %q: %w", hostname, err)
	}
	if jsonOutput {
		return writeDomainJSON(out, resp)
	}
	writeDomainDetail(out, resp.Domain)
	return nil
}

// ─── rm ──────────────────────────────────────────────────────────────────────

func newDomainRemoveCmd() *cobra.Command {
	var env, token string
	cmd := &cobra.Command{
		Use:     "rm <hostname>",
		Aliases: []string{"remove"},
		Short:   "Remove a domain and stop serving it",
		Long: `Remove a domain, its binding, and stop serving it.

This FREES THE HOSTNAME for another organization to claim, which is the only
self-service escape from a conflict: the org holding it lets go.

To stop serving a domain but KEEP it — and keep its verification, so
re-binding later costs no DNS work and no new certificate — use
` + "`forge domain unbind`" + ` instead.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := domainClient(cmd.Context(), env, token)
			if err != nil {
				return err
			}
			return runDomainRemove(cmd.Context(), c, args[0], cmd.OutOrStdout())
		},
	}
	addDomainFlags(cmd, &env, &token)
	return cmd
}

func runDomainRemove(ctx context.Context, c cloudCaller, hostname string, out io.Writer) error {
	d, err := lookupDomain(ctx, c, hostname)
	if err != nil {
		return err
	}
	if err := c.Call(ctx, procDeleteDomain, map[string]any{"domainId": d.ID}, &struct{}{}); err != nil {
		return fmt.Errorf("remove domain %q: %w", hostname, err)
	}
	fmt.Fprintf(out, "Removed %s. The hostname is free for another organization to claim.\n", hostname)
	return nil
}

// ─── bind ────────────────────────────────────────────────────────────────────

func newDomainBindCmd() *cobra.Command {
	var env, token, target, redirectTo string
	cmd := &cobra.Command{
		Use:   "bind <hostname>",
		Short: "Point a domain at one environment's workload or frontend",
		Long: `Point a domain at a target in --env, replacing any binding it has.

  forge domain bind hounders.club     --env prod --target web
  forge domain bind www.hounders.club --env prod --redirect-to hounders.club

--target names the WORKLOAD OR FRONTEND as your config names it, not a
deployment id: a deployment id is re-minted when a workload is replaced,
which is exactly when a binding must survive. It also lets you bind a domain
before its target has ever been deployed, which is the natural order for a
first launch.

--redirect-to serves a 308 to another hostname instead of proxying. The
apex/www pair is the case for it.

BINDABLE IN ANY STATE, SERVES ONLY WHEN LIVE. Bind before DNS has propagated
and it starts serving by itself once verification and the certificate
complete.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if (target == "") == (redirectTo == "") {
				return fmt.Errorf("give exactly one of --target or --redirect-to: a domain either proxies to something in %q or redirects elsewhere, never both\n"+
					"fix: `forge domain bind %s --env %s --target <workload|frontend>`, or `--redirect-to <hostname>`",
					env, args[0], env)
			}
			c, err := domainClient(cmd.Context(), env, token)
			if err != nil {
				return err
			}
			return runDomainBind(cmd.Context(), c, env, args[0], target, redirectTo, cmd.OutOrStdout())
		},
	}
	addDomainFlags(cmd, &env, &token)
	cmd.Flags().StringVar(&target, "target", "", "Workload or frontend name in --env to serve")
	cmd.Flags().StringVar(&redirectTo, "redirect-to", "", "Serve a 308 to this hostname instead of a target")
	return cmd
}

// domainEnvironmentID resolves --env to the control plane's environment id
// the same way a hosted deploy does: LookupHostedEnvironment, matching the
// name exactly within this project.
//
// Deliberately the READ path, not EnsureEnvironment. Binding a domain must
// not CREATE an environment as a side effect — a typo in --env would
// silently mint an empty env and bind a public hostname to it, and the
// command would report success.
func domainEnvironmentID(ctx context.Context, c cloudCaller, envName string) (string, error) {
	caller, ok := c.(deploytarget.HostedCaller)
	if !ok {
		return "", fmt.Errorf("the control-plane client cannot resolve environments")
	}
	return deploytarget.LookupHostedEnvironment(ctx, caller, hostedProjectName(), envName)
}

func runDomainBind(ctx context.Context, c cloudCaller, envName, hostname, target, redirectTo string, out io.Writer) error {
	d, err := lookupDomain(ctx, c, hostname)
	if err != nil {
		return err
	}
	envID, err := domainEnvironmentID(ctx, c, envName)
	if err != nil {
		return err
	}
	req := map[string]any{"domainId": d.ID, "environmentId": envID}
	if target != "" {
		req["target"] = target
	} else {
		req["redirectTo"] = redirectTo
	}
	var resp struct {
		Binding domainWireBinding `json:"binding"`
	}
	if err := c.Call(ctx, procCreateDomainBinding, req, &resp); err != nil {
		return fmt.Errorf("bind domain %q: %w", hostname, err)
	}
	if redirectTo != "" {
		fmt.Fprintf(out, "Bound %s → %s (308 redirect), in %s.\n", hostname, redirectTo, envName)
	} else {
		fmt.Fprintf(out, "Bound %s → %s, in %s.\n", hostname, target, envName)
	}
	// State, not silence: a bound domain that is not live yet is waiting
	// on the author's DNS, and that is the next thing they need told.
	if state := deploytarget.DomainStateName(d.State); state != deploytarget.DomainStateLive {
		fmt.Fprintf(out, "It is %s and will start serving once it verifies.\n", state)
		writeDNSTable(out, d.RequiredRecords)
	}
	return nil
}

// ─── unbind ──────────────────────────────────────────────────────────────────

func newDomainUnbindCmd() *cobra.Command {
	var env, token string
	cmd := &cobra.Command{
		Use:   "unbind <hostname>",
		Short: "Stop a domain serving, and keep the domain",
		Long: `Stop the domain serving, and KEEP the domain.

Its verification survives, so re-binding later costs no DNS work and no new
certificate. To give the hostname up entirely, use ` + "`forge domain rm`" + `.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := domainClient(cmd.Context(), env, token)
			if err != nil {
				return err
			}
			return runDomainUnbind(cmd.Context(), c, args[0], cmd.OutOrStdout())
		},
	}
	addDomainFlags(cmd, &env, &token)
	return cmd
}

func runDomainUnbind(ctx context.Context, c cloudCaller, hostname string, out io.Writer) error {
	d, err := lookupDomain(ctx, c, hostname)
	if err != nil {
		return err
	}
	if d.Binding == nil {
		fmt.Fprintf(out, "%s serves nothing already.\n", hostname)
		return nil
	}
	// By domain id: the caller holding the domain should not need a second
	// read to unbind it.
	if err := c.Call(ctx, procDeleteDomainBinding, map[string]any{"domainId": d.ID}, &struct{}{}); err != nil {
		return fmt.Errorf("unbind domain %q: %w", hostname, err)
	}
	fmt.Fprintf(out, "Unbound %s. The domain and its verification are kept; re-bind it any time.\n", hostname)
	return nil
}
