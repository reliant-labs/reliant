// Copyright (c) 2025 Reliant Labs
package version_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type sourcemapsStep struct {
	Name string            `yaml:"name"`
	If   string            `yaml:"if"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	With map[string]string `yaml:"with"`
}

type sourcemapsJob struct {
	Needs           any              `yaml:"needs"`
	If              string           `yaml:"if"`
	Environment     string           `yaml:"environment"`
	ContinueOnError bool             `yaml:"continue-on-error"`
	Steps           []sourcemapsStep `yaml:"steps"`
}

// TestReleaseFilesTheShippedRenderersSourceMaps pins reliant#2535's release
// wiring: the source maps a desktop release files are the maps of the
// renderer its installers ship.
//
// A tag that reuses a prebuild (ELECTRON_REUSE_PREBUILD) publishes the
// PREBUILD's installers, and a rebuild of the same commit and version is not
// the same bundle: it embeds its build time (vite-plugin-version-json.ts,
// __RELIANT_BUILD__), so every content-hashed chunk differs and the rebuild's
// maps symbolicate nothing the users run. So a prebuild uploads its maps as
// well, and the sourcemaps job takes them from the run whose installers ship:
// the prebuild's when prepare names one (prebuilt_run), this run's otherwise.
// Also held: maps are uploaded before they are stripped from the installers'
// renderer, and the job reads its credential from the release environment and
// can never fail a release.
func TestReleaseFilesTheShippedRenderersSourceMaps(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join(repoRootFromTest(t), ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Jobs map[string]sourcemapsJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &wf); err != nil {
		t.Fatal(err)
	}

	prepare, ok := wf.Jobs["prepare"]
	if !ok {
		t.Fatal("release.yml has no prepare job")
	}
	index := func(steps []sourcemapsStep, match func(sourcemapsStep) bool) int {
		for i, s := range steps {
			if match(s) {
				return i
			}
		}
		return -1
	}
	upload := index(prepare.Steps, func(s sourcemapsStep) bool {
		return strings.HasPrefix(s.Uses, "actions/upload-artifact@") && s.With["name"] == "web-dist-sourcemaps"
	})
	strip := index(prepare.Steps, func(s sourcemapsStep) bool {
		return strings.Contains(s.Run, "-name '*.map' -delete")
	})
	dist := index(prepare.Steps, func(s sourcemapsStep) bool {
		return strings.HasPrefix(s.Uses, "actions/upload-artifact@") && s.With["name"] == "web-dist"
	})
	marker := index(prepare.Steps, func(s sourcemapsStep) bool { return s.Name == "Upload the prebuild marker" })
	switch {
	case upload < 0:
		t.Fatal("prepare uploads no web-dist-sourcemaps artifact")
	case strip < 0:
		t.Fatal("prepare no longer removes the .map files from the installers' renderer")
	case dist < 0 || marker < 0:
		t.Fatal("prepare's web-dist upload or prebuild marker upload is gone: update this test with the workflow")
	case !(upload < strip && strip < dist):
		t.Fatalf("prepare must upload the maps (step %d), then strip them (step %d), then upload web-dist (step %d)", upload, strip, dist)
	}
	up := prepare.Steps[upload]
	if !strings.Contains(up.If, "inputs.prebuild_version != ''") || !strings.Contains(up.If, "github.ref_type == 'tag'") {
		t.Errorf("web-dist-sourcemaps is uploaded only when %q: a prebuild must upload its maps too, or a tag that reuses its installers files a different build's maps", up.If)
	}
	keep, _ := strconv.Atoi(up.With["retention-days"])
	markerKeep, _ := strconv.Atoi(prepare.Steps[marker].With["retention-days"])
	if keep < markerKeep {
		t.Errorf("web-dist-sourcemaps is kept %d day(s), the prebuild marker %d: a tag can reuse a prebuild whose maps are already gone", keep, markerKeep)
	}

	job, ok := wf.Jobs["sourcemaps"]
	if !ok {
		t.Fatal("release.yml has no sourcemaps job")
	}
	if job.Environment != "release" {
		t.Errorf("the sourcemaps job's environment is %q, want release: RELIANT_SOURCEMAPS_TOKEN is that environment's secret", job.Environment)
	}
	if !job.ContinueOnError {
		t.Error("the sourcemaps job must be continue-on-error: unfiled maps must never fail a release that shipped")
	}
	var fromThisRun, fromPrebuild int
	for _, s := range job.Steps {
		if !strings.HasPrefix(s.Uses, "actions/download-artifact@") || s.With["name"] != "web-dist-sourcemaps" {
			continue
		}
		switch {
		case s.If == "needs.prepare.outputs.prebuilt_run == ''" && s.With["run-id"] == "":
			fromThisRun++
		case s.If == "needs.prepare.outputs.prebuilt_run != ''" && s.With["run-id"] == "${{ needs.prepare.outputs.prebuilt_run }}":
			fromPrebuild++
		default:
			t.Errorf("step %q downloads web-dist-sourcemaps when %q from run %q: it must be this run's maps exactly when this run's installers ship, the prebuild's otherwise", s.Name, s.If, s.With["run-id"])
		}
	}
	if fromThisRun != 1 || fromPrebuild != 1 {
		t.Errorf("the sourcemaps job downloads this run's maps %d time(s) and the prebuild's %d time(s), want one each", fromThisRun, fromPrebuild)
	}
}
