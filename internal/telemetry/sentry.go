// Copyright (c) 2025 Reliant Labs
package telemetry

import (
	"regexp"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/reliant-labs/reliant/internal/version"
)

// prereleaseRegex detects prerelease versions (RC, alpha, beta)
var prereleaseRegex = regexp.MustCompile(`(?i)-rc\.|rc[0-9]+|-beta\.|beta[0-9]+|-alpha\.|alpha[0-9]+`)

// SentryReporter implements ErrorReporter using Sentry
type SentryReporter struct {
	mu          sync.RWMutex
	initialized bool
}

// SentryConfig contains configuration for initializing Sentry
type SentryConfig struct {
	DSN              string
	Environment      string // "production" or "prerelease"
	Release          string // e.g., "reliant@1.0.0"
	Debug            bool
	Enabled          bool    // Whether Sentry is enabled at all
	TracesSampleRate float64 // 0.0 to 1.0, default 0.1 for production
}

// NewSentryReporter creates a new Sentry error reporter.
// If config.Enabled is false, returns nil, nil — the caller should use a noop reporter.
func NewSentryReporter(config SentryConfig) (*SentryReporter, error) {
	if !config.Enabled {
		return nil, nil
	}

	dsn := config.DSN

	// Determine environment based on version if not specified
	environment := config.Environment
	if environment == "" {
		if prereleaseRegex.MatchString(version.Version) {
			environment = "prerelease"
		} else {
			environment = "production"
		}
	}

	// Determine release string
	release := config.Release
	if release == "" {
		release = "reliant@" + version.Version
	}

	// Set sample rates: use configured value if > 0, otherwise fall back to defaults
	tracesSampleRate := config.TracesSampleRate
	if tracesSampleRate <= 0 {
		tracesSampleRate = 0.1
		if environment == "prerelease" {
			tracesSampleRate = 1.0
		}
	}

	err := sentry.Init(sentry.ClientOptions{
		Dsn:              dsn,
		Environment:      environment,
		Release:          release,
		Debug:            config.Debug,
		TracesSampleRate: tracesSampleRate,
		// Tags that apply to all events
		Tags: map[string]string{
			"component": "backend",
		},
		BeforeSend: func(event *sentry.Event, hint *sentry.EventHint) *sentry.Event {
			// Add any pre-send filtering or enrichment here
			return event
		},
	})

	if err != nil {
		return nil, err
	}

	reporter := &SentryReporter{
		initialized: true,
	}

	return reporter, nil
}

// CaptureException captures an error with Sentry
func (r *SentryReporter) CaptureException(err error) string {
	r.mu.RLock()
	initialized := r.initialized
	r.mu.RUnlock()

	if !initialized || err == nil {
		return ""
	}

	eventID := sentry.CaptureException(err)
	if eventID != nil {
		return string(*eventID)
	}
	return ""
}

// CaptureMessage captures a message with Sentry
func (r *SentryReporter) CaptureMessage(message string) string {
	r.mu.RLock()
	initialized := r.initialized
	r.mu.RUnlock()

	if !initialized || message == "" {
		return ""
	}

	eventID := sentry.CaptureMessage(message)
	if eventID != nil {
		return string(*eventID)
	}
	return ""
}

// SetUser sets the user context for error reports
func (r *SentryReporter) SetUser(userID string, email string) {
	r.mu.RLock()
	initialized := r.initialized
	r.mu.RUnlock()

	if !initialized {
		return
	}

	sentry.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetUser(sentry.User{
			ID:    userID,
			Email: email,
		})
	})
}

// SetTag sets a tag for error reports
func (r *SentryReporter) SetTag(key string, value string) {
	r.mu.RLock()
	initialized := r.initialized
	r.mu.RUnlock()

	if !initialized {
		return
	}

	sentry.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetTag(key, value)
	})
}

// Flush waits for pending events to be sent
func (r *SentryReporter) Flush(timeoutSeconds int) bool {
	r.mu.RLock()
	initialized := r.initialized
	r.mu.RUnlock()

	if !initialized {
		return true
	}

	return sentry.Flush(time.Duration(timeoutSeconds) * time.Second)
}

// CaptureExceptionWithContext captures an error with additional context.
func (r *SentryReporter) CaptureExceptionWithContext(err error, tags map[string]string, extra map[string]interface{}) string {
	r.mu.RLock()
	initialized := r.initialized
	r.mu.RUnlock()

	if !initialized || err == nil {
		return ""
	}

	currentHub := sentry.CurrentHub()
	if currentHub == nil {
		return r.CaptureException(err)
	}

	hub := currentHub.Clone()
	hub.ConfigureScope(func(scope *sentry.Scope) {
		for k, v := range tags {
			scope.SetTag(k, v)
		}
		for k, v := range extra {
			scope.SetExtra(k, v)
		}
	})

	eventID := hub.CaptureException(err)
	if eventID != nil {
		return string(*eventID)
	}
	return ""
}

// Ensure SentryReporter implements ErrorReporter and ContextualErrorReporter.
var _ ErrorReporter = (*SentryReporter)(nil)
var _ ContextualErrorReporter = (*SentryReporter)(nil)
