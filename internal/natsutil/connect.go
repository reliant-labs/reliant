package natsutil

import (
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
func Connect(ep Endpoint, opts ...nats.Option) (*nats.Conn, error) {
	defaults := []nats.Option{
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2 * time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			logging.Warn("NATS disconnected", "error", err)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			logging.Info("NATS reconnected", "url", nc.ConnectedUrl())
		}),
		nats.ClosedHandler(func(_ *nats.Conn) {
			logging.Warn("NATS connection closed")
		}),
	}
	if ep.User != "" && ep.Password != "" {
		defaults = append(defaults, nats.UserInfo(ep.User, ep.Password))
	}
	return nats.Connect(ep.URL, append(defaults, opts...)...)
}
