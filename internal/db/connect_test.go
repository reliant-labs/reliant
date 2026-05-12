package db

import (
	"testing"
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

func TestResolveDatabaseConfig_DefaultPostgres(t *testing.T) {
	t.Setenv("DATABASE_DRIVER", "")
	t.Setenv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/reliant?sslmode=disable")

	cfg, err := ResolveDatabaseConfig("/tmp/reliant-test")
	if err != nil {
		t.Fatalf("ResolveDatabaseConfig failed: %v", err)
	}

	if cfg.Driver != DriverPostgres {
		t.Fatalf("expected postgres driver, got %q", cfg.Driver)
	}
	if cfg.DataDir != "/tmp/reliant-test" {
		t.Fatalf("expected data dir to be preserved, got %q", cfg.DataDir)
	}
}

func TestResolveDatabaseConfig_PostgresRequiresURL(t *testing.T) {
	t.Setenv("DATABASE_DRIVER", "postgres")
	t.Setenv("DATABASE_URL", "")

	_, err := ResolveDatabaseConfig("/tmp/reliant-test")
	if err == nil {
		t.Fatal("expected error when DATABASE_URL missing for postgres")
	}
}

func TestResolveDatabaseConfig_PostgresWithURL(t *testing.T) {
	t.Setenv("DATABASE_DRIVER", "postgres")
	t.Setenv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/reliant?sslmode=disable")

	cfg, err := ResolveDatabaseConfig("/tmp/reliant-test")
	if err != nil {
		t.Fatalf("ResolveDatabaseConfig failed: %v", err)
	}

	if cfg.Driver != DriverPostgres {
		t.Fatalf("expected postgres driver, got %q", cfg.Driver)
	}
	if cfg.URL == "" {
		t.Fatal("expected DATABASE_URL to be set")
	}
}
