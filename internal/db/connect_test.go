package db

import (
	"testing"
	"time"
)

func TestParseDatabaseDriver(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    DatabaseDriver
		wantErr bool
	}{
		{name: "default empty", input: "", want: DriverPostgres},
		{name: "postgres", input: "postgres", want: DriverPostgres},
		{name: "sqlite rejected", input: "sqlite", wantErr: true},
		{name: "invalid", input: "mysql", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDatabaseDriver(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestConnectGivesUpAfterConnectTimeoutOnAnUnreachableDatabase(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: waits out a 1s connect budget; runs in the full lane")
	}
	start := time.Now()
	_, err := ConnectWithConfig(DatabaseConfig{
		Driver: DriverPostgres,
		URL:    "postgres://reliant@127.0.0.1:1/reliant?sslmode=disable",
		Pool:   Pool{ConnectTimeout: time.Second},
	})
	if err == nil {
		t.Fatal("connecting to a closed port succeeded")
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Errorf("gave up after %s, want the whole 1s budget spent retrying", elapsed)
	}
}
