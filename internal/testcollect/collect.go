package testcollect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/testcapture"
	"github.com/reliant-labs/forge/pkg/testreport"
)

// CoveragePath is serverkit's read-only endpoint (pkg/serverkit.CoveragePath),
// repeated so the forge binary does not link serverkit; a test holds them
// equal.
const CoveragePath = "/debug/coverage"

// DefaultLane is the lane collected coverage is recorded under: servers are
// exercised by the e2e lane.
const DefaultLane = "e2e"

// exitUndetermined is the shared table's 2 (internal/cli/exitcodes.go): an
// instrumented workload could not be reached and nothing was collected.
const exitUndetermined = 2

// Target is one workload of the env, as the caller resolved it from the
// render: whether it has coverage to collect, and where.
type Target struct {
	Workload string
	// Runtime is the workload's runtime as declared (host, compose,
	// cluster, hosted, …); it is reported as is.
	Runtime string
	// Coverage, when set, says there is nothing to collect:
	// CoverageUnavailable or CoverageNotInstrumented.
	Coverage string
	// Unreachable, when set, says the workload is instrumented but forge
	// cannot reach it (a hosted workload): it is reported unreachable.
	Unreachable string
	// Exactly one of these is set for a reachable instrumented workload.
	Host    *HostTarget
	Compose *ComposeTarget
	Cluster *ClusterTarget
}

// HostTarget is a host process.
type HostTarget struct {
	// PullAddr is the loopback debug listener (host:port); empty when
	// Withheld says why there is no pull.
	PullAddr string
	Withheld string
	// CoverDir is the process's GOCOVERDIR, where it flushes at exit.
	CoverDir string
}

// ComposeTarget is a docker compose service.
type ComposeTarget struct {
	Service string
	// WorkingDir and Project are the compose project's directory and name,
	// which its containers carry as com.docker.compose.project.working_dir
	// and com.docker.compose.project; an empty Project matches any.
	WorkingDir string
	Project    string
	// CoverDir is GOCOVERDIR inside the container.
	CoverDir string
}

// ClusterTarget is a workload's pods on a cluster.
type ClusterTarget struct {
	Context   string
	Namespace string
	// Selector picks the workload's pods (app.kubernetes.io/name=<w>).
	Selector string
	// Port is the pods' debug listener port.
	Port int
	// Withheld, when set, is why the pods do not serve /debug/coverage.
	Withheld string
}

// Docker reaches compose containers.
type Docker interface {
	// Containers lists the containers, running or stopped, of a compose
	// service.
	Containers(ctx context.Context, c ComposeTarget) ([]string, error)
	// CopyOut returns a tar of dir's contents inside a container.
	CopyOut(ctx context.Context, container, dir string) ([]byte, error)
}

// ErrNoSuchPath is what a Docker returns from CopyOut for a directory the
// container does not have yet.
var ErrNoSuchPath = errors.New("no such path in the container")

// Pod is one running pod.
type Pod struct {
	Name    string
	Started time.Time
}

// Kube reaches pods.
type Kube interface {
	// Pods lists the running pods selector picks.
	Pods(ctx context.Context, kubeContext, namespace, selector string) ([]Pod, error)
	// Do sends req to port inside a pod. req is addressed to
	// 127.0.0.1:<port>, as the pod's own loopback listener sees it.
	Do(ctx context.Context, kubeContext, namespace, pod string, port int, req *http.Request) (*http.Response, error)
}

// Options configure Collect.
type Options struct {
	ProjectRoot string
	Env         string
	// Lane records the coverage; default DefaultLane.
	Lane string
	// Reset records the live counters as a baseline (and fences the flushed
	// ones) instead of collecting them: later collects count from here.
	Reset   bool
	Targets []Target
	// Run resolves the run directory. It is called only when there is
	// coverage to write, so a collect that finds none creates no run.
	Run func(ctx context.Context) (dir string, warnings []string, err error)
	// Command is the invocation, recorded in a lane collect creates.
	Command []string

	HTTP   *http.Client
	Docker Docker
	Kube   Kube
	// Convert converts a GOCOVERDIR; nil is testreport.ConvertCovdata.
	Convert func(ctx context.Context, goBin, dir string) ([]testreport.CovdataBuild, error)
	// ModulePath lets the conversion summary exclude generated files by
	// their repo path.
	ModulePath string
	Now        func() time.Time
}

