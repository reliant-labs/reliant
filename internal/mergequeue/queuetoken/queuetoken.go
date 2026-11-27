// Copyright (c) 2025 Reliant Labs

// Package queuetoken is the GitHub identity the merge queue lands with.
//
// The queue lands as the Reliant Queue App when it can: a second GitHub App,
// the one actor a repository's ruleset lets update its default branch
// (pipeline redesign §3.1, §5, owner decision D2). control-plane mints its
// token only for the reliant worker, proven by an assertion signed with
// GITHUB_QUEUE_APP_CALLER_SECRET (pipeline.github_queue_app_caller_secret).
//
// When it cannot, because the worker holds no caller secret or control-plane
// answers that the Queue App is not configured or not installed on the
// repository, the queue lands as the Reliant App instead, minted for the
// repository's enrolling user exactly as the worker's other GitHub writes
// are. That landing is UNENFORCED: no ruleset can tell the queue's writes from
// any agent's, so the queue still lands everything but GitHub does not stop
// others writing main. Each token says which it is ([Token.Unenforced]), and
// the lander records it on the batch.
//
//forge:exclude-contract: built by the worker's composition root for the lander; its seams are the consumer-side Minter interface and lander.Credentials, which Clients implements
package queuetoken

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
	"github.com/reliant-labs/reliant/internal/mergequeue/ghrest"
	"github.com/reliant-labs/reliant/internal/mergequeue/lander"
)

// Minter is what the source asks control-plane for. gitcredentialclient's
// client satisfies it.
type Minter interface {
	MintQueueInstallationToken(ctx context.Context, req gitcredentialclient.QueueInstallationRequest) (gitcredentialclient.InstallationToken, error)
	MintInstallationToken(ctx context.Context, req gitcredentialclient.InstallationRequest) (gitcredentialclient.InstallationToken, error)
}

// queuePermissions are what landing needs: merge and revert (contents), the
// merge call itself (pull_requests), and the display check (checks). PR
// comments ride on pull_requests.
var queuePermissions = map[string]string{"contents": "write", "pull_requests": "write", "checks": "write"}

// botPermissions are the same for the Reliant App, plus issues for the
// display comments (its default set has no checks).
var botPermissions = map[string]string{"contents": "write", "pull_requests": "write", "checks": "write", "issues": "write"}

// renewBefore is how long before expiry a cached token is replaced.
const renewBefore = 5 * time.Minute

// Token is a minted token and the identity it speaks as. It holds plaintext
// inside gitcredentialclient.Token, which never prints it.
type Token struct {
	Token gitcredentialclient.Token
	// Unenforced is true for the Reliant App fallback.
	Unenforced bool
	// Login is the bot GitHub shows for the token's writes.
	Login     string
	ExpiresAt time.Time
}

// Source mints and caches the queue's tokens per repository. It is safe for
// concurrent use.
type Source struct {
	minter       Minter
	callerSecret string
	now          func() time.Time
	logger       *slog.Logger

	mu    sync.Mutex
	cache map[string]Token
}

// Options configure a [Source].
type Options struct {
	Minter Minter
	// CallerSecret is GITHUB_QUEUE_APP_CALLER_SECRET. Empty (or too short to
	// sign with) never asks for a Queue App token.
	CallerSecret string
	Now          func() time.Time
	Logger       *slog.Logger
}

