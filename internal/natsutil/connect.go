package natsutil

import (
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Connect establishes a NATS connection with default resilience options.
// Caller-provided opts are appended after defaults, so they can override.
// If NATS_USER and NATS_PASSWORD env vars are set, they are used for auth.
func Connect(url string, opts ...nats.Option) (*nats.Conn, error) {
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
	if user, pass := os.Getenv("NATS_USER"), os.Getenv("NATS_PASSWORD"); user != "" && pass != "" {
		defaults = append(defaults, nats.UserInfo(user, pass))
	}
	return nats.Connect(url, append(defaults, opts...)...)
}
