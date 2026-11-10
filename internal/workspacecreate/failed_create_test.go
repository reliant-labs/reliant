// Copyright (c) 2025 Reliant Labs
package workspacecreate

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/worktreeerr"
)

// diskMachine models one machine's disk for #2413: a checkout holds its
// branch in its repository, and a create whose answer is "lost" still makes
// its checkout.
type diskMachine struct {
	mu sync.Mutex
	// holder is the worktree id whose checkout holds a branch, per repo.
	holder map[string]string // repo path + "\x00" + branch -> worktree id
	// loseAnswerFor makes the next create of this repo finish on disk but
	// return a deadline error, as a saturated machine's did.
	loseAnswerFor string
	// keep, when set, is the reason remove_failed_create keeps a checkout.
	keep string
	// noCommand makes remove_failed_create fail as an older daemon's does.
	noCommand bool
	// omitChecked answers as a daemon that does not report what it listed.
	omitChecked bool
	removes     []removeFailedCreatePayload
	creates     int
}

func (d *diskMachine) Send(_ context.Context, cmd string, payload, resp any, _ int32) error {
	b, _ := json.Marshal(payload)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.holder == nil {
		d.holder = map[string]string{}
	}
	switch cmd {
	case "worktree.create":
		d.creates++
		var p struct {
			ProjectPath string `json:"project_path"`
			Branch      string `json:"branch"`
			WorktreeID  string `json:"worktree_id"`
			WorkspaceID string `json:"workspace_id"`
			SubPath     string `json:"sub_path"`
		}
		_ = json.Unmarshal(b, &p)
		out := resp.(*checkoutResponse)
		key := p.ProjectPath + "\x00" + p.Branch
		if id, held := d.holder[key]; held && id != p.WorktreeID {
			*out = checkoutResponse{Error: "fatal: already checked out", ErrorCode: worktreeerr.BranchCheckedOut, ErrorDetail: "/w/" + id}
			return nil
		}
		d.holder[key] = p.WorktreeID
		if d.loseAnswerFor == p.ProjectPath {
			d.loseAnswerFor = ""
			return svcerr.DeadlineExceeded("the machine did not answer in time")
		}
		*out = checkoutResponse{Success: true, WorktreePath: filepath.Join("/w", p.WorkspaceID, p.SubPath), BaseBranch: "main"}
	case removeFailedCreateCmd:
		if d.noCommand {
			return errors.New("unknown command type: " + cmd)
		}
		var p removeFailedCreatePayload
		_ = json.Unmarshal(b, &p)
		d.removes = append(d.removes, p)
		out := resp.(*removeFailedCreateResult)
		if !d.omitChecked {
			out.Checked = append(out.Checked, p.RepoPaths...)
		}
		for _, repo := range p.RepoPaths {
			for _, c := range p.Creates {
				key := repo + "\x00" + c.Branch
				if d.holder[key] != c.WorktreeID {
					continue
				}
				co := removeFailedCreateCheckout{WorktreeID: c.WorktreeID, RepoPath: repo, Path: "/w/" + c.WorktreeID}
				if d.keep != "" {
					co.Kept = d.keep
				} else {
					co.Removed = true
					delete(d.holder, key)
				}
				out.Checkouts = append(out.Checkouts, co)
			}
		}
	}
	return nil
}

func oneRepoReq() Request {
	return Request{
		Project:       &db.Project{ID: "p1", Name: "demo"},
		Checkout:      "/proj",
		Repos:         []*core.Repo{{ID: "r1", Name: "app", RelativePath: "app"}},
		Name:          "feat",
		Branch:        "feat-x",
		OwnerDaemonID: "d1",
	}
}

// The regression the issue asks for: a create times out, the server marks
// it FAILED, the cleanup runs, and the retry is ACTIVE on the same branch.
func TestRetryAfterALostCreateAnswerSucceedsOnTheSameBranch(t *testing.T) {
	ctx := context.Background()
	st, m := &fakeStore{}, &diskMachine{loseAnswerFor: "/proj/app"}
	req := oneRepoReq()

	first, err := Insert(ctx, st, req)
	require.NoError(t, err)
	require.Error(t, Finish(ctx, st, m, req, first))
	require.Equal(t, int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED), st.rows[first.ID].Status)

	retry, err := Insert(ctx, st, req)
	require.NoError(t, err, "a FAILED row releases its name and branch")
	require.NoError(t, Finish(ctx, st, m, req, retry))
	row := st.rows[retry.ID]
	assert.Equal(t, int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE), row.Status)
	assert.Equal(t, "feat-x", row.Branch)

	require.Len(t, m.removes, 1, "the leftover is removed once, for the FAILED row only")
	assert.Equal(t, []string{"/proj/app"}, m.removes[0].RepoPaths)
	assert.Equal(t, []failedCreateRef{{WorktreeID: first.ID, Branch: "feat-x"}}, m.removes[0].Creates)
	assert.Equal(t, 3, m.creates, "the first create, the refused retry, and the retry after the cleanup")
}

