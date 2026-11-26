// Package doctor provides health checks for the forge development stack.
//
// It validates that all services (Docker containers, telemetry backends,
// debugger) are running and correctly connected.
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Status represents the outcome of a single health check.
type Status string

// Status enum values.
//
// StatusSkip and StatusUnknown are DIFFERENT answers and must never render
// the same. A check that could not determine its answer used to come out as
// a gray "–", byte-identical to a check that legitimately does not apply —
// so `App Health  app port 8080 not discovered` read exactly like
// `tool: mkcert  not required for this project`. One of those is a hole in
// the report and the other is a finished answer.
const (
	StatusPass Status = "pass"
	StatusFail Status = "fail"
	StatusWarn Status = "warn"
	// StatusSkip — NOT APPLICABLE. The check asked its question and the
	// project's own shape answers it: there is no cmd/ tree, no
	// environments declared, no mkcert needed. Nothing is missing.
	StatusSkip Status = "skip"
	// StatusUnknown — UNDETERMINED. The check could not obtain a fact it
	// needed, so it has no answer to give. This is not a pass: it rolls up
	// to Warn, and the trailer counts it separately.
	StatusUnknown Status = "unknown"
)

// CheckResult is the outcome of a single health check.
type CheckResult struct {
	Name     string        `json:"name"`
	Status   Status        `json:"status"`
	Message  string        `json:"message"`
	Evidence string        `json:"evidence,omitempty"`
	Duration time.Duration `json:"duration_ms"`
	// Cluster is the structured cluster inventory, set only by the Cluster
	// Workloads check. Message and Evidence are prose for a human; this is
	// the same facts as data, so a consumer never has to parse them. It is
	// omitted (nil) by every other check, which is a third answer distinct
	// from an unknown inventory and an empty one: forge was not asked.
	Cluster *ClusterInventory `json:"cluster,omitempty"`
}

// MarshalJSON customises the JSON output so duration is in milliseconds.
func (r CheckResult) MarshalJSON() ([]byte, error) {
	type alias CheckResult
	return json.Marshal(struct {
		alias
		Duration int64 `json:"duration_ms"`
	}{
		alias:    alias(r),
		Duration: r.Duration.Milliseconds(),
	})
}

// Report is the aggregate result of all checks.
type Report struct {
	Overall  Status        `json:"overall"`
	Duration time.Duration `json:"-"`
	Checks   []CheckResult `json:"checks"`
}

// MarshalJSON customises the JSON output so duration is in milliseconds.
func (r Report) MarshalJSON() ([]byte, error) {
	type alias Report
	return json.Marshal(struct {
		alias
		Duration int64 `json:"duration_ms"`
	}{
		alias:    alias(r),
		Duration: r.Duration.Milliseconds(),
	})
}

// CheckFunc is a function that performs a single health check.
// It receives the shared Environment (discovered ports, project config)
// and returns a result.
type CheckFunc func(ctx context.Context, env *Environment) CheckResult

// Environment holds runtime information discovered during checks,
// shared across all check functions.
type Environment struct {
	ProjectName string
	ProjectDir  string // directory containing docker-compose.yml

	// Env is the deploy environment an env-runtime run is about (""
	// for the project-health set, which spans every environment).
	Env string

	// Target holds the addresses the CALLER resolved for this run. doctor
	// discovers no host ports of its own; see [RuntimeTarget].
	Target RuntimeTarget

	// DeployShaper classifies each rendered environment the way the deploy
	// path does (see [DeployShaper]). Nil judges every environment as a
	// kubectl-applied render.
	DeployShaper DeployShaper

	// Compose runs every `docker compose` question the checks ask (see
	// [ComposeRunner]). Nil is the host's docker.
	Compose ComposeRunner

	mu    sync.RWMutex
	Ports map[string]string // "app:8080" -> "0.0.0.0:55010"

	// deployOnce/deployCache memoise the KCL render of every declared
	// environment. The deploy checks (deploy.go) all run in doctor's
	// parallel phase and all need the same manifests, so they share one
	// evaluation instead of paying for it five times.
	deployOnce  sync.Once
	deployCache []envRender
}

// SetPort stores a discovered host:port mapping.
func (e *Environment) SetPort(service string, containerPort int, hostAddr string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.Ports == nil {
		e.Ports = make(map[string]string)
	}
	e.Ports[fmt.Sprintf("%s:%d", service, containerPort)] = hostAddr
}

// GetPort retrieves a discovered host address for a service port.
func (e *Environment) GetPort(service string, containerPort int) (string, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	addr, ok := e.Ports[fmt.Sprintf("%s:%d", service, containerPort)]
	return addr, ok
}

// Doctor orchestrates running health checks.
type doctor struct {
	env    *Environment
	checks []namedCheck
}

type namedCheck struct {
	name string
	fn   CheckFunc
}

// newDoctor creates a doctor orchestrator for the given project.
func newDoctor(projectName, projectDir string) *doctor {
	return &doctor{
		env: &Environment{
			ProjectName: projectName,
			ProjectDir:  projectDir,
			Ports:       make(map[string]string),
		},
	}
}

// Register adds a check to the doctor.
func (d *doctor) register(name string, fn CheckFunc) {
	d.checks = append(d.checks, namedCheck{name: name, fn: fn})
}

