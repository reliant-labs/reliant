package controlplane

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	deployv1 "github.com/reliant-labs/reliant/gen/controlplane/services/deploy/v1"
)

// ErrHostedAppsDenied is ListHostedApps refused: the caller may not read the
// org's apps (no deploy:read in it), or the control plane did not accept
// their bearer.
var ErrHostedAppsDenied = errors.New("controlplane: the caller may not read the org's apps")

// HostedApp is one app (a hosted project) of an org, as the control plane
// records it.
type HostedApp struct {
	// Name is forge.yaml `name`: the app's identity in its org.
	Name      string
	CreatedAt time.Time
	// LastDeployedAt is the newest promotion to a live environment; nil:
	// never deployed (or every environment it reached was deleted).
	LastDeployedAt *time.Time
	// Environments are its live environments' names, persistent first.
	Environments  []string
	HasStaticSite bool
	// Sources are where it was cut and deployed from, newest first.
	Sources []HostedAppSource
	// Warning is the two-repositories warning; "" otherwise.
	Warning string
}

// HostedAppSource is one checkout an app was cut or deployed from. It never
// carries the reliant project id the source was reported with.
type HostedAppSource struct {
	// Remote is forge's canonical "host/owner/repo"; "" for a checkout with
	// no shareable remote.
	Remote string
	// Subpath is forge.yaml's directory in the repository, "/"-separated;
	// "" is the root.
	Subpath    string
	Commit     string
	LastSeenAt time.Time
}

// ListHostedApps implements Service; see its contract.
func (c *client) ListHostedApps(ctx context.Context, jwt, orgID string) ([]HostedApp, error) {
	msg := &deployv1.ListDeployAppsRequest{}
	if orgID = strings.TrimSpace(orgID); orgID != "" {
		msg.OrgId = &orgID
	}
	req := connect.NewRequest(msg)
	attachAuthorization(req, "Bearer "+strings.TrimSpace(jwt))
	resp, err := c.deploy.ListApps(ctx, req)
	if err != nil {
		switch connect.CodeOf(err) {
		case connect.CodePermissionDenied, connect.CodeUnauthenticated:
			return nil, fmt.Errorf("%w: %w", ErrHostedAppsDenied, err)
		}
		return nil, err
	}
	out := make([]HostedApp, 0, len(resp.Msg.GetApps()))
	for _, a := range resp.Msg.GetApps() {
		app := HostedApp{
			Name:          a.GetName(),
			Environments:  a.GetEnvironments(),
			HasStaticSite: a.GetHasStaticSite(),
			Warning:       a.GetCodeSources().GetWarning(),
		}
		if a.GetCreatedAt() != nil {
			app.CreatedAt = a.GetCreatedAt().AsTime()
		}
		if a.GetLastDeployedAt() != nil {
			at := a.GetLastDeployedAt().AsTime()
			app.LastDeployedAt = &at
		}
		for _, s := range a.GetCodeSources().GetSources() {
			src := HostedAppSource{
				Remote:  s.GetSource().GetRemote(),
				Subpath: s.GetSource().GetSubpath(),
				Commit:  s.GetSource().GetCommit(),
			}
			if s.GetLastSeenAt() != nil {
				src.LastSeenAt = s.GetLastSeenAt().AsTime()
			}
			app.Sources = append(app.Sources, src)
		}
		out = append(out, app)
	}
	return out, nil
}
