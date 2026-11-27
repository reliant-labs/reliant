// Copyright (c) 2025 Reliant Labs
package gatehost

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/mergequeue/gaterun"
	"github.com/reliant-labs/reliant/internal/pipeline/config"
)

// What report reads back from the boundary, and how much of it. The output
// directory is written by code running beside the pull request's, so every
// byte is data: the stream is capped as a whole and per file, it is parsed in
// memory and never extracted, and a link or any other non-regular entry is
// refused, so nothing can point the reader outside the directory.
const (
	reportMaxBytes     = 8 << 20
	resultMaxBytes     = 256 << 10
	logTailBytes       = 64 << 10
	scanDocMaxBytes    = 2 << 20
	repoResultMaxBytes = 1 << 20
)

const (
	resultFile     = "gate-result.json"
	repoResultFile = "repo-gate-result.json"
	scanDocFile    = "scan/vuln-gate.json"
	scanStdoutFile = "scan/stdout.json"
)

// output is the output directory as read: regular files by path relative to
// it, and the entries refused.
type output struct {
	files   map[string][]byte
	refused map[string]string
}

// fileCap is the most a file at rel may hold; 0 means report does not read
// it.
func fileCap(rel string) int64 {
	switch {
	case rel == resultFile:
		return resultMaxBytes
	case rel == repoResultFile:
		return repoResultMaxBytes
	case rel == scanDocFile, rel == scanStdoutFile:
		return scanDocMaxBytes
	case strings.HasPrefix(rel, "logs/") && strings.HasSuffix(rel, ".log") && strings.Count(rel, "/") == 1:
		return logTailBytes
	}
	return 0
}

// readOutput reads a CopyOut tar stream of the output directory, whose
// entries are named under the directory's base name ("out/...").
func readOutput(r io.Reader) (output, error) {
	o := output{files: map[string][]byte{}, refused: map[string]string{}}
	tr := tar.NewReader(io.LimitReader(r, reportMaxBytes+1))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return o, nil
		}
		if err != nil {
			return o, fmt.Errorf("read the gate's output: %w", err)
		}
		rel, ok := outputRel(h.Name)
		if !ok {
			o.refused[h.Name] = "outside the output directory"
			continue
		}
		if rel == "" {
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
		default:
			// A symlink or hard link is never followed, and neither is
			// anything else that is not plain data.
			o.refused[rel] = "not a regular file"
			continue
		}
		limit := fileCap(rel)
		if limit == 0 {
			continue
		}
		if h.Size > limit {
			o.refused[rel] = fmt.Sprintf("%d bytes, over the %d cap", h.Size, limit)
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, limit))
		if err != nil {
			return o, fmt.Errorf("read %s from the gate's output: %w", rel, err)
		}
		o.files[rel] = b
	}
}

// outputRel is name relative to the output directory: "" for the directory
// itself, false for anything outside it.
func outputRel(name string) (string, bool) {
	if strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return "", false
	}
	for _, part := range strings.Split(strings.TrimSuffix(name, "/"), "/") {
		if part == ".." {
			return "", false
		}
	}
	clean := path.Clean(name)
	base := path.Base(gaterun.OutDir)
	switch {
	case clean == base:
		return "", true
	case strings.HasPrefix(clean, base+"/"):
		return strings.TrimPrefix(clean, base+"/"), true
	}
	return "", false
}

// resultDoc is gate-result.json as the stage script writes it.
type resultDoc struct {
	Version  int `json:"version"`
	ExitCode int `json:"exit_code"`
	Stages   []struct {
		Name     config.StageName   `json:"name"`
		State    gaterun.StageState `json:"state"`
		ExitCode int                `json:"exit_code"`
		Seconds  int64              `json:"seconds"`
		Note     string             `json:"note"`
		// DockerSkips is the line count of the stage's EnvDockerSkips log.
		DockerSkips int `json:"docker_skips"`
	} `json:"stages"`
}

var errResultInvalid = errors.New("the gate's result is invalid")

