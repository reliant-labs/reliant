package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Every temp dir TestMain creates for the whole process is removed after the
// suite: it lives as long as the process, so no test's t.TempDir can own it,
// and an unregistered one is left behind on every run.
func TestTestMainRegistersItsProcessWideTempDirs(t *testing.T) {
	dir := connectStateDir()
	if !strings.HasPrefix(filepath.Base(dir), "forge-cli-state-") || !strings.HasPrefix(dir, os.TempDir()) {
		t.Skipf("connectStateDir is %q, not TestMain's temp dir (MkdirTemp failed?)", dir)
	}
	if !slices.Contains(sharedTempDirs(), dir) {
		t.Errorf("TestMain's connect state dir %s is not registered for removal, so every run leaks it", dir)
	}
}