// A checkout the daemon keeps (dirty, used, in use) keeps the refusal: the
// create is not tried again and the row is FAILED with git's reason.
func TestRetryKeepsTheRefusalWhenTheLeftoverIsKept(t *testing.T) {
	ctx := context.Background()
	st, m := &fakeStore{}, &diskMachine{loseAnswerFor: "/proj/app", keep: "it has uncommitted, untracked or ignored files"}
	req := oneRepoReq()
	first, _ := Insert(ctx, st, req)
	_ = Finish(ctx, st, m, req, first)

	retry, _ := Insert(ctx, st, req)
	err := Finish(ctx, st, m, req, retry)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already checked out")
	assert.Equal(t, int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED), st.rows[retry.ID].Status)
	assert.Equal(t, 2, m.creates)
}

// A daemon older than the command refuses it; the create fails as it always
// did.
func TestRetryWithAnOlderDaemonFailsAsBefore(t *testing.T) {
	ctx := context.Background()
	st, m := &fakeStore{}, &diskMachine{loseAnswerFor: "/proj/app", noCommand: true}
	req := oneRepoReq()
	first, _ := Insert(ctx, st, req)
	_ = Finish(ctx, st, m, req, first)

	retry, _ := Insert(ctx, st, req)
	require.Error(t, Finish(ctx, st, m, req, retry))
	assert.Equal(t, 2, m.creates)
}

// Only a FAILED row with no path, of the same machine, is a leftover. A
// branch held by a live workspace, or by a FAILED row of another machine,
// is never sent for removal.
func TestRetryNeverAsksToRemoveAReferencedCheckout(t *testing.T) {
	ctx := context.Background()
	for name, holder := range map[string]db.Worktree{
		"active":        {Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE), Path: "/w/live"},
		"creating":      {Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_CREATING)},
		"failed-path":   {Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED), Path: "/w/kept"},
		"other-machine": {Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED), DaemonID: ptr("d2")},
	} {
		t.Run(name, func(t *testing.T) {
			holder.ID, holder.ProjectID, holder.Branch, holder.Name = "holder", "p1", "feat-x", "older"
			if holder.DaemonID == nil {
				holder.DaemonID = ptr("d1")
			}
			st := &fakeStore{rows: map[string]db.Worktree{"holder": holder}}
			m := &diskMachine{holder: map[string]string{"/proj/app\x00feat-x": "holder"}}
			wt := &db.Worktree{ID: "new", ProjectID: "p1", Name: "feat", Branch: "feat-x", DaemonID: ptr("d1")}
			st.rows["new"] = *wt
			require.Error(t, Finish(ctx, st, m, oneRepoReq(), wt))
			assert.Empty(t, m.removes)
		})
	}
}

func ptr(s string) *string { return &s }

// failedStore is a FailedCreateStore over fixed rows.
type failedStore struct {
	rows     []*db.Worktree
	installs []*core.ProjectDaemon
	repos    map[string][]*core.Repo
	marked   []string
	listErr  error
}

