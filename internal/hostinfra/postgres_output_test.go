package hostinfra

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A first `forge env up` printed initdb's whole transcript into the
// terminal: absolute paths, the locale banner, and "Success. You can now
// start the database server using: pg_ctl -D <abs path> -l logfile start" —
// advice to do by hand what forge had just done. That output belongs in a log
// beside the data (as the IdP's does), and the terminal gets forge's own one
// line.
func TestStartPostgres_KeepsInitdbOutputOutOfTheTerminal(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a real postgres; runs in full mode")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	projectDir := t.TempDir()
	spec := Spec{Name: "postgres", Engine: EnginePostgres, Port: port, Database: "app", User: "postgres", Password: "postgres"}
	t.Cleanup(func() { _ = Stop(projectDir, spec) })

	// Start writes to the process's stdout; capture it.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	captured := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		captured <- string(b)
	}()
	startErr := Start(context.Background(), projectDir, spec)
	os.Stdout = stdout
	_ = w.Close()
	out := <-captured
	if startErr != nil {
		t.Fatalf("Start: %v\noutput:\n%s", startErr, out)
	}

	for _, noise := range []string{"initdb", "pg_ctl", "The files belonging to this database system", projectDir} {
		if strings.Contains(out, noise) {
			t.Errorf("terminal output carries postgres's own transcript (%q):\n%s", noise, out)
		}
	}
	if !strings.Contains(out, "postgres serving on :") {
		t.Errorf("forge's own status line is missing:\n%s", out)
	}
	logBody, err := os.ReadFile(filepath.Join(spec.dataDir(projectDir), "postgres.log"))
	if err != nil {
		t.Fatalf("the transcript should be in postgres.log: %v", err)
	}
	if !strings.Contains(string(logBody), "database system") {
		t.Errorf("postgres.log does not hold initdb's output:\n%s", logBody)
	}
}
