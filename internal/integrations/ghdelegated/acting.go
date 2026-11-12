package ghdelegated

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/provenance"
)

// ActingCredential implements Service.
func (b *broker) ActingCredential(ctx context.Context, ownerUserID string, req httpaction.CredentialRequest) (httpaction.Credential, error) {
	if req.ActingIdentity != httpaction.IdentityApp {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeInvalidArgument,
			Message: fmt.Sprintf("unknown acting identity %q: the GitHub broker has the owner and the App's bot", req.ActingIdentity)}
	}
	return b.mintBot(ctx, ownerUserID, "", BotConnectionID, req)
}

// OwnsConnectionID implements Service.
func (b *broker) OwnsConnectionID(id string) bool {
	_, ok := ParseOrgAppConnectionID(id)
	return ok
}

// OrgActingCredential implements Service.
func (b *broker) OrgActingCredential(ctx context.Context, ownerUserID, orgID string, req httpaction.CredentialRequest) (httpaction.Credential, error) {
	if _, ok := ParseOrgAppConnectionID(req.ConnectionID); !ok {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeInvalidArgument,
			Message: fmt.Sprintf("%q does not name an organization's GitHub App installation", req.ConnectionID)}
	}
	if orgID == "" {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition,
			Message: "an organization's GitHub connection is only usable in a run of that organization"}
	}
	return b.mintBot(ctx, ownerUserID, orgID, req.ConnectionID, req)
}

// mintBot mints the App's installation token for the one repository req
// names, through the org's linked installation when orgID is set, and
// reports connID as the credential that spoke.
func (b *broker) mintBot(ctx context.Context, ownerUserID, orgID, connID string, req httpaction.CredentialRequest) (httpaction.Credential, error) {
	if ownerUserID == "" {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: "the run has no owner to act for"}
	}
	if b.installations == nil {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition,
			Message: "this automation writes as the Reliant bot, but this deployment has no GitHub App to act as: set the automation to write as its owner"}
	}
	owner, repo, err := targetRepo(req.Params)
	if err != nil {
		return nil, err
	}
	// Only the permissions the action needs, not the default three: a comment
	// does not need contents.
	perms := permissionsFor(req.ActionID)
	tok, err := b.installations.MintInstallationToken(ctx, gitcredentialclient.InstallationRequest{
		ExternalUserID: ownerUserID, Owner: owner, Repo: repo, Permissions: perms, OrgID: orgID,
	})
	switch {
	case err == nil:
	case errors.Is(err, gitcredentialclient.ErrAppNotLinked):
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition,
			Message: fmt.Sprintf("the GitHub App installation that covers %s/%s is not linked to this organization: an organization owner or admin links it in the organization's settings", owner, repo), Err: err}
	case errors.Is(err, gitcredentialclient.ErrNotOrgMember):
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition,
			Message: "the run's owner is not a member of the organization whose GitHub connection this step names", Err: err}
	case errors.Is(err, gitcredentialclient.ErrAppNotConfigured):
		// The control plane holds no App key (a dev or e2e stack, or a deploy
		// before the key is seeded). The write fails; it is never made as the
		// owner instead.
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition,
			Message: "this automation writes as the Reliant bot, but the control plane has no GitHub App key to mint its token: an administrator must configure the App, or set the automation to write as its owner", Err: err}
	case errors.Is(err, gitcredentialclient.ErrOwnerCannotPush):
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition,
			Message: fmt.Sprintf("this automation acts as the Reliant GitHub App, which writes only where its owner can push, and the owner cannot push to %s/%s", owner, repo), Err: err}
	case errors.Is(err, gitcredentialclient.ErrAppNotInstalled):
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition,
			Message: fmt.Sprintf("the Reliant GitHub App is not installed on %s/%s: install it there, or set the automation to act as its owner", owner, repo), Err: err}
	case errors.Is(err, gitcredentialclient.ErrAppPermissions):
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition,
			Message: "the Reliant GitHub App has not been granted the permissions this action needs: its owner must accept the updated permissions in the App's installation settings", Err: err}
	case errors.Is(err, gitcredentialclient.ErrNotConnected):
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition,
			Message: "GitHub is not connected: the App's bot only writes where its owner can push, which Reliant checks with the owner's GitHub connection. Connect GitHub in Settings", Err: err}
	case errors.Is(err, gitcredentialclient.ErrNeedsReconnect):
		return nil, &httpaction.CredentialError{Code: httpaction.CodeNeedsReauth,
			Message: "GitHub needs to be reconnected: reconnect GitHub in Settings", Err: err}
	default:
		b.logger.WarnContext(ctx, "minting a GitHub App installation token failed", "err", err)
		return nil, &httpaction.CredentialError{Code: httpaction.CodeUnavailable,
			Message: "could not get a GitHub App token right now; try again", Err: err}
	}
	if tok.Token.Empty() {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeInternal, Message: "control-plane returned no installation token"}
	}
	// A step that names an org's installation writes through that one. The
	// control plane mints through whichever installation covers the
	// repository, so one the org linked but the step did not name is
	// refused, as is a control plane that does not say (it predates org
	// installations and so never checked the link either).
	if want, ok := ParseOrgAppConnectionID(connID); ok && tok.InstallationID != want {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition,
			Message: fmt.Sprintf("%s/%s is not covered by the GitHub App installation this step names (%s): name the organization's installation that covers it", owner, repo, connID)}
	}
	return &botCredential{
		credential: credential{token: tok.Token, host: b.host},
		connID:     connID,
		owner:      tok.ActingForLogin,
		bot:        tok.BotLogin,
		automation: req.Automation,
	}, nil
}

