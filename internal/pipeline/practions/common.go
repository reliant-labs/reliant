// Copyright (c) 2025 Reliant Labs
package practions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/pipeline/config"
	"github.com/reliant-labs/reliant/internal/pipeline/prpolicy"
	"github.com/reliant-labs/reliant/internal/serveraction"
)

// What pr.next may answer (the `pr` workflow's route switch).
const (
	NextReview   = "review"
	NextFix      = "fix"
	NextAnswer   = "answer"
	NextEnqueue  = "enqueue"
	NextClose    = "close"
	NextEscalate = "escalate"
	NextNone     = "none"
	// NextPush and NextDecide are pr.checked's and pr.pushed's answers.
	NextPush   = "push"
	NextDecide = "decide"
)

// ownerLease is how long a run's claim on a PR head holds without a write:
// the `pr` workflow's run timeout. A run that died is taken over after it,
// and the due scan wakes the PR then (next_action_at), so a lost run never
// strands it.
const ownerLease = 3 * time.Hour

// repageAfter is how long a PR that needs a person waits before it is paged
// again.
const repageAfter = 2 * time.Hour

// DefaultCheckRounds is how many times the fixer's scoped check may fail on
// one fix attempt before the PR needs a person (pipeline.yaml
// `fix.check_rounds`, which config does not parse yet).
const DefaultCheckRounds = 3

// principalName is who a structural verdict names as its reviewer.
const principalName = "reliant/pr.next@1"

// prCall is one action's view of the PR it was called for, read at the
// start of the call.
type prCall struct {
	req  *serveraction.Request
	rec  *core.PRRecord
	row  *core.PRPipeline
	repo *core.PipelineRepo
	cfg  config.Resolved
	as   Caller
	now  time.Time
}

func (p *prCall) shadow() bool { return p.repo.Mode == core.PipelineRepoModeShadow }

// load reads the PR record, its pipeline row and its enrolled repository,
// and checks that the run may act on it: the repository is the run's
// project's, in the run's org. The repository's mode is read here, on
// every call, so a mode change takes effect at the next action.
func (s *Service) load(ctx context.Context, req *serveraction.Request) (*prCall, error) {
	rec, err := s.record(ctx, req)
	if err != nil {
		return nil, err
	}
	row, err := s.deps.Store.GetPRPipeline(ctx, rec.ID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, serveraction.Errorf(serveraction.CodeFailedPrecondition, "%s#%d is not in the pipeline: its repository is not enrolled", rec.Repository, rec.Number)
	}
	repo, err := s.deps.Store.GetPipelineRepo(ctx, row.RepoID)
	if err != nil {
		return nil, err
	}
	if repo == nil {
		return nil, serveraction.Errorf(serveraction.CodeFailedPrecondition, "%s#%d's repository is no longer enrolled", rec.Repository, rec.Number)
	}
	// Tenancy: the run acts for a project of an org; the PR must be that
	// project's. Fail closed on anything the run's records do not say.
	pr := req.Principal
	if pr.ProjectID == "" || pr.ProjectID != repo.ProjectID || pr.OrgID != repo.OrgID {
		return nil, serveraction.Errorf(serveraction.CodePermissionDenied, "this run's project cannot act on %s#%d", rec.Repository, rec.Number)
	}
	cfg, err := resolveConfig(repo.ConfigJSON)
	if err != nil {
		return nil, serveraction.Errorf(serveraction.CodeFailedPrecondition, "%s's pipeline.yaml (at %s) does not resolve: %v", repo.Repository, repo.ConfigSHA, err)
	}
	inv := req.Invocation
	return &prCall{
		req: req, rec: rec, row: row, repo: repo, cfg: cfg, now: s.now(),
		as: Caller{RunID: inv.RunID, NodeID: inv.NodeID, ToolCallID: inv.ExecutionKey, Automation: pr.Automation},
	}, nil
}

// record finds the PR the call names: by `record_id`, or by `repository`
// (owner/name) and `number` among the repositories the run's project has
// enrolled. The second form is what a GitHub firing of the `pr` workflow
// knows: its trigger.record carries the repository and number but no id.
func (s *Service) record(ctx context.Context, req *serveraction.Request) (*core.PRRecord, error) {
	id, _ := req.Params["record_id"].(string)
	if id = strings.TrimSpace(id); id != "" {
		rec, err := s.deps.Store.GetPRRecordByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if rec == nil {
			return nil, serveraction.Errorf(serveraction.CodeNotFound, "no PR record %s", id)
		}
		return rec, nil
	}
	repository, _ := req.Params["repository"].(string)
	repository = strings.TrimSpace(repository)
	number := 0
	switch n := req.Params["number"].(type) {
	case float64:
		number = int(n)
	case int:
		number = n
	case int64:
		number = int(n)
	}
	if repository == "" || number <= 0 {
		return nil, serveraction.Errorf(serveraction.CodeInvalidArgument, "%s needs record_id, or repository and number", req.Name)
	}
	repos, err := s.deps.Store.ListEnrolledPipelineRepos(ctx)
	if err != nil {
		return nil, err
	}
	pr := req.Principal
	for _, r := range repos {
		if r.ProjectID != pr.ProjectID || r.OrgID != pr.OrgID || !strings.EqualFold(r.Repository, repository) {
			continue
		}
		rec, err := s.deps.Store.GetPRRecord(ctx, r.Provider, r.Repository, number, []string{r.RepositoryID})
		if err != nil {
			return nil, err
		}
		if rec != nil {
			return rec, nil
		}
	}
	return nil, serveraction.Errorf(serveraction.CodeNotFound, "%s#%d is not a PR of a repository this run's project has enrolled", repository, number)
}

