// Package testreport is forge's test record vocabulary. This file is the run
// directory's layout: the manifests `forge test begin` and `forge test capture`
// write.
package testreport

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// The run directory: what `forge test begin` and `forge test capture` write,
// and what every reader of a local test record (forge test report, the control
// plane's ingest) reads. One directory per run, one subdirectory per lane:
//
//	.forge/tests/runs/<ulid>/
//	  run.json                 RunManifest (forge.test.run/v1)
//	  <lane>/capture.json      LaneManifest (forge.test.lane/v1)
//	  <lane>/results.json      the raw `go test -json` stream, secret-scrubbed
//	  <lane>/cover.out         a -coverprofile the lane's own command wrote
//	  <lane>/covdata/          GOCOVERDIR content from instrumented binaries
//
// A run is one execution of one or more lanes on one tree, so every lane in
// it shares the run's commit, tree hash and CI identity. The documents here
// are the manifests only: normalised results and coverage are separate
// documents derived from the raw files beside them, so a later parser fix can
// re-derive them.

// Schema identifiers of the two manifests.
const (
	RunManifestSchema  = "forge.test.run/v1"
	LaneManifestSchema = "forge.test.lane/v1"
)

// File and directory names of the layout. RunsDir is slash-separated and
// relative to the project root; the rest are relative to a run or lane
// directory.
const (
	RunsDir          = ".forge/tests/runs"
	RunManifestFile  = "run.json"
	LaneManifestFile = "capture.json"
	// LaneResultsFile is the captured `go test -json` stream. A report file
	// copied with `capture --results` keeps a name of its own
	// (LaneManifest.ResultsFile says which).
	LaneResultsFile  = "results.json"
	LaneCoverProfile = "cover.out"
	LaneCoverDataDir = "covdata"
)

// LaneFormat is the format of a lane's results.
type LaneFormat string

// The results formats a lane records.
const (
	LaneFormatGoJSON  LaneFormat = "go-json"
	LaneFormatJUnit   LaneFormat = "junit"
	LaneFormatVitest  LaneFormat = "vitest"
	LaneFormatUnknown LaneFormat = "unknown"
)

// LaneResults says whether a lane's results were recognised.
type LaneResults string

const (
	// LaneResultsParsed: the results file holds the declared format.
	LaneResultsParsed LaneResults = "parsed"
	// LaneResultsUnparsed: a results file was stored, as captured, but it
	// is not (or not only) the declared format — a command that never ran
	// `go test -json`, or a report file of another shape.
	LaneResultsUnparsed LaneResults = "unparsed"
	// LaneResultsMissing: there is no results file — `--results` named a
	// file the command never wrote, or the stream could not be stored.
	LaneResultsMissing LaneResults = "missing"
)

// RunManifest is run.json, written once by `forge test begin`. Only Lanes
// changes afterwards: each `forge test capture` appends its lane.
type RunManifest struct {
	Schema    string    `json:"schema"`
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	// Commit is HEAD, full hex; empty outside git or before the first commit.
	Commit string `json:"commit,omitempty"`
	// TreeHash is the git tree of the content the lanes ran on, uncommitted
	// changes included. Empty when it was not hashed (no git, or a dirty tree
	// too large to hash quickly).
	TreeHash string `json:"tree_hash,omitempty"`
	// Dirty: the checkout had uncommitted changes, or is not a git checkout.
	Dirty bool `json:"dirty"`
	// Branch is the checked-out branch, else the CI's branch name; empty on
	// a detached HEAD outside CI.
	Branch string `json:"branch,omitempty"`
	// BaseSHA is the commit the change is measured against, when known (a
	// pull request's base, a merge group's base, or `begin --base`).
	BaseSHA string `json:"base_sha,omitempty"`
	// Repo is the canonical remote, e.g. "github.com/acme/shop".
	Repo string `json:"repo,omitempty"`
	// Host names the machine (a hash, never a raw hostname).
	Host         string      `json:"host,omitempty"`
	CI           *ManifestCI `json:"ci,omitempty"`
	ForgeVersion string      `json:"forge_version,omitempty"`
	// Project is forge.yaml's name.
	Project string `json:"project,omitempty"`
	// Lanes names the captured lanes, in the order they finished. Each has a
	// directory of that name holding its LaneManifest.
	Lanes []string `json:"lanes"`
	// Warnings are the facts begin could not record, said rather than left
	// as a silently empty field.
	Warnings []string `json:"warnings,omitempty"`
}

// ManifestCI is the CI run a test run belongs to. RunID and Attempt are the
// provider's own values (GitHub keeps GITHUB_RUN_ID across re-runs and counts
// GITHUB_RUN_ATTEMPT), so (provider, run_id, attempt) names one attempt.
type ManifestCI struct {
	Provider string `json:"provider"`
	RunID    string `json:"run_id"`
	RunURL   string `json:"run_url,omitempty"`
	Attempt  int    `json:"attempt,omitempty"`
	// Event is what started the run (GitHub: push, pull_request,
	// merge_group, …), when the provider says.
	Event string `json:"event,omitempty"`
	// Ref is the ref the run was started for (GitHub: GITHUB_REF).
	Ref string `json:"ref,omitempty"`
}

