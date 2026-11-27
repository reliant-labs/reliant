// Copyright (c) 2025 Reliant Labs
package practions

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/writeledger"
)

// CatalogGitHub is GitHub through the integration catalog's own actions
// (github/pr.get, branch.get, issue.comment, issue.update), run the way
// an action node runs them: credentials resolved for the run on the worker,
// every write recorded in the write ledger, so the comment it posts is
// attributed to the run and an event it causes is known as its echo.
// Reads speak as the run's owner; writes as the Reliant GitHub App's bot
// (acting identity app), whatever the activation's own identity.
type CatalogGitHub struct {
	Catalog     *catalog.Catalog
	Runner      writeledger.Runner
	Credentials httpaction.CredentialSource
}

func (g *CatalogGitHub) run(ctx context.Context, as Caller, uses string, params map[string]any, write bool) (*httpaction.Result, error) {
	if g == nil || g.Catalog == nil || g.Runner == nil || g.Credentials == nil {
		return nil, errors.New("practions: GitHub is not configured on this worker")
	}
	action, err := g.Catalog.Resolve(uses)
	if err != nil {
		return nil, err
	}
	site := httpaction.CallSite{RunID: as.RunID, NodeID: as.NodeID, ToolCallID: as.ToolCallID, Automation: as.Automation}
	if write {
		site.ActingIdentity = httpaction.IdentityApp
	}
	res, err := g.Runner.RunAuthenticated(ctx, action.Manifest, action.Spec, params, g.Credentials, site)
	if err != nil {
		return nil, err
	}
	if res.IsError {
		return nil, fmt.Errorf("%s: %s (status %d)", uses, res.Content, res.StatusCode)
	}
	return res, nil
}

func ownerRepo(repo string) (string, string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return "", "", fmt.Errorf("repository %q is not owner/name", repo)
	}
	return owner, name, nil
}

// PullRequest reads the PR (github/pr.get).
func (g *CatalogGitHub) PullRequest(ctx context.Context, as Caller, repo string, number int) (PullRequest, error) {
	owner, name, err := ownerRepo(repo)
	if err != nil {
		return PullRequest{}, err
	}
	res, err := g.run(ctx, as, "github/pr.get@1", map[string]any{"owner": owner, "repo": name, "pull_number": number}, false)
	if err != nil {
		return PullRequest{}, err
	}
	d := res.Data
	pr := PullRequest{Number: number}
	pr.State, _ = d["state"].(string)
	pr.Merged, _ = d["merged"].(bool)
	pr.Draft, _ = d["draft"].(bool)
	pr.Author, _ = d["user"].(string)
	pr.Body, _ = d["body"].(string)
	if h, ok := d["head"].(map[string]any); ok {
		pr.HeadSHA, _ = h["sha"].(string)
		pr.HeadRef, _ = h["ref"].(string)
	}
	if b, ok := d["base"].(map[string]any); ok {
		pr.BaseRef, _ = b["ref"].(string)
	}
	if pr.HeadSHA == "" {
		return PullRequest{}, fmt.Errorf("%s#%d: GitHub returned no head", repo, number)
	}
	return pr, nil
}

// BranchTip reads the commit a branch points at (github/branch.get): a ref,
// never contents. A branch that does not exist is an error.
func (g *CatalogGitHub) BranchTip(ctx context.Context, as Caller, repo, branch string) (string, error) {
	owner, name, err := ownerRepo(repo)
	if err != nil {
		return "", err
	}
	res, err := g.run(ctx, as, "github/branch.get@1", map[string]any{"owner": owner, "repo": name, "branch": branch}, false)
	if err != nil {
		return "", err
	}
	sha, _ := res.Data["sha"].(string)
	if exists, _ := res.Data["exists"].(bool); !exists || sha == "" {
		return "", fmt.Errorf("%s has no branch %q", repo, branch)
	}
	return sha, nil
}

// Comment posts on the PR's conversation (github/issue.comment) as the bot.
func (g *CatalogGitHub) Comment(ctx context.Context, as Caller, repo string, number int, body string) error {
	owner, name, err := ownerRepo(repo)
	if err != nil {
		return err
	}
	_, err = g.run(ctx, as, "github/issue.comment@1", map[string]any{"owner": owner, "repo": name, "issue_number": number, "body": body}, true)
	return err
}

// Close closes the PR (github/issue.update, state closed) as the bot.
func (g *CatalogGitHub) Close(ctx context.Context, as Caller, repo string, number int) error {
	owner, name, err := ownerRepo(repo)
	if err != nil {
		return err
	}
	_, err = g.run(ctx, as, "github/issue.update@1",
		map[string]any{"owner": owner, "repo": name, "issue_number": number, "state": "closed", "state_reason": "not_planned"}, true)
	return err
}
