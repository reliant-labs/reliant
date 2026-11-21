package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/session"
)

// A project with a database and a login broker keeps sign-in sessions
// server-side: providers.go builds one Manager and hands it to both the
// broker and the authenticator. Without a database (or a broker) there is no
// Sessions provider and the broker keeps its access-token cookie.
func TestProvidersGraph_SessionsWireTheBrokerAndTheAuthenticator(t *testing.T) {
	with := renderFormatted(t, "providers-graph.go.tmpl", providersGraphData{Module: "example.com/rbc", HasDatabase: true,
		ConfigFields: map[string]bool{}, Sessions: true})
	for _, want := range []string{
		"func NewSessions(d SessionsDeps) (*Sessions, error) {",
		"Store:     session.NewPostgresStore(d.DB.DB),",
		"Refresher: SessionRefresher(d.Config),",
		"authenticator.UseSessions(d.Sessions.Manager)",
		"RegisterLoginBroker(mux, d.Config, d.Logger, WithSessions(sessions))",
		"func (s *Sessions) Run(ctx context.Context) error {",
	} {
		if !strings.Contains(with, want) {
			t.Errorf("providers.go with sessions lacks %q", want)
		}
	}
	// UseSessions is guarded: a nil *Manager in the interface would route
	// every session id to a nil receiver.
	if !strings.Contains(with, "if d.Sessions != nil && d.Sessions.Manager != nil {") {
		t.Error("UseSessions is not guarded against a nil Manager")
	}
	broker := renderFormatted(t, "providers-graph.go.tmpl", providersGraphData{Module: "example.com/rbc", HasDatabase: true,
		ConfigFields: map[string]bool{}, Sessions: true, BrokerConfigType: "WebConfig"})
	for _, want := range []string{"Broker *config.WebConfig", "Refresher: SessionRefresher(d.Broker),"} {
		if !strings.Contains(broker, want) {
			t.Errorf("providers.go reading a command config lacks %q", want)
		}
	}
	without := renderFormatted(t, "providers-graph.go.tmpl", providersGraphData{Module: "example.com/rbc", ConfigFields: map[string]bool{}})
	if strings.Contains(without, "Sessions") || strings.Contains(without, "forge/pkg/session") {
		t.Error("providers.go without a database names sessions")
	}
	if !strings.Contains(without, "RegisterLoginBroker(mux, d.Config, d.Logger)\n") {
		t.Error("providers.go without sessions no longer mounts the broker as before")
	}
}

func TestWriteSessionsMigration(t *testing.T) {
	dir := t.TempDir()
	if err := writeSessionsMigration(dir); err != nil {
		t.Fatal(err)
	}
	migDir := filepath.Join(dir, "db", "migrations")
	entries, err := os.ReadDir(migDir)
	if err != nil || len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), "_create_auth_sessions.up.sql") {
		t.Fatalf("migrations = %v, %v", entries, err)
	}
	body, _ := os.ReadFile(filepath.Join(migDir, entries[0].Name()))
	if !strings.HasPrefix(string(body), "-- forge:backward-compatible") || !strings.Contains(string(body), session.SchemaSQL) ||
		!strings.Contains(string(body), "YOURS from birth") {
		t.Errorf("migration body:\n%s", body)
	}
	// A migration that already creates the table — forge's or the
	// project's own — is not doubled.
	if err := writeSessionsMigration(dir); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, "db", "migrations"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "db", "migrations", "0001_mine.up.sql"),
		[]byte("create table if not exists app.\"auth_sessions\" (id_hash bytea primary key);"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeSessionsMigration(other); err != nil {
		t.Fatal(err)
	}
	for d, want := range map[string]int{migDir: 1, filepath.Join(other, "db", "migrations"): 1} {
		if entries, _ := os.ReadDir(d); len(entries) != want {
			t.Errorf("%s holds %d migrations, want %d", d, len(entries), want)
		}
	}
}
