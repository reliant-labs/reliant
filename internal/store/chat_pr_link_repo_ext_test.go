// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"slices"
	"strconv"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The chat → pull request links (chat_pr_links): written explicitly where a
// pull request is made, inferred from its head branch when its record first
// appears, explicit beating inferred.

var linkAt = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

const (
	linkRepo   = "acme/widgets"
	linkRepoID = "4242"
	linkInst   = "inst-1"
	linkBranch = "feat/links"
	linkSHA1   = "1111111111111111111111111111111111111111"
	linkSHA2   = "2222222222222222222222222222222222222222"
)

type linkFixture struct {
	t   *testing.T
	env *storetest.Env
	ctx context.Context
	seq int
}

func newLinkFixture(t *testing.T) *linkFixture {
	t.Helper()
	return &linkFixture{t: t, env: storetest.New(t), ctx: context.Background()}
}

// project creates userID's project on remote (any spelling git accepts).
func (f *linkFixture) project(id, userID, remote string) {
	f.t.Helper()
	if err := f.env.Repo.CreateProject(f.ctx, &db.Project{
		ID: id, UserID: userID, Name: id, Path: "/home/" + userID + "/" + id, IsGitRepo: true,
		RemoteURL: &remote, CreatedAt: linkAt, UpdatedAt: linkAt, LastActive: linkAt,
	}); err != nil {
		f.t.Fatalf("project %s: %v", id, err)
	}
}

// worktree creates a worktree of projectID on branch.
func (f *linkFixture) worktree(id, projectID, branch string, main bool) {
	f.t.Helper()
	if err := store.InsertWorktree(f.ctx, f.env.ORM, &core.Worktree{
		ID: id, Name: id, Path: "/wt/" + id, Branch: branch, BaseBranch: "main", ProjectID: projectID,
		Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE), IsMain: main,
		CreatedAt: linkAt, UpdatedAt: linkAt, LastActive: linkAt,
	}); err != nil {
		f.t.Fatalf("worktree %s: %v", id, err)
	}
}

// chat creates userID's chat in projectID, in worktreeID when not "".
func (f *linkFixture) chat(id, userID, projectID, worktreeID string) {
	f.t.Helper()
	c := &store.Chat{ID: id, Title: id, ProjectID: projectID, UserID: userID,
		CreatedAt: linkAt, UpdatedAt: linkAt, LastActive: linkAt}
	if worktreeID != "" {
		c.WorktreeID = &worktreeID
	}
	if err := store.CreateChat(f.ctx, f.env.ORM, c); err != nil {
		f.t.Fatalf("chat %s: %v", id, err)
	}
}

// access records that userID's GitHub credential sees the repository, as
// confirmed at at.
func (f *linkFixture) access(userID string, at time.Time) {
	f.t.Helper()
	if _, err := f.env.SQL.Exec(`INSERT INTO integration_event_access
		(user_id, integration_id, account_key, resource_key, refreshed_at) VALUES ($1, 'github', $2, $3, $4)`,
		userID, linkInst, linkRepoID, at); err != nil {
		f.t.Fatalf("access %s: %v", userID, err)
	}
}

// record applies a pull_request event for #number on head/branch, the
// branch in the pull request's own repository.
func (f *linkFixture) record(number int, branch, head, previous string, updated time.Duration) {
	f.t.Helper()
	f.recordFrom("Acme/Widgets", number, branch, head, previous, updated)
}

// recordFrom is record with the head branch in headRepo ("" when the event
// named none).
func (f *linkFixture) recordFrom(headRepo string, number int, branch, head, previous string, updated time.Duration) {
	f.t.Helper()
	f.seq++
	c := core.PRRecordChange{
		Delivery: core.PRRecordDelivery{Provider: "github", DeliveryKey: "link-" + strconv.Itoa(f.seq), EventType: "pull_request.x", AppliedAt: linkAt},
		Snapshot: &core.PRRecordSnapshot{
			ID: "rec-" + strconv.Itoa(f.seq), Provider: "github", AccountKey: linkInst, RepositoryID: linkRepoID,
			Repository: "Acme/Widgets", Number: number, State: core.PRRecordOpen, HeadSHA: head, HeadRef: branch,
			BaseRef: "main", SnapshotAt: linkAt.Add(updated), ObservedAt: linkAt.Add(updated), Full: true,
			HeadRepository: headRepo,
		},
		HeadID:          "head-" + strconv.Itoa(f.seq),
		PreviousHeadSHA: previous,
	}
	if previous != "" {
		c.Snapshot.Delta = core.PRRecordDelta{FromHead: previous, HeadSHA: head, HeadRef: branch}
	}
	if _, err := store.ApplyPRRecordChange(f.ctx, f.env.ORM, c); err != nil {
		f.t.Fatalf("apply record #%d: %v", number, err)
	}
}