func (s *failedStore) ListFailedWorktreesForDaemon(_ context.Context, daemonID string, since time.Time, limit int) ([]*db.Worktree, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	var out []*db.Worktree
	for _, w := range s.rows {
		if w.DaemonID != nil && *w.DaemonID == daemonID && w.CreatedAt.After(since) {
			out = append(out, w)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *failedStore) ListProjectDaemonsForDaemon(_ context.Context, daemonID string) ([]*core.ProjectDaemon, error) {
	return s.installs, nil
}

func (s *failedStore) ListReposByProject(_ context.Context, projectID string) ([]*core.Repo, error) {
	return s.repos[projectID], nil
}

func (s *failedStore) MergeWorktreeCleanupMetadata(_ context.Context, id string, fn func(m *db.CleanupMetadata, archived bool) bool) error {
	m := &db.CleanupMetadata{}
	if fn(m, false) && m.DirectoryDeleted {
		s.marked = append(s.marked, id)
	}
	return nil
}

func failedRow(id, project, branch string) *db.Worktree {
	return &db.Worktree{
		ID: id, ProjectID: project, Branch: branch, DaemonID: ptr("d1"),
		Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED), CreatedAt: time.Now(),
	}
}

// On connect the machine is asked about its FAILED rows, per project, in the
// project's repositories on that machine. A row with nothing left is recorded
// cleaned up; a kept one is asked about again next time.
func TestRemoveFailedCreatesOnConnect(t *testing.T) {
	ctx := context.Background()
	st := &failedStore{
		rows: []*db.Worktree{failedRow("gone", "p1", "a"), failedRow("left", "p1", "b"), failedRow("elsewhere", "p2", "c")},
		installs: []*core.ProjectDaemon{
			{ProjectID: "p1", DaemonID: "d1", Path: "/proj", InstallState: core.ProjectInstallInstalled},
		},
		repos: map[string][]*core.Repo{"p1": {{RelativePath: "x"}, {RelativePath: "y"}}},
	}
	m := &diskMachine{holder: map[string]string{"/proj/y\x00b": "left"}}
	assert.Equal(t, 1, RemoveFailedCreatesOn(ctx, st, m, "d1"))

	require.Len(t, m.removes, 1, "p2 has no checkout on this machine, so nothing is sent for it")
	assert.Equal(t, []string{"/proj/x", "/proj/y"}, m.removes[0].RepoPaths)
	assert.ElementsMatch(t, []string{"gone", "left"}, st.marked)

	// A kept checkout leaves its row to be asked about again.
	st.marked = nil
	m = &diskMachine{holder: map[string]string{"/proj/y\x00b": "left"}, keep: "a process is running in it"}
	assert.Equal(t, 0, RemoveFailedCreatesOn(ctx, st, m, "d1"))
	assert.Equal(t, []string{"gone"}, st.marked)
}

// An older daemon (or a machine that went away again) settles nothing.
func TestRemoveFailedCreatesOnConnectWithAnOlderDaemonRecordsNothing(t *testing.T) {
	st := &failedStore{
		rows:     []*db.Worktree{failedRow("w", "p1", "a")},
		installs: []*core.ProjectDaemon{{ProjectID: "p1", DaemonID: "d1", Path: "/proj"}},
		repos:    map[string][]*core.Repo{"p1": {{RelativePath: "."}}},
	}
	assert.Equal(t, 0, RemoveFailedCreatesOn(context.Background(), st, &diskMachine{noCommand: true}, "d1"))
	assert.Empty(t, st.marked)
}

// Repositories the daemon could not read settle nothing: what is in them is
// unknown.
func TestSettledNeedsEveryRepositoryRead(t *testing.T) {
	res := removeFailedCreateResult{Checked: []string{"/proj/y"}, Unchecked: []string{"/proj/x"}}
	assert.Empty(t, res.settled([]string{"a"}, []string{"/proj/x", "/proj/y"}))
	res = removeFailedCreateResult{Checked: []string{"/proj/x"}, Checkouts: []removeFailedCreateCheckout{
		{WorktreeID: "a", Removed: true}, {WorktreeID: "b", Kept: "dirty"},
	}}
	assert.Equal(t, []string{"a", "c"}, res.settled([]string{"a", "b", "c"}, []string{"/proj/x"}))
}

// [B1]: a row is settled only from a complete look. A repository the daemon
// does not report listing, a daemon that reports none, or no repositories
// at all settle nothing.
func TestSettledNeedsEveryRepositoryConfirmedListed(t *testing.T) {
	res := removeFailedCreateResult{Checked: []string{"/proj/x"}}
	assert.Empty(t, res.settled([]string{"a"}, []string{"/proj/x", "/proj/y"}), "a repository the daemon did not list")
	assert.Empty(t, removeFailedCreateResult{}.settled([]string{"a"}, []string{"/proj/x"}), "a daemon that reports nothing listed")
	assert.Empty(t, res.settled([]string{"a"}, nil), "no repositories to look in")
	assert.Equal(t, []string{"a"}, res.settled([]string{"a"}, []string{"/proj/x"}))
}

// [B1]: a project with no repositories on record, or a daemon that does not
// confirm what it listed, leaves every failed row pending for a later connect.
func TestRemoveFailedCreatesOnConnectNeedsACompleteLook(t *testing.T) {
	ctx := context.Background()
	st := &failedStore{
		rows:     []*db.Worktree{failedRow("w", "p1", "a")},
		installs: []*core.ProjectDaemon{{ProjectID: "p1", DaemonID: "d1", Path: "/proj"}},
		repos:    map[string][]*core.Repo{},
	}
	m := &diskMachine{}
	assert.Equal(t, 0, RemoveFailedCreatesOn(ctx, st, m, "d1"))
	assert.Empty(t, m.removes, "no repositories on record: nothing to look in")
	assert.Empty(t, st.marked)

	st.repos = map[string][]*core.Repo{"p1": {{RelativePath: "."}}}
	assert.Equal(t, 0, RemoveFailedCreatesOn(ctx, st, &diskMachine{omitChecked: true}, "d1"))
	assert.Empty(t, st.marked, "a daemon that does not confirm what it listed settles nothing")

	assert.Equal(t, 0, RemoveFailedCreatesOn(ctx, st, &diskMachine{}, "d1"))
	assert.Equal(t, []string{"w"}, st.marked)
}
