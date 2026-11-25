package testflakes

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/atomicfile"

	"github.com/reliant-labs/forge/internal/testcapture"
	"github.com/reliant-labs/forge/pkg/testreport"
)

// The lanes the flake commands record their own runs under.
const (
	// LaneFlakeGate is the lane `forge test flakes gate` records its -count=3
	// run under, so its retry passes feed the flake record.
	LaneFlakeGate = "flake-gate"
	// LaneConfirm is testreport.LaneConfirm: `forge test flakes confirm`'s
	// reruns.
	LaneConfirm = string(testreport.LaneConfirm)
)

// streamTee is a command's stdout when it is a `go test -json` stream: every
// byte is kept for the record, and each line is rendered as the text `go
// test` prints, so asking for -json costs the reader nothing.
type streamTee struct {
	buf     bytes.Buffer
	render  *testcapture.Renderer
	pending []byte
}

func newStreamTee(out io.Writer, verbose bool) *streamTee {
	t := &streamTee{}
	if out != nil {
		t.render = testcapture.NewRenderer(out, verbose)
	}
	return t
}

func (t *streamTee) Write(p []byte) (int, error) {
	t.buf.Write(p)
	if t.render == nil {
		return len(p), nil
	}
	t.pending = append(t.pending, p...)
	for {
		i := bytes.IndexByte(t.pending, '\n')
		if i < 0 {
			break
		}
		t.line(t.pending[:i], true)
		t.pending = t.pending[i+1:]
	}
	return len(p), nil
}

func (t *streamTee) line(raw []byte, newline bool) {
	var ev testcapture.Event
	if len(raw) > 0 && raw[0] == '{' && json.Unmarshal(raw, &ev) == nil && ev.Action != "" {
		t.render.Event(ev)
		return
	}
	text := string(raw)
	if newline {
		text += "\n"
	}
	t.render.Text(text)
}

// Close flushes the renderer.
func (t *streamTee) Close() {
	if t.render == nil {
		return
	}
	if len(t.pending) > 0 {
		t.line(t.pending, false)
		t.pending = nil
	}
	t.render.Close()
}

// laneRun is one lane the flake commands record.
type laneRun struct {
	Lane       string
	Command    []string
	StartedAt  time.Time
	FinishedAt time.Time
	ExitCode   int
	Error      string
	// Stream is the lane's `go test -json` output, every invocation's
	// concatenated.
	Stream []byte
}

// openRun is the run a flake command records into: $FORGE_TEST_RUN when it
// names one, else a new run under the project's .forge/tests/runs/. It never
// fails: recording is false when there is no run, and the warnings say why.
func openRun(ctx context.Context, b testcapture.BeginOptions) (dir string, recording bool, warnings []string) {
	res := testcapture.Begin(ctx, b)
	if !res.Recording {
		_ = os.Remove(res.Dir) // the unused scratch directory, if empty
		return "", false, res.Warnings
	}
	return res.Dir, true, res.Warnings
}

// recordLane writes l as a lane of runDir, the way `forge test capture`
// records one: the stream secret-scrubbed into <lane>/results.json, and
// <lane>/capture.json through testcapture.RecordLane, which also adds the
// lane to run.json. A lane recorded again in the same run replaces the
// earlier capture.
func recordLane(runDir string, l laneRun) (string, error) {
	if err := testreport.ValidateLaneName(l.Lane); err != nil {
		return "", err
	}
	laneDir := testreport.LaneDir(runDir, l.Lane)
	if _, err := os.Stat(filepath.Join(laneDir, testreport.LaneManifestFile)); err == nil {
		if err := os.RemoveAll(laneDir); err != nil {
			return "", fmt.Errorf("cannot clear the earlier capture of lane %s: %w", l.Lane, err)
		}
	}
	if err := os.MkdirAll(laneDir, 0o755); err != nil {
		return "", err
	}
	stored, stats := scrubStream(l.Stream)
	m := testreport.LaneManifest{
		Schema:     testreport.LaneManifestSchema,
		Lane:       l.Lane,
		Command:    scrubArgs(l.Command),
		StartedAt:  l.StartedAt.UTC(),
		FinishedAt: l.FinishedAt.UTC(),
		ExitCode:   l.ExitCode,
		Error:      l.Error,
		Format:     testreport.LaneFormatGoJSON,
		Stream:     &stats,
		Coverage:   []testreport.LaneCoverageInput{},
	}
	if err := atomicfile.WriteFile(filepath.Join(laneDir, testreport.LaneResultsFile), stored, 0o644); err != nil {
		m.Results = testreport.LaneResultsMissing
		m.Warnings = append(m.Warnings, fmt.Sprintf("cannot store the stream: %v", err))
	} else {
		m.ResultsFile = testreport.LaneResultsFile
		m.Results = testreport.LaneResultsUnparsed
		if stats.Events > 0 {
			m.Results = testreport.LaneResultsParsed
		}
	}
	return laneDir, testcapture.RecordLane(runDir, m)
}

// scrubStream is the stream as stored: each event's Output and each other
// line secret-scrubbed, as capture stores a lane's stream.
func scrubStream(stream []byte) ([]byte, testreport.StreamStats) {
	var (
		out   bytes.Buffer
		stats testreport.StreamStats
		s     testcapture.Scrubber
	)
	sc := bufio.NewScanner(bytes.NewReader(stream))
	sc.Buffer(make([]byte, 0, 64<<10), 64<<20)
	for sc.Scan() {
		raw := sc.Bytes()
		stats.Lines++
		var fields map[string]json.RawMessage
		if len(raw) > 0 && raw[0] == '{' && json.Unmarshal(raw, &fields) == nil && fields["Action"] != nil {
			stats.Events++
			var output string
			if json.Unmarshal(fields["Output"], &output) == nil && output != "" {
				if clean := s.Text(output); clean != output {
					enc, _ := json.Marshal(clean)
					fields["Output"] = enc
					if b, err := json.Marshal(fields); err == nil {
						raw = b
					}
					stats.Scrubbed++
				}
			}
			out.Write(raw)
			out.WriteByte('\n')
			continue
		}
		text, drop := s.Line(string(raw))
		if drop {
			continue
		}
		if text != string(raw) {
			stats.Scrubbed++
		}
		out.WriteString(text)
		out.WriteByte('\n')
	}
	return out.Bytes(), stats
}

func scrubArgs(argv []string) []string {
	out := make([]string, len(argv))
	var s testcapture.Scrubber
	for i, a := range argv {
		out[i] = s.Text(a)
	}
	return out
}

// parseStream reads a lane's `go test -json` stream. A stream with no events
// is nil, never an error: the lane still ran.
func parseStream(stream []byte, lane string) *testreport.Results {
	res, err := testreport.ParseResults(bytes.NewReader(stream), testreport.ResultsOptions{Format: testreport.FormatGoJSON, Lane: testreport.Lane(lane)})
	if err != nil || res.Stats.Events == 0 {
		return nil
	}
	return res
}

// childEnv is env with the capture variables a lane's command sees.
func childEnv(env []string, runDir, lane string) []string {
	if runDir == "" {
		return env
	}
	out := make([]string, 0, len(env)+3)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case testcapture.EnvRun, testcapture.EnvLane, testcapture.EnvLaneDir:
			continue
		}
		out = append(out, kv)
	}
	return append(out,
		testcapture.EnvRun+"="+runDir,
		testcapture.EnvLane+"="+lane,
		testcapture.EnvLaneDir+"="+testreport.LaneDir(runDir, lane),
	)
}