func (f *linkFixture) write(number int, source, runID string, chats ...string) int {
	f.t.Helper()
	n, err := store.WriteChatPRLinks(f.ctx, f.env.ORM, core.ChatPRLinkWrite{
		PRRef:   core.PRRef{Provider: "github", Repository: "Acme/Widgets", Number: number},
		ChatIDs: chats, RunID: runID, Source: source, At: linkAt,
	})
	if err != nil {
		f.t.Fatalf("write #%d: %v", number, err)
	}
	return n
}

// prLinks is #number's links as "chat:source".
func (f *linkFixture) prLinks(number int) []string {
	f.t.Helper()
	links, err := store.ListPRChatLinks(f.ctx, f.env.ORM, core.PRRef{Provider: "github", Repository: linkRepo, Number: number})
	if err != nil {
		f.t.Fatalf("list #%d: %v", number, err)
	}
	var out []string
	for _, l := range links {
		out = append(out, l.ChatID+":"+l.Source)
	}
	slices.Sort(out)
	return out
}

func (f *linkFixture) chatLinks(chatID string) []core.ChatPRLink {
	f.t.Helper()
	links, err := store.ListChatPRLinks(f.ctx, f.env.ORM, chatID)
	if err != nil {
		f.t.Fatalf("list %s: %v", chatID, err)
	}
	return links
}

// standard is alice's project on the repository, her worktree on the branch
// with two chats in it, her chat outside it, and her fresh access.
func (f *linkFixture) standard() {
	f.project("proj-a", "alice", "git@github.com:Acme/widgets.git")
	f.worktree("wt-a", "proj-a", linkBranch, false)
	f.chat("chat-a1", "alice", "proj-a", "wt-a")
	f.chat("chat-a2", "alice", "proj-a", "wt-a")
	f.chat("chat-other", "alice", "proj-a", "")
	f.access("alice", linkAt)
}

