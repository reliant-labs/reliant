package cli

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// hostedEnvResolver maps a forge environment NAME to the control plane's id
// for the environment of that name, in the caller's organization and this
// project.
//
// The id is what every per-environment hosted RPC is keyed on (secrets are
// stored per environment id on the control plane), and the NAME is what the
// user types. Keeping this a one-method interface, declared here at its first
// consumer, is deliberate: the hosted deploy target resolves the same thing,
// and whichever lands second shares or replaces this rather than growing a
// second resolver with its own opinion of "which environment".
//
// This file holds the whole default implementation so it can be lifted as
// one unit.
type hostedEnvResolver interface {
	ResolveEnvironmentID(ctx context.Context, envName string) (string, error)
}

// cloudEnvResolver resolves through deploytarget.LookupHostedEnvironment —
// the ONE exact-name lookup, shared with the hosted deploy provider so the two
// cannot disagree about "which environment". project scopes the lookup: env
// identity on the control plane is (org, project, name), so two projects'
// `prod` envs never collide.
type cloudEnvResolver struct {
	client  cloudCaller
	project string
}

// cloudEnvironment is the subset of controlplane.v1.DeployEnvironment forge
// reads. Declared locally because forge does not vendor
// the control plane's protos.
type cloudEnvironment struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Project string `json:"project,omitempty"`
}

type cloudEnvironmentsResponse struct {
	Environments []cloudEnvironment `json:"environments"`
}

// errHostedEnvNotFound is the resolver's "no environment of that name"
// answer, distinguishable with errors.Is. The release ledger reads it as
// "never promoted" (an env that does not exist yet has run nothing); every
// write path keeps it an error.
var errHostedEnvNotFound = deploytarget.ErrHostedEnvironmentNotFound

func (r cloudEnvResolver) ResolveEnvironmentID(ctx context.Context, envName string) (string, error) {
	return deploytarget.LookupHostedEnvironment(ctx, r.client, r.project, envName)
}

// ensureHostedEnv makes the control-plane environment of this NAME (in this
// project, of this kind) exist and returns its id. THE rule for hosted
// writes: every MUTATING hosted command (promote, secret set/unset,
// deploy) ensures the env first, because the env is DECLARED in this
// project's KCL — whichever of them runs first on a fresh env creates it, and
// none of them can deadlock on another having run. Reads (topology, status,
// secret list, releases, env up's secret pull) never call this: they report
// an env the control plane has not seen as `environment_id: ""`.
//
// Idempotent server-side (EnsureEnvironment). The kind is IMMUTABLE there: an
// ensure whose kind disagrees with the stored row is refused rather than
// silently changing it, so a persistent env can never be turned into a local
// (pullable) one by editing KCL.
func ensureHostedEnv(ctx context.Context, client cloudCaller, ref deploytarget.HostedEnvRef) (string, error) {
	id, _, err := deploytarget.EnsureHostedEnvironment(ctx, client, ref)
	return id, err
}

// declareHostedEnv is the ensure that F-DECL's declaration step makes: the
// env of this name exists on the control plane, in this project, of this
// kind.
//
// IT LEARNS NOTHING FROM THE RESPONSE. This used to also read the org's
// image push base off the ensured environment and write it to .forge/state,
// so the offline consumers could read it back. That whole mechanism is gone:
// the push base is composed from the env's own declaration
// (platformPushBase), which needs no call, cannot go stale, and lets a fresh
// checkout render before it has ever authenticated.
func declareHostedEnv(ctx context.Context, client cloudCaller, ref deploytarget.HostedEnvRef) error {
	_, err := ensureHostedEnv(ctx, client, ref)
	return err
}

// hostedEnvRefFor is the ONE derivation of a control-plane environment's
// address from its rendered KCL: the project name, the env name, and the
// kind (hostedEnvKindOf).
func hostedEnvRefFor(envName string, e *KCLEntities) deploytarget.HostedEnvRef {
	return deploytarget.HostedEnvRef{Project: hostedProjectName(), Name: envName, Kind: hostedEnvKindOf(e)}
}

