package cli

// `forge obs tokens list|create|rotate|revoke`: the telemetry ingest tokens of
// a project's envs (control-plane TelemetryTokenService). `forge obs init`
// mints the first one; these manage the rest of their life — a token per CI
// system or cluster, rotation before expiry, revocation of a leaked one.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsout"
)

// obsAdminDeps are the seams of the token and receiver verbs.
type obsAdminDeps struct {
	Connect func(ctx context.Context, controlPlane, project, org string, bind bool) (obsSession, error)
	Stdout  io.Writer
}

// obsAdminDepsOverride replaces the real seams in tests.
var obsAdminDepsOverride *obsAdminDeps

func defaultObsAdminDeps(cmd *cobra.Command) obsAdminDeps {
	if o := obsAdminDepsOverride; o != nil {
		return *o
	}
	return obsAdminDeps{Connect: connectObsControlPlane, Stdout: obsout.NewSanitizingWriter(cmd.OutOrStdout())}
}

// obsAdminSession signs in to the project's control plane: --control-plane,
// else forge.yaml monitoring.control_plane, else Reliant's.
func obsAdminSession(ctx context.Context, d obsAdminDeps) (obsSession, string, error) {
	_, cfg, err := initConfig()
	if err != nil {
		return obsSession{}, "", err
	}
	project := obsProjectName()
	if project == "" {
		return obsSession{}, "", obsbackend.Wrong("name the project: run from a directory with a forge.yaml, or pass --project <name>")
	}
	var mon *config.MonitoringConfig
	if cfg != nil {
		mon = cfg.Monitoring
	}
	cp := monitoringControlPlane(mon)
	if f := obsControlPlaneFlag; f != "" {
		cp = f
	}
	s, err := d.Connect(ctx, cp, project, "", false)
	return s, project, err
}

// envID resolves env of project on the session's control plane.
func (s obsSession) envID(ctx context.Context, project, env string) (string, error) {
	id, err := deploytarget.LookupHostedEnvironment(ctx, s.Caller, project, env)
	if errors.Is(err, deploytarget.ErrHostedEnvironmentNotFound) {
		return "", obsbackend.Wrong("env %s of project %s is not registered on %s", env, project, s.ControlPlane).
			With(obsbackend.Cmd("forge", "obs", "init", env))
	}
	if err != nil {
		return "", obsbackend.MapCloudError(err)
	}
	return id, nil
}

func newObsTokensCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tokens",
		Short: "List, create, rotate and revoke an env's telemetry ingest tokens",
		Long: `Telemetry ingest tokens are what an app forge does not deploy sends its OTLP
with (OTEL_EXPORTER_OTLP_HEADERS=authorization=Bearer <token>). Each is for one
env, has a name, expires (90 days by default, and at most: the control plane
refuses a longer --ttl), and nothing renews it: rotate it before it expires
(list says "expires soon: rotate it" in its last 14 days), with a grace period
so the old one keeps working while the new one rolls out, or revoke it at once. Minting, rotating and revoking need the obs:write
permission (owners and admins hold it); listing needs membership.

  forge obs tokens list prod
  forge obs tokens create prod --name github-actions
  forge obs tokens rotate <id> --grace 24h     # the old one keeps working for 24h
  forge obs tokens revoke <id> --reason leaked`,
	}
	cmd.AddCommand(newObsTokensListCmd(), newObsTokensCreateCmd(), newObsTokensRotateCmd(), newObsTokensRevokeCmd())
	return cmdutil.StrictGroup(cmd)
}

func newObsTokensListCmd() *cobra.Command {
	var jsonOut, all bool
	cmd := &cobra.Command{
		Use:   "list [env]",
		Short: "List ingest tokens (metadata only; a token is shown once, at mint)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := defaultObsAdminDeps(cmd)
			ctx := cmd.Context()
			s, project, err := obsAdminSession(ctx, d)
			if err != nil {
				return err
			}
			req := map[string]any{"orgId": s.OrgID, "includeRevoked": all}
			if len(args) == 1 {
				id, err := s.envID(ctx, project, args[0])
				if err != nil {
					return err
				}
				req["environmentId"] = id
			}
			var out struct {
				Tokens []wireIngestToken `json:"tokens"`
			}
			if err := s.Caller.Call(ctx, procListIngestTokens, req, &out); err != nil {
				return ingestTokenErr(err)
			}
			if jsonOut {
				return writeJSON(d.Stdout, map[string]any{"schema": "forge.obs.tokens/v1", "tokens": emptyIfNil(out.Tokens)})
			}
			if len(out.Tokens) == 0 {
				fmt.Fprintln(d.Stdout, "no ingest tokens")
				return nil
			}
			rows := make([][]string, 0, len(out.Tokens))
			for _, t := range out.Tokens {
				rows = append(rows, []string{t.ID, t.Name, t.EnvironmentID, t.ExpiresAt.Format("2006-01-02"), tokenState(t, time.Now())})
			}
			obsout.Table(d.Stdout, []string{"ID", "NAME", "ENVIRONMENT", "EXPIRES", "STATE"}, rows, 40)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print a forge.obs.tokens/v1 document")
	cmd.Flags().BoolVar(&all, "all", false, "include revoked tokens")
	return cmd
}

// tokenState is one word for a token's standing at now.
func tokenState(t wireIngestToken, now time.Time) string {
	switch {
	case t.RevokedAt != nil && !t.RevokedAt.After(now):
		return "revoked"
	case t.RevokedAt != nil:
		return "rotating (works until " + t.RevokedAt.Format(time.RFC3339) + ")"
	case !t.ExpiresAt.After(now):
		return "expired"
	case t.ExpiresAt.Sub(now) < 14*24*time.Hour:
		return "expires soon: rotate it"
	}
	return "live"
}

func newObsTokensCreateCmd() *cobra.Command {
	var name string
	var ttl time.Duration
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "create <env>",
		Short: "Mint an ingest token for an env (printed once)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := defaultObsAdminDeps(cmd)
			ctx := cmd.Context()
			s, project, err := obsAdminSession(ctx, d)
			if err != nil {
				return err
			}
			id, err := s.envID(ctx, project, args[0])
			if err != nil {
				return err
			}
			tok, err := createIngestToken(ctx, s, id, name, ttl)
			if err != nil {
				return err
			}
			return writeMinted(d.Stdout, tok, jsonOut)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "the token's label (ci, laptop, prod-k8s)")
	_ = cmd.MarkFlagRequired("name")
	cmd.Flags().DurationVar(&ttl, "ttl", 0, "lifetime (default and maximum 90 days, at least 1h)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the token as JSON")
	return cmd
}