// TestChatPRLinkExplicitBeatsInferred: an explicit link upgrades the chat's
// inferred one and removes the pull request's inferred links to other chats;
// a pull request with an explicit link gets no inferred ones.
func TestChatPRLinkExplicitBeatsInferred(t *testing.T) {
	t.Parallel()
	t.Run("inferred first", func(t *testing.T) {
		t.Parallel()
		f := newLinkFixture(t)
		f.standard()
		f.record(7, linkBranch, linkSHA1, "", 0)
		if got, want := f.prLinks(7), []string{"chat-a1:head_ref", "chat-a2:head_ref"}; !slices.Equal(got, want) {
			t.Fatalf("inferred links = %v, want %v", got, want)
		}
		if l := f.chatLinks("chat-a1"); len(l) != 1 || l[0].RepositoryID != linkRepoID || l[0].UserID != "alice" || l[0].ProjectID != "proj-a" {
			t.Fatalf("inferred link = %+v", l)
		}
		// The run of chat-a1 opened it, explicitly: chat-a1's link becomes
		// explicit and carries the run, chat-a2's guess goes.
		if n := f.write(7, core.ChatPRLinkIntegration, "run-1", "chat-a1"); n != 1 {
			t.Fatalf("linked %d chats, want 1", n)
		}
		if got, want := f.prLinks(7), []string{"chat-a1:integration"}; !slices.Equal(got, want) {
			t.Fatalf("after explicit = %v, want %v", got, want)
		}
		if l := f.chatLinks("chat-a1"); l[0].RunID != "run-1" || l[0].RepositoryID != linkRepoID {
			t.Fatalf("upgraded link = %+v", l[0])
		}
	})
	t.Run("explicit first", func(t *testing.T) {
		t.Parallel()
		f := newLinkFixture(t)
		f.standard()
		// A chat outside the worktree pushed the branch and opened the pull
		// request: it links, and the worktree's chats are not guessed.
		f.write(7, core.ChatPRLinkWorktreePR, "", "chat-other")
		if l := f.chatLinks("chat-other"); len(l) != 1 || l[0].RepositoryID != "" {
			t.Fatalf("explicit before the record = %+v", l)
		}
		f.record(7, linkBranch, linkSHA1, "", 0)
		if got, want := f.prLinks(7), []string{"chat-other:worktree_pr"}; !slices.Equal(got, want) {
			t.Fatalf("links = %v, want %v", got, want)
		}
		// The record's arrival taught the explicit link its repository id.
		if l := f.chatLinks("chat-other"); l[0].RepositoryID != linkRepoID {
			t.Fatalf("repository id = %q, want %q", l[0].RepositoryID, linkRepoID)
		}
	})
	t.Run("an explicit source is kept", func(t *testing.T) {
		t.Parallel()
		f := newLinkFixture(t)
		f.standard()
		f.write(7, core.ChatPRLinkWorktreePR, "", "chat-a1")
		f.write(7, core.ChatPRLinkIntegration, "run-2", "chat-a1")
		if l := f.chatLinks("chat-a1"); len(l) != 1 || l[0].Source != core.ChatPRLinkWorktreePR || l[0].RunID != "run-2" {
			t.Fatalf("link = %+v, want the first explicit source with the run learned", l)
		}
	})
}

// TestChatPRLinkManyToMany: a chat with two pull requests, and a pull
// request with two chats.
func TestChatPRLinkManyToMany(t *testing.T) {
	t.Parallel()
	f := newLinkFixture(t)
	f.standard()
	f.write(7, core.ChatPRLinkWorktreePR, "", "chat-a1")
	f.write(8, core.ChatPRLinkIntegration, "run-8", "chat-a1")
	// CreateWorktreePR links every chat of the worktree; a missing chat is
	// skipped.
	if n := f.write(9, core.ChatPRLinkWorktreePR, "", "chat-a1", "chat-a2", "chat-gone"); n != 2 {
		t.Fatalf("linked %d chats, want 2", n)
	}
	var numbers []int
	for _, l := range f.chatLinks("chat-a1") {
		numbers = append(numbers, l.Number)
	}
	slices.Sort(numbers)
	if !slices.Equal(numbers, []int{7, 8, 9}) {
		t.Fatalf("chat-a1's pull requests = %v, want [7 8 9]", numbers)
	}
	if got, want := f.prLinks(9), []string{"chat-a1:worktree_pr", "chat-a2:worktree_pr"}; !slices.Equal(got, want) {
		t.Fatalf("#9's chats = %v, want %v", got, want)
	}
	// One pull request's second explicit chat keeps the first.
	f.write(7, core.ChatPRLinkIntegration, "", "chat-a2")
	if got, want := f.prLinks(7), []string{"chat-a1:worktree_pr", "chat-a2:integration"}; !slices.Equal(got, want) {
		t.Fatalf("#7's chats = %v, want %v", got, want)
	}
}

// TestChatPRLinkSurvivesForcePush: a force-push (a synchronize whose before
// is not the head's ancestor) changes the record's head and nothing about
// its links: they name the pull request, never a sha, and inference runs only
// on the record's first appearance.
func TestChatPRLinkSurvivesForcePush(t *testing.T) {
	t.Parallel()
	f := newLinkFixture(t)
	f.standard()
	f.record(7, linkBranch, linkSHA1, "", 0)
	f.write(7, core.ChatPRLinkIntegration, "run-1", "chat-a1")
	before := f.chatLinks("chat-a1")

	// Another worktree on the same branch appears (the branch reused); a
	// re-inference would now find it ambiguous and must not run at all.
	f.worktree("wt-b", "proj-a", linkBranch, false)
	f.chat("chat-b", "alice", "proj-a", "wt-b")
	f.record(7, linkBranch, linkSHA2, linkSHA1, time.Minute)

	rec, err := store.GetPRRecord(f.ctx, f.env.ORM, "github", linkRepo, 7, []string{linkRepoID})
	if err != nil || rec == nil || rec.HeadSHA != linkSHA2 {
		t.Fatalf("record after the force-push = %+v, %v", rec, err)
	}
	if got, want := f.prLinks(7), []string{"chat-a1:integration"}; !slices.Equal(got, want) {
		t.Fatalf("links after the force-push = %v, want %v", got, want)
	}
	if after := f.chatLinks("chat-a1"); len(after) != 1 || after[0] != before[0] {
		t.Fatalf("link changed by the force-push:\n before %+v\n after  %+v", before, after)
	}
}

