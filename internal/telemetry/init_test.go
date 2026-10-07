// Copyright (c) 2025 Reliant Labs
package telemetry

import "testing"

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