// New returns a source.
func New(o Options) (*Source, error) {
	if o.Minter == nil {
		return nil, errors.New("queuetoken: a minter is required")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	return &Source{minter: o.Minter, callerSecret: o.CallerSecret, now: o.Now, logger: o.Logger, cache: map[string]Token{}}, nil
}

// Token returns a token for repo, from the cache while it has more than
// renewBefore to live.
func (s *Source) Token(ctx context.Context, repo *core.PipelineRepo) (Token, error) {
	s.mu.Lock()
	cached, ok := s.cache[repo.ID]
	s.mu.Unlock()
	if ok && s.now().Add(renewBefore).Before(cached.ExpiresAt) {
		return cached, nil
	}
	tok, err := s.mint(ctx, repo)
	if err != nil {
		return Token{}, err
	}
	s.mu.Lock()
	s.cache[repo.ID] = tok
	s.mu.Unlock()
	return tok, nil
}

func (s *Source) mint(ctx context.Context, repo *core.PipelineRepo) (Token, error) {
	gh, err := ghport.ParseRepo(repo.Repository)
	if err != nil {
		return Token{}, err
	}
	if len(s.callerSecret) >= gitcredentialclient.QueueCallerSecretMinLen {
		assertion, err := gitcredentialclient.SignQueueCallerAssertion(s.callerSecret, s.now())
		if err != nil {
			return Token{}, err
		}
		tok, err := s.minter.MintQueueInstallationToken(ctx, gitcredentialclient.QueueInstallationRequest{
			Owner: gh.Owner, Repo: gh.Name, Permissions: queuePermissions, CallerAssertion: assertion,
		})
		switch {
		case err == nil:
			return Token{Token: tok.Token, Login: tok.BotLogin, ExpiresAt: tok.ExpiresAt}, nil
		case errors.Is(err, gitcredentialclient.ErrQueueAppNotConfigured), errors.Is(err, gitcredentialclient.ErrQueueAppNotInstalled):
			s.logger.InfoContext(ctx, "merge queue lands unenforced: no Queue App for the repository",
				"repository", repo.Repository, "reason", err.Error())
		default:
			return Token{}, fmt.Errorf("queuetoken: Queue App token for %s: %w", repo.Repository, err)
		}
	}
	if repo.CreatedByUserID == "" {
		return Token{}, fmt.Errorf("queuetoken: %s has no enrolling user to mint the Reliant App's token for", repo.Repository)
	}
	req := gitcredentialclient.InstallationRequest{
		ExternalUserID: repo.CreatedByUserID, Owner: gh.Owner, Repo: gh.Name, Permissions: botPermissions,
	}
	tok, err := s.minter.MintInstallationToken(ctx, req)
	if errors.Is(err, gitcredentialclient.ErrAppPermissions) {
		// An installation that never granted checks: land without the
		// display check rather than not land (control-plane's default set,
		// contents, pull_requests and issues, is what merging needs).
		s.logger.WarnContext(ctx, "merge queue's Reliant App token has no checks permission: landing without the display check",
			"repository", repo.Repository)
		req.Permissions = nil
		tok, err = s.minter.MintInstallationToken(ctx, req)
	}
	if err != nil {
		return Token{}, fmt.Errorf("queuetoken: Reliant App token for %s: %w", repo.Repository, err)
	}
	return Token{Token: tok.Token, Unenforced: true, Login: tok.BotLogin, ExpiresAt: tok.ExpiresAt}, nil
}

// Clients is the lander's [lander.Credentials] over GitHub's REST API.
type Clients struct {
	Source *Source
	// HTTPClient performs the REST calls (ghrest's default when nil).
	HTTPClient *http.Client
}

var _ lander.Credentials = (*Clients)(nil)

// Client implements lander.Credentials: a REST client whose every call
// carries the repository's current token, and the identity it lands as.
func (c *Clients) Client(ctx context.Context, repo *core.PipelineRepo) (ghport.Client, lander.Identity, error) {
	tok, err := c.Source.Token(ctx, repo)
	if err != nil {
		return nil, lander.Identity{}, err
	}
	client := ghrest.NewInstrumented(ghrest.Deps{
		HTTPClient: c.HTTPClient,
		Token: func(ctx context.Context, _ ghport.Repo) (string, error) {
			t, err := c.Source.Token(ctx, repo)
			if err != nil {
				return "", err
			}
			var plain string
			_ = t.Token.Use(func(s string) error { plain = s; return nil })
			return plain, nil
		},
	})
	return client, lander.Identity{Unenforced: tok.Unenforced, Login: tok.Login}, nil
}