// targetRepo is the one repository a call touches. A GitHub action names it in
// owner and repo (the manifest's patterns keep both free of slashes and dots),
// and a call that names neither has no repository to scope a bot token to, so it
// is refused: never widened to "every repository the App can reach".
func targetRepo(params map[string]any) (owner, repo string, err error) {
	owner, _ = params["owner"].(string)
	repo, _ = params["repo"].(string)
	owner, repo = strings.TrimSpace(owner), strings.TrimSpace(repo)
	if owner == "" || repo == "" {
		return "", "", &httpaction.CredentialError{Code: httpaction.CodeInvalidArgument,
			Message: "acting as the GitHub App needs the repository the action writes to (owner and repo)"}
	}
	return owner, repo, nil
}

// actionPermissions is the least a bot token needs per action: the area it
// changes, at write, and nothing else. A review changes pull requests;
// pr.create needs contents too, because opening a pull request from a branch
// reads it. An action outside the table gets control-plane's default (issues,
// pull_requests and contents at write), which is the cap either way.
//
// The issue.* writes that take an issue_number also need pull_requests: the
// number may name a pull request (GitHub's issues API serves both), and an App
// token with issues alone is refused ("Resource not accessible by integration")
// when it labels or comments on a pull request. issue.create opens an issue,
// never a pull request, so issues alone is its least.
var actionPermissions = map[string]map[string]string{
	"issue.create":            {"issues": "write"},
	"issue.update":            issueOrPull,
	"issue.comment":           issueOrPull,
	"issue.comment.update":    issueOrPull,
	"issue.labels.add":        issueOrPull,
	"issue.labels.remove":     issueOrPull,
	"pr.review.create":        {"pull_requests": "write"},
	"pr.review_comment.reply": {"pull_requests": "write"},
	"pr.update":               {"pull_requests": "write"},
	"pr.create":               {"pull_requests": "write", "contents": "read"},
	// Auto-merge, the merge queue and a merge write the base branch.
	"pr.merge": {"pull_requests": "write", "contents": "write"},
	// A check run is checks alone; GitHub lets only an App write one.
	"check_run.create": {"checks": "write"},
	"check_run.update": {"checks": "write"},
	// git_publish's push (writeledger.GitPushAction): a branch is contents.
	"git.push": {"contents": "write"},
}

// issueOrPull is the least for a write to an issue that may be a pull request.
var issueOrPull = map[string]string{"issues": "write", "pull_requests": "write"}

// permissionsFor looks an action up in actionPermissions.
func permissionsFor(actionID string) map[string]string { return actionPermissions[actionID] }

// botCredential is the App's installation token for one repository. It pins and
// scrubs exactly as the owner's does, and in addition signs what it authors.
type botCredential struct {
	credential
	// connID is the identity that spoke: BotConnectionID, or the org's
	// installation (OrgAppConnectionID).
	connID     string
	owner      string
	bot        string
	automation string
}

var _ httpaction.Attributing = (*botCredential)(nil)

// ConnectionID reports the App identity, so a result and an audit say which
// credential spoke.
func (c *botCredential) ConnectionID() string {
	if c.connID == "" {
		return BotConnectionID
	}
	return c.connID
}

func (c *botCredential) String() string {
	return "ghdelegated.botCredential{" + c.host + "}"
}
func (c *botCredential) GoString() string           { return c.String() }
func (c *botCredential) LogValue() slog.Value       { return slog.StringValue(c.String()) }
func (c *botCredential) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, c.String()) }

// authored lists, per action, the body fields that hold text a person reads and
// whether a missing field is created: a pull request opened with no
// description is still the bot's, an update that omits `body` leaves it alone.
var authored = map[string]httpaction.Attribution{
	"issue.create":            {Fields: []string{"body"}, Ensure: true},
	"issue.update":            {Fields: []string{"body"}},
	"issue.comment":           {Fields: []string{"body"}},
	"issue.comment.update":    {Fields: []string{"body"}},
	"pr.review.create":        {Fields: []string{"body"}, Ensure: false},
	"pr.review_comment.reply": {Fields: []string{"body"}},
	"pr.create":               {Fields: []string{"body"}, Ensure: true},
	"pr.update":               {Fields: []string{"body"}},
}

// Attribution implements httpaction.Attributing.
func (c *botCredential) Attribution(integrationID, actionID string) *httpaction.Attribution {
	if integrationID != IntegrationID {
		return nil
	}
	a, ok := authored[actionID]
	if !ok {
		return nil
	}
	a.Footer = Footer(c.automation, c.owner)
	return &a
}

// Footer is the signature on what the bot writes, so a reader can tell whose
// automation spoke. The owner's login is GitHub's (never free text from the
// run), and the automation's name is the trigger's.
func Footer(automation, ownerLogin string) string {
	name := strings.TrimSpace(automation)
	if name == "" {
		name = "an automation"
	}
	owner := strings.TrimSpace(ownerLogin)
	if owner == "" {
		return provenance.SignaturePrefix + escapeInline(name) + ".</sub>"
	}
	return provenance.SignaturePrefix + escapeInline(name) + ", an automation of @" + owner + ".</sub>"
}

// escapeInline keeps a trigger name from breaking out of the footer's markup or
// mentioning anyone: it is the owner's text, but it lands in a public comment.
func escapeInline(s string) string {
	r := strings.NewReplacer("<", "&lt;", ">", "&gt;", "@", "@\u200b", "\n", " ", "\r", " ")
	return r.Replace(s)
}

// Apply is the owner credential's: https to the pinned host only.
func (c *botCredential) Apply(req *http.Request) error { return c.credential.Apply(req) }
