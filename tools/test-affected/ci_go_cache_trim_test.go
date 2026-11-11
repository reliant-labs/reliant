package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestGoCacheJobSavesOnlyWhatItUsed pins ci.yml's go-cache job (reliant#2061):
// it notes the time before the setup action restores the build cache, and
// trims the cache to the entries its builds used after the last build and
// before the save. Without the trim the entry every Go job restores grew by
// every superseded compile on main (3.3 -> 3.8 GB compressed in a day) until
// the repository's 10 GB cache budget evicted it.
func TestGoCacheJobSavesOnlyWhatItUsed(t *testing.T) {
	steps := loadCIWorkflow(t).job(t, "go-cache").Steps
	at := func(pred func(ciStep) bool) int {
		for i, s := range steps {
			if pred(s) {
				return i
			}
		}
		return -1
	}
	noted := at(func(s ciStep) bool { return strings.Contains(s.Run, "GOCACHE_RESTORED_AT=$(date +%s)") })
	setup := at(func(s ciStep) bool { return s.Uses == "./.github/actions/setup" })
	lastBuild := -1
	for i, s := range steps {
		if strings.Contains(s.Run, "task build:packages") || strings.Contains(s.Run, "task deps:go-protoc-plugins") {
			lastBuild = i
		}
	}
	trim := at(func(s ciStep) bool {
		return strings.Contains(s.Run, "GOCACHE_RESTORED_AT") && strings.Contains(s.Run, "-mmin +") && strings.Contains(s.Run, "-delete")
	})
	save := at(func(s ciStep) bool {
		return strings.HasPrefix(s.Uses, "actions/cache/save") && s.With["path"] == "~/.cache/go-build"
	})
	switch {
	case noted < 0 || setup < 0 || noted > setup:
		t.Errorf("go-cache must record GOCACHE_RESTORED_AT before the setup action restores the build cache (note at %d, setup at %d)", noted, setup)
	case lastBuild < 0 || save < 0:
		t.Errorf("go-cache must build (task build:packages, task deps:go-protoc-plugins) and save ~/.cache/go-build (builds end at %d, save at %d)", lastBuild, save)
	case trim < 0 || trim < lastBuild || trim > save:
		t.Errorf("go-cache must trim the build cache after its last build (%d) and before the save (%d); trim at %d", lastBuild, save, trim)
	}
	if trim >= 0 && !strings.Contains(steps[trim].Run, "+ 65") {
		t.Errorf("the trim must keep an hour of slack beyond the restore: Go refreshes a used entry's mtime only when it is over an hour old\n%s", steps[trim].Run)
	}
}

// TestFreeDiskOnlyWhenTheDiskIsShort pins the setup action's two free-disk
// steps to skip the deletion on a runner that already has the room the gates
// were proven in (reliant#2061: ~110s of every release-gate part's setup went
// to freeing space on a disk with 88 GB free).
func TestFreeDiskOnlyWhenTheDiskIsShort(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot, ".github/actions/setup/action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var action struct {
		Runs struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Run  string            `yaml:"run"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(raw, &action); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, s := range action.Runs.Steps {
		if !strings.Contains(s.Run, "rm -rf /usr/share/dotnet") {
			continue
		}
		seen++
		check := strings.Index(s.Run, `-ge "${FREE_DISK_MIN_GB}"`)
		rm := strings.Index(s.Run, "rm -rf /usr/share/dotnet")
		if s.Env["FREE_DISK_MIN_GB"] == "" || check < 0 || check > rm {
			t.Errorf("step %q must compare the free space with FREE_DISK_MIN_GB before it deletes anything", s.Name)
		}
	}
	if seen != 2 {
		t.Fatalf("found %d free-disk steps, want 2 (waiting and background)", seen)
	}
}