func (o *Options) defaults() {
	if o.Lane == "" {
		o.Lane = DefaultLane
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if o.Convert == nil {
		o.Convert = testreport.ConvertCovdata
	}
	if o.Now == nil {
		o.Now = time.Now
	}
}

// stamp fills the document's head: the exit code the command exits with,
// and its message when that is not 0.
func stamp(doc *Document, code int, msg string) *Document {
	doc.Schema, doc.ExitCode, doc.OK, doc.Error = Schema, code, code == 0, msg
	return doc
}

// ResetMarker is the reset fence of one workload (fence.go):
// .forge/tests/covdata/<env>/.reset/<workload>. A workload name never starts
// with '.', so it cannot collide with a workload's GOCOVERDIR beside it.
func ResetMarker(projectRoot, env, workload string) string {
	return filepath.Join(projectRoot, ".forge", "tests", "covdata", env, ".reset", workload)
}

// Collect collects (or with Reset, zeroes) the coverage of every target. It
// never fails for one workload: what it could not reach it reports. The
// document's head carries the exit code (the shared table's): 0 when anything
// was collected or there was nothing to collect, 2 when an instrumented
// workload could not be reached and nothing was collected, 1 for a bad lane.
func Collect(ctx context.Context, opts Options) *Document {
	opts.defaults()
	doc := &Document{Env: opts.Env, Lane: opts.Lane, Reset: opts.Reset, Workloads: []WorkloadResult{}}
	if err := testreport.ValidateLaneName(opts.Lane); err != nil {
		return stamp(doc, 1, err.Error())
	}
	var writes []pending
	for _, t := range opts.Targets {
		r := WorkloadResult{Name: t.Workload, Runtime: t.Runtime}
		// The name becomes a directory under the run and under
		// .forge/tests/covdata, which a collect removes and rewrites, so a
		// name that is not one plain path element is never touched.
		nameErr := validateWorkloadName(t.Workload)
		switch {
		case nameErr != nil:
			r.State, r.Reason = StateFailed, nameErr.Error()
		case t.Coverage != "":
			r.State, r.Coverage = StateUnavailable, t.Coverage
			if t.Coverage == CoverageNotInstrumented {
				r.State = StateNotInstrumented
			}
		case t.Unreachable != "":
			r.State, r.Reason = StateUnreachable, t.Unreachable
		case opts.Reset:
			resetTarget(ctx, opts, t, &r)
		default:
			if builds := gatherTarget(ctx, opts, t, &r); len(builds) > 0 {
				writes = append(writes, pending{idx: len(doc.Workloads), builds: builds})
			}
		}
		doc.Workloads = append(doc.Workloads, r)
	}
	if len(writes) > 0 {
		run, warnings, err := opts.Run(ctx)
		doc.Warnings = append(doc.Warnings, warnings...)
		if err != nil {
			for _, p := range writes {
				r := &doc.Workloads[p.idx]
				r.State, r.Reason = StateFailed, "no run to record into: "+err.Error()
			}
		} else {
			doc.Run = run
			recordRun(ctx, opts, doc, run, writes)
		}
	}
	var unreached []string
	for _, r := range doc.Workloads {
		switch r.State {
		case StateCollected, StateReset:
			doc.Collected++
		case StateUnreachable, StateFailed:
			unreached = append(unreached, r.Name)
		}
	}
	code, msg := 0, ""
	if doc.Collected == 0 && len(unreached) > 0 {
		what := "no coverage collected"
		if opts.Reset {
			what = "nothing reset"
		}
		code, msg = exitUndetermined, fmt.Sprintf("forge test collect %s: %s: %s could not be reached", opts.Env, what, strings.Join(unreached, ", "))
		doc.Suggested = append(doc.Suggested, "forge env status "+opts.Env)
	}
	if doc.Run != "" && !opts.Reset {
		doc.Suggested = append(doc.Suggested, "forge test report "+doc.Run)
	}
	return stamp(doc, code, msg)
}

// validateWorkloadName refuses a workload name that is not exactly one plain
// path element: empty, ".", "..", or holding a separator (either OS's), a NUL
// or a volume name. Collect joins the name into paths it removes and
// rewrites, so a rendered env must not be able to point them elsewhere.
func validateWorkloadName(name string) error {
	if name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, `/\:`+"\x00") ||
		filepath.Base(name) != name || filepath.VolumeName(name) != "" {
		return fmt.Errorf("workload name %q is not a single path element; not collected", name)
	}
	return nil
}

