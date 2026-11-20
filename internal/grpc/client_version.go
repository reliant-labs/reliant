// Copyright (c) 2026 Reliant Labs
package grpc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/clientversion"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/observability"
)

// The upgrade_required refusal's google.rpc.ErrorInfo: a standard detail
// every Connect client can decode without importing a reliant proto.
const (
	upgradeRequiredInfoReason = "UPGRADE_REQUIRED"
	upgradeRequiredInfoDomain = "reliantlabs.io"
)

// What the gate counts (observability.ClientVersionTotal's outcome label).
const (
	clientOutcomeMet                 = "met"
	clientOutcomeBelow               = "below"
	clientOutcomeRefused             = "refused"
	clientOutcomeNoFloor             = "no_floor"
	clientOutcomeUnparseable         = "unparseable"
	clientOutcomeUnidentified        = "unidentified"
	clientOutcomeUnidentifiedRefused = "unidentified_refused"
	clientOutcomeLegacyNotFound      = "legacy_not_found"

	clientKindNone    = "none"
	clientKindUnknown = "unknown"
)

const (
	reliantV1ProcedurePrefix = "/reliant.v1."
	systemProcedurePrefix    = "/" + reliantv1connect.SystemServiceName + "/"
)

// clientVersionGated reports whether the client version policy applies to a
// procedure (or URL path): every reliant.v1 rpc but SystemService's, which a
// stale client must always reach to learn the floors. Nothing outside
// reliant.v1 — health, webhooks, OAuth callbacks, metrics, the plain-HTTP
// routes — is ever refused for its client.
func clientVersionGated(procedure string) bool {
	return strings.HasPrefix(procedure, reliantV1ProcedurePrefix) &&
		!strings.HasPrefix(procedure, systemProcedurePrefix)
}

// clientVersionGate is the user chain's client version check
// (ClientVersionPolicy, ServerProcessConfig.client_versions). It reads the
// Reliant-Client header of every gated rpc, unary and streaming, counts it
// (reliant_client_version_total{kind,outcome}) and stamps it on the server
// span. It refuses only when the policy says so:
//
//   - enforce: a client whose version parses and orders below its kind's
//     floor gets FailedPrecondition, x-forge-error-reason upgrade_required
//     and an ErrorInfo detail naming the floor and how to upgrade;
//   - refuse_unidentified: a call with no header at all gets the same code
//     and reason. (A browser-origin one never gets here: the API's edge
//     answers it a bare 404 first, refuseLegacyUnidentified.)
//
// A header that does not parse is never refused, whatever the policy: an
// unknown kind or version cannot be compared to anything. It sits in the
// chain's Extras, after authentication, so a caller is authenticated before
// learning the deployment's floors from a refusal (the floors are public on
// SystemService anyway).
type clientVersionGate struct {
	policy *configv1.ClientVersionPolicy
}

func newClientVersionGate(policy *configv1.ClientVersionPolicy) clientVersionGate {
	return clientVersionGate{policy: policy}
}

// check judges one call; a non-nil error is the refusal to answer it with.
func (g clientVersionGate) check(ctx context.Context, procedure string, header http.Header) error {
	if !clientVersionGated(procedure) {
		return nil
	}
	raw := header.Get(clientversion.Header)
	client, err := clientversion.Parse(raw)
	switch {
	case errors.Is(err, clientversion.ErrMissing):
		if g.policy.GetRefuseUnidentified() {
			countClient(clientKindNone, clientOutcomeUnidentifiedRefused)
			logging.InfoContext(ctx, "refused a call with no Reliant-Client header (refuse_unidentified)",
				"procedure", procedure)
			return unidentifiedRefusal()
		}
		countClient(clientKindNone, clientOutcomeUnidentified)
		return nil
	case err != nil:
		countClient(clientKindUnknown, clientOutcomeUnparseable)
		logging.DebugContext(ctx, "unparseable Reliant-Client header; served",
			"procedure", procedure, "reliant_client", raw, "error", err)
		return nil
	}

	trace.SpanFromContext(ctx).SetAttributes(
		attribute.String("reliant.client.kind", string(client.Kind)),
		attribute.String("reliant.client.version", client.Version),
	)
	floor := clientversion.FloorFor(g.policy, client.Kind)
	switch clientversion.Judge(client.Version, floor.MinVersion) {
	case clientversion.Met:
		countClient(string(client.Kind), clientOutcomeMet)
	case clientversion.Below:
		if g.policy.GetEnforce() {
			countClient(string(client.Kind), clientOutcomeRefused)
			logging.InfoContext(ctx, "refused a client below its minimum version",
				"procedure", procedure, "client_kind", client.Kind, "client_version", client.Version,
				"min_version", floor.MinVersion)
			return upgradeRefusal(client, floor)
		}
		countClient(string(client.Kind), clientOutcomeBelow)
		logging.InfoContext(ctx, "client below its minimum version (served: enforce is off)",
			"procedure", procedure, "client_kind", client.Kind, "client_version", client.Version,
			"min_version", floor.MinVersion)
	default:
		countClient(string(client.Kind), clientOutcomeNoFloor)
	}
	return nil
}

