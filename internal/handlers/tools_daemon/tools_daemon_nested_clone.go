// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/ospath"
)

// ── Cloning a project's nested repositories ────────────────────────────
//
// Setting a project up on a machine (CreateProjectFromRepo) queues the clone
// of its root through the control plane, and records the project's other
// repositories with a remote — the ones below the root, like forge/ inside
// reliant-labs/ — as pending project_daemon_repos rows. A clone needs an
// empty target, so they can only run once the root has landed; the gateway
// holding the machine's stream runs them then, each into its relative path,
// with the same git.clone the root used (so the machine's git credential
// helper authenticates them the same way). The project_daemons row stays
// installing until every one is settled, then becomes installed, or failed
// with a reason naming each repository that did not clone and git's words.
//
// Two triggers, both on the machine's gateway: the machine's announcement
// that the root landed (handleFileSystemChanged), and every connect
// (resumeNestedClonesOnConnect), which picks up a run whose gateway went away
// and rows an older gateway left pending. Either starts one pass per
// connection and checkout (nestedPassFor); a trigger that arrives while it
// runs makes it look again before it ends.
//
// A pass is authorized before every claim: the project must still be the
// connected user's, in the machine's current scope (its owner still a live
// member of its org). Anything else, or a failed lookup, stops the pass with
// nothing claimed or sent.
//
// It claims one repository at a time, immediately before cloning it
// (ClaimNextProjectDaemonRepo): the shallowest ready one, never one inside a
// repository still to clone, so a parent and its child never clone at once.
// A claim's lease runs past the latest the machine can still be running the
// clone sent under it (its own timeout, nestedCloneLease), and settling is
// fenced on the claim, so a claim is taken over only once that clone cannot
// still be running, and the pass it was taken from records nothing. A pass
// whose connection went away mid-clone leaves the claim to run out; a pass
// that finds every remaining repository held by another claim waits for the
// earliest lease, for as long as its own connection lives, so a quick
// reconnect still settles the checkout without another reconnect.

const (
	// nestedCloneTimeoutMs bounds one clone on the machine.
	nestedCloneTimeoutMs = restoreCloneTimeoutMs
	// nestedLeaseMargin is how long past the clone's own timeout a claim
	// still holds: for the command to reach the machine, and git to be
	// reaped and the checkout renamed into place once it is killed.
	nestedLeaseMargin = 2 * time.Minute
	// nestedCloneLease is how long a claim holds: past the latest the
	// machine can still be running the clone sent under it.
	nestedCloneLease = time.Duration(nestedCloneTimeoutMs)*time.Millisecond + nestedLeaseMargin
	// nestedPassTimeout bounds one pass over a checkout's nested
	// repositories, waits for another pass's claim included.
	nestedPassTimeout = 2 * time.Hour
	// nestedWaitMin and nestedWaitMax bound one wait for another claim: a
	// lease read off the database's clock, compared with this gateway's, is
	// re-read at least this often.
	nestedWaitMin = time.Second
	nestedWaitMax = 30 * time.Second
	// nestedSettleTimeout bounds recording a clone's outcome once its
	// connection has gone: a clone that finished is still recorded.
	nestedSettleTimeout = 10 * time.Second
	// nestedFailureReasonMax bounds the reason stored on the project's row.
	nestedFailureReasonMax = 1000
)

// errNestedProjectNotMachines: the checkout's project is no longer the
// connected user's, or no longer in the machine's scope.
var errNestedProjectNotMachines = errors.New("the project is not the machine's to set up")

// errCloneConnectionGone: the machine's connection went away while a clone
// was sent on it. The machine may still be running the clone.
var errCloneConnectionGone = errors.New("the machine's connection went away")

// NestedCloneStore is what cloning a checkout's nested repositories reads and
// writes: the nested rows (store.ProjectDaemonExtStore) and the settle of
// the checkout's own row (*db.Repo).
type NestedCloneStore interface {
	ClaimNextProjectDaemonRepo(ctx context.Context, projectID, daemonID, claimID string, lease time.Duration) (*core.NestedRepoClaim, error)
	SettleProjectDaemonRepo(ctx context.Context, projectID, daemonID, repoID, claimID, installErr string) (bool, error)
	ListProjectDaemonReposForPair(ctx context.Context, projectID, daemonID string) ([]*core.ProjectDaemonRepo, error)
	SettleProjectDaemonFromNestedReposInScope(ctx context.Context, projectID, daemonID, installErr string, scope *core.OrgScope) (bool, error)
}

// nestedCloneAdapter joins the repository and the checkout rows into the
// NestedCloneStore the clone pass takes.
type nestedCloneAdapter struct {
	ProjectReconcileRepo
	ProjectDaemons
}

