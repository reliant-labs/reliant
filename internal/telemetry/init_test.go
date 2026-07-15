// Copyright (c) 2025 Reliant Labs
package telemetry

import "testing"

func TestNewReporterFromEnv(t *testing.T) {
	tests := []struct {
		name         string
		devOrTest    bool
		sentryDSN    string
		sentryEnable string // value for SENTRY_ENABLED; "" means unset
		wantSentry   bool
	}{
		{
			name:       "dev with dsn stays noop",
			devOrTest:  true,
			sentryDSN:  "https://public@example.ingest.sentry.io/1",
			wantSentry: false,
		},
		{
			name:       "prod without dsn stays noop",
			devOrTest:  false,
			sentryDSN:  "",
			wantSentry: false,
		},
		{
			name:         "prod with dsn but explicitly disabled stays noop",
			devOrTest:    false,
			sentryDSN:    "https://public@example.ingest.sentry.io/1",
			sentryEnable: "false",
			wantSentry:   false,
		},
		{
			name:       "prod with dsn reports via sentry",
			devOrTest:  false,
			sentryDSN:  "https://public@example.ingest.sentry.io/1",
			wantSentry: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SENTRY_DSN", tt.sentryDSN)
			if tt.sentryEnable != "" {
				t.Setenv("SENTRY_ENABLED", tt.sentryEnable)
			} else {
				t.Setenv("SENTRY_ENABLED", "")
			}

			reporter := NewReporterFromEnv(tt.devOrTest)

			_, isSentry := reporter.(*SentryReporter)
			if isSentry != tt.wantSentry {
				t.Fatalf("NewReporterFromEnv(devOrTest=%v): got sentry=%v, want sentry=%v (%T)",
					tt.devOrTest, isSentry, tt.wantSentry, reporter)
			}
		})
	}
}
