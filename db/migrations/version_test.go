package migrations

import (
	"os/exec"
	"path"
	"strings"
	"testing"
)

func TestQueueAllowUnisolatedLandingVersionExceedsBase(t *testing.T) {
	if testing.Short() {
		t.Skip("reads git history through a subprocess, outside Go's test cache; runs in task test")
	}

	// Pin the reviewed BASE, not a moving origin/main.
	const base = "a7bdba80fef9f1e64aec3df2ce06694883b206a9"
	const version = "20261126211429"
	output, err := exec.Command("git", "ls-tree", "-r", "--name-only", base, "--", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("read migration versions at BASE %s: %v\n%s", base, err, output)
	}

	var latest string
	for _, file := range strings.Split(string(output), "\n") {
		if !strings.HasSuffix(file, ".up.sql") {
			continue
		}
		candidate, _, ok := strings.Cut(path.Base(file), "_")
		if ok && candidate > latest {
			latest = candidate
		}
	}
	if latest == "" {
		t.Fatal("no migrations found at BASE")
	}
	if version <= latest {
		t.Fatalf("queue migration version %s must exceed BASE's latest migration %s", version, latest)
	}
}
