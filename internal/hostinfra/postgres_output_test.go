package hostinfra

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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
	spec := Spec{Name: "postgres", Env: "dev", Engine: EnginePostgres, Port: port, Database: "app", User: "postgres", Password: "postgres"}
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
	// The instance's log is .forge/logs/<env>/<name>.log, where the local
	// collector tails it. It holds initdb's transcript AND the running
	// server's own lines, which before went to a temp file unlinked once the
	// server was up.
	logPath := LogPath(projectDir, spec.Env, spec.Name)
	var logBody []byte
	for deadline := time.Now().Add(10 * time.Second); ; {
		logBody, err = os.ReadFile(logPath) // #nosec G304 -- test temp dir
		if err == nil && strings.Contains(string(logBody), "ready to accept connections") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s should hold the running server's log (\"ready to accept connections\"): %v\n%s", logPath, err, logBody)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(string(logBody), "The files belonging to this database system") {
		t.Errorf("%s does not hold initdb's output:\n%s", logPath, logBody)
	}
	// The path an earlier forge wrote still reads the same log.
	if runtime.GOOS != "windows" {
		legacy, err := os.ReadFile(filepath.Join(spec.dataDir(projectDir), "postgres.log"))
		if err != nil || !strings.Contains(string(legacy), "ready to accept connections") {
			t.Errorf("the legacy postgres.log should read the live log: %v", err)
		}
	}
}
