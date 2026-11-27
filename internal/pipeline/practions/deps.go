// Copyright (c) 2025 Reliant Labs
package practions

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/pipeline/grants"
	"github.com/reliant-labs/reliant/internal/pipeline/prpolicy"
	"github.com/reliant-labs/reliant/internal/serveraction"
)

// Store is the pipeline's database as the actions use it. *db.Repo
// satisfies it.
type Store interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error

	GetPRRecordByID(ctx context.Context, id string) (*core.PRRecord, error)
	GetPRRecord(ctx context.Context, provider, repository string, number int, repositoryIDs []string) (*core.PRRecord, error)
	ListPRRecordsAtHead(ctx context.Context, provider, repositoryID, head, except string) ([]*core.PRRecord, error)
	GetPipelineRepo(ctx context.Context, id string) (*core.PipelineRepo, error)
	ListEnrolledPipelineRepos(ctx context.Context) ([]*core.PipelineRepo, error)
	ListReposByProject(ctx context.Context, projectID string) ([]*core.Repo, error)

	GetPRPipeline(ctx context.Context, recordID string) (*core.PRPipeline, error)
	ListOpenPRPipelines(ctx context.Context, repoID string) ([]*core.PRPipeline, error)
	ResetPRPipelineForHead(ctx context.Context, recordID string, version int64, head string, next core.PRPipelineNext) (*core.PRPipeline, error)
	TransitionPRPipeline(ctx context.Context, recordID string, version int64, to core.PRPipeline) (*core.PRPipeline, error)

	InsertPRHeadVerdict(ctx context.Context, v core.PRHeadVerdictRecord) (*core.PRHeadVerdictRecord, error)
	ListPRHeadVerdicts(ctx context.Context, recordID string) ([]*core.PRHeadVerdictRecord, error)

	NextPipelineAttempt(ctx context.Context, a core.PipelineAttempt) (*core.PipelineAttempt, error)
	FinishPipelineAttempt(ctx context.Context, id string, outcome core.PipelineAttemptOutcome, detail json.RawMessage) (*core.PipelineAttempt, error)
	ListPipelineAttempts(ctx context.Context, kind core.PipelineAttemptSubjectKind, subjectID string, stage core.PipelineAttemptStage) ([]*core.PipelineAttempt, error)

	ActivePipelineHold(ctx context.Context, recordID string, now time.Time) (*core.PipelineDecision, error)
	ActivePipelineDecisionsFor(ctx context.Context, kind core.PipelineDecisionSubjectKind, subjectID, head string, now time.Time) ([]*core.PipelineDecision, error)
	LatestQueueEntryForRecord(ctx context.Context, recordID string) (*core.QueueEntry, error)
}

// Caller is the run and node a GitHub call is made for: the credential
// source resolves the run's owner and activation from it, and the write
// ledger attributes the call to it.
type Caller struct {
	RunID, NodeID, ToolCallID string
	// Automation is the trigger the run belongs to, for the footer on what
	// the bot writes.
	Automation string
}

// PullRequest is what the actions read of a PR on GitHub.
type PullRequest struct {
	Number int
	// State is open or closed; a merged PR is closed with Merged set.
	State   string
	Merged  bool
	Draft   bool
	HeadSHA string
	// HeadRef is the head branch's name, which the fixer checks out.
	HeadRef string
	BaseRef string
	Author  string
	Body    string
}

// ChangedFiles is a PR's diff as the risk tier reads it, with each file's
// blob sha at the head, which tells a later round which files changed since.
type ChangedFiles struct {
	Diff  prpolicy.Diff
	Blobs map[string]string
}

// GitHub is the PR reads and writes the actions make: metadata only, never
// a file's contents or a diff (internal/architecture's
// TestServerRolesRunNoCode). Reads are made as the run's owner; Comment and
// Close as the Reliant GitHub App's bot. Only the writer (writer.go) calls
// Comment and Close, so shadow mode is enforced in one place.
type GitHub interface {
	PullRequest(ctx context.Context, as Caller, repo string, number int) (PullRequest, error)
	// BranchTip is the commit a branch points at now.
	BranchTip(ctx context.Context, as Caller, repo, branch string) (string, error)
	Comment(ctx context.Context, as Caller, repo string, number int, body string) error
	Close(ctx context.Context, as Caller, repo string, number int) error
}