// hostedProjectName is the forge project name (forge.yaml `name`) — the
// project dimension of a control-plane environment's identity. A seam so
// tests without a forge.yaml on disk state it.
var hostedProjectName = func() string {
	cfg, err := loadProjectConfig()
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.Name
}

// hostedEnvKindOf derives the control-plane environment kind of an env that
// declares control_plane. Pure.
//
//   - anything hosted (a workload bound to OnHosted, a hosted
//     ManagedDatabase, an OnHosted frontend) → PERSISTENT: the platform
//     runs it, and its secrets are write-only.
//   - nothing hosted, something on a cluster the author operates (a
//     workload bound to OnCluster, or a cluster ManagedDatabase) →
//     SELF_MANAGED: the control plane keeps its ledger, forge applies it,
//     and its secrets are write-only.
//   - otherwise → LOCAL: every workload runs on a developer machine, and
//     the control plane is only its secret store (pullable).
//
// The SELF_MANAGED split is what keeps "runs on my laptop" and "runs on my
// own cluster" apart. Before it, a cluster env with a control plane was
// classified LOCAL, which made its production secrets readable back through
// the local-secret pull.
//
// An env-wide cluster_target alone does not make an env self-managed: it
// carries support objects (a Namespace, gateways) for workloads that still
// run on the developer's machine.
//
// "" when the env declares no control plane. The kind is a PREDICATE over
// the env's items, never a mode an env selects.
func hostedEnvKindOf(e *KCLEntities) deploytarget.HostedEnvKind {
	if e == nil || e.ControlPlane == nil {
		return ""
	}
	if e.HasHosted() {
		return deploytarget.HostedEnvPersistent
	}
	if runsOnOwnCluster(e) {
		return deploytarget.HostedEnvSelfManaged
	}
	return deploytarget.HostedEnvLocal
}

// runsOnOwnCluster reports whether anything in the env runs on a cluster the
// author operates: a cluster-bound workload or a cluster ManagedDatabase.
func runsOnOwnCluster(e *KCLEntities) bool {
	if len(e.WorkloadsOn(RuntimeCluster)) > 0 {
		return true
	}
	for _, d := range e.Databases {
		if !d.Hosted() {
			return true
		}
	}
	return false
}

// isLocalControlPlaneEnv reports whether the env declares a control plane that
// is ONLY its secret store (kind LOCAL): nothing in it is hosted.
func isLocalControlPlaneEnv(e *KCLEntities) bool {
	return hostedEnvKindOf(e) == deploytarget.HostedEnvLocal
}

// hostedControlPlaneKindName is the lower-case vocabulary the JSON reports
// use for a kind: "local" | "persistent" | "self_managed" | "" — the same
// words as pkg/release.EnvKind.
func hostedControlPlaneKindName(k deploytarget.HostedEnvKind) string {
	switch k {
	case deploytarget.HostedEnvLocal:
		return string(release.EnvLocal)
	case deploytarget.HostedEnvPersistent:
		return string(release.EnvPersistent)
	case deploytarget.HostedEnvSelfManaged:
		return string(release.EnvSelfManaged)
	default:
		return ""
	}
}

// refuseLocalEnvDeploy is `forge env deploy`'s refusal for a LOCAL env whose
// workloads all run on this machine: nothing is hosted and nothing is applied
// to a cluster, so there is nothing to deploy (the control plane refuses a
// publish too, but this fires before any RPC).
func refuseLocalEnvDeploy(envName string) error {
	return fmt.Errorf("env %q is LOCAL: it declares control_plane but binds nothing to it (no forge.OnHosted workload, hosted database or OnHosted frontend), "+
		"so the control plane is only its secret store, and its workloads run on this machine.\n"+
		"There is nothing for `forge env deploy` to publish or apply.\n"+
		"fix: run it with `forge env up %s`, or bind a workload to forge.OnHosted / forge.OnCluster", envName, envName)
}