// loadActive is load for an action that changes the PR: mode off refuses it.
func (s *Service) loadActive(ctx context.Context, req *serveraction.Request) (*prCall, error) {
	p, err := s.load(ctx, req)
	if err != nil {
		return nil, err
	}
	if p.repo.Mode == core.PipelineRepoModeOff {
		return nil, serveraction.Errorf(serveraction.CodeFailedPrecondition, "%s's pipeline mode is off", p.repo.Repository)
	}
	return p, nil
}

// resolveConfig is the repository's pipeline.yaml as config_json holds it
// (read from its base branch, validated), or the defaults before one has
// been read.
func resolveConfig(raw json.RawMessage) (config.Resolved, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed == "{}" {
		return config.Defaults(), nil
	}
	return config.Load(raw)
}

// casError turns a lost compare-and-set into an answer the workflow can
// branch on: the PR changed under the action (a new head, another run), so
// decide again.
func casError(err error) error {
	if errors.Is(err, core.ErrPipelineVersionConflict) {
		return &serveraction.Error{Code: serveraction.CodeAborted, Retryable: true,
			Message: "the PR's pipeline state changed while this action ran (a new head or another run); decide again"}
	}
	return err
}

// transition writes to at the version p read, and keeps p's row current.
func (s *Service) transition(ctx context.Context, p *prCall, to core.PRPipeline) error {
	row, err := s.deps.Store.TransitionPRPipeline(ctx, p.row.RecordID, p.row.Version, to)
	if err != nil {
		return casError(err)
	}
	p.row = row
	return nil
}

// The kinds of attempt the actions record, in detail_json.kind.
const (
	attemptReview = "review"
	attemptFix    = "fix"
	attemptCheck  = "check"
)

// attemptDetail is pipeline_attempts.detail_json as the actions write it.
type attemptDetail struct {
	Kind string `json:"kind"`
	// ExecutionKey is the action execution that wrote the row: a retry of
	// that execution finds its own row instead of writing another.
	ExecutionKey string `json:"execution_key,omitempty"`
	// Sessions are the threads that worked on the attempt.
	Sessions []string `json:"sessions,omitempty"`
	// FixAttemptID is the fix attempt a check belongs to.
	FixAttemptID string `json:"fix_attempt_id,omitempty"`

	Risk        string            `json:"risk,omitempty"`
	RiskReasons []string          `json:"risk_reasons,omitempty"`
	Files       map[string]string `json:"files,omitempty"`
	// FilesUnknown: no gate pool daemon listed the head's files, so Files
	// is empty for want of a list, not because nothing changed. The
	// verdict's owner decisions then match every path (fail closed).
	FilesUnknown bool `json:"files_unknown,omitempty"`

	Blockers []prpolicy.Finding `json:"blockers,omitempty"`
	Evidence map[string]any     `json:"evidence,omitempty"`

	Stages   []CheckStage `json:"stages,omitempty"`
	Failures []failure    `json:"failures,omitempty"`

	// PushedKey is the pr.pushed execution that finished a fix attempt.
	PushedKey  string `json:"pushed_key,omitempty"`
	PushedHead string `json:"pushed_head,omitempty"`
	Verdict    string `json:"verdict,omitempty"`
}

func detailOf(a *core.PipelineAttempt) attemptDetail {
	var d attemptDetail
	if a != nil && len(a.DetailJSON) > 0 {
		_ = json.Unmarshal(a.DetailJSON, &d)
	}
	return d
}

func (d attemptDetail) raw() json.RawMessage {
	b, err := json.Marshal(d)
	if err != nil {
		return nil
	}
	return b
}

// failure is a prpolicy.CheckFailure as JSON.
type failure struct {
	Check  string `json:"check"`
	Test   string `json:"test,omitempty"`
	Detail string `json:"detail,omitempty"`
}

func toCheckFailures(fs []failure) []prpolicy.CheckFailure {
	out := make([]prpolicy.CheckFailure, 0, len(fs))
	for _, f := range fs {
		out = append(out, prpolicy.CheckFailure{Check: f.Check, Test: f.Test, Detail: f.Detail})
	}
	return out
}

// attempts lists the PR's attempts at stage that carry kind.
func (s *Service) attempts(ctx context.Context, recordID string, stage core.PipelineAttemptStage, kind string) ([]*core.PipelineAttempt, error) {
	all, err := s.deps.Store.ListPipelineAttempts(ctx, core.PipelineAttemptSubjectPR, recordID, stage)
	if err != nil {
		return nil, err
	}
	out := all[:0:0]
	for _, a := range all {
		if detailOf(a).Kind == kind {
			out = append(out, a)
		}
	}
	return out, nil
}

