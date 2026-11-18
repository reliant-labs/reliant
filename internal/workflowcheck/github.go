// Copyright (c) 2025 Reliant Labs
package workflowcheck

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
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

// CheckRuns implements GitHub. Every sync asks again, before any credential
// is minted, whether the check may still be written as its activation's
// identity:
//
//   - the trigger's owner is still allowed to act for its trigger: the rule a
//     firing applies to its creator (authz.WriteTriggerDenial, through
//     triggers.CreatorLeftOrg), so an owner removed from the trigger's org
//     after the firing opened the check writes nothing more;
//   - the org's integrations policy allows GitHub;
//   - the App can be minted for the owner on this one repository (the
//     broker refuses an owner who can no longer push there, or a repository
//     the App is not installed on).
//
// A refusal is ErrNotAuthorized and final; a lookup that could not be made
// (a membership or policy outage, a mint that failed in transit) is a plain
// error the syncer retries. Neither writes anything: it fails closed.
func (g *github) CheckRuns(ctx context.Context, id Identity, owner, repo string) (CheckRuns, error) {
	if id.ActingIdentity != httpaction.IdentityApp {
		return nil, notAuthorized("a check run is written by the Reliant GitHub App, so the automation must act as the Reliant bot; it acts as its owner", nil)
	}
	if g.broker == nil {
		return nil, notAuthorized("this deployment has no GitHub App to write check runs as", nil)
	}
	if err := ownerMayWrite(ctx, id); err != nil {
		return nil, err
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
		return nil, notAuthorized(fmt.Sprintf("the organization's policy does not allow the %s integration", githubID), nil)
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
		return nil, credentialRefusal(err)
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
		err := fmt.Errorf("%s: GitHub answered %d: %s", uses, res.StatusCode, truncate(res.Content, 1024))
		switch {
		case res.Retryable || res.StatusCode >= 500:
			// A rate limit (429, or a 403 the catalog's error table reads
			// as one) or GitHub failing: retried with backoff.
			return nil, err
		case res.StatusCode == 401 || res.StatusCode == 403:
			// The App lost the repository between the mint and the write.
			return nil, notAuthorized(err.Error(), err)
		case res.StatusCode >= 400:
			// GitHub refused the request itself (422 invalid, 404 or 410
			// gone): sending it again gets the same answer.
			return nil, &rejectedError{status: res.StatusCode, err: err}
		}
		return nil, err
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

// ErrNotAuthorized is a check that may no longer be written as its
// activation's identity: its owner left the trigger's org, the activation
// does not act as the App, the org's policy refuses GitHub, or the App cannot
// be minted for the owner on the repository. It is final: the syncer abandons
// the check rather than retry, and writes nothing more to it.
var ErrNotAuthorized = errors.New("not authorized to write this check")

type notAuthorizedError struct {
	reason string
	cause  error
}

func notAuthorized(reason string, cause error) error {
	return &notAuthorizedError{reason: reason, cause: cause}
}

func (e *notAuthorizedError) Error() string        { return e.reason }
func (e *notAuthorizedError) Unwrap() error        { return e.cause }
func (e *notAuthorizedError) Is(target error) bool { return target == ErrNotAuthorized }

// ErrRejected is a write GitHub refused as such: 422 (validation failed: an
// invalid output or annotation, or a commit the repository does not have) or
// 404/410 (the repository or the check run is gone, or the App cannot see
// it). Sending it again gets the same answer, so it is never retried as is:
// a 422 on a write's details is sent once more without them (the syncer's
// fallback), and anything else rejected abandons the check.
var ErrRejected = errors.New("GitHub rejected the check run write")

type rejectedError struct {
	status int
	err    error
}

func (e *rejectedError) Error() string        { return e.err.Error() }
func (e *rejectedError) Unwrap() error        { return e.err }
func (e *rejectedError) Is(target error) bool { return target == ErrRejected }

// invalidWrite reports whether err is GitHub's 422: the request was well
// addressed but what it said was refused.
func invalidWrite(err error) bool {
	var r *rejectedError
	return errors.As(err, &r) && r.status == 422
}

// ownerMayWrite asks, as a firing does of its trigger's creator, whether the
// owner may still act for the trigger the check came from: a trigger of no
// org is its owner's alone; one of an org needs its owner to be a live
// member. A membership outage fails closed, retried.
func ownerMayWrite(ctx context.Context, id Identity) error {
	trigger := &core.Trigger{UserID: id.UserID}
	if id.OrgID != "" {
		org := id.OrgID
		trigger.OrgID = &org
	}
	switch denial := authz.WriteTriggerDenial(ctx, id.UserID, trigger); {
	case denial == nil:
		return nil
	case errors.Is(denial, authz.ErrMembershipUnavailable):
		return fmt.Errorf("could not check that the automation's owner is still a member of its organization: %w", denial)
	case errors.Is(denial, authz.ErrNotOrgMember):
		return notAuthorized("the automation's owner is no longer a member of its organization", denial)
	default:
		return notAuthorized("the automation's owner may no longer act for it", denial)
	}
}

// credentialRefusal classifies a failed mint: a refusal the owner or an admin
// must fix (no push access, the App not installed, a policy) is
// ErrNotAuthorized; an outage is retried.
func credentialRefusal(err error) error {
	var ce *httpaction.CredentialError
	if errors.As(err, &ce) {
		switch ce.Code {
		case httpaction.CodeFailedPrecondition, httpaction.CodeNeedsReauth, httpaction.CodeInvalidArgument, httpaction.CodeNotFound:
			return notAuthorized(ce.Message, err)
		}
	}
	return err
}

// fixedCredential is a credential source that answers every request with the
// one credential the writer minted.
type fixedCredential struct{ cred httpaction.Credential }

func (f fixedCredential) Credential(context.Context, httpaction.CredentialRequest) (httpaction.Credential, error) {
	return f.cred, nil
}