// Files lists a PR's changed files without its diff reaching the worker: a
// job on a daemon of the repository's gate pool, which diffs head against
// its merge base with base (the base branch's tip) and reports each file's
// status, line counts and blob. The worker never reads the diff.
type Files interface {
	ChangedFiles(ctx context.Context, repo *core.PipelineRepo, base, head string) (ChangedFiles, error)
}

// ErrFilesUnavailable is a Files that cannot list the files and will not
// soon: the gate pool has no member that can (none connected, or every one
// refused or runs a Reliant too old), or a member's report did not hold
// up. pr.next then judges the head high risk, so a model reviews it,
// rather than wait or read the diff on the worker.
var ErrFilesUnavailable = errors.New("practions: no gate pool daemon can list the changed files")

// InboxItem is a page to a person: one item in the run owner's Inbox, on
// the run's chat.
type InboxItem struct {
	// ID is the item's id, derived from the execution that pages, so a
	// retried page is the same item.
	ID string
	// UserID is whose Inbox: the run's owner.
	UserID                          string
	ChatID, RunID, ThreadID, StepID string
	Title, Body                     string
}

// ErrInboxItemExists is a page whose item already exists.
var ErrInboxItemExists = errors.New("practions: the inbox item already exists")

// Inbox pages people.
type Inbox interface {
	// Page creates the item; an item with the same id already there is
	// ErrInboxItemExists.
	Page(ctx context.Context, item InboxItem) error
}

// Deps are the actions' collaborators. Store and GitHub are required; a nil
// Inbox pages nobody (pr.needs_person still comments and records).
type Deps struct {
	Store  Store
	GitHub GitHub
	// Files lists a head's changed files for pr.next's risk tier. Nil
	// lists none: every head pr.next reviews is judged high risk and goes
	// to a model ("changed files unavailable without a gate pool daemon").
	// forge:optional-dep
	Files Files
	// forge:optional-dep
	Inbox Inbox
	// Grants decides whether a run holds a pipeline capability. pr.verdict
	// records a pass only from a run whose activation holds approve (in mode
	// on; shadow logs what it would refuse). Nil refuses every pass in mode
	// on.
	// forge:optional-dep
	Grants grants.Authorizer
	// Now is the clock; nil is time.Now.
	// forge:optional-dep
	Now func() time.Time
	// forge:optional-dep
	Logger *slog.Logger
}

// Service is the pr.* actions over Deps.
type Service struct {
	deps Deps
	log  *slog.Logger
}

// New returns the actions over deps.
func New(deps Deps) (*Service, error) {
	if deps.Store == nil || deps.GitHub == nil {
		return nil, errors.New("practions: Store and GitHub are required")
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	log := deps.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Service{deps: deps, log: log.With("component", "practions")}, nil
}

// Handlers binds each pr.* action to its handler, by canonical name, for
// serveraction.Config.Handlers.
func (s *Service) Handlers() map[string]serveraction.Handler {
	return map[string]serveraction.Handler{
		serveraction.PRNext:        serveraction.HandlerFunc(s.Next),
		serveraction.PRVerdict:     serveraction.HandlerFunc(s.Verdict),
		serveraction.PRChecked:     serveraction.HandlerFunc(s.Checked),
		serveraction.PRPushed:      serveraction.HandlerFunc(s.Pushed),
		serveraction.PRClose:       serveraction.HandlerFunc(s.Close),
		serveraction.PRNeedsPerson: serveraction.HandlerFunc(s.NeedsPerson),
		serveraction.SweepPR:       serveraction.HandlerFunc(s.Sweep),
	}
}

func (s *Service) now() time.Time { return s.deps.Now().UTC() }
