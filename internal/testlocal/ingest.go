package testlocal

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/testcapture"
	"github.com/reliant-labs/forge/pkg/testreport"
)

// IngestFormats are the formats `forge test ingest` copies into a run.
var IngestFormats = []string{"junit", "go-json", "vitest", "lcov", "cobertura", "istanbul"}

// IngestOptions configures [Ingest].
type IngestOptions struct {
	// RunDir is the run to add the lane to; it must hold a run.json.
	RunDir string
	Lane   string
	// Format is one of IngestFormats.
	Format string
	// Files are the reports to copy. A go-json lane may take several
	// streams (they are concatenated); a JUnit or Vitest lane takes one.
	Files []string
	Now   func() time.Time
}

// IngestResult is the lane as recorded.
type IngestResult struct {
	LaneDir  string
	Manifest testreport.LaneManifest
}

// Ingest copies reports a runner forge does not wrap (pytest's JUnit, jest's
// LCOV, …) into a lane of a run, and records the lane as capture would have.
// Results are secret-scrubbed as capture scrubs a stream. A lane may be
// ingested more than once, results and coverage separately, but holds one
// results file.
func Ingest(opts IngestOptions) (IngestResult, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if err := testreport.ValidateLaneName(opts.Lane); err != nil {
		return IngestResult{}, err
	}
	if !slices.Contains(IngestFormats, opts.Format) {
		return IngestResult{}, fmt.Errorf("--format %q: use one of %s", opts.Format, strings.Join(IngestFormats, ", "))
	}
	if len(opts.Files) == 0 {
		return IngestResult{}, errors.New("no report files to ingest")
	}
	if _, err := testreport.ReadRunManifest(opts.RunDir); err != nil {
		return IngestResult{}, fmt.Errorf("run %s: %w", opts.RunDir, err)
	}
	laneDir := testreport.LaneDir(opts.RunDir, opts.Lane)
	// Ingest copies reports INTO the lane, so a report already inside it
	// is refused before anything is written: the copy could otherwise land
	// on, or beside, the very file it reads (a symlink resolved too).
	for _, p := range opts.Files {
		if insideDir(laneDir, p) {
			return IngestResult{}, fmt.Errorf("%s is already inside lane %s of the run (%s); ingest copies a report into the run, so pass the runner's own file", p, opts.Lane, laneDir)
		}
	}
	if err := os.MkdirAll(laneDir, 0o755); err != nil {
		return IngestResult{}, err
	}
	now := opts.Now().UTC()
	m, err := testreport.ReadLaneManifest(opts.RunDir, opts.Lane)
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		m = testreport.LaneManifest{
			Schema: testreport.LaneManifestSchema, Lane: opts.Lane, StartedAt: now,
			Format: testreport.LaneFormatUnknown, Results: testreport.LaneResultsMissing,
			Coverage: []testreport.LaneCoverageInput{},
			Warnings: []string{"ingested: the runner's own exit code is not known, so the lane's verdict is its results"},
		}
	default:
		return IngestResult{}, err
	}
	m.Command = append([]string{"forge", "test", "ingest", "--lane", opts.Lane, "--format", opts.Format}, baseNames(opts.Files)...)
	m.FinishedAt = now

	f := testreport.Format(opts.Format)
	if f.IsResults() {
		if err := ingestResults(&m, laneDir, f, opts.Files); err != nil {
			return IngestResult{}, err
		}
	} else {
		if err := ingestCoverage(&m, laneDir, opts.Format, opts.Files); err != nil {
			return IngestResult{}, err
		}
	}
	if err := testcapture.RecordLane(opts.RunDir, m); err != nil {
		return IngestResult{}, err
	}
	return IngestResult{LaneDir: laneDir, Manifest: m}, nil
}

func baseNames(files []string) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = filepath.Base(f)
	}
	return out
}

func ingestResults(m *testreport.LaneManifest, laneDir string, f testreport.Format, files []string) error {
	if m.Results != testreport.LaneResultsMissing && m.ResultsFile != "" {
		return fmt.Errorf("lane %s already holds results (%s); ingest another lane", m.Lane, m.ResultsFile)
	}
	if f != testreport.FormatGoJSON && len(files) > 1 {
		return fmt.Errorf("a %s lane takes one results file, got %d; ingest each under its own lane", f, len(files))
	}
	var data []byte
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
			data = append(data, '\n')
		}
		data = append(data, b...)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return fmt.Errorf("%s is empty", strings.Join(files, ", "))
	}
	lf := testreport.LaneFormat(f)
	m.Format = lf
	m.Results = testreport.LaneResultsUnparsed
	if testreport.DetectFormat(data[:min(len(data), 8<<10)]) == f {
		m.Results = testreport.LaneResultsParsed
	}
	name := testcapture.ResultsName(lf, files[0])
	var s testcapture.Scrubber
	if err := atomicfile.WriteFile(filepath.Join(laneDir, name), []byte(s.Text(string(data))), 0o644); err != nil {
		return err
	}
	m.ResultsFile = name
	return nil
}

func ingestCoverage(m *testreport.LaneManifest, laneDir, format string, files []string) error {
	ext := map[string]string{"lcov": ".info", "cobertura": ".xml", "istanbul": ".json"}[format]
	conventional := map[string]string{"lcov": "lcov.info", "cobertura": "cobertura-coverage.xml", "istanbul": "coverage-final.json"}[format]
	// Every input is read and checked before the first write, so a bad
	// one leaves the lane as it was.
	staged := make([][]byte, len(files))
	for i, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if got := testreport.DetectFormat(b[:min(len(b), 8<<10)]); got != testreport.Format(format) {
			return fmt.Errorf("%s is not %s coverage (it reads as %s)", p, format, got)
		}
		staged[i] = b
	}
	for _, b := range staged {
		name := conventional
		for i := 2; fileExists(filepath.Join(laneDir, name)); i++ {
			name = fmt.Sprintf("%s-%d%s", strings.TrimSuffix(conventional, filepath.Ext(conventional)), i, ext)
		}
		if err := atomicfile.WriteFile(filepath.Join(laneDir, name), b, 0o644); err != nil {
			return err
		}
		m.Coverage = append(m.Coverage, testreport.LaneCoverageInput{Path: name, Format: format})
	}
	return nil
}

// insideDir reports whether p, with symlinks resolved, is dir or lies
// beneath it. A dir that does not exist yet holds nothing.
func insideDir(dir, p string) bool {
	canon := func(x string) (string, bool) {
		a, err := filepath.Abs(x)
		if err != nil {
			return "", false
		}
		if r, err := filepath.EvalSymlinks(a); err == nil {
			a = r
		}
		return a, true
	}
	d, ok1 := canon(dir)
	f, ok2 := canon(p)
	if !ok1 || !ok2 {
		return false
	}
	rel, err := filepath.Rel(d, f)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