// LaneManifest is <lane>/capture.json, written by `forge test capture` after
// the lane's command exited.
type LaneManifest struct {
	Schema     string    `json:"schema"`
	Lane       string    `json:"lane"`
	Command    []string  `json:"command"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	// ExitCode is the command's exit status, exactly what capture exited
	// with: 128+n for a command killed by signal n, 127 for a command that
	// was not found, 126 for one that could not start.
	ExitCode int `json:"exit_code"`
	// Error says why the command did not run, when it did not.
	Error   string      `json:"error,omitempty"`
	Format  LaneFormat  `json:"format"`
	Results LaneResults `json:"results"`
	// ResultsFile is the results file, relative to the lane directory;
	// empty when Results is missing.
	ResultsFile string `json:"results_file,omitempty"`
	// Stream counts what a captured `go test -json` stream held.
	Stream *StreamStats `json:"stream,omitempty"`
	// Coverage lists the coverage files found in the lane directory after
	// the command exited.
	Coverage []LaneCoverageInput `json:"coverage"`
	// Warnings are capture-side failures (a full disk, an unwritable lane
	// directory). None of them changed ExitCode.
	Warnings []string `json:"warnings,omitempty"`
}

// StreamStats counts the lines of a captured stream.
type StreamStats struct {
	Lines int `json:"lines"`
	// Events are lines that decoded as `go test -json` events; the rest
	// (a build error, a line a wrapper printed) were stored verbatim.
	Events int `json:"events"`
	// Scrubbed are lines a secret pattern rewrote.
	Scrubbed int `json:"scrubbed,omitempty"`
}

// LaneCoverageInput is one coverage input in a lane directory.
type LaneCoverageInput struct {
	// Path is relative to the lane directory, slash-separated; a directory
	// ends in "/".
	Path string `json:"path"`
	// Format is go-profile (a -coverprofile), go-covdata (a GOCOVERDIR),
	// lcov, istanbul or cobertura.
	Format string `json:"format"`
}

// The coverage formats LaneCoverageInput.Format names.
const (
	CoverageGoProfile = "go-profile"
	CoverageGoCovdata = "go-covdata"
	CoverageLCOV      = "lcov"
	CoverageIstanbul  = "istanbul"
	CoverageCobertura = "cobertura"
)

// laneNamePattern keeps a lane name one portable path segment that can never
// collide with run.json: letters, digits, '-' and '_', starting with a letter
// or digit.
var laneNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// ValidateLaneName reports whether name can be a lane: one path segment of
// letters, digits, '-' and '_' (at most 64), so it is the same directory on
// every OS.
func ValidateLaneName(name string) error {
	if !laneNamePattern.MatchString(name) {
		return fmt.Errorf("lane name %q: use letters, digits, '-' and '_' (at most 64, starting with a letter or digit), e.g. unit or integration", name)
	}
	return nil
}

// RunsRoot is the directory holding a project's runs.
func RunsRoot(projectRoot string) string {
	return filepath.Join(projectRoot, filepath.FromSlash(RunsDir))
}

// LaneDir is a lane's directory in a run.
func LaneDir(runDir, lane string) string { return filepath.Join(runDir, lane) }

// ReadRunManifest reads runDir's run.json.
func ReadRunManifest(runDir string) (RunManifest, error) {
	var m RunManifest
	if err := readManifest(filepath.Join(runDir, RunManifestFile), RunManifestSchema, &m, &m.Schema); err != nil {
		return RunManifest{}, err
	}
	return m, nil
}

// ReadLaneManifest reads a lane's capture.json.
func ReadLaneManifest(runDir, lane string) (LaneManifest, error) {
	var m LaneManifest
	if err := readManifest(filepath.Join(LaneDir(runDir, lane), LaneManifestFile), LaneManifestSchema, &m, &m.Schema); err != nil {
		return LaneManifest{}, err
	}
	return m, nil
}

// ErrSchema is returned for a manifest of another schema.
var ErrSchema = errors.New("unexpected manifest schema")

func readManifest(path, schema string, v any, got *string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if *got != schema {
		return fmt.Errorf("%s: %w: %q, want %q", path, ErrSchema, *got, schema)
	}
	return nil
}

// CapturedLanes lists the lanes of runDir that hold a capture.json, sorted.
// It reads the directory rather than run.json's Lanes, so a lane whose append
// to run.json failed is still found.
func CapturedLanes(runDir string) ([]string, error) {
	entries, err := os.ReadDir(runDir)
	if err != nil {
		return nil, err
	}
	var lanes []string
	for _, e := range entries {
		if !e.IsDir() || ValidateLaneName(e.Name()) != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(runDir, e.Name(), LaneManifestFile)); err == nil {
			lanes = append(lanes, e.Name())
		}
	}
	sort.Strings(lanes)
	return lanes, nil
}