// rootLanded reports whether the checkout's root is on the machine's disk.
type rootLanded func(ctx context.Context) (bool, error)

// nestedAuthorize reports the machine's current scope when the checkout's
// project may still be set up on it, else an error: the pass stops.
type nestedAuthorize func(ctx context.Context) (*core.OrgScope, error)

// hasUnsettledNestedRepos reports whether rows have a clone still to run.
func hasUnsettledNestedRepos(rows []*core.ProjectDaemonRepo) bool {
	for _, r := range rows {
		if r != nil && r.State.Unsettled() {
			return true
		}
	}
	return false
}

// connCloneRunner runs a git.clone on the machine behind conn.
func (s *Service) connCloneRunner(conn *daemonConnection, timeoutMs int32) cloneRunner {
	return func(ctx context.Context, step cloneStep) error {
		payload, err := json.Marshal(step)
		if err != nil {
			return err
		}
		resp, err := s.sendCommandToConn(ctx, conn, &reliantv1.DaemonCommandRequest{
			RequestId:   uuid.NewString(),
			CommandType: "git.clone",
			Payload:     payload,
			TimeoutMs:   timeoutMs,
		})
		if err != nil {
			select {
			case <-conn.done:
				return fmt.Errorf("%w: %w", errCloneConnectionGone, err)
			default:
			}
			return err
		}
		if !resp.GetSuccess() {
			msg := strings.TrimSpace(resp.GetErrorMessage())
			if msg == "" {
				msg = "the machine reported no reason"
			}
			return fmt.Errorf("%s", msg)
		}
		return nil
	}
}

// nestedPassKey names the one pass a gateway runs for a checkout on one
// connection.
type nestedPassKey struct {
	conn      *daemonConnection
	projectID string
}

// nestedPassState is a running pass: again asks it to look once more before
// it ends, because a trigger arrived while it ran.
type nestedPassState struct{ again bool }

// nestedPassFor is the pass that clones the checkout's nested repositories on
// the machine behind conn: authorized against the connection's user and the
// machine's current scope, cloning with git.clone on conn.
func (s *Service) nestedPassFor(conn *daemonConnection, projectID, rootPath string, landed rootLanded) *nestedClonePass {
	return &nestedClonePass{
		store:     nestedCloneAdapter{s.deps.Repo, s.deps.Installs},
		run:       s.connCloneRunner(conn, nestedCloneTimeoutMs),
		landed:    landed,
		authorize: s.nestedCloneAuthorizer(conn, projectID),
		wait:      waitOrDone,
		now:       time.Now,
		claimID:   uuid.NewString(),
		projectID: projectID,
		daemonID:  conn.daemonID,
		rootPath:  rootPath,
	}
}

// nestedCloneAuthorizer authorizes a pass over projectID's checkout on the
// machine behind conn, read again at every call: the machine's scope (which
// fails while its owner is not a live member of its org), and the project,
// which must be the connected user's and in that scope. Fails closed.
func (s *Service) nestedCloneAuthorizer(conn *daemonConnection, projectID string) nestedAuthorize {
	return func(ctx context.Context) (*core.OrgScope, error) {
		scope, _, err := s.machineProjectScope(ctx, conn)
		if err != nil {
			return nil, err
		}
		project, err := s.deps.Repo.GetProjectWithUserCheck(ctx, projectID, conn.userID)
		if err != nil {
			return nil, err
		}
		if project == nil || !scope.Includes(project.OrgID) {
			return nil, errNestedProjectNotMachines
		}
		return scope, nil
	}
}

// cloneNestedReposAsync runs the checkout's nested-clone pass off the
// caller's path: this runs on the connection's inbound worker, and clones
// take minutes. conn.done bounds it. A pass already running for the checkout
// on this connection is asked to look again instead of starting another, so
// the stream of announcements a set-up produces runs one pass.
func (s *Service) cloneNestedReposAsync(ctx context.Context, conn *daemonConnection, projectID, rootPath string, landed rootLanded) {
	if conn == nil {
		return
	}
	key := nestedPassKey{conn: conn, projectID: projectID}
	s.nestedMu.Lock()
	if st, running := s.nestedPasses[key]; running {
		st.again = true
		s.nestedMu.Unlock()
		return
	}
	if s.nestedPasses == nil {
		s.nestedPasses = make(map[nestedPassKey]*nestedPassState)
	}
	s.nestedPasses[key] = &nestedPassState{}
	s.nestedMu.Unlock()

	pass := s.nestedPassFor(conn, projectID, rootPath, landed)
	go func() {
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nestedPassTimeout)
		defer cancel()
		go func() {
			select {
			case <-conn.done:
				cancel()
			case <-runCtx.Done():
			}
		}()
		for {
			if pass.runPass(runCtx) {
				if err := s.deps.Refetch.EmitUserRefetch(runCtx, conn.userID, db.RefetchFileTree, db.RefetchOpts{ProjectID: &projectID}); err != nil {
					logging.WarnContext(runCtx, LOG_PREFIX_TOOLS_DAEMON+" nested clones: could not tell clients the checkout settled",
						"error", err, "projectID", projectID, "daemonID", conn.daemonID)
				}
			}
			s.nestedMu.Lock()
			st := s.nestedPasses[key]
			if runCtx.Err() == nil && st != nil && st.again {
				st.again = false
				s.nestedMu.Unlock()
				continue
			}
			delete(s.nestedPasses, key)
			s.nestedMu.Unlock()
			return
		}
	}()
}

