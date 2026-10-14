// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/reliant-labs/reliant/internal/gitpublish"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/writeledger"
)

// githubReader answers gitpublish's three questions through the github
// integration's read-only actions, authenticated as the run's identity. It
// makes no write.
type githubReader struct {
	catalog     *catalog.Catalog
	runner      writeledger.Runner
	credentials httpaction.CredentialSource
	site        httpaction.CallSite
}

var _ gitpublish.GitHubReader = (*githubReader)(nil)

func (g *githubReader) call(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	resolved, err := g.catalog.Resolve("github/" + action + "@1")
	if err != nil {
		return nil, err
	}
	res, err := g.runner.RunAuthenticated(ctx, resolved.Manifest, resolved.Spec, params, g.credentials, g.site)
	if err != nil {
		return nil, err
	}
	if res.IsError {
		if res.StatusCode == http.StatusNotFound {
			return nil, errGitHubNotFound
		}
		return nil, fmt.Errorf("github %s: %s", action, res.Content)
	}
	return res.Data, nil
}

var errGitHubNotFound = errors.New("github: not found")

func (g *githubReader) DefaultBranch(ctx context.Context, r gitpublish.Repo) (string, error) {
	data, err := g.call(ctx, "repo.get", map[string]any{"owner": r.Owner, "repo": r.Name})
	if err != nil {
		return "", err
	}
	def, _ := data["default_branch"].(string)
	if def == "" {
		return "", errors.New("github returned no default branch")
	}
	return def, nil
}

func (g *githubReader) Protected(ctx context.Context, r gitpublish.Repo, branch string) (bool, error) {
	data, err := g.call(ctx, "branch.get", map[string]any{"owner": r.Owner, "repo": r.Name, "branch": branch})
	if err != nil {
		return false, err
	}
	p, _ := data["protected"].(bool)
	return p, nil
}

func (g *githubReader) PullRequest(ctx context.Context, r gitpublish.Repo, number int64) (gitpublish.PullRequestHead, error) {
	data, err := g.call(ctx, "pr.get", map[string]any{"owner": r.Owner, "repo": r.Name, "pull_number": number})
	if errors.Is(err, errGitHubNotFound) {
		return gitpublish.PullRequestHead{}, gitpublish.ErrPullRequestNotFound
	}
	if err != nil {
		return gitpublish.PullRequestHead{}, err
	}
	str := func(m map[string]any, k string) string { s, _ := m[k].(string); return s }
	head, _ := data["head"].(map[string]any)
	base, _ := data["base"].(map[string]any)
	return gitpublish.PullRequestHead{
		State: str(data, "state"), HeadRepo: str(head, "repo"), HeadRef: str(head, "ref"), BaseRepo: str(base, "repo"),
	}, nil
}

// tokenOf extracts the bearer a credential would send to GitHub, by applying it
// to a request and reading the header back. It is the only way to the plaintext:
// a credential never hands out its token any other way. The token is for ONE
// push and goes into one daemon command payload.
func tokenOf(cred httpaction.Credential) (string, error) {
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return "", err
	}
	if err := cred.Apply(req); err != nil {
		return "", err
	}
	auth := strings.TrimSpace(req.Header.Get("Authorization"))
	for _, prefix := range []string{"Bearer ", "bearer ", "token ", "Token "} {
		if t, ok := strings.CutPrefix(auth, prefix); ok && strings.TrimSpace(t) != "" {
			return strings.TrimSpace(t), nil
		}
	}
	return "", &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition,
		Message: "the GitHub credential is not a token that can push over git: connect GitHub with the Reliant app or a personal access token"}
}
