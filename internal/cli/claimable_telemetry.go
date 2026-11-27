package cli

// What a claimable deploy tells Reliant about itself, beyond the deploy:
//
//   - its SHAPE (claimableShapeOf): the kind of app and how it signs users in,
//     named to the control plane in X-Forge-Client (cloud.WithDeployShape)
//     so the claimable funnel can be broken down by template;
//   - a REFUSAL forge raises itself (trackClaimableRefused): one
//     forge_claimable_refused Event, because the control plane never hears of
//     a deploy forge turned down before asking it for anything.
//
// Both carry fixed vocabularies only: never a path, a project, workload or
// env name, a message, or anything from the user's files.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"runtime"
	"strings"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/observe/cliev"
)

// claimableShapeOf is the shape a claimable deploy declares, from the env's
// render. Only what the deploy publishes counts (hosted workloads and
// frontends):
//
//   - forge-native: a workload forge builds from Go (forge.GoBuild), with or
//     without a frontend;
//   - dockerfile-app: else a workload built from a Dockerfile (forge.DockerBuild)
//     or a prebuilt image (no build), as an adopted `--kind app` declares;
//   - other: else any workload (a shell or remote build);
//   - nextjs-static / vite: no workload, and every hosted frontend is a
//     Next.js export, or every one a Vite build;
//   - other: anything else (an Expo app, a Next.js and a Vite site together,
//     nothing hosted).
//
// auth is managed when the env declares a forge.ManagedIdentity, else none.
func claimableShapeOf(e *KCLEntities) cloud.DeployShape {
	shape := cloud.DeployShape{Template: cloud.TemplateOther, Auth: cloud.AuthNone}
	if e == nil {
		return shape
	}
	if len(e.AllIdentities()) > 0 {
		shape.Auth = cloud.AuthManaged
	}
	var goBuilt, imageBuilt, otherBuilt bool
	for _, w := range e.Workloads {
		if !w.OnRuntime(RuntimeHosted) {
			continue
		}
		switch w.Build.Type {
		case "go":
			goBuilt = true
		case "docker", "":
			imageBuilt = true
		default:
			otherBuilt = true
		}
	}
	switch {
	case goBuilt:
		shape.Template = cloud.TemplateForgeNative
		return shape
	case imageBuilt:
		shape.Template = cloud.TemplateDockerfileApp
		return shape
	case otherBuilt:
		return shape
	}
	var nextjs, vite, other int
	for _, fe := range e.Frontends {
		if !frontendIsHosted(fe) {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(fe.Type)) {
		case "nextjs":
			nextjs++
		case "vite", "vite-spa":
			vite++
		default:
			other++
		}
	}
	switch {
	case other > 0:
	case nextjs > 0 && vite == 0:
		shape.Template = cloud.TemplateNextjsStatic
	case vite > 0 && nextjs == 0:
		shape.Template = cloud.TemplateVite
	}
	return shape
}

// claimableRefusedEvent is the Event a refused `forge env deploy --claimable`
// sends.
const claimableRefusedEvent = "forge_claimable_refused"

// The forge_claimable_refused reason vocabulary. lint and commit_hook belong
// to it too (the control plane's dashboards know all of them), but forge
// cannot yet tell those failures from the rest of a build, so it sends build.
const (
	refusedSignedIn           = "claimable_signed_in"
	refusedIneligible         = "claimable_ineligible"
	refusedUnmergedMigrations = "unmerged_migrations"
	refusedInvalidFlags       = "invalid_flags"
	refusedUnavailable        = "unavailable"
	refusedBuild              = "build"
	refusedOther              = "other"
)

// claimableRefusedReason classifies a refusal: by the reason a
// claimableRefusal carries, else by the step that raised it (stage, one of
// the reasons above or "").
func claimableRefusedReason(err error, stage string) string {
	var r *claimableRefusal
	if errors.As(err, &r) {
		switch r.reason {
		case reasonClaimableSignedIn:
			return refusedSignedIn
		case reasonClaimableIneligible:
			return refusedIneligible
		case cloud.ReasonClaimableUnavailable:
			return refusedUnavailable
		}
	}
	if stage != "" {
		return stage
	}
	return refusedOther
}

// claimableRefusedAttrs are the Event's props: the reason, the release and
// platform, the driving agent, and the deploy's shape ("unknown" before the
// env rendered).
func claimableRefusedAttrs(reason string, shape cloud.DeployShape, getenv func(string) string) []slog.Attr {
	harness, _ := buildinfo.DetectAgent(getenv)
	if harness == "" {
		harness = cloud.HarnessNone
	}
	template, auth := shape.Template, shape.Auth
	if template == "" {
		template, auth = "unknown", "unknown"
	}
	return []slog.Attr{
		slog.String("reason", reason),
		slog.String("version", buildinfo.Version()),
		slog.String("os", runtime.GOOS),
		slog.String("arch", runtime.GOARCH),
		slog.String("harness", harness),
		slog.String("template", template),
		slog.String("auth", auth),
	}
}

// trackClaimableRefused records the forge_claimable_refused Event for the
// batch Execute flushes as the process exits, the failed exit included. It is
// a no-op wherever forge's telemetry is off: DO_NOT_TRACK, FORGE_TELEMETRY=off,
// `forge telemetry off`, a CI runner, a development build, and an embedded
// forge (`reliant forge`, which never starts a client).
func trackClaimableRefused(reason string, shape cloud.DeployShape) {
	trackEvent(context.Background(), claimableRefusedEvent, claimableRefusedAttrs(reason, shape, os.Getenv)...)
}

// trackEvent records one of forge's Events. A var so the tests observe what
// is tracked without a telemetry endpoint.
var trackEvent = cliev.Track
