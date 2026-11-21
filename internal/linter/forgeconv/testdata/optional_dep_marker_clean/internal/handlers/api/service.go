package api

import (
	"log/slog"
)

type Clock interface{ Now() }
type Config struct{}
type EventPublisher interface{}

// Deps holds dependencies for the api service.
type Deps struct {
	Logger *slog.Logger
	Config *Config
	Clock  Clock

	// NATSPublisher is intentionally optional.
	// forge:optional-dep
	NATSPublisher EventPublisher
}
