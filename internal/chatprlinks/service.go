// Package chatprlinks records which pull requests a chat produced
// (chat_pr_links; dev-docs/design/agent-analytics.md §3.2, decision 10), the
// join behind cost per merged PR, and reads them back for the chat's owner.
//
// A link is written where the pull request is made: CreateWorktreePR
// (LinkWorktreePR) and a run's GitHub pr.create through the integration write
// ledger (LinkRunPR). The fallback, a pr_records row whose head branch
// matches a chat's worktree, is inferred in the store when the record first
// appears (store.linkRecordedPR). Explicit beats inferred; the store holds
// that rule, so every writer gets it.
//
// Links are the owner's: a shared chat's viewer, a collaborator and an
// outsider read none (ListForChat), because which pull requests a chat
// produced is the input to what it cost.
package chatprlinks

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// Deps holds dependencies for the chatprlinks package.
type Deps struct {
	// Store keeps the links.
	Store Store
	// Chats reads the chat a read is decided on.
	Chats Chats
	// Now is the clock links are stamped with; nil is time.Now.
	//
	// forge:optional-dep
	Now func() time.Time
}

//forge:contract
type service struct {
	deps Deps
}

// New creates a new chatprlinks service instance.
//
// forge:constructor
func New(deps Deps) (Service, error) {
	if deps.Store == nil {
		return nil, svcerr.Internal("chatprlinks: Deps.Store is required")
	}
	if deps.Chats == nil {
		return nil, svcerr.Internal("chatprlinks: Deps.Chats is required")
	}
	return &service{deps: deps}, nil
}

func (s *service) now() time.Time {
	if s.deps.Now != nil {
		return s.deps.Now()
	}
	return time.Now()
}

// LinkWorktreePR records that the pull request at prURL was opened from a
// worktree by CreateWorktreePR: every chat of userID's that works in the
// worktree is linked to it. It returns how many chats it linked; a URL that
// names no GitHub pull request links none, and is no error.
func (s *service) LinkWorktreePR(ctx context.Context, userID, worktreeID, prURL string) (int, error) {
	ref, ok := ParsePRURL(prURL)
	if !ok || userID == "" || worktreeID == "" {
		return 0, nil
	}
	chats, err := s.deps.Store.ListWorktreeChatIDs(ctx, userID, worktreeID)
	if err != nil {
		return 0, err
	}
	return s.deps.Store.WriteChatPRLinks(ctx, core.ChatPRLinkWrite{
		PRRef: ref, ChatIDs: chats, Source: core.ChatPRLinkWorktreePR, At: s.now().UTC(),
	})
}

// LinkRunPR records that a run of chatID (runID, the workflows row, when the
// call was a run's step) opened the pull request through the GitHub
// integration. It returns how many chats it linked (0 or 1).
func (s *service) LinkRunPR(ctx context.Context, chatID, runID string, pr core.PRRef) (int, error) {
	if chatID == "" || pr.Provider != core.ChatPRLinkGitHub || pr.Repository == "" || pr.Number <= 0 {
		return 0, nil
	}
	return s.deps.Store.WriteChatPRLinks(ctx, core.ChatPRLinkWrite{
		PRRef: pr, ChatIDs: []string{chatID}, RunID: runID, Source: core.ChatPRLinkIntegration, At: s.now().UTC(),
	})
}

// ListForChat returns the pull requests a chat produced, for its owner only.
// Anyone else — a viewer of the shared chat, an outsider, a credential bound
// to another org — is answered core.ErrChatNotFound, as for a chat that does
// not exist, so a link never discloses the chat or what it cost.
func (s *service) ListForChat(ctx context.Context, userID, chatID string) ([]core.ChatPRLink, error) {
	if userID == "" || chatID == "" {
		return nil, core.ErrChatNotFound
	}
	chat, err := s.deps.Chats.GetChat(ctx, chatID)
	if err != nil {
		return nil, err
	}
	access, denied := authz.ReadChatAccess(ctx, userID, chat)
	if denied != nil || access != authz.AccessOwner {
		return nil, core.ErrChatNotFound
	}
	return s.deps.Store.ListChatPRLinks(ctx, chatID)
}

// ParsePRURL reads a GitHub pull request's web URL
// (https://github.com/<owner>/<name>/pull/<number>, any trailing path, query
// or fragment) into the pull request it names, the repository lowercased.
// Anything else is false: another host, an issue URL, a malformed number.
func ParsePRURL(raw string) (core.PRRef, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return core.PRRef{}, false
	}
	if host := strings.ToLower(u.Hostname()); host != "github.com" && host != "www.github.com" {
		return core.PRRef{}, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 || parts[0] == "" || parts[1] == "" || parts[2] != "pull" {
		return core.PRRef{}, false
	}
	n, err := strconv.Atoi(parts[3])
	if err != nil || n <= 0 {
		return core.PRRef{}, false
	}
	return core.PRRef{
		Provider:   core.ChatPRLinkGitHub,
		Repository: strings.ToLower(parts[0] + "/" + parts[1]),
		Number:     n,
	}, true
}
