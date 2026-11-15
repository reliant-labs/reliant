package cli

import (
	"context"
	"errors"
	"strings"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/obsbackend"
)

// openObsCloud builds the cloud backend for env: the control plane the env
// declares, the caller's credential (flag, env var, login, or the host's
// credential helper; never a prompt), and the org and environment the reads
// are bound to. The backend declares the scope each of its procedures needs
// (obs:read, obs:triage), so the client elevates a narrow login or helper
// credential for it, for the person behind it, before the first read rather
// than after a refusal.
//
// The endpoint and credential env var come from the env's stamped target
// (obs_target.go), so nothing here renders KCL when the declaration is
// unchanged and the first request goes out as soon as the credential resolves.
//
// Every failure here is "could not look" (exit 2): no credential, an
// unreachable control plane, an env the control plane has never seen.
func openObsCloud(ctx context.Context, env string) (obsbackend.Cloud, error) {
	t, err := resolveObsTarget(ctx, env)
	if err != nil {
		return obsbackend.Cloud{}, obsbackend.Undetermined("env %s: cannot read its declaration", env).Cause(err).
			With(obsbackend.Cmd("forge", "env", "list"))
	}
	ep, cred, err := resolveCloudTargetFor(env, t.Declaration(), "")
	if err != nil {
		return obsbackend.Cloud{}, obsbackend.Undetermined("env %s: cannot reach its control plane", env).Cause(err)
	}
	project := obsProjectName()
	if project != "" {
		ep.ProjectName = project
	}
	client := cloud.NewClient(ep, cred)
	obsbackend.DeclareCloudScopes(client)
	org, err := cloud.ResolveOrganization(ctx, client)
	if err != nil {
		return obsbackend.Cloud{}, obsbackend.MapCloudError(err)
	}
	id, err := deploytarget.LookupHostedEnvironment(ctx, client, project, env)
	if errors.Is(err, deploytarget.ErrHostedEnvironmentNotFound) {
		if t.Facts.HasHosted || t.Facts.HasCluster || t.Facts.HasLocal {
			return obsbackend.Cloud{}, obsbackend.Undetermined("env %s does not exist on its control plane yet: nothing has been deployed to it", env).
				With(obsbackend.Cmd("forge", "env", "deploy", env))
		}
		// Nothing forge deploys: a monitoring-only env is registered by
		// `forge obs init`, which also mints the token its app sends with.
		return obsbackend.Cloud{}, obsbackend.Undetermined("env %s does not exist on its control plane yet: register it and mint its ingest token", env).
			With(obsbackend.Cmd("forge", "obs", "init", env))
	}
	if err != nil {
		return obsbackend.Cloud{}, obsbackend.MapCloudError(err)
	}
	return obsbackend.Cloud{Env: env, EnvID: id, OrgID: org, Caller: client}, nil
}

// obsProjectName is the forge project the obs verbs read: `forge obs
// --project`, else forge.yaml `name`.
func obsProjectName() string {
	if p := strings.TrimSpace(obsProjectFlag); p != "" {
		return p
	}
	return hostedProjectName()
}
