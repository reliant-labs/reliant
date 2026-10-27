// Copyright (c) 2025 Reliant Labs
package prhandoffadapter

import (
	"context"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/writeledger"
	"github.com/reliant-labs/reliant/internal/prhandoff"
)

// ReviewerPolicy authorizes stable provider reviewer IDs in an authenticated
// owner/repository scope. No default accepts app branding, prose or comments.
type ReviewerPolicy interface {
	AuthorizedReviewer(context.Context, string, int64, int64) (bool, error)
}

// GitHub fetches provider snapshots through existing credentialed read actions.
// Construct per authenticated call site; OwnerID is server-resolved. It is not
// a public webhook handler and must not be constructed from webhook JSON.
type GitHub struct {
	OwnerID     string
	Site        httpaction.CallSite
	Catalog     *catalog.Catalog
	Runner      writeledger.Runner
	Credentials httpaction.CredentialSource
	Reviewers   ReviewerPolicy
}

func (g GitHub) call(ctx context.Context, owner, action string, params map[string]any) (map[string]any, error) {
	if owner == "" || owner != g.OwnerID || g.Site.RunID == "" || g.Catalog == nil || g.Runner == nil || g.Credentials == nil {
		return nil, prhandoff.ErrRefused
	}
	resolved, err := g.Catalog.Resolve("github/" + action + "@1")
	if err != nil {
		return nil, err
	}
	res, err := g.Runner.RunAuthenticated(ctx, resolved.Manifest, resolved.Spec, params, g.Credentials, g.Site)
	if err != nil {
		return nil, err
	}
	if res == nil || res.IsError {
		return nil, fmt.Errorf("PR handoff GitHub %s read failed", action)
	}
	return res.Data, nil
}

func repoParams(name string) (map[string]any, error) {
	owner, repo, ok := strings.Cut(name, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return nil, prhandoff.ErrRefused
	}
	return map[string]any{"owner": owner, "repo": repo}, nil
}

// Repository resolves stable GitHub repository identity, failing on renames or
// redirects instead of treating a recycled owner/name as the enrolled repo.
func (g GitHub) Repository(ctx context.Context, owner, name string) (prhandoff.Repository, error) {
	p, err := repoParams(name)
	if err != nil {
		return prhandoff.Repository{}, err
	}
	data, err := g.call(ctx, owner, "repo.get", p)
	if err != nil {
		return prhandoff.Repository{}, err
	}
	canonical, _ := data["full_name"].(string)
	id := integer(data["id"])
	if id <= 0 || !strings.EqualFold(canonical, name) {
		return prhandoff.Repository{}, prhandoff.ErrRefused
	}
	return prhandoff.Repository{ID: id, Name: strings.ToLower(canonical)}, nil
}

// BranchHead reads the full provider head after successful publication.
func (g GitHub) BranchHead(ctx context.Context, owner string, repo prhandoff.Repository, branch string) (string, error) {
	actual, err := g.Repository(ctx, owner, repo.Name)
	if err != nil {
		return "", err
	}
	if actual != repo {
		return "", prhandoff.ErrRefused
	}
	p, err := repoParams(repo.Name)
	if err != nil {
		return "", err
	}
	p["branch"] = branch
	data, err := g.call(ctx, owner, "branch.get", p)
	if err != nil {
		return "", err
	}
	sha, _ := data["sha"].(string)
	return sha, nil
}

// Pull verifies stable base/head repository IDs using provider reads because
// the existing PR action projects repository names, not IDs. Forks are refused.
func (g GitHub) Pull(ctx context.Context, owner string, repo prhandoff.Repository, number int64) (prhandoff.Pull, error) {
	actual, err := g.Repository(ctx, owner, repo.Name)
	if err != nil {
		return prhandoff.Pull{}, err
	}
	if actual != repo {
		return prhandoff.Pull{}, prhandoff.ErrRefused
	}
	p, err := repoParams(repo.Name)
	if err != nil {
		return prhandoff.Pull{}, err
	}
	p["pull_number"] = number
	data, err := g.call(ctx, owner, "pr.get", p)
	if err != nil {
		return prhandoff.Pull{}, err
	}
	head, _ := data["head"].(map[string]any)
	base, _ := data["base"].(map[string]any)
	headName, _ := head["repo"].(string)
	baseName, _ := base["repo"].(string)
	if !strings.EqualFold(baseName, repo.Name) || !strings.EqualFold(headName, repo.Name) {
		return prhandoff.Pull{}, prhandoff.ErrRefused
	}
	branch, _ := head["ref"].(string)
	sha, _ := head["sha"].(string)
	state, _ := data["state"].(string)
	return prhandoff.Pull{Repo: repo, Number: integer(data["number"]), ObjectID: integer(data["id"]), HeadRepoID: repo.ID, Branch: branch, HeadSHA: sha, Open: state == "open"}, nil
}

// Review refetches review facts and authorizes the stable reviewer ID. It never
// accepts caller-provided findings, run IDs, SHA, verdict or author association.
func (g GitHub) Review(ctx context.Context, owner string, repo prhandoff.Repository, number int64, reviewID string) (prhandoff.Completion, error) {
	if g.Reviewers == nil {
		return prhandoff.Completion{}, prhandoff.ErrRefused
	}
	pull, err := g.Pull(ctx, owner, repo, number)
	if err != nil {
		return prhandoff.Completion{}, err
	}
	p, err := repoParams(repo.Name)
	if err != nil {
		return prhandoff.Completion{}, err
	}
	p["pull_number"] = number
	data, err := g.call(ctx, owner, "pr.reviews.list", p)
	if err != nil {
		return prhandoff.Completion{}, err
	}
	items, _ := data["items"].([]any)
	for _, item := range items {
		row, ok := item.(map[string]any)
		if !ok || fmt.Sprint(integer(row["id"])) != reviewID {
			continue
		}
		userID := integer(row["user_id"])
		if userID <= 0 {
			return prhandoff.Completion{}, prhandoff.ErrRefused
		}
		allowed, err := g.Reviewers.AuthorizedReviewer(ctx, owner, repo.ID, userID)
		if err != nil {
			return prhandoff.Completion{}, err
		}
		if !allowed {
			return prhandoff.Completion{}, prhandoff.ErrRefused
		}
		state, _ := row["state"].(string)
		sha, _ := row["commit_id"].(string)
		verdict := strings.ToLower(state)
		if verdict != "approved" && verdict != "changes_requested" {
			return prhandoff.Completion{}, prhandoff.ErrRefused
		}
		return prhandoff.Completion{Pull: pull, ReviewID: reviewID, HeadSHA: sha, Verdict: verdict}, nil
	}
	return prhandoff.Completion{}, prhandoff.ErrRefused
}
