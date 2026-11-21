package testreport

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestValidateLaneName(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"unit", "integration", "web-e2e", "go_1", "E2E"} {
		if err := ValidateLaneName(ok); err != nil {
			t.Errorf("ValidateLaneName(%q) = %v, want ok", ok, err)
		}
	}
	for _, bad := range []string{"", "run.json", "a/b", "..", ".hidden", "-x", "test:integration", `a\b`, strings.Repeat("a", 65)} {
		if err := ValidateLaneName(bad); err == nil {
			t.Errorf("ValidateLaneName(%q) = nil, want it refused", bad)
		}
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The wire shape is the contract control-plane and every reader parse: the
// field names here are pinned, not derived from the struct under test.
func TestReadRunManifest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, RunManifestFile), `{
  "schema": "forge.test.run/v1",
  "id": "01JD0000000000000000000000",
  "created_at": "2026-11-21T01:02:03Z",
  "commit": "c0ffee",
  "tree_hash": "7ree",
  "dirty": false,
  "branch": "main",
  "base_sha": "ba5e",
  "repo": "github.com/acme/shop",
  "host": "h1",
  "ci": {"provider": "github", "run_id": "42", "run_url": "https://github.com/acme/shop/actions/runs/42", "attempt": 2, "event": "push", "ref": "refs/heads/main"},
  "forge_version": "v1.10.0",
  "project": "shop",
  "lanes": ["unit", "web"]
}`)
	got, err := ReadRunManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := RunManifest{
		Schema: RunManifestSchema, ID: "01JD0000000000000000000000", CreatedAt: time.Date(2026, 11, 21, 1, 2, 3, 0, time.UTC),
		Commit: "c0ffee", TreeHash: "7ree", Branch: "main", BaseSHA: "ba5e", Repo: "github.com/acme/shop", Host: "h1",
		CI:           &ManifestCI{Provider: "github", RunID: "42", RunURL: "https://github.com/acme/shop/actions/runs/42", Attempt: 2, Event: "push", Ref: "refs/heads/main"},
		ForgeVersion: "v1.10.0", Project: "shop", Lanes: []string{"unit", "web"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadRunManifest =\n%+v\nwant\n%+v", got, want)
	}
}

func TestReadLaneManifest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "unit", LaneManifestFile), `{
  "schema": "forge.test.lane/v1",
  "lane": "unit",
  "command": ["go", "test", "-json", "./..."],
  "started_at": "2026-11-21T01:02:03Z",
  "finished_at": "2026-11-21T01:02:04Z",
  "exit_code": 1,
  "format": "go-json",
  "results": "parsed",
  "results_file": "results.json",
  "stream": {"lines": 10, "events": 9, "scrubbed": 1},
  "coverage": [{"path": "cover.out", "format": "go-profile"}, {"path": "covdata/", "format": "go-covdata"}]
}`)
	got, err := ReadLaneManifest(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	if got.ExitCode != 1 || got.Format != LaneFormatGoJSON || got.Results != LaneResultsParsed || got.ResultsFile != LaneResultsFile ||
		got.Stream == nil || *got.Stream != (StreamStats{Lines: 10, Events: 9, Scrubbed: 1}) ||
		len(got.Coverage) != 2 || got.Coverage[1] != (LaneCoverageInput{Path: "covdata/", Format: CoverageGoCovdata}) ||
		!got.FinishedAt.Equal(time.Date(2026, 11, 21, 1, 2, 4, 0, time.UTC)) {
		t.Fatalf("ReadLaneManifest = %+v", got)
	}
}

func TestReadManifestRefusesAnotherSchema(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, RunManifestFile), `{"schema": "forge.test.run/v2"}`)
	if _, err := ReadRunManifest(dir); !errors.Is(err, ErrSchema) {
		t.Fatalf("ReadRunManifest of a v2 run = %v, want ErrSchema", err)
	}
	if _, err := ReadLaneManifest(dir, "missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadLaneManifest of no lane = %v, want ErrNotExist", err)
	}
}

func TestCapturedLanes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, RunManifestFile), `{}`)
	writeFile(t, filepath.Join(dir, "web", LaneManifestFile), `{}`)
	writeFile(t, filepath.Join(dir, "unit", LaneManifestFile), `{}`)
	writeFile(t, filepath.Join(dir, "running", LaneCoverProfile), "mode: atomic\n")
	got, err := CapturedLanes(dir)
	if err != nil || !reflect.DeepEqual(got, []string{"unit", "web"}) {
		t.Fatalf("CapturedLanes = %v, %v; want the lanes with a capture.json", got, err)
	}
	if RunsRoot("/p") != filepath.Join("/p", ".forge", "tests", "runs") || LaneDir("/r", "unit") != filepath.Join("/r", "unit") {
		t.Fatal("layout paths moved")
	}
}