func (g clientVersionGate) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if err := g.check(ctx, req.Spec().Procedure, req.Header()); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

// WrapStreamingClient is the identity: this interceptor only ever wraps
// handlers.
func (clientVersionGate) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (g clientVersionGate) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if err := g.check(ctx, conn.Spec().Procedure, conn.RequestHeader()); err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

func countClient(kind, outcome string) {
	observability.ClientVersionTotal.WithLabelValues(kind, outcome).Inc()
}

// upgradeRefusal is the refusal of a client below its floor.
func upgradeRefusal(client clientversion.Client, floor clientversion.Floor) error {
	how := floor.UpgradeCommand
	if how == "" {
		how = floor.UpgradeURL
	}
	if how == "" {
		how = "install the latest version"
	}
	msg := fmt.Sprintf("this %s (%s) is older than the minimum this server supports (%s); upgrade: %s",
		client.Kind, client.Version, floor.MinVersion, how)
	return upgradeRequired(msg, string(client.Kind), client.Version, floor)
}

// unidentifiedRefusal is the refusal of a call with no Reliant-Client header
// while the policy refuses those.
func unidentifiedRefusal() error {
	return upgradeRequired("this client sends no Reliant-Client version; upgrade to the latest version",
		"", "", clientversion.Floor{})
}

func upgradeRequired(msg, kind, version string, floor clientversion.Floor) error {
	return svcerr.WithDetail(
		svcerr.WithReason(svcerr.FailedPrecondition(msg), errreason.UpgradeRequired),
		&errdetails.ErrorInfo{
			Reason: upgradeRequiredInfoReason,
			Domain: upgradeRequiredInfoDomain,
			Metadata: map[string]string{
				"client_kind":     kind,
				"client_version":  version,
				"min_version":     floor.MinVersion,
				"upgrade_url":     floor.UpgradeURL,
				"upgrade_command": floor.UpgradeCommand,
			},
		})
}

// refuseLegacyUnidentified is the API edge's lever for web and desktop
// bundles built before clients named themselves: with
// enforce_legacy_unidentified or refuse_unidentified on, a browser-origin
// call (it carries Origin; the CLI, the SDKs and curl send none) to a gated
// reliant.v1 rpc with no Reliant-Client header is answered a bare HTTP 404 —
// no Connect error body — which those bundles read as an unrouted procedure
// (rpcErrorPolicy.ts isUnroutedProcedure) and answer with their "app out of
// date" modal. Nothing else reaches an old tab. It sits inside CORS, so the
// browser lets the page see the 404 instead of a CORS failure; a preflight
// never reaches it.
func refuseLegacyUnidentified(policy *configv1.ClientVersionPolicy) func(http.Handler) http.Handler {
	if !policy.GetEnforceLegacyUnidentified() && !policy.GetRefuseUnidentified() {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodOptions &&
				r.Header.Get("Origin") != "" &&
				strings.TrimSpace(r.Header.Get(clientversion.Header)) == "" &&
				clientVersionGated(r.URL.Path) {
				countClient(clientKindNone, clientOutcomeLegacyNotFound)
				http.NotFound(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