// within reports whether p is dir or lies beneath it.
func within(dir, p string) bool {
	r, err := filepath.Rel(dir, p)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}

// pending is a workload whose builds are waiting to be written: the index of
// its result in the document, and its builds, newest first.
type pending struct {
	idx    int
	builds []*build
}

// gatherTarget reads every source of t, fills r and returns its builds,
// newest first.
func gatherTarget(ctx context.Context, opts Options, t Target, r *WorkloadResult) []*build {
	rec, err := readReset(ResetMarker(opts.ProjectRoot, opts.Env, t.Workload))
	if err != nil {
		// Without the record, what a reset left out cannot be told apart.
		r.State, r.Reason = StateFailed, "not collected: "+err.Error()
		return nil
	}
	fenced := rec.fence()
	var files []covFile
	live := 0 // live sources attempted (pull, pod, container)
	add := func(s Source, fs []covFile) {
		r.Sources = append(r.Sources, s)
		files = append(files, fs...)
	}
	switch {
	case t.Host != nil:
		h := t.Host
		if h.PullAddr != "" {
			live++
			fs, s := pullHTTP(ctx, opts.HTTP, h.PullAddr)
			add(s, fs)
		} else if h.Withheld != "" {
			r.Notes = append(r.Notes, "no live pull: "+h.Withheld)
		}
		fs, err := readDir(h.CoverDir)
		fs = fenceOut(fs, fenced, dirSource(opts.ProjectRoot, h.CoverDir))
		for i := range fs {
			fs[i].scope = hostScope
		}
		s := Source{Kind: SourceExitFlush, From: rel(opts.ProjectRoot, h.CoverDir), Status: SourceOK, Files: len(fs)}
		switch {
		case err != nil:
			s.Status, s.Error = SourceFailed, err.Error()
		case len(fs) == 0:
			s.Status = SourceEmpty
		}
		add(s, fs)
	case t.Compose != nil:
		live++
		gatherCompose(ctx, opts, t.Compose, fenced, add)
	case t.Cluster != nil:
		c := t.Cluster
		if c.Withheld != "" {
			r.Notes = append(r.Notes, "no live pull: "+c.Withheld)
			break
		}
		live++
		gatherPods(ctx, opts, c, add)
	}

	builds, orphans := groupBuilds(files)
	for _, h := range orphans {
		r.Notes = append(r.Notes, fmt.Sprintf("counter files of build %s have no covmeta file; skipped", short(h)))
	}
	if len(builds) > 0 {
		return builds
	}
	r.State, r.Reason = settleEmpty(r.Sources, live)
	if r.State == StateEmpty && t.Compose != nil {
		r.Notes = append(r.Notes, "a container's counters are written to GOCOVERDIR when its process exits (compose withholds /debug/coverage: pprof binds 0.0.0.0 there); restart or stop the service gracefully, then collect")
	}
	return nil
}

// settleEmpty decides the state of a workload no build was read from.
func settleEmpty(sources []Source, live int) (state, reason string) {
	var failed, unreachable []string
	reached := false
	for _, s := range sources {
		switch s.Status {
		case SourceFailed:
			failed = append(failed, s.From+": "+s.Error)
		case SourceUnreachable:
			unreachable = append(unreachable, s.From+": "+s.Error)
		case SourceEmpty, SourceOK:
			if s.Kind != SourceExitFlush {
				reached = true
			}
		}
	}
	switch {
	case len(failed) > 0:
		return StateFailed, strings.Join(failed, "; ")
	case reached:
		return StateEmpty, "reached, with no coverage data yet"
	case live == 0:
		return StateUnreachable, "no live pull, and no counters flushed at exit"
	}
	return StateUnreachable, strings.Join(unreachable, "; ")
}