// resumeNestedClonesOnConnect resumes, on the machine behind conn, the nested
// clones of every checkout that has some still to run: one whose gateway went
// away mid-way, or whose root landed while no gateway of this release held
// the machine. A checkout whose root clone failed waits for the user to set
// it up again; one whose root has not landed yet waits for it. Each pass
// authorizes its project before it claims anything (nestedCloneAuthorizer):
// a checkout of another org's project the machine still has on record, or
// one whose owner left the machine's org, clones nothing.
func (s *Service) resumeNestedClonesOnConnect(ctx context.Context, conn *daemonConnection, check daemonPathChecker) {
	if conn == nil {
		return
	}
	projectIDs, err := s.deps.Installs.ListProjectsWithUnsettledDaemonRepos(ctx, conn.daemonID)
	if err != nil {
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" nested clones: could not list the machine's checkouts with nested repositories to clone",
			"error", err, "daemonID", conn.daemonID)
		return
	}
	if len(projectIDs) == 0 {
		return
	}
	rows, err := s.deps.Installs.ListProjectDaemonsForDaemon(ctx, conn.daemonID)
	if err != nil {
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" nested clones: could not read the machine's checkouts",
			"error", err, "daemonID", conn.daemonID)
		return
	}
	byProject := make(map[string]*core.ProjectDaemon, len(rows))
	for _, r := range rows {
		if r != nil {
			byProject[r.ProjectID] = r
		}
	}
	for _, projectID := range projectIDs {
		row := byProject[projectID]
		if row == nil || row.InstallState == core.ProjectInstallFailed || strings.TrimSpace(row.Path) == "" {
			continue
		}
		path := row.Path
		landed := func(ctx context.Context) (bool, error) {
			state, err := check(ctx, path)
			return state.Exists, err
		}
		s.cloneNestedReposAsync(ctx, conn, projectID, path, landed)
	}
}

// nestedClonePass clones one checkout's nested repositories into it.
type nestedClonePass struct {
	store     NestedCloneStore
	run       cloneRunner
	landed    rootLanded
	authorize nestedAuthorize
	// wait sleeps d, reporting false when ctx ended first.
	wait func(ctx context.Context, d time.Duration) bool
	now  func() time.Time
	// claimID names this pass's claims.
	claimID                       string
	projectID, daemonID, rootPath string
}

// waitOrDone sleeps d, reporting false when ctx ended first.
func waitOrDone(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// runPass is the testable core: once the checkout's root has landed, clone
// each nested repository it can claim into its relative path, one at a time,
// shallowest first, and record each outcome. When every remaining one is held
// by another claim, wait for the earliest lease and look again. When none is
// left to clone, settle the checkout's own row: installed, or failed naming
// each repository that did not clone. It reports whether it settled that row.
// It ends early, settling nothing, when the project is no longer the
// machine's to set up or ctx ends.
func (p *nestedClonePass) runPass(ctx context.Context) bool {
	ok, err := p.landed(ctx)
	if err != nil || !ok {
		// Not yet: the root's own clone lands first, and its announcement
		// (or the next connect) runs this again.
		return false
	}
	for ctx.Err() == nil {
		scope, err := p.authorize(ctx)
		if err != nil {
			logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" nested clones: the project is not the machine's to set up; cloning nothing",
				"error", err, "projectID", p.projectID, "daemonID", p.daemonID)
			return false
		}
		claim, err := p.store.ClaimNextProjectDaemonRepo(ctx, p.projectID, p.daemonID, p.claimID, nestedCloneLease)
		if err != nil {
			logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" nested clones: could not claim a nested repository",
				"error", err, "projectID", p.projectID, "daemonID", p.daemonID)
			return false
		}
		if claim != nil && claim.Repo != nil {
			p.cloneOne(ctx, claim)
			continue
		}
		rows, err := p.store.ListProjectDaemonReposForPair(ctx, p.projectID, p.daemonID)
		if err != nil {
			logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" nested clones: could not read the nested repositories",
				"error", err, "projectID", p.projectID, "daemonID", p.daemonID)
			return false
		}
		if !hasUnsettledNestedRepos(rows) {
			return settleNestedCheckout(ctx, p.store, p.projectID, p.daemonID, rows, scope)
		}
		// Every repository left is held by another claim, or sits inside one
		// that is: wait for the earliest lease to run out, then look again.
		if !p.wait(ctx, nextNestedWake(rows, p.now())) {
			return false
		}
	}
	return false
}

