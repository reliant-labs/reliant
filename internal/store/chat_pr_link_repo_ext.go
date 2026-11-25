package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/gitremote"
)

// Chat → pull request links (chat_pr_links; dev-docs/design/agent-analytics.md
// §3.2). The table is forge:no-entity, so its columns are named here.
//
// Every write to one pull request's links takes that pull request's advisory
// lock first (lockChatPRLinks), so an explicit write and an inference never
// interleave: "explicit beats inferred" holds whichever arrives first.
//
// Every insert selects its chat FOR KEY SHARE: a project delete or account
// purge locks the chats it deletes FOR UPDATE, so a link either commits first
// (and is deleted with the chat) or finds the chat gone and inserts nothing.

const chatPRLinkColumns = `id, chat_id, user_id, project_id, coalesce(run_id, '') AS run_id, provider, repository,
	repository_id, pr_number, source, created_at, updated_at`

type chatPRLinkRow struct {
	ID, ChatID, UserID, ProjectID, RunID string
	Provider, Repository, RepositoryID   string
	PRNumber                             int
	Source                               string
	CreatedAt, UpdatedAt                 time.Time
}

func (r chatPRLinkRow) core() core.ChatPRLink {
	return core.ChatPRLink{
		ID: r.ID, ChatID: r.ChatID, UserID: r.UserID, ProjectID: r.ProjectID, RunID: r.RunID,
		PRRef:        core.PRRef{Provider: r.Provider, Repository: r.Repository, Number: r.PRNumber},
		RepositoryID: r.RepositoryID, Source: r.Source, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// chatPRRef is ref as stored: the repository lowercased and trimmed.
func chatPRRef(ref core.PRRef) core.PRRef {
	ref.Repository = strings.ToLower(strings.TrimSpace(ref.Repository))
	return ref
}

// lockChatPRLinks takes the transaction-scoped lock on one pull request's
// links. ctx must carry a transaction.
func lockChatPRLinks(ctx context.Context, db orm.Context, ref core.PRRef) error {
	key := fmt.Sprintf("chat-pr-links:%s:%s#%d", ref.Provider, ref.Repository, ref.Number)
	if _, err := db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
		return fmt.Errorf("lock chat pr links: %w", err)
	}
	return nil
}

// WriteChatPRLinks records that w.ChatIDs produced w's pull request, as an
// explicit link (w.Source is never core.ChatPRLinkHeadRef), and returns how
// many chats it linked. A chat that does not exist is skipped.
//
// Explicit beats inferred: a chat's inferred link to the pull request becomes
// explicit, and the pull request's inferred links to any other chat are
// removed. An explicit link already there keeps its source; it only learns
// the run and the repository id it lacked.
func WriteChatPRLinks(ctx context.Context, db orm.Context, w core.ChatPRLinkWrite) (int, error) {
	if !core.ChatPRLinkExplicit(w.Source) {
		return 0, fmt.Errorf("write chat pr links: %q is not an explicit source", w.Source)
	}
	ref := chatPRRef(w.PRRef)
	if ref.Provider == "" || ref.Repository == "" || ref.Number <= 0 || len(w.ChatIDs) == 0 {
		return 0, nil
	}
	var linked []string
	err := db.RunTx(ctx, orm.ReadCommitted, func(tx context.Context) error {
		linked = nil
		if err := lockChatPRLinks(tx, db, ref); err != nil {
			return err
		}
		// The repository id is the record's, when the record came first.
		if err := db.Bun().NewRaw(`INSERT INTO chat_pr_links AS l
			(id, chat_id, user_id, project_id, run_id, provider, repository, repository_id, pr_number, source, created_at, updated_at)
			SELECT gen_random_uuid()::text, c.id, c.user_id, c.project_id, NULLIF(?3, ''), ?0, ?1,
				coalesce((SELECT r.repository_id FROM pr_records r
					WHERE r.provider = ?0 AND lower(r.repository) = ?1 AND r.number = ?2
					ORDER BY r.observed_at DESC LIMIT 1), ''),
				?2, ?4, ?5, ?5
			FROM chats c WHERE c.id = ANY(?6)
			FOR KEY SHARE OF c
			ON CONFLICT (chat_id, provider, repository, pr_number) DO UPDATE SET
				source = CASE WHEN l.source = 'head_ref' THEN EXCLUDED.source ELSE l.source END,
				run_id = coalesce(l.run_id, EXCLUDED.run_id),
				repository_id = CASE WHEN l.repository_id = '' THEN EXCLUDED.repository_id ELSE l.repository_id END,
				updated_at = EXCLUDED.updated_at
			RETURNING l.chat_id`,
			ref.Provider, ref.Repository, ref.Number, w.RunID, w.Source, w.At.UTC(), pgdialect.Array(w.ChatIDs)).
			Scan(tx, &linked); err != nil {
			return fmt.Errorf("write chat pr links: %w", err)
		}
		if len(linked) == 0 {
			return nil
		}
		if _, err := db.Bun().NewRaw(`DELETE FROM chat_pr_links
			WHERE provider = ?0 AND repository = ?1 AND pr_number = ?2 AND source = 'head_ref' AND NOT (chat_id = ANY(?3))`,
			ref.Provider, ref.Repository, ref.Number, pgdialect.Array(linked)).Exec(tx); err != nil {
			return fmt.Errorf("drop inferred chat pr links: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(linked), nil
}

// ListWorktreeChatIDs returns the ids of userID's chats that work in a
// worktree: every chat bound to it, and the chat it was made for. Oldest
// first.
func ListWorktreeChatIDs(ctx context.Context, db orm.Context, userID, worktreeID string) ([]string, error) {
	var ids []string
	if err := db.Bun().NewRaw(`SELECT c.id FROM chats c
		WHERE c.user_id = ?0 AND (c.worktree_id = ?1 OR c.id = (SELECT w.chat_id FROM worktrees w WHERE w.id = ?1))
		ORDER BY c.created_at, c.id`, userID, worktreeID).Scan(ctx, &ids); err != nil {
		return nil, fmt.Errorf("list worktree chats: %w", err)
	}
	return ids, nil
}

// ListChatPRLinks returns a chat's links, oldest first. It checks no access:
// the caller decides on the chat (internal/chatprlinks reads for its owner
// only).
func ListChatPRLinks(ctx context.Context, db orm.Context, chatID string) ([]core.ChatPRLink, error) {
	var rows []chatPRLinkRow
	if err := db.Bun().NewRaw(`SELECT `+chatPRLinkColumns+` FROM chat_pr_links
		WHERE chat_id = ? ORDER BY created_at, id`, chatID).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list chat pr links: %w", err)
	}
	out := make([]core.ChatPRLink, len(rows))
	for i, r := range rows {
		out[i] = r.core()
	}
	return out, nil
}

// ListPRChatLinks returns one pull request's links, oldest first. It checks
// no access.
func ListPRChatLinks(ctx context.Context, db orm.Context, ref core.PRRef) ([]core.ChatPRLink, error) {
	ref = chatPRRef(ref)
	var rows []chatPRLinkRow
	if err := db.Bun().NewRaw(`SELECT `+chatPRLinkColumns+` FROM chat_pr_links
		WHERE provider = ? AND repository = ? AND pr_number = ? ORDER BY created_at, id`,
		ref.Provider, ref.Repository, ref.Number).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list pr chat links: %w", err)
	}
	out := make([]core.ChatPRLink, len(rows))
	for i, r := range rows {
		out[i] = r.core()
	}
	return out, nil
}

// linkRecordedPR runs when a pr_records row first appears, in the transaction
// that created it: the pull request's existing links learn its repository
// id, and, when none of them is explicit, the head_ref fallback infers its
// chats (inferChatPRLinks). ctx must carry a transaction.
func linkRecordedPR(ctx context.Context, db orm.Context, s core.PRRecordSnapshot) error {
	ref := chatPRRef(core.PRRef{Provider: s.Provider, Repository: s.Repository, Number: s.Number})
	if ref.Repository == "" || ref.Number <= 0 {
		return nil
	}
	if err := lockChatPRLinks(ctx, db, ref); err != nil {
		return err
	}
	if _, err := db.Bun().NewRaw(`UPDATE chat_pr_links SET repository_id = ?3
		WHERE provider = ?0 AND repository = ?1 AND pr_number = ?2 AND repository_id = ''`,
		ref.Provider, ref.Repository, ref.Number, s.RepositoryID).Exec(ctx); err != nil {
		return fmt.Errorf("fill chat pr link repository id: %w", err)
	}
	return inferChatPRLinks(ctx, db, ref, s)
}

// inferChatPRLinks is the head_ref fallback. It links the chats of the one
// worktree whose branch is the pull request's head branch, in a live project
// with a repository whose remote is the pull request's repository, when:
//
//   - the head branch lives in the pull request's own repository: a fork's
//     pull request (or one whose head repository is unknown) names a branch
//     of another repository, which a worktree of this one never is, even
//     when the names are the same;
//   - no explicit link names the pull request (explicit beats inferred);
//   - the head branch is not the base branch, and the worktree is not a
//     project's main checkout (a fork's PR from its main names no worktree);
//   - exactly ONE worktree matches: a branch name two worktrees share (two
//     people's "fix-typo", or one reused after an archive) names neither, so
//     nothing is inferred rather than a guess;
//   - the chat's owner can see the repository at the provider: a fresh
//     integration_event_access grant for it, the same rule the PR record
//     reads use. A clone of a repository is not access to its pull requests.
func inferChatPRLinks(ctx context.Context, db orm.Context, ref core.PRRef, s core.PRRecordSnapshot) error {
	head := strings.TrimSpace(s.HeadRef)
	if head == "" || head == s.BaseRef || !strings.EqualFold(strings.TrimSpace(s.HeadRepository), ref.Repository) {
		return nil
	}
	var explicit bool
	if err := db.Bun().NewRaw(`SELECT EXISTS (SELECT 1 FROM chat_pr_links
		WHERE provider = ?0 AND repository = ?1 AND pr_number = ?2 AND source <> 'head_ref')`,
		ref.Provider, ref.Repository, ref.Number).Scan(ctx, &explicit); err != nil {
		return fmt.Errorf("infer chat pr links: read explicit links: %w", err)
	}
	if explicit {
		return nil
	}
	// Every live worktree on the branch, with every remote of its project:
	// the remotes are compared in canonical form below, because a remote can
	// be spelled in any form gitremote.Canonical accepts.
	var candidates []struct {
		WorktreeID string
		Remote     string
	}
	if err := db.Bun().NewRaw(`SELECT w.id AS worktree_id, c.remote
		FROM worktrees w
		JOIN projects p ON p.id = w.project_id AND p.delete_after IS NULL
		LEFT JOIN repos r ON r.project_id = p.id
		CROSS JOIN LATERAL (SELECT COALESCE(r.remote_url, CASE WHEN r.id IS NULL OR r.relative_path = '' THEN p.remote_url END) AS remote) c
		WHERE w.branch = ?0 AND NOT w.is_main AND w.deleted_at IS NULL AND c.remote IS NOT NULL AND c.remote <> ''`,
		head).Scan(ctx, &candidates); err != nil {
		return fmt.Errorf("infer chat pr links: read worktrees: %w", err)
	}
	want := strings.ToLower(providerHost(ref.Provider) + "/" + ref.Repository)
	matched := map[string]bool{}
	for _, c := range candidates {
		if strings.ToLower(gitremote.Canonical(c.Remote)) == want {
			matched[c.WorktreeID] = true
		}
	}
	if len(matched) != 1 {
		return nil
	}
	var worktreeID string
	for id := range matched {
		worktreeID = id
	}
	at := s.ObservedAt.UTC()
	if _, err := db.Bun().NewRaw(`INSERT INTO chat_pr_links
		(id, chat_id, user_id, project_id, run_id, provider, repository, repository_id, pr_number, source, created_at, updated_at)
		SELECT gen_random_uuid()::text, c.id, c.user_id, c.project_id, NULL, ?0, ?1, ?2, ?3, 'head_ref', ?4, ?4
		FROM chats c
		WHERE (c.worktree_id = ?5 OR c.id = (SELECT w.chat_id FROM worktrees w WHERE w.id = ?5))
			AND EXISTS (SELECT 1 FROM integration_event_access a
				WHERE a.user_id = c.user_id AND a.integration_id = ?0 AND a.account_key = ?6
					AND a.resource_key = ?2 AND a.refreshed_at >= ?7)
		FOR KEY SHARE OF c
		ON CONFLICT (chat_id, provider, repository, pr_number) DO NOTHING`,
		ref.Provider, ref.Repository, s.RepositoryID, ref.Number, at, worktreeID,
		s.AccountKey, at.Add(-core.IntegrationAccessFreshness)).Exec(ctx); err != nil {
		return fmt.Errorf("infer chat pr links: %w", err)
	}
	return nil
}

// providerHost is the host a provider's repositories are cloned from, as
// gitremote.Canonical spells it.
func providerHost(provider string) string {
	if provider == core.ChatPRLinkGitHub {
		return "github.com"
	}
	return provider
}