// parseStages checks gate-result.json against the plan the gate was given
// and returns its stages and its exit code.
func parseStages(plan []gaterun.PlanStage, b []byte) ([]gaterun.StageResult, int, error) {
	bad := func(format string, a ...any) ([]gaterun.StageResult, int, error) {
		return nil, 0, fmt.Errorf("%w: %s", errResultInvalid, fmt.Sprintf(format, a...))
	}
	var doc resultDoc
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return bad("%s: %v", resultFile, err)
	}
	if doc.Version != 1 {
		return bad("version %d", doc.Version)
	}
	if len(doc.Stages) != len(plan) {
		return bad("%d stages, the plan has %d", len(doc.Stages), len(plan))
	}
	out := make([]gaterun.StageResult, len(plan))
	stopped := false
	for i, s := range doc.Stages {
		p := plan[i]
		if s.Name != p.Name {
			return bad("stage %d is %q, the plan's is %q", i, s.Name, p.Name)
		}
		r := gaterun.StageResult{Name: s.Name, Verdict: s.State, Exit: s.ExitCode, Duration: time.Duration(max(s.Seconds, 0)) * time.Second, Summary: p.Reason}
		switch {
		case p.State == config.StageOff:
			if s.State != gaterun.StageOff {
				return bad("stage %s is off in the plan, %q in the result", s.Name, s.State)
			}
		case p.State == config.StageUnavailable:
			if s.State != gaterun.StageUnavailable {
				return bad("stage %s is unavailable in the plan, %q in the result", s.Name, s.State)
			}
		case stopped:
			if s.State != gaterun.StageSkipped {
				return bad("stage %s ran after a stage that did not pass", s.Name)
			}
			r.Summary = "an earlier stage did not pass"
		default:
			if s.State != classStage(gaterun.ClassifyExit(s.ExitCode)) {
				return bad("stage %s is %q with exit %d", s.Name, s.State, s.ExitCode)
			}
			stopped = s.State != gaterun.StagePass
			r.Summary = ""
			if s.Note == "forge_vuln_gate_missing" {
				r.Summary = "forge vuln gate is not in the gate image"
			}
			// A count from inside the boundary is data: it can only add a
			// note, never change a verdict.
			r.SkippedDockerTests = max(s.DockerSkips, 0)
			if r.SkippedDockerTests > 0 && r.Summary == "" {
				r.Summary = fmt.Sprintf("%d container test(s) skipped: docker unavailable (gate)", r.SkippedDockerTests)
			}
		}
		if s.State == gaterun.StageOff || s.State == gaterun.StageUnavailable || s.State == gaterun.StageSkipped {
			r.Exit = -1
		}
		out[i] = r
	}
	return out, doc.ExitCode, nil
}

func classStage(c gaterun.Class) gaterun.StageState {
	switch c {
	case gaterun.ClassPass:
		return gaterun.StagePass
	case gaterun.ClassFail:
		return gaterun.StageFail
	default:
		return gaterun.StageUnproven
	}
}

// stagesClass is the gate's class from its stages: any fail fails it, any
// unproven leaves it unproven, and off, unavailable and skipped stages
// decide nothing.
func stagesClass(stages []gaterun.StageResult) gaterun.Class {
	c := gaterun.ClassPass
	for _, s := range stages {
		switch s.Verdict {
		case gaterun.StageFail:
			return gaterun.ClassFail
		case gaterun.StageUnproven:
			c = gaterun.ClassUnproven
		}
	}
	return c
}

// scanDoc is the part of forge's vuln document the gate reads.
type scanDoc struct {
	Gate struct {
		Verdict string `json:"verdict"`
		Reason  string `json:"reason"`
	} `json:"gate"`
}

// judgeScan refines a builtin sbom_scan stage from forge's document: exit 0
// is pass, warn, or off (forge's vuln_gate mode off, shown as off, never as
// passed); exit 1 is a block; exit 2 is unscanned or stale. A document that
// is missing or disagrees with the exit leaves the stage unproven.
func judgeScan(st *gaterun.StageResult, o output) *gaterun.ScanReport {
	raw, ok := o.files[scanDocFile]
	if !ok {
		raw, ok = o.files[scanStdoutFile]
	}
	var doc scanDoc
	if ok && json.Unmarshal(raw, &doc) != nil {
		ok = false
	}
	var rep *gaterun.ScanReport
	if ok {
		rep = &gaterun.ScanReport{Verdict: doc.Gate.Verdict, Document: json.RawMessage(raw)}
	}
	switch st.Verdict {
	case gaterun.StagePass:
		switch {
		case !ok:
			st.Verdict, st.Summary = gaterun.StageUnproven, "the scan exited 0 but wrote no readable verdict document"
		case doc.Gate.Verdict == "pass":
		case doc.Gate.Verdict == "warn":
			st.Verdict, st.Summary = gaterun.StageWarn, "a finding warns; nothing blocks"
		case doc.Gate.Verdict == "off":
			st.Verdict, st.Summary = gaterun.StageOff, "the project's vuln_gate mode is off"
		default:
			st.Verdict, st.Summary = gaterun.StageUnproven, fmt.Sprintf("the scan exited 0 with verdict %q", doc.Gate.Verdict)
		}
	case gaterun.StageFail:
		st.Summary = "a finding blocks (vuln_gate_blocked)"
	case gaterun.StageUnproven:
		if st.Summary == "" {
			st.Summary = "not scanned (vuln_gate_unscanned)"
			if ok && doc.Gate.Verdict != "" {
				st.Summary = fmt.Sprintf("not scanned: %s (vuln_gate_unscanned)", doc.Gate.Verdict)
			}
		}
	}
	return rep
}