func gatherCompose(ctx context.Context, opts Options, c *ComposeTarget, fenced fenceSet, add func(Source, []covFile)) {
	from := "service " + c.Service
	if opts.Docker == nil {
		add(Source{Kind: SourceContainer, From: from, Status: SourceUnreachable, Error: "docker is not available"}, nil)
		return
	}
	ids, err := opts.Docker.Containers(ctx, *c)
	if err != nil {
		add(Source{Kind: SourceContainer, From: from, Status: SourceUnreachable, Error: err.Error()}, nil)
		return
	}
	if len(ids) == 0 {
		add(Source{Kind: SourceContainer, From: from, Status: SourceUnreachable, Error: "no container of this compose service"}, nil)
		return
	}
	for _, id := range ids {
		s := Source{Kind: SourceContainer, From: "container " + short(id), Status: SourceOK}
		data, err := opts.Docker.CopyOut(ctx, id, c.CoverDir)
		if errors.Is(err, ErrNoSuchPath) {
			s.Status = SourceEmpty
			add(s, nil)
			continue
		}
		if err != nil {
			s.Status, s.Error = SourceUnreachable, err.Error()
			add(s, nil)
			continue
		}
		fs, err := readTar(bytes.NewReader(data), path.Base(c.CoverDir))
		if err != nil {
			s.Status, s.Error = SourceFailed, err.Error()
			add(s, nil)
			continue
		}
		fs = fenceOut(fs, fenced, containerSource(id))
		s.Files = len(fs)
		if len(fs) == 0 {
			s.Status = SourceEmpty
		}
		add(s, fs)
	}
}

func gatherPods(ctx context.Context, opts Options, c *ClusterTarget, add func(Source, []covFile)) {
	from := c.Namespace + " " + c.Selector
	if opts.Kube == nil {
		add(Source{Kind: SourcePod, From: from, Status: SourceUnreachable, Error: "no cluster client"}, nil)
		return
	}
	pods, err := opts.Kube.Pods(ctx, c.Context, c.Namespace, c.Selector)
	if err != nil {
		add(Source{Kind: SourcePod, From: from, Status: SourceUnreachable, Error: err.Error()}, nil)
		return
	}
	if len(pods) == 0 {
		add(Source{Kind: SourcePod, From: from, Status: SourceUnreachable, Error: "no running pod (on " + c.Context + ")"}, nil)
		return
	}
	for _, p := range pods {
		s := Source{Kind: SourcePod, From: c.Namespace + "/" + p.Name}
		req, err := coverageRequest(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(c.Port)))
		var resp *http.Response
		if err == nil {
			resp, err = opts.Kube.Do(ctx, c.Context, c.Namespace, p.Name, c.Port, req)
		}
		fs := readResponse(resp, err, &s)
		for i := range fs {
			fs[i].live, fs[i].started, fs[i].scope = true, p.Started, podScope(c.Namespace, p.Name)
		}
		add(s, fs)
	}
}

// coverageRequest is serverkit's read: GET, the only request it serves.
func coverageRequest(ctx context.Context, addr string) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+CoveragePath, nil)
}

// pullHTTP pulls a host's /debug/coverage.
func pullHTTP(ctx context.Context, c *http.Client, addr string) ([]covFile, Source) {
	s := Source{Kind: SourcePull, From: addr}
	req, err := coverageRequest(ctx, addr)
	if err != nil {
		s.Status, s.Error = SourceFailed, err.Error()
		return nil, s
	}
	resp, err := c.Do(req)
	fs := readResponse(resp, err, &s)
	now := time.Now()
	for i := range fs {
		fs[i].live, fs[i].started, fs[i].scope = true, now, hostScope
	}
	return fs, s
}

// readResponse reads serverkit's answer into s and returns its files.
func readResponse(resp *http.Response, err error, s *Source) []covFile {
	if err != nil {
		s.Status, s.Error = SourceUnreachable, describeDialErr(err)
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		s.Status, s.Error = SourceFailed, "answered 404: the running binary was not built with -cover, or does not serve "+CoveragePath+" (restart it so it runs the instrumented build)"
		return nil
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		s.Status, s.Error = SourceFailed, fmt.Sprintf("answered %s: %s", resp.Status, strings.TrimSpace(string(body)))
		return nil
	}
	fs, err := readTar(resp.Body, "")
	if err != nil {
		s.Status, s.Error = SourceFailed, err.Error()
		return nil
	}
	s.Status, s.Files = SourceOK, len(fs)
	if len(fs) == 0 {
		s.Status = SourceEmpty
	}
	return fs
}

