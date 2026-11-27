// Copyright (c) 2025 Reliant Labs
package candidate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/mergequeue/buildjob"
	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
	"github.com/reliant-labs/reliant/internal/mergequeue/ghrest"
	"github.com/reliant-labs/reliant/internal/mergequeue/gitport"
)

// Credentials is a build job's GitHub credential: a short-lived installation
// token for one repository, scoped to what a build does (contents:write to
// fetch and push, pull_requests:read to read a PR back). It is asked for per
// job and held in memory only: git gets it through its environment for a
// fetch or a push (GitCredentialEnv), the GitHub client per call, and it is
// never written to disk or put in a URL.
type Credentials interface {
	Token(ctx context.Context, repository string) (string, error)
}

// Host runs the merge queue's code operations (buildjob's pipeline.job.*
// commands) on a machine of the repository's gate pool: [Job] over a git
// cache per repository, by job id. Start runs a job in the background (or
// answers for the one already running under that id), Status reads it,
// Cancel stops it. A finished job is kept for Retain, so a server that lost
// its connection can still read the result.
//
// The credentialed steps are the fetches and pushes, and only those: the
// cache's other git commands and the regen command run without it, regen
// inside the credential sandbox ([SandboxRunner]). A build pushes only its
// batch's queue ref and, for a regenerated entry, the PR's branch leased on
// the head the server queued (pushAllowed); a paths or config job pushes
// nothing.
type Host struct {
	// Root holds one bare git cache per repository
	// (<Root>/<owner>/<name>.git), kept across jobs so a repository is
	// fetched in full once. Jobs on one repository run one at a time.
	// [Host.Prune] holds the caches to an age and size budget.
	Root string
	// Credentials authenticate fetches, pushes and GitHub calls. nil runs
	// them with none (a test against a local fake remote).
	Credentials Credentials
	// GitHub is the GitHub API client; nil is the REST client over
	// Credentials.
	GitHub ghport.Client
	// RemoteURL is repository's git remote; nil is
	// https://github.com/<owner>/<name>.git.
	RemoteURL func(repository string) string
	// Runner runs regen; nil is SandboxRunner{}.
	Runner Runner
	// Retain is how long a finished job is kept; 0 is 2h.
	Retain time.Duration

	mu    sync.Mutex
	jobs  map[string]*hostJob
	repos map[string]*sync.Mutex
	gh    ghport.Client
	now   func() time.Time
}

type hostJob struct {
	cancel   context.CancelFunc
	done     bool
	reply    buildjob.StatusReply
	finished time.Time
}

func (h *Host) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// Handle answers one pipeline job command: the daemon's command handler is
// this, over its Host.
func (h *Host) Handle(ctx context.Context, command string, payload []byte) ([]byte, error) {
	switch command {
	case buildjob.CommandStart:
		var req buildjob.StartRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload: %w", err)
		}
		return json.Marshal(h.Start(ctx, req))
	case buildjob.CommandStatus, buildjob.CommandCancel:
		var req buildjob.StatusRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload: %w", err)
		}
		if command == buildjob.CommandCancel {
			h.Cancel(req.JobID)
			return []byte("{}"), nil
		}
		return json.Marshal(h.Status(req.JobID))
	}
	return nil, fmt.Errorf("not a pipeline job command: %q", command)
}