// RunSequential runs checks that must complete before parallel checks
// (e.g., Docker check discovers ports needed by other checks).
// RunParallel runs remaining checks concurrently.
func (d *doctor) run(ctx context.Context, sequential []string) Report {
	start := time.Now()

	seqSet := make(map[string]bool, len(sequential))
	for _, name := range sequential {
		seqSet[name] = true
	}

	var results []CheckResult

	// Phase 1: sequential checks (order preserved).
	for _, c := range d.checks {
		if !seqSet[c.name] {
			continue
		}
		r := d.runCheck(ctx, c)
		results = append(results, r)
		// If a sequential check fails, still continue — other checks
		// will gracefully degrade when ports are missing.
	}

	// Phase 2: parallel checks.
	var parallel []namedCheck
	for _, c := range d.checks {
		if seqSet[c.name] {
			continue
		}
		parallel = append(parallel, c)
	}

	if len(parallel) > 0 {
		var mu sync.Mutex
		var wg sync.WaitGroup
		wg.Add(len(parallel))
		parallelResults := make([]CheckResult, len(parallel))
		for i, c := range parallel {
			go func(idx int, check namedCheck) {
				defer wg.Done()
				r := d.runCheck(ctx, check)
				mu.Lock()
				parallelResults[idx] = r
				mu.Unlock()
			}(i, c)
		}
		wg.Wait()
		results = append(results, parallelResults...)
	}

	report := Report{
		Overall:  StatusPass,
		Duration: time.Since(start),
		Checks:   results,
	}

	for _, r := range results {
		if r.Status == StatusFail {
			report.Overall = StatusFail
			break
		}
		// An undetermined check is not a pass. It is not a failure either —
		// nothing is known to be broken — so it lands on Warn alongside a
		// real warning, and printReport names the two separately.
		if (r.Status == StatusWarn || r.Status == StatusUnknown) && report.Overall == StatusPass {
			report.Overall = StatusWarn
		}
	}

	return report
}

func (d *doctor) runCheck(ctx context.Context, c namedCheck) CheckResult {
	start := time.Now()
	r := c.fn(ctx, d.env)
	r.Name = c.name
	r.Duration = time.Since(start)
	return r
}

// ANSI helpers.
const (
	colorGreen  = "\033[32m"
	colorRed    = "\033[31m"
	colorYellow = "\033[33m"
	colorGray   = "\033[90m"
	colorReset  = "\033[0m"
)

func statusIcon(s Status) string {
	switch s {
	case StatusPass:
		return colorGreen + "✓" + colorReset
	case StatusFail:
		return colorRed + "✗" + colorReset
	case StatusWarn:
		return colorYellow + "!" + colorReset
	case StatusSkip:
		return colorGray + "–" + colorReset
	case StatusUnknown:
		// Deliberately NOT the gray dash: "I could not tell" must be
		// visually distinct from "does not apply here".
		return colorYellow + "?" + colorReset
	default:
		return "?"
	}
}

// PrintReport writes a human-readable report to w.
func printReport(w io.Writer, report Report, verbose bool) {
	for _, r := range report.Checks {
		icon := statusIcon(r.Status)
		name := fmt.Sprintf("%-20s", r.Name)
		_, _ = fmt.Fprintf(w, "  %s %s %s", icon, name, r.Message)
		if r.Duration > 0 {
			_, _ = fmt.Fprintf(w, "  %s(%s)%s", colorGray, r.Duration.Round(time.Millisecond), colorReset)
		}
		_, _ = fmt.Fprintln(w)

		if verbose && r.Evidence != "" {
			for _, line := range strings.Split(r.Evidence, "\n") {
				_, _ = fmt.Fprintf(w, "    %s%s%s\n", colorGray, line, colorReset)
			}
		}
	}
	_, _ = fmt.Fprintln(w)

	var undetermined, warnings int
	for _, r := range report.Checks {
		switch r.Status {
		case StatusUnknown:
			undetermined++
		case StatusWarn:
			warnings++
		}
	}

	switch report.Overall {
	case StatusPass:
		_, _ = fmt.Fprintf(w, "  %s All checks passed %s(%s)%s\n", colorGreen+"✓"+colorReset, colorGray, report.Duration.Round(time.Millisecond), colorReset)
	case StatusWarn:
		// Name the undetermined checks in the trailer. Folding them into
		// "some checks have warnings" is how a hole in the report reads as
		// a soft pass.
		var parts []string
		if undetermined > 0 {
			parts = append(parts, fmt.Sprintf("%d UNDETERMINED (not a pass — forge could not obtain the facts)", undetermined))
		}
		if warnings > 0 {
			parts = append(parts, fmt.Sprintf("%d warning(s)", warnings))
		}
		summary := "Some checks have warnings"
		if len(parts) > 0 {
			summary = strings.Join(parts, ", ")
		}
		_, _ = fmt.Fprintf(w, "  %s %s %s(%s)%s\n", colorYellow+"!"+colorReset, summary, colorGray, report.Duration.Round(time.Millisecond), colorReset)
	case StatusFail:
		var failures int
		for _, r := range report.Checks {
			if r.Status == StatusFail {
				failures++
			}
		}
		_, _ = fmt.Fprintf(w, "  %s %d check(s) failed %s(%s)%s\n", colorRed+"✗"+colorReset, failures, colorGray, report.Duration.Round(time.Millisecond), colorReset)
	}
}

// PrintJSON writes the report as JSON to w.
func printJSON(w io.Writer, report Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}
