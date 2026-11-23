package grpc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/gitremote"
	"github.com/reliant-labs/reliant/internal/logging"
)

// EnsureProjectForAppPath is control-plane's "a deployed app gets a linked
// project in its org" (control-plane's app-project link intents drainer).
const EnsureProjectForAppPath = "/internal/projects/ensure-for-app"

// ensureProjectFieldMax bounds every request field; ids, names, a
// host/owner/repo and a subpath all fit well inside it.
const ensureProjectFieldMax = 512

// EnsureProjectForAppStore finds or creates the org's project for an app.
// store.ProjectAppEnsureExtStore is one.
type EnsureProjectForAppStore interface {
	EnsureProjectForApp(ctx context.Context, in db.EnsureProjectForAppInput) (db.EnsureProjectForAppResult, error)
}

// OrgMembership answers whether a user belongs to an org. *orgs.Resolver is
// one: ok=false is a deployment with no orgs, an error an outage.
type OrgMembership interface {
	IsMember(ctx context.Context, userID, orgID string) (member, ok bool, err error)
}

type ensureProjectForAppRequest struct {
	OrgID   string `json:"org_id"`
	UserID  string `json:"user_id"`
	AppName string `json:"app_name"`
	Remote  string `json:"remote"`
	Subpath string `json:"subpath"`
}

type ensureProjectForAppResponse struct {
	ProjectID string `json:"project_id"`
	Created   bool   `json:"created"`
	// LinkedBy is "existing" (a project in the org already had the remote)
	// or "created".
	LinkedBy string `json:"linked_by"`
}

// NewEnsureProjectForAppHandler is control-plane's internal-service route
// that gives a deployed forge app a project in the org it was deployed into:
// the org's live project that is a checkout of the app's remote, whoever owns
// it, else a new remote-only one owned by the deploying user
// (store.EnsureProjectForApp). The app is recorded on the project's repo
// (repo_forge_apps) either way.
//
// It accepts only the short-lived internal-service JWT (the one
// /internal/hosted-http/redeem accepts), never a user's credential. That
// token names no user, so the user's membership of the org is checked here,
// fail closed: an outage is 503, not a member (or a deployment with no orgs)
// is 403 {"code":"not_member"}, and nothing is read or written outside the
// org.
func NewEnsureProjectForAppHandler(store EnsureProjectForAppStore, members OrgMembership, secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if secret == "" || store == nil || members == nil || !validHostedRedeemBearer(r.Header.Get("Authorization"), secret) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var req ensureProjectForAppRequest
		if err := decoder.Decode(&req); err != nil || decoder.Decode(new(any)) != io.EOF || !validEnsureProjectRequest(req) {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		member, ok, err := members.IsMember(r.Context(), req.UserID, req.OrgID)
		if err != nil {
			logging.WarnContext(r.Context(), "[ensure-project-for-app] membership check failed", "org_id", req.OrgID, "error", err)
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		if !ok || !member {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "not_member"})
			return
		}
		res, err := store.EnsureProjectForApp(r.Context(), db.EnsureProjectForAppInput{
			OrgID: req.OrgID, UserID: req.UserID, AppName: req.AppName, Remote: req.Remote, Subpath: req.Subpath,
		})
		if err != nil {
			logging.WarnContext(r.Context(), "[ensure-project-for-app] ensuring the project failed", "org_id", req.OrgID, "error", err)
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		out := ensureProjectForAppResponse{ProjectID: res.ProjectID, Created: res.Created, LinkedBy: "existing"}
		if res.Created {
			out.LinkedBy = "created"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
}

// validEnsureProjectRequest requires every field but subpath, bounds them,
// and requires remote to be already canonical ("host/owner/repo"), as
// control-plane records a deploy's code source.
func validEnsureProjectRequest(req ensureProjectForAppRequest) bool {
	for _, f := range []string{req.OrgID, req.UserID, req.AppName, req.Remote, req.Subpath} {
		if len(f) > ensureProjectFieldMax || !utf8.ValidString(f) || strings.ContainsAny(f, "\x00\r\n") {
			return false
		}
	}
	// The ids must be present and exactly as sent: no surrounding space.
	for _, id := range []string{req.OrgID, req.UserID} {
		if id == "" || strings.TrimSpace(id) != id {
			return false
		}
	}
	if strings.TrimSpace(req.AppName) == "" {
		return false
	}
	if c := gitremote.Canonical(req.Remote); c == "" || c != req.Remote {
		return false
	}
	sub := gitremote.NormalizeSubpath(req.Subpath)
	return sub != ".." && !strings.HasPrefix(sub, "../") && !strings.Contains(sub, "/../")
}
