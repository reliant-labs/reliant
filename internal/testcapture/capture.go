package testcapture

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/gofrs/flock"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/pkg/testreport"
)

// Render modes for a captured `go test -json` stream.
const (
	// RenderAuto is RenderVerbose when the command passes -v, else RenderText.
	RenderAuto = "auto"
	// RenderText prints what `go test` without -v prints.
	RenderText = "text"
	// RenderVerbose prints every event's output, what `go test -v` prints.
	RenderVerbose = "verbose"
	// RenderRaw prints the stream unchanged, for a pipe into another reader.
	RenderRaw = "raw"
)

// ioDrainDelay bounds how long capture waits, after the command exited, for
// its stdout to close. A background process the command left behind can hold
// the pipe open for ever; its output after this is not captured.
const ioDrainDelay = 10 * time.Second

// CaptureOptions are capture's inputs. Zero fields default to the process's
// own.
type CaptureOptions struct {
	// RunDir is the run to record into: --run, else EnvRun, else a run
	// begin creates.
	RunDir string
	Lane   string
	// Results is a report file the command writes (JUnit, Vitest JSON),
	// copied into the lane directory after it exits. Without it, the
	// command's stdout is the `go test -json` stream.
	Results string
	// Format declares the results' format; "" detects it.
	Format string
	Render string
	// Command is run as given, never through a shell.
	Command []string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	// Environ is the command's environment, before the EnvRun/EnvLane/
	// EnvLaneDir exports.
	Environ []string
	Getenv  func(string) string
	Now     func() time.Time
	// Begin creates the run when there is none yet.
	Begin BeginOptions
}

