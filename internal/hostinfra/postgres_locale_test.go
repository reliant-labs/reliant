package hostinfra

import (
	"context"
	"net"
	"reflect"
	"testing"
)

// initdb derived its locale from LANG/LC_*, so a macOS shell exporting
// LC_ALL=C.UTF-8 (a locale macOS does not have) failed every first
// `forge env up` with "invalid locale settings". postgresConfig names the
// locale and encoding itself, the way pgtest does for the test databases.
func TestPostgresConfig_NamesItsLocaleAndEncoding(t *testing.T) {
	cfg := reflect.ValueOf(postgresConfig(Spec{Name: "postgres", Port: 5432}, t.TempDir()))
	for field, want := range map[string]string{"locale": "C", "encoding": "UTF8"} {
		f := cfg.FieldByName(field)
		if !f.IsValid() {
			t.Fatalf("embeddedpostgres.Config has no %q field; re-check how the locale reaches initdb", field)
		}
		if got := f.String(); got != want {
			t.Errorf("postgresConfig %s = %q, want %q: initdb would otherwise read it from LANG/LC_*", field, got, want)
		}
	}
}

// The behaviour itself: a locale environment initdb cannot honour must not
// stop forge's postgres from initializing and serving. A bogus LC_ALL is the
// portable stand-in for macOS's missing C.UTF-8 (Linux has C.UTF-8).
func TestStartPostgres_IgnoresAnUnusableLocaleEnvironment(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a real postgres; runs in full mode")
	}
	t.Setenv("LC_ALL", "xx_XX.UTF-8")
	t.Setenv("LANG", "xx_XX.UTF-8")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	projectDir := t.TempDir()
	spec := Spec{Name: "postgres", Engine: EnginePostgres, Port: port, Database: "app", User: "postgres", Password: "postgres"}
	t.Cleanup(func() { _ = Stop(projectDir, spec) })

	if err := Start(context.Background(), projectDir, spec); err != nil {
		t.Fatalf("Start under LC_ALL=xx_XX.UTF-8: %v", err)
	}
}