func describeDialErr(err error) string {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "connection refused (not running)"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timed out"
	}
	return err.Error()
}

// resetTarget starts t's coverage from zero without changing the process:
// it records each live process's counters as its baseline (baseline.go) and
// fences off the counter files flushed before it, by source, name and
// content (fence.go). Nothing is written unless the live processes answered,
// so a reset that saw nothing leaves the next collect as it was.
func resetTarget(ctx context.Context, opts Options, t Target, r *WorkloadResult) {
	at := opts.Now()
	// record publishes the baselines and the fence as one reset record
	// (reset.go): all of the reset, or, on failure, none of it.
	record := func(taken []baselineProcess, seen []fenceEntry) bool {
		if err := publishReset(ResetMarker(opts.ProjectRoot, opts.Env, t.Workload), at, seen, taken); err != nil {
			r.State, r.Reason = StateFailed, "reset not recorded (the previous reset still applies): "+err.Error()
			return false
		}
		return true
	}
	// baselineOf turns a pull into its processes' baselines, or fails the reset.
	baselineOf := func(fs []covFile, scope string) ([]baselineProcess, bool) {
		taken, err := takeBaselines(ctx, opts, scope, fs)
		if err != nil {
			r.State, r.Reason = StateFailed, "take the reset baseline (nothing recorded, the earlier counters are kept): "+err.Error()
			return nil, false
		}
		return taken, true
	}
	switch {
	case t.Host != nil:
		var taken []baselineProcess
		if t.Host.PullAddr == "" {
			r.Notes = append(r.Notes, "live counters not baselined: "+t.Host.Withheld)
		} else {
			fs, s := pullHTTP(ctx, opts.HTTP, t.Host.PullAddr)
			r.Sources = append(r.Sources, s)
			// Only an answer from the process gives a baseline: a refusal,
			// or no answer at all (down, timed out), records nothing.
			switch s.Status {
			case SourceOK, SourceEmpty:
			case SourceUnreachable:
				r.State, r.Reason = StateUnreachable, s.Error+" (not reset: the earlier counters are kept)"
				return
			default:
				r.State, r.Reason = StateFailed, s.Error+" (not reset: the earlier counters are kept)"
				return
			}
			var ok bool
			if taken, ok = baselineOf(fs, hostScope); !ok {
				return
			}
		}
		fs, err := readDir(t.Host.CoverDir)
		if err != nil {
			r.State, r.Reason = StateFailed, "list the counters flushed before the reset: "+err.Error()
			return
		}
		if !record(taken, fenceEntries(dirSource(opts.ProjectRoot, t.Host.CoverDir), fs)) {
			return
		}
	case t.Compose != nil:
		seen, err := composeFenceEntries(ctx, opts, t.Compose)
		if err != nil {
			r.State, r.Reason = StateFailed, "list the counters flushed before the reset: "+err.Error()
			return
		}
		if !record(nil, seen) {
			return
		}
		r.Notes = append(r.Notes, "compose withholds "+CoveragePath+", so a container's live counters cannot be baselined; the counter files flushed before now are left out of every later collect")
	case t.Cluster != nil:
		if t.Cluster.Withheld != "" {
			r.State, r.Reason = StateUnreachable, "no live pull: "+t.Cluster.Withheld
			return
		}
		// A pod's GOCOVERDIR is never read, so there is nothing flushed to
		// fence: the pods' baselines are the whole reset.
		var taken []baselineProcess
		gatherPods(ctx, opts, t.Cluster, func(s Source, fs []covFile) {
			r.Sources = append(r.Sources, s)
			if len(fs) > 0 && r.State == "" {
				if b, ok := baselineOf(fs, fs[0].scope); ok {
					taken = append(taken, b...)
				}
			}
		})
		if r.State != "" {
			return
		}
		ok := slices.ContainsFunc(r.Sources, func(s Source) bool { return s.Status == SourceOK || s.Status == SourceEmpty })
		if !ok {
			r.State, r.Reason = settleEmpty(r.Sources, 1)
			return
		}
		if !record(taken, nil) {
			return
		}
	default:
		r.State, r.Reason = StateFailed, "nothing could be reset"
		return
	}
	r.State = StateReset
}

