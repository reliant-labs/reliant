// Copyright (c) 2025 Reliant Labs
//
// forge:exclude-contract
//
// Registry/lookup-table package: the exported vars are populated once at init
// by the packages that register into them, then read. A getter returns the
// same map or slice header, so it moves the mutation surface without
// narrowing it.
package types

import (
	"context"
	"time"
)

// EvaluationContext contains context for evaluating feature flags
type EvaluationContext struct {
	UserID      string                 `json:"user_id,omitempty"`
	SessionID   string                 `json:"session_id,omitempty"`
	Environment string                 `json:"environment,omitempty"`
	Version     string                 `json:"version,omitempty"`
	Country     string                 `json:"country,omitempty"`
	DeviceType  string                 `json:"device_type,omitempty"`
	Browser     string                 `json:"browser,omitempty"`
	Language    string                 `json:"language,omitempty"`
	IPAddress   string                 `json:"ip_address,omitempty"`
	Timestamp   time.Time              `json:"timestamp,omitempty"`
	Custom      map[string]interface{} `json:"custom,omitempty"`
}

// NewEvaluationContext creates a new evaluation context
func NewEvaluationContext() *EvaluationContext {
	return &EvaluationContext{
		Timestamp: time.Now(),
		Custom:    make(map[string]interface{}),
	}
}

// WithUserID sets the user ID
func (ec *EvaluationContext) WithUserID(userID string) *EvaluationContext {
	ec.UserID = userID
	return ec
}

// WithSessionID sets the session ID
func (ec *EvaluationContext) WithSessionID(sessionID string) *EvaluationContext {
	ec.SessionID = sessionID
	return ec
}

// WithEnvironment sets the environment
func (ec *EvaluationContext) WithEnvironment(env string) *EvaluationContext {
	ec.Environment = env
	return ec
}

// WithCustomAttribute adds a custom attribute
func (ec *EvaluationContext) WithCustomAttribute(key string, value interface{}) *EvaluationContext {
	if ec.Custom == nil {
		ec.Custom = make(map[string]interface{})
	}
	ec.Custom[key] = value
	return ec
}

// SettingsGetter interface for retrieving settings
type SettingsGetter interface {
	GetString(ctx context.Context, key string) (string, error)
	GetBool(ctx context.Context, key string) (bool, error)
	GetInt(ctx context.Context, key string) (int, error)
	GetFloat(ctx context.Context, key string) (float64, error)
}