// TestChatPRLinkInferenceGuards: the head_ref fallback infers only from one
// unambiguous worktree, on the pull request's own repository, of a project
// that is live, for an owner who can see the repository.
func TestChatPRLinkInferenceGuards(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup func(f *linkFixture)
		want  []string
	}{
		{"one worktree on the branch", func(f *linkFixture) {}, []string{"chat-a1:head_ref", "chat-a2:head_ref"}},
		{"a branch two worktrees share names neither", func(f *linkFixture) {
			f.project("proj-b", "bob", "https://github.com/acme/widgets")
			f.worktree("wt-b", "proj-b", linkBranch, false)
			f.chat("chat-b", "bob", "proj-b", "wt-b")
			f.access("bob", linkAt)
		}, nil},
		{"a worktree of another repository is no match", func(f *linkFixture) {
			f.project("proj-c", "carol", "https://github.com/acme/gadgets.git")
			f.worktree("wt-c", "proj-c", linkBranch, false)
			f.chat("chat-c", "carol", "proj-c", "wt-c")
		}, []string{"chat-a1:head_ref", "chat-a2:head_ref"}},
		{"an archived worktree is no match", func(f *linkFixture) {
			if _, err := f.env.SQL.Exec(`UPDATE worktrees SET deleted_at = $1 WHERE id = 'wt-a'`, linkAt); err != nil {
				f.t.Fatal(err)
			}
		}, nil},
		{"a stale access grant links nothing", func(f *linkFixture) {
			if _, err := f.env.SQL.Exec(`UPDATE integration_event_access SET refreshed_at = $1`, linkAt.Add(-2*core.IntegrationAccessFreshness)); err != nil {
				f.t.Fatal(err)
			}
		}, nil},
		{"a project scheduled for deletion is no match", func(f *linkFixture) {
			if _, err := f.env.SQL.Exec(`UPDATE projects SET delete_after = $1 WHERE id = 'proj-a'`, linkAt.Add(time.Hour)); err != nil {
				f.t.Fatal(err)
			}
		}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newLinkFixture(t)
			f.standard()
			tc.setup(f)
			f.record(7, linkBranch, linkSHA1, "", 0)
			if got := f.prLinks(7); !slices.Equal(got, tc.want) {
				t.Fatalf("links = %v, want %v", got, tc.want)
			}
		})
	}
	// A fork's pull request names a branch of the fork: a worktree of the
	// base repository on a branch of the same name did not produce it.
	for name, headRepo := range map[string]string{
		"a fork's pull request on the same branch name infers nothing": "someone/widgets",
		"a head repository the event did not name infers nothing":      "",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newLinkFixture(t)
			f.standard()
			f.recordFrom(headRepo, 7, linkBranch, linkSHA1, "", 0)
			if got := f.prLinks(7); got != nil {
				t.Fatalf("links = %v, want none", got)
			}
		})
	}
	t.Run("a pull request from the base branch or a main checkout infers nothing", func(t *testing.T) {
		t.Parallel()
		f := newLinkFixture(t)
		f.standard()
		f.worktree("wt-main", "proj-a", "main", true)
		f.chat("chat-main", "alice", "proj-a", "wt-main")
		f.record(7, "main", linkSHA1, "", 0)
		if got := f.prLinks(7); got != nil {
			t.Fatalf("links = %v, want none", got)
		}
	})
}