func newObsTokensRotateCmd() *cobra.Command {
	var grace, ttl time.Duration
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "rotate <token-id>",
		Short: "Mint a replacement; the old token keeps working for --grace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := defaultObsAdminDeps(cmd)
			ctx := cmd.Context()
			s, _, err := obsAdminSession(ctx, d)
			if err != nil {
				return err
			}
			req := map[string]any{"orgId": s.OrgID, "tokenId": args[0], "grace": fmt.Sprintf("%ds", int64(grace.Seconds()))}
			if ttl > 0 {
				req["ttl"] = fmt.Sprintf("%ds", int64(ttl.Seconds()))
			}
			var out mintedIngestToken
			if err := s.Caller.Call(ctx, procRotateIngestToken, req, &out); err != nil {
				return ingestTokenErr(err)
			}
			return writeMinted(d.Stdout, out, jsonOut)
		},
	}
	cmd.Flags().DurationVar(&grace, "grace", 24*time.Hour, "how long the old token keeps working (0s revokes it now; at most 7 days)")
	cmd.Flags().DurationVar(&ttl, "ttl", 0, "the new token's lifetime (default and maximum 90 days, at least 1h)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the token as JSON")
	return cmd
}

func newObsTokensRevokeCmd() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "revoke <token-id>",
		Short: "Revoke an ingest token now (the collector drops it within minutes)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := defaultObsAdminDeps(cmd)
			ctx := cmd.Context()
			s, _, err := obsAdminSession(ctx, d)
			if err != nil {
				return err
			}
			var out struct {
				Revoked bool `json:"revoked"`
			}
			if err := s.Caller.Call(ctx, procRevokeIngestToken, map[string]any{"orgId": s.OrgID, "tokenId": args[0], "reason": reason}, &out); err != nil {
				return ingestTokenErr(err)
			}
			if out.Revoked {
				fmt.Fprintf(d.Stdout, "revoked %s\n", args[0])
			} else {
				fmt.Fprintf(d.Stdout, "%s was already revoked\n", args[0])
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "why, for the audit trail")
	return cmd
}

// writeMinted prints a freshly minted token: the header to store, once.
func writeMinted(w io.Writer, t mintedIngestToken, jsonOut bool) error {
	if jsonOut {
		return writeJSON(w, map[string]any{"schema": "forge.obs.token/v1", "token": t.Token, "otel_env": map[string]string{
			"OTEL_EXPORTER_OTLP_ENDPOINT": t.OTLPEndpoint, "OTEL_EXPORTER_OTLP_HEADERS": t.OTLPHeaders,
			"OTEL_EXPORTER_OTLP_PROTOCOL": otlpProtocol,
		}, "previous": t.Previous})
	}
	fmt.Fprintf(w, "ingest token %q (%s), expires %s — printed ONCE: store it as a secret\n\n", t.Token.Name, t.Token.ID, t.Token.ExpiresAt.Format("2006-01-02"))
	fmt.Fprintf(w, "  export OTEL_EXPORTER_OTLP_ENDPOINT=%s\n", envQuote(t.OTLPEndpoint))
	fmt.Fprintf(w, "  export OTEL_EXPORTER_OTLP_HEADERS=%s\n", envQuote(t.OTLPHeaders))
	fmt.Fprintf(w, "  export OTEL_EXPORTER_OTLP_PROTOCOL=%s\n", otlpProtocol)
	if p := t.Previous; p != nil && p.RevokedAt != nil {
		fmt.Fprintf(w, "\nthe old token %s keeps working until %s\n", p.ID, p.RevokedAt.Format(time.RFC3339))
	}
	return nil
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// emptyIfNil makes an absent list an empty JSON array.
func emptyIfNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
