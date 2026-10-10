// Copyright (c) 2025 Reliant Labs
package telemetry

import (
	"testing"

	"github.com/getsentry/sentry-go"
)

func TestNewReporterFromEnv(t *testing.T) {
	tests := []struct {
		name         string
		reliantEnv   string // value for RELIANT_ENV; "" means unset, as in a prod pod
		sentryDSN    string
		sentryEnable string // value for SENTRY_ENABLED; "" means unset
		wantSentry   bool
	}{
		{
			name:       "dev with dsn stays noop",
			reliantEnv: "dev",
			sentryDSN:  "https://public@example.ingest.sentry.io/1",
			wantSentry: false,
		},
		{
			name:       "test with dsn stays noop",
			reliantEnv: "test",
			sentryDSN:  "https://public@example.ingest.sentry.io/1",
			wantSentry: false,
		},
		{
			name:       "prod without dsn stays noop",
			reliantEnv: "prod",
			sentryDSN:  "",
			wantSentry: false,
		},
		{
			name:         "prod with dsn but explicitly disabled stays noop",
			reliantEnv:   "prod",
			sentryDSN:    "https://public@example.ingest.sentry.io/1",
			sentryEnable: "false",
			wantSentry:   false,
		},
		{
			name:       "prod with dsn reports via sentry",
			reliantEnv: "prod",
			sentryDSN:  "https://public@example.ingest.sentry.io/1",
			wantSentry: true,
		},
		{
			// Prod pods set neither RELIANT_ENV nor NODE_ENV.
			name:       "no environment set is prod and reports via sentry",
			reliantEnv: "",
			sentryDSN:  "https://public@example.ingest.sentry.io/1",
			wantSentry: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("RELIANT_ENV", tt.reliantEnv)
			t.Setenv("NODE_ENV", "")
			t.Setenv("SENTRY_DSN", tt.sentryDSN)
			t.Setenv("SENTRY_ENABLED", tt.sentryEnable)

			reporter := NewReporterFromEnv()

			_, isSentry := reporter.(*SentryReporter)
			if isSentry != tt.wantSentry {
				t.Fatalf("NewReporterFromEnv() with RELIANT_ENV=%q: got sentry=%v, want sentry=%v (%T)",
					tt.reliantEnv, isSentry, tt.wantSentry, reporter)
			}
		})
	}
}

// The deploy states its environment in SENTRY_ENVIRONMENT. Before, the server
// reporter ignored it and guessed from the version string, so the deploy could
// not say which environment its events belong to.
func TestNewReporterFromEnv_EnvironmentFromDeploy(t *testing.T) {
	t.Setenv("RELIANT_ENV", "prod")
	t.Setenv("NODE_ENV", "")
	t.Setenv("SENTRY_DSN", "https://public@example.ingest.sentry.io/1")
	t.Setenv("SENTRY_ENABLED", "")

	for _, tt := range []struct {
		name, env, want string
	}{
		{"declared by the deploy", "staging", "staging"},
		{"unset falls back to the version guess", "", "production"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SENTRY_ENVIRONMENT", tt.env)
			if _, ok := NewReporterFromEnv().(*SentryReporter); !ok {
				t.Fatal("expected a Sentry reporter")
			}
			if got := sentry.CurrentHub().Client().Options().Environment; got != tt.want {
				t.Errorf("Environment = %q, want %q", got, tt.want)
			}
		})
	}
}