// cloneOne clones the claimed repository and records the outcome under the
// claim. A clone cut off by its connection going away is left claimed: the
// machine may still be running it, and its lease runs out once it cannot.
func (p *nestedClonePass) cloneOne(ctx context.Context, claim *core.NestedRepoClaim) {
	repo := claim.Repo
	rel := strings.Trim(strings.TrimSpace(repo.RelativePath), "/")
	var cloneErr string
	switch {
	case claim.InsideFailed:
		cloneErr = "not cloned, because the repository it sits in did not clone"
	case rel == "" || strings.TrimSpace(repo.RemoteURL) == "":
		cloneErr = "no remote is recorded for it"
	default:
		if err := p.run(ctx, cloneStep{Repo: strings.TrimSpace(repo.RemoteURL), Path: ospath.Join(p.rootPath, rel)}); err != nil {
			if ctx.Err() != nil || errors.Is(err, errCloneConnectionGone) {
				return
			}
			cloneErr = boundReason(err.Error(), nestedFailureReasonMax)
		}
	}
	if cloneErr != "" {
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" nested clones: a nested repository did not clone",
			"projectID", p.projectID, "daemonID", p.daemonID, "repoID", repo.RepoID, "relativePath", rel, "reason", cloneErr)
	}
	// A clone that finished is recorded even if the connection went just now.
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nestedSettleTimeout)
	defer cancel()
	settled, err := p.store.SettleProjectDaemonRepo(settleCtx, p.projectID, p.daemonID, repo.RepoID, p.claimID, cloneErr)
	switch {
	case err != nil:
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" nested clones: could not record a nested repository's clone",
			"error", err, "projectID", p.projectID, "daemonID", p.daemonID, "repoID", repo.RepoID)
	case !settled:
		logging.InfoContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" nested clones: the claim was taken over or the set-up was planned again; not recorded",
			"projectID", p.projectID, "daemonID", p.daemonID, "repoID", repo.RepoID)
	}
}

// nextNestedWake is how long to wait before looking again at rows whose
// remaining repositories are all held: until the earliest lease runs out,
// within [nestedWaitMin, nestedWaitMax].
func nextNestedWake(rows []*core.ProjectDaemonRepo, now time.Time) time.Duration {
	wait := nestedWaitMax
	for _, r := range rows {
		if r == nil || r.State != core.NestedRepoCloning || r.LeaseUntil == nil {
			continue
		}
		if d := r.LeaseUntil.Sub(now); d < wait {
			wait = d
		}
	}
	if wait < nestedWaitMin {
		wait = nestedWaitMin
	}
	return wait
}

// settleNestedCheckout settles the checkout's row from rows, its nested
// repositories, once none is left to clone: failed naming each one that did
// not clone (from installed too), else installed. It is fenced on the plan
// and a no-op on a project moved out of the machine's scope
// (SettleProjectDaemonFromNestedReposInScope). It reports whether the row is
// settled now (installed, or marked failed by this call).
func settleNestedCheckout(ctx context.Context, store NestedCloneStore, projectID, daemonID string, rows []*core.ProjectDaemonRepo, scope *core.OrgScope) bool {
	if hasUnsettledNestedRepos(rows) {
		return false // another pass finishes them
	}
	settled, err := store.SettleProjectDaemonFromNestedReposInScope(ctx, projectID, daemonID, nestedFailureReason(rows), scope)
	if err != nil {
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" nested clones: could not settle the checkout",
			"error", err, "projectID", projectID, "daemonID", daemonID)
		return false
	}
	return settled
}

// nestedFailureReason names each nested repository that did not clone and
// why, for the checkout's install_error; "" when every one cloned.
func nestedFailureReason(rows []*core.ProjectDaemonRepo) string {
	var parts []string
	for _, r := range rows {
		if r == nil || r.State != core.NestedRepoFailed {
			continue
		}
		name := r.Name
		if name == "" {
			name = r.RelativePath
		}
		parts = append(parts, fmt.Sprintf("%s (%s/): %s", name, strings.Trim(r.RelativePath, "/"), r.Error))
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return boundReason("the project cloned, but its nested repository "+parts[0], nestedFailureReasonMax)
	default:
		return boundReason(fmt.Sprintf("the project cloned, but %d of its nested repositories did not: %s",
			len(parts), strings.Join(parts, "; ")), nestedFailureReasonMax)
	}
}

func boundReason(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max]
	}
	return s
}