func (o *CaptureOptions) defaults() {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Environ == nil {
		o.Environ = os.Environ()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Stdin == nil {
		o.Stdin = os.Stdin
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if o.Render == "" {
		o.Render = RenderAuto
	}
	if o.Begin.Getenv == nil {
		o.Begin.Getenv = o.Getenv
	}
}

// CaptureResult is how the command ended.
type CaptureResult struct {
	// ExitCode is the command's exit status — what capture exits with,
	// whatever happened to the record.
	ExitCode int
	// StartErr is why the command never ran (ExitCode 127 or 126).
	StartErr error
	// LaneDir is the lane's directory; "" when nothing was recorded.
	LaneDir string
}

// Capture runs the command and records it as a lane of a run. The command's
// exit status is the result, always: a record that cannot be written is a
// warning on stderr (and in capture.json, when that can be written), never a
// different verdict.
func Capture(ctx context.Context, opts CaptureOptions) CaptureResult {
	if ctx == nil {
		ctx = context.Background()
	}
	opts.defaults()
	if len(opts.Command) == 0 {
		return CaptureResult{ExitCode: 126, StartErr: errors.New("no command to run")}
	}
	c := &capture{opts: opts}
	if Off(opts.Getenv) {
		return c.passThrough(ctx)
	}
	c.prepare(ctx)
	if !c.recording {
		return c.passThrough(ctx)
	}
	return c.run(ctx)
}

type capture struct {
	opts      CaptureOptions
	recording bool
	runDir    string
	laneDir   string
	lane      testreport.LaneManifest
	warnings  []string
}

func (c *capture) warn(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	c.warnings = append(c.warnings, msg)
	fmt.Fprintf(c.opts.Stderr, "forge test capture: warning: %s\n", msg)
}

// prepare settles the run and lane directories. Any failure leaves recording
// off, and the command still runs.
func (c *capture) prepare(ctx context.Context) {
	if err := testreport.ValidateLaneName(c.opts.Lane); err != nil {
		c.warn("%v; running the command without recording it", err)
		return
	}
	run, ok := c.resolveRun(ctx)
	if !ok {
		return
	}
	c.runDir = run
	c.laneDir = testreport.LaneDir(run, c.opts.Lane)
	// A lane captured again in the same run replaces its earlier capture:
	// a stale cover.out the new command never wrote must not be recorded
	// as its coverage. Only a directory capture itself made is removed.
	if _, err := os.Stat(filepath.Join(c.laneDir, testreport.LaneManifestFile)); err == nil {
		if err := os.RemoveAll(c.laneDir); err != nil {
			c.warn("cannot clear the earlier capture of lane %s: %v", c.opts.Lane, err)
		}
	}
	if err := os.MkdirAll(c.laneDir, 0o755); err != nil {
		c.warn("cannot create %s: %v; running the command without recording it", c.laneDir, err)
		return
	}
	c.recording = true
}

// resolveRun is the run to record into: --run, else EnvRun, else a new run.
// A directory with no run.json yet (one a Taskfile chose) is made a run, as
// begin does for EnvRun. ok is false when there is no run to record into.
func (c *capture) resolveRun(ctx context.Context) (string, bool) {
	run := c.opts.RunDir
	if run == "" {
		run = strings.TrimSpace(c.opts.Getenv(EnvRun))
	}
	if run == "" {
		return c.beginRun(ctx, c.opts.Begin, "")
	}
	abs, err := filepath.Abs(run)
	if err != nil {
		c.warn("--run %s: %v; running the command without recording it", run, err)
		return "", false
	}
	if _, err := os.Stat(filepath.Join(abs, testreport.RunManifestFile)); err == nil {
		return abs, true
	}
	b := c.opts.Begin
	b.Getenv = func(k string) string {
		if k == EnvRun {
			return abs
		}
		return c.opts.Getenv(k)
	}
	return c.beginRun(ctx, b, abs)
}

// beginRun begins a run; want is the directory it must be in ("" any).
func (c *capture) beginRun(ctx context.Context, b BeginOptions, want string) (string, bool) {
	res := Begin(ctx, b)
	for _, w := range res.Warnings {
		c.warn("%s", w)
	}
	if res.Recording && (want == "" || res.Dir == want) {
		return res.Dir, true
	}
	if !res.Recording {
		_ = os.Remove(res.Dir) // the unused scratch directory, if empty
	}
	if want == "" {
		c.warn("no run to record into; running the command without recording it")
	} else {
		c.warn("%s is not a run and cannot be made one; running the command without recording it", want)
		c.ensureLaneDir(want)
	}
	return "", false
}

// ensureLaneDir creates <run>/<lane> so a command told to write its coverage
// there (-coverprofile=<run>/<lane>/cover.out) does not fail for the want of
// a directory, even when nothing is recorded.
func (c *capture) ensureLaneDir(run string) {
	if run == "" || testreport.ValidateLaneName(c.opts.Lane) != nil {
		return
	}
	_ = os.MkdirAll(testreport.LaneDir(run, c.opts.Lane), 0o755)
}

// passThrough runs the command with nothing recorded.
func (c *capture) passThrough(ctx context.Context) CaptureResult {
	run := c.opts.RunDir
	if run == "" {
		run = strings.TrimSpace(c.opts.Getenv(EnvRun))
	}
	c.ensureLaneDir(run)
	code, err := execute(ctx, c.opts.Command, c.opts.Environ, c.opts.Stdin, c.opts.Stdout, c.opts.Stderr)
	return CaptureResult{ExitCode: code, StartErr: err}
}

func (c *capture) run(ctx context.Context) CaptureResult {
	o := c.opts
	c.lane = testreport.LaneManifest{
		Schema:   testreport.LaneManifestSchema,
		Lane:     o.Lane,
		Command:  scrubArgs(o.Command),
		Coverage: []testreport.LaneCoverageInput{},
	}
	env := append(slices.Clone(o.Environ),
		EnvRun+"="+c.runDir,
		EnvLane+"="+o.Lane,
		EnvLaneDir+"="+c.laneDir,
	)

	format := testreport.LaneFormat(o.Format)
	switch format {
	case "", testreport.LaneFormatGoJSON, testreport.LaneFormatJUnit, testreport.LaneFormatVitest:
	default:
		c.warn("--format %q is not one of go-json, junit, vitest; detecting the format instead", o.Format)
		format = ""
	}
	stdout := o.Stdout
	var st *stream
	if o.Results == "" {
		if format != "" && format != testreport.LaneFormatGoJSON {
			c.warn("--format %s names a report file: pass it with --results; reading stdout as a go test -json stream", format)
		}
		format = testreport.LaneFormatGoJSON
		st = c.openStream()
		stdout = st
	}

	c.lane.StartedAt = o.Now().UTC()
	code, startErr := execute(ctx, o.Command, env, o.Stdin, stdout, o.Stderr)
	c.lane.FinishedAt = o.Now().UTC()
	c.lane.ExitCode = code
	if startErr != nil {
		c.lane.Error = startErr.Error()
	}

	if st != nil {
		c.closeStream(st)
	} else {
		c.copyResults(format)
	}
	c.lane.Coverage = scanCoverage(c.laneDir, c.lane.ResultsFile)
	c.lane.Warnings = c.warnings
	c.record()
	return CaptureResult{ExitCode: code, StartErr: startErr, LaneDir: c.laneDir}
}

// record writes capture.json and adds the lane to run.json.
func (c *capture) record() {
	if err := RecordLane(c.runDir, c.lane); err != nil {
		c.warn("%v", err)
	}
}

// RecordLane writes m as <runDir>/<m.Lane>/capture.json and adds the lane to
// run.json, under the run's lock: how capture records a lane, and how anything
// else that records one (forge test ingest) must, so every reader finds lanes
// the same way. The lane directory must exist.
func RecordLane(runDir string, m testreport.LaneManifest) error {
	if err := testreport.ValidateLaneName(m.Lane); err != nil {
		return err
	}
	if m.Schema == "" {
		m.Schema = testreport.LaneManifestSchema
	}
	if m.Coverage == nil {
		m.Coverage = []testreport.LaneCoverageInput{}
	}
	data, err := marshalManifest(m)
	if err == nil {
		err = atomicfile.WriteFile(filepath.Join(testreport.LaneDir(runDir, m.Lane), testreport.LaneManifestFile), data, 0o644)
	}
	if err != nil {
		return fmt.Errorf("cannot write lane %s's capture.json: %w", m.Lane, err)
	}
	if err := appendLane(runDir, m.Lane); err != nil {
		return fmt.Errorf("cannot add lane %s to run.json: %w", m.Lane, err)
	}
	return nil
}

// appendLane adds lane to run.json's lanes under a lock, since lanes of one
// run may finish at once.
func appendLane(runDir, lane string) error {
	lock := flock.New(filepath.Join(runDir, ".run.lock"))
	if err := lock.Lock(); err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()
	m, err := testreport.ReadRunManifest(runDir)
	if err != nil {
		return err
	}
	if slices.Contains(m.Lanes, lane) {
		return nil
	}
	m.Lanes = append(m.Lanes, lane)
	return writeRunManifest(runDir, m)
}

// scrubArgs is the command as recorded: an argument can carry a credential
// (a -ldflags -X token, a DSN) as easily as an output line can.
func scrubArgs(argv []string) []string {
	out := make([]string, len(argv))
	var s Scrubber
	for i, a := range argv {
		out[i] = s.Text(a)
	}
	return out
}

// stream is the command's stdout when it is a `go test -json` stream: each
// line is stored (scrubbed) in results.json and rendered to the terminal.
type stream struct {
	c       *capture
	out     io.Writer
	render  *Renderer
	raw     bool
	file    *os.File
	buf     *bufio.Writer
	pending []byte
	scrub   Scrubber
	stats   testreport.StreamStats
	failed  bool
}

func (c *capture) openStream() *stream {
	mode := c.opts.Render
	if mode == RenderAuto {
		mode = RenderText
		if verboseCommand(c.opts.Command) {
			mode = RenderVerbose
		}
	}
	s := &stream{c: c, out: c.opts.Stdout, raw: mode == RenderRaw}
	if !s.raw {
		s.render = NewRenderer(c.opts.Stdout, mode == RenderVerbose)
	}
	f, err := os.Create(filepath.Join(c.laneDir, testreport.LaneResultsFile))
	if err != nil {
		c.warn("cannot store the stream: %v", err)
		s.failed = true
		return s
	}
	s.file, s.buf = f, bufio.NewWriterSize(f, 64<<10)
	return s
}

// maxPendingLine bounds a line the stream has not ended: past it the bytes
// are handled as a line of their own rather than held without limit.
const maxPendingLine = 64 << 20

func (s *stream) Write(p []byte) (int, error) {
	s.pending = append(s.pending, p...)
	for {
		i := bytes.IndexByte(s.pending, '\n')
		if i < 0 {
			break
		}
		s.line(s.pending[:i], true)
		s.pending = s.pending[i+1:]
	}
	if len(s.pending) > maxPendingLine {
		s.line(s.pending, false)
		s.pending = nil
	}
	// Never an error: a failed store must not stop the command's output.
	return len(p), nil
}

func (s *stream) line(raw []byte, newline bool) {
	s.stats.Lines++
	var ev Event
	isEvent := len(raw) > 0 && raw[0] == '{' && json.Unmarshal(raw, &ev) == nil && ev.Action != ""

	stored, keep := raw, true
	if isEvent {
		s.stats.Events++
		if out := s.scrub.Text(ev.Output); out != ev.Output {
			stored = replaceOutput(raw, ev.Output, out)
			s.stats.Scrubbed++
		}
	} else {
		text, drop := s.scrub.Line(string(raw))
		if drop {
			keep = false
		} else if text != string(raw) {
			stored = []byte(text)
			s.stats.Scrubbed++
		}
	}
	if keep {
		s.store(stored)
	}

	switch {
	case s.raw:
		_, _ = s.out.Write(raw)
		if newline {
			_, _ = s.out.Write([]byte{'\n'})
		}
	case isEvent:
		s.render.Event(ev)
	default:
		text := string(raw)
		if newline {
			text += "\n"
		}
		s.render.Text(text)
	}
}

func (s *stream) store(line []byte) {
	if s.buf == nil || s.failed {
		return
	}
	if _, err := s.buf.Write(line); err == nil {
		_, err = s.buf.Write([]byte{'\n'})
		if err == nil {
			return
		}
	}
	s.failed = true
	s.c.warn("storing the stream failed part-way (disk full?); results.json is incomplete")
}

// replaceOutput rewrites the Output of the event in raw. The event's other
// bytes are kept as cmd/go wrote them: the encoded output is replaced in
// place, and only when it cannot be found is the event re-encoded.
func replaceOutput(raw []byte, from, to string) []byte {
	oldEnc, err1 := json.Marshal(from)
	newEnc, err2 := json.Marshal(to)
	if err1 == nil && err2 == nil && bytes.Count(raw, oldEnc) == 1 {
		return bytes.Replace(raw, oldEnc, newEnc, 1)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || err2 != nil {
		return []byte(`{"Action":"output","Output":"[redacted]\n"}`)
	}
	fields["Output"] = newEnc
	out, err := json.Marshal(fields)
	if err != nil {
		return []byte(`{"Action":"output","Output":"[redacted]\n"}`)
	}
	return out
}

func (c *capture) closeStream(s *stream) {
	if len(s.pending) > 0 {
		s.line(s.pending, false)
		s.pending = nil
	}
	if s.render != nil {
		s.render.Close()
	}
	stats := s.stats
	c.lane.Stream = &stats
	c.lane.Format = testreport.LaneFormatGoJSON
	if s.file != nil {
		err := s.buf.Flush()
		if cerr := s.file.Close(); err == nil {
			err = cerr
		}
		if err != nil && !s.failed {
			s.failed = true
			c.warn("storing the stream failed (disk full?): %v", err)
		}
		c.lane.ResultsFile = testreport.LaneResultsFile
	}
	switch {
	case s.file == nil:
		c.lane.Results = testreport.LaneResultsMissing
		c.lane.ResultsFile = ""
	case stats.Events > 0:
		c.lane.Results = testreport.LaneResultsParsed
	default:
		c.lane.Results = testreport.LaneResultsUnparsed
	}
}

// verboseCommand reports a command that asked `go test` for -v.
func verboseCommand(argv []string) bool {
	for _, a := range argv {
		if a == "-args" {
			break
		}
		switch a {
		case "-v", "--v", "-v=true", "-test.v", "-test.v=true":
			return true
		}
	}
	return false
}

// copyResults copies the --results report into the lane directory, scrubbed.
func (c *capture) copyResults(declared testreport.LaneFormat) {
	src := c.opts.Results
	data, err := os.ReadFile(src)
	if err != nil {
		c.lane.Format = declared
		if c.lane.Format == "" {
			c.lane.Format = testreport.LaneFormatUnknown
		}
		c.lane.Results = testreport.LaneResultsMissing
		if errors.Is(err, os.ErrNotExist) {
			c.warn("the command wrote no %s", src)
		} else {
			c.warn("cannot read %s: %v", src, err)
		}
		return
	}
	detected := detectFormat(data)
	c.lane.Format = declared
	if declared == "" {
		c.lane.Format = detected
	}
	c.lane.Results = testreport.LaneResultsUnparsed
	if detected != testreport.LaneFormatUnknown && detected == c.lane.Format {
		c.lane.Results = testreport.LaneResultsParsed
	}
	name := ResultsName(c.lane.Format, src)
	var s Scrubber
	if err := atomicfile.WriteFile(filepath.Join(c.laneDir, name), []byte(s.Text(string(data))), 0o644); err != nil {
		c.warn("cannot store %s: %v", src, err)
		c.lane.Results = testreport.LaneResultsMissing
		return
	}
	c.lane.ResultsFile = name
}

// ResultsName is the name a copied report is stored under in a lane
// directory: one per format (results.json, junit.xml, vitest.json), so a
// reader finds it from capture.json's results_file without guessing. An
// unknown format keeps src's extension.
func ResultsName(f testreport.LaneFormat, src string) string {
	switch f {
	case testreport.LaneFormatGoJSON:
		return testreport.LaneResultsFile
	case testreport.LaneFormatJUnit:
		return "junit.xml"
	case testreport.LaneFormatVitest:
		return "vitest.json"
	}
	ext := strings.ToLower(filepath.Ext(src))
	if len(ext) > 10 || strings.ContainsAny(ext, `/\`) {
		ext = ""
	}
	return "results" + ext
}

// detectFormat sniffs a report file's format.
func detectFormat(data []byte) testreport.LaneFormat {
	head := bytes.TrimSpace(data)
	if len(head) > 64<<10 {
		head = head[:64<<10]
	}
	switch {
	case bytes.HasPrefix(head, []byte("<")) && (bytes.Contains(head, []byte("<testsuite")) || bytes.Contains(head, []byte("<testsuites"))):
		return testreport.LaneFormatJUnit
	case bytes.HasPrefix(head, []byte("{")):
		if bytes.Contains(head, []byte(`"numTotalTests"`)) || bytes.Contains(head, []byte(`"testResults"`)) {
			return testreport.LaneFormatVitest
		}
		first, _, _ := bytes.Cut(head, []byte("\n"))
		var ev Event
		if json.Unmarshal(first, &ev) == nil && ev.Action != "" {
			return testreport.LaneFormatGoJSON
		}
	}
	return testreport.LaneFormatUnknown
}

// scanCoverage lists the coverage inputs in the lane directory.
func scanCoverage(laneDir, resultsFile string) []testreport.LaneCoverageInput {
	files := []testreport.LaneCoverageInput{}
	entries, err := os.ReadDir(laneDir)
	if err != nil {
		return files
	}
	for _, e := range entries {
		name := e.Name()
		if name == testreport.LaneManifestFile || name == resultsFile {
			continue
		}
		path := filepath.Join(laneDir, name)
		if e.IsDir() {
			if name == testreport.LaneCoverDataDir || hasCovMeta(path) {
				files = append(files, testreport.LaneCoverageInput{Path: name + "/", Format: testreport.CoverageGoCovdata})
			}
			continue
		}
		if f := coverageFormat(name, path); f != "" {
			files = append(files, testreport.LaneCoverageInput{Path: name, Format: f})
		}
	}
	slices.SortFunc(files, func(a, b testreport.LaneCoverageInput) int { return strings.Compare(a.Path, b.Path) })
	return files
}

func hasCovMeta(dir string) bool {
	matches, _ := filepath.Glob(filepath.Join(dir, "covmeta.*"))
	return len(matches) > 0
}

func coverageFormat(name, path string) string {
	lower := strings.ToLower(name)
	switch {
	case lower == "lcov.info" || strings.HasSuffix(lower, ".lcov"):
		return testreport.CoverageLCOV
	case lower == "coverage-final.json":
		return testreport.CoverageIstanbul
	}
	head := readHead(path, 512)
	switch {
	case bytes.HasPrefix(head, []byte("mode: ")):
		return testreport.CoverageGoProfile
	case strings.HasSuffix(lower, ".xml") && bytes.Contains(head, []byte("<coverage")):
		return testreport.CoverageCobertura
	}
	return ""
}

func readHead(path string, n int) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, n)
	k, _ := io.ReadFull(f, buf)
	return buf[:k]
}

// execute runs argv and returns its exit status: the command's own, 128+n
// when signal n killed it, 127 when it was not found and 126 when it could
// not start. Signals sent to this process alone are forwarded to it; a
// terminal's Ctrl-C reaches it directly, as they share a process group.
func execute(ctx context.Context, argv []string, env []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	// Registered before the command starts, so no signal sent once it runs
	// can miss it (and end this process, the command's verdict with it).
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	return executeForwarding(ctx, argv, env, stdin, stdout, stderr, sigs)
}

// executeForwarding is execute with the signals to forward as a channel.
func executeForwarding(ctx context.Context, argv []string, env []string, stdin io.Reader, stdout, stderr io.Writer, sigs <-chan os.Signal) (int, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.WaitDelay = ioDrainDelay
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return 127, err
		}
		return 126, err
	}
	done := make(chan struct{})
	forwarded := make(chan struct{})
	go func() {
		defer close(forwarded)
		for {
			select {
			case s := <-sigs:
				_ = cmd.Process.Signal(s)
			case <-ctx.Done():
				_ = cmd.Process.Signal(os.Interrupt)
				return
			case <-done:
				return
			}
		}
	}()
	_ = cmd.Wait()
	close(done)
	<-forwarded
	return exitStatus(cmd.ProcessState), nil
}

func exitStatus(ps *os.ProcessState) int {
	if ps == nil {
		return 1
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	if code := ps.ExitCode(); code >= 0 {
		return code
	}
	return 1
}
