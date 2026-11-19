package pgtest

import "testing"

// TestPgCtlOptions pins the restart's -o string (cwd_windows.go): the port,
// then every override double-quoted for CMD, in a stable order. A server
// restarted without the overrides would come back without the Windows
// shared-memory settings startParameters decided on.
func TestPgCtlOptions(t *testing.T) {
	got := pgCtlOptions(5433, map[string]string{"fsync": "off", "max_connections": "200"})
	want := `-p 5433 -c fsync="off" -c max_connections="200"`
	if got != want {
		t.Errorf("pgCtlOptions = %q, want %q", got, want)
	}
	if got := pgCtlOptions(1, nil); got != "-p 1" {
		t.Errorf("no overrides: %q, want %q", got, "-p 1")
	}
}
