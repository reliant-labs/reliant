// Copyright (c) 2025 Reliant Labs
package mergequeue

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
	"github.com/reliant-labs/reliant/internal/mergequeue/gitport"
)

// Enrolled lists the repositories whose mode is not off: *db.Repo's
// ListEnrolledPipelineRepos.
type Enrolled interface {
	ListEnrolledPipelineRepos(ctx context.Context) ([]*core.PipelineRepo, error)
}

// Run drives the queue of every enrolled repository until ctx ends: it looks
// for newly enrolled repositories every interval and drives each one
// ([Scheduler.Drive]) on its own goroutine. Every worker runs it; the leader
// lock makes exactly one of them drive each repository at a time.
func (s *Scheduler) Run(ctx context.Context, repos Enrolled, every time.Duration) error {
	if every <= 0 {
		every = DefaultTickInterval
	}
	var (
		mu      sync.Mutex
		driving = map[string]bool{}
		wg      sync.WaitGroup
	)
	defer wg.Wait()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		list, err := repos.ListEnrolledPipelineRepos(ctx)
		if err != nil && ctx.Err() == nil {
			s.logger.WarnContext(ctx, "merge queue: list enrolled repositories", "error", err)
		}
		for _, r := range list {
			mu.Lock()
			if driving[r.ID] {
				mu.Unlock()
				continue
			}
			driving[r.ID] = true
			mu.Unlock()
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				_ = s.Drive(ctx, id, every)
				mu.Lock()
				delete(driving, id)
				mu.Unlock()
			}(r.ID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// DefaultTickInterval is how often a worker ticks each repository.
const DefaultTickInterval = 30 * time.Second

// GitHubAccess is how the queue reaches one repository on GitHub: the client
// it reads (and, as the lander, writes) with, and the token git fetches and
// pushes with. The Queue App's installation token (M10/M11), or the existing
// bot's when the App is absent (unenforced).
type GitHubAccess func(ctx context.Context, repo *core.PipelineRepo) (client ghport.Client, token string, err error)

// GitWorkdirs keeps one bare git repository per enrolled repository under
// Root, the queue's workdir for building candidates. The token is asked
// for on every Open (a tick), so an installation token's renewal is the
// access's business; it reaches git as an HTTP header in git's environment,
// never in the remote URL or on disk.
type GitWorkdirs struct {
	Root   string
	Access GitHubAccess
	// BaseURL is the git host; empty is https://github.com.
	BaseURL string
}

// Open implements Workdirs.
func (w GitWorkdirs) Open(ctx context.Context, repo *core.PipelineRepo) (Workdir, error) {
	if w.Root == "" || w.Access == nil {
		return Workdir{}, errors.New("mergequeue: GitWorkdirs needs Root and Access")
	}
	client, token, err := w.Access(ctx, repo)
	if err != nil {
		return Workdir{}, err
	}
	base := strings.TrimRight(w.BaseURL, "/")
	if base == "" {
		base = "https://github.com"
	}
	var env []string
	if token != "" {
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
		env = []string{
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http." + base + "/.extraheader",
			"GIT_CONFIG_VALUE_0=AUTHORIZATION: basic " + basic,
		}
	}
	dir := filepath.Join(w.Root, repo.ID+".git")
	if err := os.MkdirAll(w.Root, 0o700); err != nil {
		return Workdir{}, err
	}
	git, err := gitport.Init(ctx, dir, gitport.Options{Env: env})
	if err != nil {
		return Workdir{}, err
	}
	return Workdir{Git: git, Remote: base + "/" + repo.Repository + ".git", GitHub: client}, nil
}