// byExecution is the attempt this execution already wrote, or nil.
func byExecution(attempts []*core.PipelineAttempt, key string) *core.PipelineAttempt {
	if key == "" {
		return nil
	}
	for _, a := range attempts {
		if detailOf(a).ExecutionKey == key {
			return a
		}
	}
	return nil
}

// running is the run's attempt still in flight at head, or nil.
func running(attempts []*core.PipelineAttempt, runID, head string) *core.PipelineAttempt {
	for i := len(attempts) - 1; i >= 0; i-- {
		a := attempts[i]
		if a.Outcome == "" && a.RunID == runID && (head == "" || a.HeadSHA == head) {
			return a
		}
	}
	return nil
}

// blockersOf are a verdict's findings that block. Its findings_json holds the
// round's findings as they count (prpolicy.Decision.Findings), so a late
// finding is already a nit there.
func blockersOf(v *core.PRHeadVerdictRecord) []prpolicy.Finding {
	var out []prpolicy.Finding
	for _, f := range findingsOf(v) {
		if !strings.EqualFold(strings.TrimSpace(string(f.Severity)), string(prpolicy.SeverityNit)) {
			out = append(out, f)
		}
	}
	return out
}

func findingsOf(v *core.PRHeadVerdictRecord) []prpolicy.Finding {
	if v == nil || len(v.FindingsJSON) == 0 {
		return nil
	}
	var fs []prpolicy.Finding
	_ = json.Unmarshal(v.FindingsJSON, &fs)
	return fs
}

// latestOnHead is the highest round's verdict on head, or nil.
func latestOnHead(verdicts []*core.PRHeadVerdictRecord, head string) *core.PRHeadVerdictRecord {
	var best *core.PRHeadVerdictRecord
	for _, v := range verdicts {
		if v.HeadSHA == head && (best == nil || v.Round > best.Round) {
			best = v
		}
	}
	return best
}

// unmetDecisions are the decisions a verdict requires that no decision in
// force on the head meets.
func (s *Service) unmetDecisions(ctx context.Context, p *prCall, v *core.PRHeadVerdictRecord) ([]string, error) {
	if len(v.RequiredDecisions) == 0 {
		return nil, nil
	}
	active, err := s.deps.Store.ActivePipelineDecisionsFor(ctx, core.PipelineDecisionSubjectPRHead, p.rec.ID, v.HeadSHA, p.now)
	if err != nil {
		return nil, err
	}
	var unmet []string
	for _, k := range v.RequiredDecisions {
		met := slices.ContainsFunc(active, func(d *core.PipelineDecision) bool {
			return d.Kind == k && d.HeadSHA == v.HeadSHA &&
				(d.State == core.PipelineDecisionApproved || d.State == core.PipelineDecisionApplied)
		})
		if !met {
			unmet = append(unmet, string(k))
		}
	}
	return unmet, nil
}

// requiredDecisions are the owner decisions pipeline.yaml's owner_approval
// requires of a head with these changed paths. A when or when_cmd the
// server cannot evaluate yet requires its decision (fail closed).
func requiredDecisions(cfg config.Resolved, head string, paths []string) []core.PipelineDecisionKind {
	var out []core.PipelineDecisionKind
	for _, d := range prpolicy.RequiredDecisions(head, paths, "", cfg.OwnerApproval, prpolicy.ApprovalConditions{}) {
		out = append(out, core.PipelineDecisionKind(d.Kind))
	}
	return out
}

// requiredDecisionsAnyPath is requiredDecisions for a head whose changed
// paths are not known: an owner_approval entry gated on paths requires its
// decision as though a path matched, so an unknown diff never skips one.
func requiredDecisionsAnyPath(cfg config.Resolved, head string) []core.PipelineDecisionKind {
	approvals := slices.Clone(cfg.OwnerApproval)
	for i := range approvals {
		approvals[i].Paths = nil
	}
	cfg.OwnerApproval = approvals
	return requiredDecisions(cfg, head, nil)
}

func diffPaths(d prpolicy.Diff) []string {
	paths := make([]string, 0, len(d.Files))
	for _, f := range d.Files {
		paths = append(paths, f.Path)
		if f.PreviousPath != "" {
			paths = append(paths, f.PreviousPath)
		}
	}
	return paths
}

// jsonValue makes v JSON-shaped (maps, slices, strings, float64s), as an
// action's output must be.
func jsonValue(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

func findingsValue(fs []prpolicy.Finding) []any {
	if fs == nil {
		fs = []prpolicy.Finding{}
	}
	v, _ := jsonValue(fs).([]any)
	if v == nil {
		v = []any{}
	}
	return v
}

func prName(p *prCall) string { return fmt.Sprintf("%s#%d", p.rec.Repository, p.rec.Number) }

func timePtr(t time.Time) *time.Time { return &t }
