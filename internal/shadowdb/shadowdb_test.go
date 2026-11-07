package shadowdb

import (
	"net"
	"strings"
	"testing"
)

func TestToMaintenanceServer(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{
			name: "app db reduced to maintenance db, sslmode preserved",
			dsn:  "postgres://u:p@host:5432/myapp?sslmode=require",
			want: "postgres://u:p@host:5432/postgres?sslmode=require",
		},
		{
			name: "sslmode defaulted to disable when absent",
			dsn:  "postgres://u:p@host:5432/myapp",
			want: "postgres://u:p@host:5432/postgres?sslmode=disable",
		},
		{
			name: "in-network compose host is preserved verbatim (server coords only)",
			dsn:  "postgres://postgres:postgres@postgres:5432/smoke?sslmode=disable",
			want: "postgres://postgres:postgres@postgres:5432/postgres?sslmode=disable",
		},
		{
			name: "unparseable dsn yields empty",
			dsn:  "://not a url",
			want: "",
		},
		{
			name: "hostless dsn yields empty",
			dsn:  "postgres:///onlypath",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toMaintenanceServer(tt.dsn); got != tt.want {
				t.Fatalf("toMaintenanceServer(%q) = %q, want %q", tt.dsn, got, tt.want)
			}
		})
	}
}

func TestToMaintenanceServerNeverKeepsAppDatabase(t *testing.T) {
	// The app database name must never survive reduction — the shadow is
	// created off the maintenance DB so the app DB is never opened/mutated.
	got := toMaintenanceServer("postgres://u:p@host:5432/production_data?sslmode=disable")
	if strings.Contains(got, "production_data") {
		t.Fatalf("app database leaked into shadow server URL: %q", got)
	}
	if !strings.Contains(got, "/postgres?") {
		t.Fatalf("expected maintenance db /postgres, got %q", got)
	}
}

func TestDevStackConventionDSNHonorsEnv(t *testing.T) {
	t.Setenv("POSTGRES_USER", "alice")
	t.Setenv("POSTGRES_PASSWORD", "s3cr3t")
	t.Setenv("POSTGRES_PORT", "6001")
	got := devStackConventionDSN()
	want := "postgres://alice:s3cr3t@localhost:6001/postgres?sslmode=disable"
	if got != want {
		t.Fatalf("devStackConventionDSN() = %q, want %q", got, want)
	}
}

func TestDevStackConventionDSNDefaults(t *testing.T) {
	t.Setenv("POSTGRES_USER", "")
	t.Setenv("POSTGRES_PASSWORD", "")
	t.Setenv("POSTGRES_PORT", "")
	got := devStackConventionDSN()
	want := "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable"
	if got != want {
		t.Fatalf("devStackConventionDSN() defaults = %q, want %q", got, want)
	}
}

// TestResolveOverrideHonoredWithoutProbe verifies the escape hatch:
// FORGE_TEST_POSTGRES_URL is returned as-is (reduced to the maintenance DB)
// without any reachability probe, so an explicit override is honored even
// when — as here — nothing is actually listening.
func TestResolveOverrideHonoredWithoutProbe(t *testing.T) {
	t.Setenv("FORGE_TEST_POSTGRES_URL", "postgres://u:p@127.0.0.1:"+deadPort(t)+"/whatever?sslmode=disable")
	got := Resolve(t.TempDir())
	if !strings.HasSuffix(got, "/postgres?sslmode=disable") {
		t.Fatalf("override not honored/reduced: %q", got)
	}
}

// TestResolveFallsBackWhenNothingReachable verifies that when no override,
// no exported DATABASE_URL, no per-env config, and the dev-stack convention
// port is closed, Resolve returns "" — the signal for the caller to use
// embedded postgres (the zero-setup / CI path).
func TestResolveFallsBackWhenNothingReachable(t *testing.T) {
	t.Setenv("FORGE_TEST_POSTGRES_URL", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("POSTGRES_USER", "postgres")
	t.Setenv("POSTGRES_PASSWORD", "postgres")
	t.Setenv("POSTGRES_PORT", deadPort(t)) // closed port → probe fails fast
	if got := Resolve(t.TempDir()); got != "" {
		t.Fatalf("expected empty (embedded fallback), got %q", got)
	}
}

// deadPort returns a port that is bound-then-released, so it is almost
// certainly closed for the duration of the test — CanReach against it gets
// an immediate connection-refused.
func deadPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	_ = l.Close()
	return port
}