// Start starts req's job unless one with its id exists, whose state it
// answers instead. At most req.MaxConcurrent jobs run at once.
func (h *Host) Start(_ context.Context, req buildjob.StartRequest) buildjob.StartReply {
	if err := req.Validate(); err != nil {
		return buildjob.StartReply{State: buildjob.JobRefused, Reason: err.Error()}
	}
	if h.Root == "" {
		return buildjob.StartReply{State: buildjob.JobRefused, Reason: "this machine has no pipeline job directory"}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked()
	if jb, ok := h.jobs[req.JobID]; ok {
		if jb.done {
			return buildjob.StartReply{State: buildjob.JobDone}
		}
		return buildjob.StartReply{State: buildjob.JobRunning}
	}
	running := 0
	for _, jb := range h.jobs {
		if !jb.done {
			running++
		}
	}
	if running >= max(req.MaxConcurrent, 1) {
		return buildjob.StartReply{State: buildjob.JobBusy, Reason: fmt.Sprintf("%d pipeline jobs running", running)}
	}
	if h.jobs == nil {
		h.jobs = map[string]*hostJob{}
	}
	runCtx, cancel := context.WithCancel(context.Background())
	jb := &hostJob{cancel: cancel}
	h.jobs[req.JobID] = jb
	go func() {
		defer cancel()
		reply := h.run(runCtx, req)
		h.mu.Lock()
		jb.done, jb.reply, jb.finished = true, reply, h.clock()
		h.mu.Unlock()
	}()
	return buildjob.StartReply{State: buildjob.JobRunning}
}

// Status answers for jobID.
func (h *Host) Status(jobID string) buildjob.StatusReply {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked()
	jb, ok := h.jobs[jobID]
	switch {
	case !ok:
		return buildjob.StatusReply{State: buildjob.JobUnknown}
	case !jb.done:
		return buildjob.StatusReply{State: buildjob.JobRunning}
	}
	return jb.reply
}

// Cancel stops jobID's job; its result (an error) stays readable.
func (h *Host) Cancel(jobID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if jb, ok := h.jobs[jobID]; ok && !jb.done {
		jb.cancel()
	}
}

func (h *Host) pruneLocked() {
	retain := h.Retain
	if retain <= 0 {
		retain = 2 * time.Hour
	}
	now := h.clock()
	for id, jb := range h.jobs {
		if jb.done && now.Sub(jb.finished) > retain {
			delete(h.jobs, id)
		}
	}
}

// repoLock is the lock of repository's git cache.
func (h *Host) repoLock(repository string) *sync.Mutex {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.repos == nil {
		h.repos = map[string]*sync.Mutex{}
	}
	key := strings.ToLower(repository)
	if h.repos[key] == nil {
		h.repos[key] = &sync.Mutex{}
	}
	return h.repos[key]
}

func (h *Host) github() (ghport.Client, error) {
	if h.GitHub != nil {
		return h.GitHub, nil
	}
	if h.Credentials == nil {
		return nil, errors.New("candidate: the job host has no GitHub client and no credentials")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.gh == nil {
		creds := h.Credentials
		h.gh = ghrest.NewInstrumented(ghrest.Deps{Token: func(ctx context.Context, repo ghport.Repo) (string, error) {
			return creds.Token(ctx, repo.String())
		}})
	}
	return h.gh, nil
}

func (h *Host) remote(repository string) string {
	if h.RemoteURL != nil {
		return h.RemoteURL(repository)
	}
	return "https://github.com/" + repository + ".git"
}

// run runs one job to its reply.
func (h *Host) run(ctx context.Context, req buildjob.StartRequest) buildjob.StatusReply {
	fail := func(err error) buildjob.StatusReply {
		if ctx.Err() != nil {
			err = fmt.Errorf("canceled: %w", err)
		}
		return buildjob.StatusReply{State: buildjob.JobDone, Error: err.Error()}
	}
	repository := req.Repository()
	repo, err := ghport.ParseRepo(repository)
	if err != nil {
		return fail(err)
	}
	lock := h.repoLock(repository)
	lock.Lock()
	defer lock.Unlock()
	if ctx.Err() != nil {
		return fail(ctx.Err())
	}
	gh, err := h.github()
	if err != nil {
		return fail(err)
	}
	var credEnv []string
	if h.Credentials != nil {
		tok, err := h.Credentials.Token(ctx, repository)
		if err != nil {
			return fail(fmt.Errorf("candidate: get a GitHub token for %s: %w", repository, err))
		}
		credEnv = GitCredentialEnv(tok)
	}
	dir := filepath.Join(h.Root, strings.ToLower(repo.Owner), strings.ToLower(repo.Name)+".git")
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return fail(fmt.Errorf("candidate: make the git cache directory: %w", err))
	}
	// The cache's lease, for the whole job: Prune never removes a cache a
	// job holds, whichever daemon process runs it, and reads its last use
	// from the stamps (prune.go).
	lease, err := acquireLease(ctx, dir)
	if err != nil {
		return fail(err)
	}
	stampLease(dir, h.clock())
	defer func() {
		stampLease(dir, h.clock())
		releaseLease(lease)
	}()
	local, err := gitport.Init(ctx, dir, gitport.Options{})
	if err != nil {
		return fail(err)
	}
	net, err := gitport.Open(ctx, dir, gitport.Options{Env: credEnv})
	if err != nil {
		return fail(err)
	}
	git := &netGit{Port: local, net: net, allow: pushAllowed(req)}
	job := Job{Ports: Ports{Git: git, GitHub: gh, Remote: h.remote(repository), Runner: h.Runner}}
	switch req.Kind {
	case buildjob.KindBuild:
		res, err := job.Build(ctx, *req.Build)
		if err != nil {
			return fail(err)
		}
		return buildjob.StatusReply{State: buildjob.JobDone, Build: &res}
	case buildjob.KindPaths:
		res, err := job.ChangedPaths(ctx, *req.Paths)
		if err != nil {
			return fail(err)
		}
		return buildjob.StatusReply{State: buildjob.JobDone, Paths: &res}
	case buildjob.KindConfig:
		res, err := job.ReadConfig(ctx, *req.Config)
		if err != nil {
			return fail(err)
		}
		return buildjob.StatusReply{State: buildjob.JobDone, Config: &res}
	}
	return fail(fmt.Errorf("candidate: kind %q", req.Kind))
}

// netGit is a job's git: local commands through Port, which carries no
// credential, and the two that talk to the remote through net, which does.
// Every push is held to allow first.
type netGit struct {
	gitport.Port
	net   gitport.Port
	allow func(gitport.PushInput) error
}

// Fetch implements gitport.Port.
func (g *netGit) Fetch(ctx context.Context, url string, refspecs ...string) error {
	return g.net.Fetch(ctx, url, refspecs...)
}

// Push implements gitport.Port.
func (g *netGit) Push(ctx context.Context, url string, in gitport.PushInput) error {
	if err := g.allow(in); err != nil {
		return err
	}
	return g.net.Push(ctx, url, in)
}

// GuardPushes is p with every push held to where req's job may push (see
// [Host]); a refused push is [ErrPushRefused] and never reaches p.
func GuardPushes(p gitport.Port, req buildjob.StartRequest) gitport.Port {
	return &netGit{Port: p, net: p, allow: pushAllowed(req)}
}

// ErrPushRefused is a push a job may not make.
var ErrPushRefused = errors.New("candidate: a pipeline job may not make this push")

// pushAllowed is where req's job may push. A build creates its batch's queue
// ref, and moves a PR's branch only from a head the server queued in this
// batch (a regen commit, leased on that head). GitHub cannot scope a token
// to refs, so the job holds itself to this, and the server checks what it
// reports through the GitHub API. Every other job pushes nothing.
func pushAllowed(req buildjob.StartRequest) func(gitport.PushInput) error {
	return func(in gitport.PushInput) error {
		if req.Kind != buildjob.KindBuild || req.Build == nil {
			return fmt.Errorf("%w: a %s job pushes nothing", ErrPushRefused, req.Kind)
		}
		if in.ExpectedOld == "" {
			return fmt.Errorf("%w: %s without a lease", ErrPushRefused, in.Ref)
		}
		if in.Ref == buildjob.QueueRef(req.Build.BatchID) {
			return nil
		}
		heads := make([]string, 0, len(req.Build.Entries))
		for _, e := range req.Build.Entries {
			heads = append(heads, e.Head)
		}
		if strings.HasPrefix(in.Ref, "refs/heads/") && !strings.HasPrefix(in.Ref, buildjob.QueueRefPrefix) && slices.Contains(heads, in.ExpectedOld) {
			return nil
		}
		return fmt.Errorf("%w: %s (leased on %s)", ErrPushRefused, in.Ref, in.ExpectedOld)
	}
}

// GitCredentialEnv is the environment a git child authenticates to
// github.com with: the token in one variable of its own environment, and a
// credential helper, scoped to https://github.com, that prints it. Nothing
// is written to a config file, the token is in no argument and no URL, and
// any other helper for that URL is cleared first. Append it to a hermetic
// environment that sets no GIT_CONFIG_COUNT (gitport.HermeticEnv); "" is
// none.
func GitCredentialEnv(token string) []string {
	if token == "" {
		return nil
	}
	const key = "credential.https://github.com.helper"
	return []string{
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=" + key,
		"GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_KEY_1=" + key,
		`GIT_CONFIG_VALUE_1=!f() { test "$1" = get || exit 0; printf 'username=x-access-token\npassword=%s\n' "$` + tokenEnv + `"; }; f`,
		tokenEnv + "=" + token,
	}
}

// tokenEnv is the variable GitCredentialEnv's helper reads.
const tokenEnv = "RELIANT_QUEUE_GITHUB_TOKEN" //nolint:gosec // G101: a variable name, not a credential.
