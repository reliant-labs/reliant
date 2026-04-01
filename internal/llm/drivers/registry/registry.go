// Copyright (c) 2025 Reliant Labs
package registry

import (
	"context"
	"sync"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// Client represents the interface that all driver clients must implement
// This interface is used by the driver registry for factory functions
type Client interface {
	Name() string
	SendMessages(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) (*llm.DriverResponse, error)
	StreamResponse(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) <-chan llm.DriverEvent
	ValidateKey(ctx context.Context) error
}

// DriverFactory is a function that creates a driver client from options
// Returns the Client interface and any error during creation
type DriverFactory func(opts *llm.DriverOptions) (Client, error)

// registry holds registered driver factories
var (
	driverRegistry   = make(map[models.Family]DriverFactory)
	driverRegistryMu sync.RWMutex
)

// RegisterDriver registers a driver factory for a given family
// This is typically called from each driver's init() function
func RegisterDriver(family models.Family, factory DriverFactory) {
	driverRegistryMu.Lock()
	defer driverRegistryMu.Unlock()
	driverRegistry[family] = factory
}

// GetDriverFactory retrieves a registered driver factory
func GetDriverFactory(family models.Family) (DriverFactory, bool) {
	driverRegistryMu.RLock()
	defer driverRegistryMu.RUnlock()
	factory, ok := driverRegistry[family]
	return factory, ok
}