// composeFenceEntries lists the counter files in every container of the
// service, each by its container: what a reset fences off. A container with
// no GOCOVERDIR yet has none; one that cannot be read fails the reset, since
// its files would otherwise be collected.
func composeFenceEntries(ctx context.Context, opts Options, c *ComposeTarget) ([]fenceEntry, error) {
	if opts.Docker == nil {
		return nil, errors.New("docker is not available")
	}
	ids, err := opts.Docker.Containers(ctx, *c)
	if err != nil {
		return nil, err
	}
	var seen []fenceEntry
	for _, id := range ids {
		data, err := opts.Docker.CopyOut(ctx, id, c.CoverDir)
		if errors.Is(err, ErrNoSuchPath) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("container %s: %w", short(id), err)
		}
		fs, err := readTar(bytes.NewReader(data), path.Base(c.CoverDir))
		if err != nil {
			return nil, fmt.Errorf("container %s: %w", short(id), err)
		}
		seen = append(seen, fenceEntries(containerSource(id), fs)...)
	}
	return seen, nil
}

// recordRun writes each collected workload's builds into the run, converts
// the newest, and records the lane.
func recordRun(ctx context.Context, opts Options, doc *Document, run string, writes []pending) {
	laneDir := testreport.LaneDir(run, opts.Lane)
	var inputs []testreport.LaneCoverageInput
	for _, p := range writes {
		r := &doc.Workloads[p.idx]
		relDir := testreport.LaneCoverDataDir + "/" + r.Name
		dir := filepath.Join(laneDir, filepath.FromSlash(relDir))
		covRoot := filepath.Join(laneDir, filepath.FromSlash(testreport.LaneCoverDataDir))
		if err := validateWorkloadName(r.Name); err != nil || dir == covRoot || !within(covRoot, dir) {
			r.State, r.Reason = StateFailed, fmt.Sprintf("workload name %q would write outside the run's coverage directory; not collected", r.Name)
			continue
		}
		// The pulls are cumulative and the flushed files are re-read
		// whole, so a collect replaces the workload's previous one.
		err := os.RemoveAll(dir)
		for i, b := range p.builds {
			if err != nil {
				break
			}
			d, label := dir, ""
			if i > 0 {
				d, label = filepath.Join(dir, "stale", b.hash), StaleBuildLabel
			}
			err = b.write(d)
			out := Build{MetaHash: b.hash, CounterFiles: b.counterFiles(), Dir: filepath.ToSlash(mustRel(run, d)), Label: label}
			if i == 0 {
				r.Build = &out
			} else {
				r.Stale = append(r.Stale, out)
			}
		}
		if err != nil {
			r.State, r.Reason, r.Build, r.Stale = StateFailed, "write coverage into the run: "+err.Error(), nil, nil
			continue
		}
		r.State = StateCollected
		if len(r.Stale) > 0 {
			r.Notes = append(r.Notes, fmt.Sprintf("%d older build(s) of this binary kept as %q and never merged: the newest build is the workload's coverage", len(r.Stale), StaleBuildLabel))
		}
		input := testreport.LaneCoverageInput{Path: relDir + "/", Format: testreport.CoverageGoCovdata}
		rec, err := readReset(ResetMarker(opts.ProjectRoot, opts.Env, r.Name))
		if err != nil {
			// The counts since the reset are unknown: record no input
			// rather than one that counts the time before it.
			r.State, r.Reason, r.Build, r.Stale = StateFailed, "not collected: "+err.Error(), nil, nil
			continue
		}
		comb, err := combineProcesses(ctx, opts, p.builds[0], rec.baselines())
		var tally *testreport.Tally
		switch {
		case err != nil:
			// The counts since the reset are unknown, and the raw ones would
			// count the time before it: record no input rather than a wrong one.
			r.State, r.Reason, r.Build, r.Stale = StateFailed, "combine the build's processes: "+err.Error(), nil, nil
			continue
		case comb != nil:
			if err := atomicfile.WriteFile(filepath.Join(dir, CombinedProfile), comb.profile, 0o644); err != nil {
				r.State, r.Reason, r.Build, r.Stale = StateFailed, "write coverage into the run: "+err.Error(), nil, nil
				continue
			}
			input = testreport.LaneCoverageInput{Path: relDir + "/" + CombinedProfile, Format: testreport.CoverageGoProfile}
			r.Build.Profile = r.Build.Dir + "/" + CombinedProfile
			if comb.subtracted > 0 || len(comb.restarted) > 0 {
				r.Build.SinceReset = &SinceReset{Processes: comb.subtracted, Restarted: len(comb.restarted)}
			}
			for _, name := range comb.restarted {
				r.Notes = append(r.Notes, name+" restarted since the reset: counted whole")
			}
			tally, err = tallyProfiles(opts, [][]byte{comb.profile})
		default:
			tally, err = convert(ctx, opts, dir)
		}
		if err != nil {
			r.Notes = append(r.Notes, "not converted (the files are kept; `forge test report` converts them): "+err.Error())
		} else {
			r.Build.Coverage = tally
		}
		inputs = append(inputs, input)
	}
	if len(inputs) == 0 {
		return
	}
	var warnings []string
	for _, r := range doc.Workloads {
		if r.State == StateUnreachable || r.State == StateFailed {
			warnings = append(warnings, fmt.Sprintf("collect: workload %s %s: %s", r.Name, r.State, r.Reason))
		}
	}
	if err := recordLane(opts, run, inputs, warnings); err != nil {
		doc.Warnings = append(doc.Warnings, err.Error())
	}
}

