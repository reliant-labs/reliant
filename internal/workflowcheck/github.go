// Copyright (c) 2025 Reliant Labs
package workflowcheck

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

// The catalog's check_run actions (reliant #1942): the engine writes a check
// exactly as a workflow's own action node would.
const (
	usesCreate = "github/check_run.create@1"
	usesUpdate = "github/check_run.update@1"
	usesList   = "github/check_run.list@1"
	githubID   = "github"
)

// ActionRunner runs a catalog action with a credential source.
// *httpaction.Runner satisfies it.
type ActionRunner interface {
	RunAuthenticated(ctx context.Context, integration *reliantv1.IntegrationManifest, action *reliantv1.ActionSpec, params map[string]any, src httpaction.CredentialSource, site httpaction.CallSite) (*httpaction.Result, error)
}

// AppBroker mints the GitHub App's credential for one repository, on behalf
// of an owner: the GitHub delegated broker (connauth.ActingBroker) is one.
type AppBroker interface {
	ActingCredential(ctx context.Context, ownerUserID string, req httpaction.CredentialRequest) (httpaction.Credential, error)
}

// GitHubDeps are the GitHub writer's collaborators.
type GitHubDeps struct {
	Runner ActionRunner
	// Broker is the App's delegated broker; nil fails every write, saying
	// this deployment cannot act as the App.
	Broker AppBroker
	// Catalog resolves the check_run actions; nil is the builtin catalog.
	Catalog *catalog.Catalog
}

// NewGitHub builds the writer the syncer posts checks with: the catalog's
// check_run actions, authenticated as the GitHub App's installation token
// for the one repository written, minted for the trigger's owner. A check
// run can only be written by an App, so a trigger that acts as its owner
// cannot have one.
func NewGitHub(deps GitHubDeps) GitHub {
	c := deps.Catalog
	if c == nil {
		c = catalog.MustBuiltin()
	}
	return &github{runner: deps.Runner, broker: deps.Broker, catalog: c}
}

type github struct {
	runner  ActionRunner
	broker  AppBroker
	catalog *catalog.Catalog
}

// CheckRuns implements GitHub: it applies the org's integrations policy and
// mints the App's credential once, for every write of one sync.
func (g *github) CheckRuns(ctx context.Context, id Identity, owner, repo string) (CheckRuns, error) {
	if id.ActingIdentity != httpaction.IdentityApp {
		return nil, errors.New("a check run is written by the Reliant GitHub App, so the automation must act as the Reliant bot; it acts as its owner")
	}
	if g.broker == nil {
		return nil, errors.New("this deployment has no GitHub App to write check runs as")
	}
	create, err := g.catalog.Resolve(usesCreate)
	if err != nil {
		return nil, err
	}
	d, err := orgpolicy.JudgeIntegration(ctx, id.OrgID, githubID, false)
	if err != nil {
		return nil, fmt.Errorf("read the organization's integrations policy: %w", err)
	}
	if d.Refused {
		return nil, fmt.Errorf("the organization's policy does not allow the %s integration", githubID)
	}
	params := map[string]any{"owner": owner, "repo": repo}
	cred, err := g.broker.ActingCredential(ctx, id.UserID, httpaction.CredentialRequest{
		ActingIdentity: httpaction.IdentityApp,
		Automation:     id.Automation,
		ActionID:       create.Spec.GetId(),
		Mutates:        true,
		Params:         params,
		IntegrationID:  githubID,
		Connection:     create.Manifest.GetConnection(),
		ServerPlaced:   true,
	})
	if err != nil {
		return nil, err
	}
	return &checkRuns{g: g, cred: fixedCredential{cred}, owner: owner, repo: repo, site: httpaction.CallSite{
		ActingIdentity: httpaction.IdentityApp, Automation: id.Automation,
	}}, nil
}

type checkRuns struct {
	g           *github
	cred        fixedCredential
	owner, repo string
	site        httpaction.CallSite
}

func (c *checkRuns) Find(ctx context.Context, headSHA, name, externalID string) (int64, error) {
	data, err := c.run(ctx, usesList, map[string]any{"head_sha": headSHA, "check_name": name, "filter": "all"})
	if err != nil {
		return 0, err
	}
	items, _ := data["items"].([]any)
	for _, item := range items {
		run, _ := item.(map[string]any)
		if ext, _ := run["external_id"].(string); ext == externalID && externalID != "" {
			return intOf(run["id"])
		}
	}
	return 0, nil
}

func (c *checkRuns) Create(ctx context.Context, w Write) (int64, error) {
	params := writeParams(w)
	params["name"], params["head_sha"] = w.Name, w.HeadSHA
	data, err := c.run(ctx, usesCreate, params)
	if err != nil {
		return 0, err
	}
	return intOf(data["id"])
}

func (c *checkRuns) Update(ctx context.Context, id int64, w Write) error {
	params := writeParams(w)
	params["check_run_id"] = id
	_, err := c.run(ctx, usesUpdate, params)
	return err
}

func (c *checkRuns) run(ctx context.Context, uses string, params map[string]any) (map[string]any, error) {
	action, err := c.g.catalog.Resolve(uses)
	if err != nil {
		return nil, err
	}
	params["owner"], params["repo"] = c.owner, c.repo
	res, err := c.g.runner.RunAuthenticated(ctx, action.Manifest, action.Spec, params, c.cred, c.site)
	if err != nil {
		return nil, err
	}
	if res.IsError {
		return nil, fmt.Errorf("%s: GitHub answered %d: %s", uses, res.StatusCode, truncate(res.Content, 1024))
	}
	return res.Data, nil
}

// writeParams is w as the check_run actions' params, without the fields
// only one of them takes.
func writeParams(w Write) map[string]any {
	p := map[string]any{}
	if w.Status != "" {
		p["status"] = w.Status
	}
	if w.Conclusion != "" {
		p["conclusion"] = w.Conclusion
	}
	if w.ExternalID != "" {
		p["external_id"] = w.ExternalID
	}
	if w.DetailsURL != "" {
		p["details_url"] = w.DetailsURL
	}
	if w.StartedAt != nil {
		p["started_at"] = w.StartedAt.UTC().Format(time.RFC3339)
	}
	if w.CompletedAt != nil {
		p["completed_at"] = w.CompletedAt.UTC().Format(time.RFC3339)
	}
	if w.Output != nil {
		out := w.Output.params()
		if len(w.Output.Annotations) > 0 {
			list := make([]any, len(w.Output.Annotations))
			for i, a := range w.Output.Annotations {
				list[i] = a
			}
			out["annotations"] = list
		}
		p["output"] = out
	}
	return p
}

func intOf(v any) (int64, error) {
	switch n := v.(type) {
	case int64:
		return n, nil
	case int:
		return int64(n), nil
	case float64:
		return int64(n), nil
	case string:
		return strconv.ParseInt(n, 10, 64)
	}
	return 0, fmt.Errorf("GitHub returned no check run id")
}

// fixedCredential is a credential source that answers every request with the
// one credential the writer minted.
type fixedCredential struct{ cred httpaction.Credential }

func (f fixedCredential) Credential(context.Context, httpaction.CredentialRequest) (httpaction.Credential, error) {
	return f.cred, nil
}
