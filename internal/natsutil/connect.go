// Package natsutil is how every reliant role connects to NATS and subscribes
// on it: the connection's resilience options, and subscriptions that survive
// the broker refusing them.
//
//forge:lint-disable-next-line forge-exclude-contract-outbound-io: the package IS the NATS client setup every role shares; its callers take *nats.Conn and *natsutil.Subscription directly, and a contract over them would mock nats.go itself
//forge:exclude-contract: thin helpers over *nats.Conn; Subscription is a concrete handle the caller owns, with no second implementation to substitute
package natsutil

import (
	"errors"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Endpoint is a NATS server and the credentials to present to it.
type Endpoint struct {
	// URL is the server URL; it may embed credentials (nats://user:pass@host).
	URL string
	// User and Password authenticate out of band; used only when both are
	// set.
	User     string
	Password string
}

// Connect establishes a NATS connection with default resilience options.
// Caller-provided opts are appended after defaults, so they can override.
// The endpoint's User and Password, when both are set, are used for auth.
//
// Each reliant role (api-server, worker, gateway) connects as its own NATS
// user with least-privilege grants (control-plane deploy/nats/permissions.yaml),
// which brings two failure modes this connection handles:
//
//   - An auth refusal is retried, never final (IgnoreAuthErrorAbort). A rollout
//     can restart this process before every broker knows its user; nats.go
//     would otherwise close the connection for good on the second refusal and
//     the process would stay off NATS after the broker caught up.
//   - A permissions violation (publishing or subscribing outside the role's
//     grants) drops only that message or subscription, so it is logged at
//     ERROR with the subject: a feature is broken, and the fix is a line in
//     permissions.yaml. The exception is a subscription made with
//     SubscribeWithRecovery: its refusal is routed to it, and it re-subscribes
//     until the broker's grants catch up (logged at WARN per retry).
func Connect(ep Endpoint, opts ...nats.Option) (*nats.Conn, error) {
	defaults := []nats.Option{
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2 * time.Second),
		nats.IgnoreAuthErrorAbort(),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			logging.Warn("NATS disconnected", "error", err)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			logging.Info("NATS reconnected", "url", nc.ConnectedUrl())
		}),
		nats.ClosedHandler(func(_ *nats.Conn) {
			logging.Warn("NATS connection closed")
		}),
		nats.ErrorHandler(func(_ *nats.Conn, sub *nats.Subscription, err error) {
			asyncError(sub, err)
		}),
	}
	if ep.User != "" && ep.Password != "" {
		defaults = append(defaults, nats.UserInfo(ep.User, ep.Password))
	}
	return nats.Connect(ep.URL, append(defaults, opts...)...)
}

// asyncError logs an error raised outside any call: a permissions violation
// or a credential refusal at ERROR, anything else (a slow consumer) at WARN.
func asyncError(sub *nats.Subscription, err error) {
	if err == nil {
		return
	}
	subscription := ""
	if sub != nil {
		subscription = sub.Subject
	}
	msg := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, nats.ErrPermissionViolation) || strings.Contains(msg, "permissions violation"):
		logging.Error("NATS refused an operation outside this service's permissions; grant the subject in control-plane deploy/nats/permissions.yaml if the code needs it",
			"error", err, "subscription", subscription)
	case errors.Is(err, nats.ErrAuthorization) || strings.Contains(msg, "authorization violation"):
		logging.Error("NATS refused this service's credentials; the client keeps retrying (is this role's NATS user in the broker's config yet? control-plane docs/nats-auth.md)",
			"error", err)
	default:
		logging.Warn("NATS async error", "error", err, "subscription", subscription)
	}
}
