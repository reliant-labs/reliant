package api

import (
	"log/slog"
)

type Clock interface{ Now() }
type Config struct{}
type EventPublisher interface{}

// Deps holds dependencies for the api service.
//
// forge:optional-dep
type Deps struct {
	Logger *slog.Logger
	Config *Config
	Clock  Clock

	NATSPublisher EventPublisher
}

// helper does nothing.
//
// forge:optional-dep
func helper() {}