// repoReport is the part of the repository's own .reliant/gate-result.json
// the queue reads (pipeline redesign §3.4); everything else stays raw.
type repoReport struct {
	FailingTests []string `json:"failing_tests"`
	InfraMatches []string `json:"infra_matches"`
}

// maxListed bounds the failing tests and infra matches a result carries.
const maxListed = 500

// assemble is report's verdict: the backend's end, the stages as the result
// file says, and the two cross-checked. Every disagreement is unproven.
func assemble(gateID string, iso gaterun.Isolation, plan []gaterun.PlanStage, res Result, o output) gaterun.GateResult {
	g := gaterun.GateResult{GateID: gateID, Isolation: iso, Duration: res.Duration}
	var stages []gaterun.StageResult
	unproven := func(code, reason string) gaterun.GateResult {
		g.Class, g.ReasonCode, g.Reason = gaterun.ClassUnproven, code, reason
		g.Stages = stageMap(stages)
		return g
	}

	var docExit int
	var perr error
	if reason, refused := o.refused[resultFile]; refused {
		perr = fmt.Errorf("%w: %s refused: %s", errResultInvalid, resultFile, reason)
	} else if b, ok := o.files[resultFile]; !ok {
		perr = fmt.Errorf("%w: the gate wrote no %s", errResultInvalid, resultFile)
	} else {
		stages, docExit, perr = parseStages(plan, b)
	}
	for i := range stages {
		st := &stages[i]
		if log, ok := o.files["logs/"+string(st.Name)+".log"]; ok {
			st.LogTail = string(log)
		}
		if plan[i].Kind == config.StageBuiltinScan && (st.Verdict == gaterun.StagePass || st.Verdict == gaterun.StageFail || st.Verdict == gaterun.StageUnproven) {
			g.Scan = judgeScan(st, o)
		}
	}
	if raw, ok := o.files[repoResultFile]; ok && json.Valid(raw) {
		g.RepoResult = json.RawMessage(raw)
		var rr repoReport
		if json.Unmarshal(raw, &rr) == nil {
			g.FailingTests = rr.FailingTests[:min(len(rr.FailingTests), maxListed)]
			g.InfraMatches = rr.InfraMatches[:min(len(rr.InfraMatches), maxListed)]
		}
	}

	if res.Class == gaterun.ClassUnproven {
		code := gaterun.ReasonBackendFailed
		if res.TimedOut || res.OOMKilled {
			code = gaterun.ReasonLimitExceeded
		}
		return unproven(code, "the gate's end was not seen whole: "+res.Reason)
	}
	if perr != nil {
		stages = nil
		return unproven(gaterun.ReasonResultInvalid, perr.Error())
	}
	stagesExit := gaterun.ClassifyExit(docExit)
	// The script's exit, its own record of it, and its stages must agree
	// before the scan's refinement (which can only move a pass to unproven).
	if gaterun.ClassifyExit(res.ExitCode) != stagesExit || stagesExit != unrefinedClass(stages, plan) {
		return unproven(gaterun.ReasonResultInvalid, fmt.Sprintf("%v: the gate exited %d, its result says %d", errResultInvalid, res.ExitCode, docExit))
	}
	g.Class = stagesClass(stages)
	g.Stages = stageMap(stages)
	for _, st := range stages {
		g.SkippedDockerTests += st.SkippedDockerTests
	}
	for _, st := range stages {
		if st.Verdict == gaterun.StageFail || st.Verdict == gaterun.StageUnproven {
			g.ReasonCode = gaterun.ReasonStage
			g.Reason = fmt.Sprintf("stage %s: %s (exit %d)", st.Name, st.Verdict, st.Exit)
			if st.Summary != "" {
				g.Reason += ": " + st.Summary
			}
			break
		}
	}
	return g
}

func stageMap(stages []gaterun.StageResult) map[config.StageName]gaterun.StageResult {
	if stages == nil {
		return nil
	}
	m := make(map[config.StageName]gaterun.StageResult, len(stages))
	for _, s := range stages {
		m[s.Name] = s
	}
	return m
}

// unrefinedClass is the class the stage script computed: the stages' class
// with a builtin scan that exited 0 counted as a pass, whatever its document
// said.
func unrefinedClass(stages []gaterun.StageResult, plan []gaterun.PlanStage) gaterun.Class {
	c := gaterun.ClassPass
	for i, s := range stages {
		state := s.Verdict
		if plan[i].Kind == config.StageBuiltinScan && s.Exit == 0 {
			state = gaterun.StagePass
		}
		switch state {
		case gaterun.StageFail:
			return gaterun.ClassFail
		case gaterun.StageUnproven:
			c = gaterun.ClassUnproven
		}
	}
	return c
}