// recordLane adds the inputs to the lane's capture.json, creating the lane
// when no capture recorded it: a collect after `forge test capture --lane e2e`
// joins that lane, results and all.
func recordLane(opts Options, run string, inputs []testreport.LaneCoverageInput, warnings []string) error {
	m, err := testreport.ReadLaneManifest(run, opts.Lane)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("lane %s's capture.json is unreadable, so the collected coverage is not listed in it: %w", opts.Lane, err)
		}
		now := opts.Now().UTC()
		m = testreport.LaneManifest{
			Lane: opts.Lane, Command: opts.Command, StartedAt: now, FinishedAt: now,
			Format: testreport.LaneFormatUnknown, Results: testreport.LaneResultsMissing,
		}
	}
	for _, in := range inputs {
		// A workload's input is its covdata directory or its since-reset
		// profile in it: either replaces whichever an earlier collect listed.
		dir := strings.TrimSuffix(strings.TrimSuffix(in.Path, CombinedProfile), "/") + "/"
		m.Coverage = slices.DeleteFunc(m.Coverage, func(c testreport.LaneCoverageInput) bool {
			return c.Path == dir || c.Path == dir+CombinedProfile
		})
		m.Coverage = append(m.Coverage, in)
	}
	slices.SortFunc(m.Coverage, func(a, b testreport.LaneCoverageInput) int { return strings.Compare(a.Path, b.Path) })
	for _, w := range warnings {
		if !slices.Contains(m.Warnings, w) {
			m.Warnings = append(m.Warnings, w)
		}
	}
	return testcapture.RecordLane(run, m)
}

// convert converts the newest build in dir and tallies it, generated code
// excluded.
func convert(ctx context.Context, opts Options, dir string) (*testreport.Tally, error) {
	builds, err := opts.Convert(ctx, "", dir) // "": the go on PATH, like every other forge go call
	if err != nil {
		return nil, err
	}
	profiles := make([][]byte, 0, len(builds))
	for _, b := range builds {
		profiles = append(profiles, b.Profile)
	}
	return tallyProfiles(opts, profiles)
}

// tallyProfiles tallies text profiles, generated code excluded.
func tallyProfiles(opts Options, profiles [][]byte) (*testreport.Tally, error) {
	var docs []*testreport.Coverage
	for _, prof := range profiles {
		d, err := testreport.ParseCoverage(bytes.NewReader(prof), testreport.CoverageOptions{Format: testreport.FormatGoCover, Lane: testreport.Lane(opts.Lane)})
		if errors.Is(err, testreport.ErrNoInput) {
			continue
		}
		if err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	if len(docs) == 0 {
		return nil, errors.New("the conversion produced no profile")
	}
	merged, err := testreport.Merge(docs, testreport.MergeOptions{ModulePath: opts.ModulePath})
	if err != nil {
		return nil, err
	}
	tally := merged.Summary().Combined
	return &tally, nil
}

func rel(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(r, "..") {
		return filepath.ToSlash(r)
	}
	return p
}

func mustRel(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil {
		return r
	}
	return p
}
