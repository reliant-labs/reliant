package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/deploytarget"
)

// The console contract for WHERE an environment runs.
//
// `forge env status --json` carries these as fields on its env-level object,
// in both its one-env and its all-environments form. They are ADDITIVE: no
// existing field changes meaning, and a consumer that ignores them sees the
// old document.
//
//   - destination     hosted | cluster | compose | host | static | mixed
//     Always set for an env declared in this checkout. Derived from the
//     env's own render, never from machine state.
//   - control_plane_kind  local | persistent — set for every env that
//     declares control_plane. persistent ⇔ something in the env is hosted
//     (destination "hosted", or "mixed" with a hosted vote); a LOCAL env keeps
//     its secrets on the control plane and runs nothing on it.
//   - endpoint        any env declaring control_plane: the control plane's
//     normalized base URL.
//   - environment_id  hosted only: the control plane's id for the env. EMPTY
//     when the env has never been ensured (never deployed) or the control
//     plane could not be read — never fabricated.
//   - verdict         hosted only: the environment-level deploystate verdict
//     (unknown | converging | converged | diverged | degraded).
//   - workloads[]     hosted only: one deploytarget.HostedWorkloadStatus per
//     deployment — name, tier, url, hostname, verdict, verdict_reason,
//     observed_state, observed_digest, desired_digest, drifted, last_error.

// The destination vocabulary.
const (
	destinationHosted  = "hosted"
	destinationCluster = "cluster"
	destinationCompose = "compose"
	destinationHost    = "host"
	destinationStatic  = "static"
	destinationMixed   = "mixed"
)

// envDestination is where one env runs, as the contract above reports it.
type envDestination struct {
	Destination string
	// ControlPlaneKind is "local" | "persistent" for an env that declares
	// control_plane, "" otherwise.
	ControlPlaneKind string
	Endpoint         string
	EnvironmentID    string
	Verdict          string
	Workloads        []deploytarget.HostedWorkloadStatus
	// Note explains a hosted read that failed; the other fields stay
	// honest (empty) rather than guessed.
	Note string
}

// destinationOf classifies a rendered env. Pure.
//
// Every deployable thing votes for where it runs: each workload by its own
// runtime (hosted, cluster, compose, host), each database by its runtime,
// host infra for "host", each frontend by its runtime (OnHosted is hosted,
// OnBucket and OnFirebase are "static"; a dev server or a build-only
// frontend deploys nowhere). One
// kind is that kind, several are "mixed". Hosted is one vote among them, not
// an env mode: an env whose every workload is hosted is "hosted", and an env
// that runs one workload on the platform and another on a cluster is "mixed".
// An env that declares nothing deployable runs nothing anywhere but the local
// machine, which is "host".
func destinationOf(e *KCLEntities) string {
	if e == nil {
		return ""
	}
	kinds := destinationKindSet(e)
	switch len(kinds) {
	case 0:
		return destinationHost
	case 1:
		for k := range kinds {
			return k
		}
	}
	return destinationMixed
}

// destinationKindSet is every destination kind the env deploys to.
func destinationKindSet(e *KCLEntities) map[string]bool {
	kinds := map[string]bool{}
	for _, w := range e.Workloads {
		switch w.Runtime.Type {
		case RuntimeHosted:
			kinds[destinationHosted] = true
		case RuntimeCluster:
			kinds[destinationCluster] = true
		case RuntimeCompose:
			kinds[destinationCompose] = true
		case RuntimeHost:
			kinds[destinationHost] = true
		}
	}
	if len(e.Infra) > 0 {
		kinds[destinationHost] = true
	}
	for _, d := range e.Databases {
		if d.Hosted() {
			kinds[destinationHosted] = true
		} else {
			kinds[destinationCluster] = true
		}
	}
	for _, f := range e.Frontends {
		switch {
		case frontendIsHosted(f):
			kinds[destinationHosted] = true
		case f.Runtime.Ships():
			kinds[destinationStatic] = true
		}
	}
	return kinds
}

// hostedStatusReader reads a hosted env's status. A seam so topology/status
// tests state the control plane's answer.
type hostedStatusReader func(ctx context.Context, envName string, e *KCLEntities) (deploytarget.HostedEnvStatus, error)

// readHostedStatusFromDeclaration builds a client from the env's own
// declaration and credential precedence, then reads its status.
func readHostedStatusFromDeclaration(ctx context.Context, envName string, e *KCLEntities) (deploytarget.HostedEnvStatus, error) {
	ep, err := cloud.ResolveEndpoint(envName, declarationFromEntities(e))
	if err != nil {
		return deploytarget.HostedEnvStatus{}, err
	}
	cred, err := cloud.ResolveCredential("", ep)
	if err != nil {
		return deploytarget.HostedEnvStatus{}, err
	}
	return deploytarget.ReadHostedStatus(ctx, hostedDeployClient(ep, cred), hostedProjectName(), envName)
}

// resolveEnvDestination fills the contract for one rendered env. read may be
// nil, in which case a hosted env reports destination and endpoint only.
func resolveEnvDestination(ctx context.Context, envName string, e *KCLEntities, read hostedStatusReader) envDestination {
	out := envDestination{Destination: destinationOf(e), ControlPlaneKind: hostedControlPlaneKindName(hostedEnvKindOf(e))}
	if out.ControlPlaneKind != "" {
		if decl := declarationFromEntities(e); decl != nil {
			if ep, err := cloud.ResolveEndpoint(envName, decl); err == nil {
				out.Endpoint = ep.URL
			}
		}
	}
	if out.Destination != destinationHosted {
		return out
	}
	if read == nil {
		return out
	}
	st, err := read(ctx, envName, e)
	switch {
	case errors.Is(err, deploytarget.ErrHostedEnvironmentNotFound):
		out.Note = "the control plane has no environment of this name yet — never deployed"
		return out
	case err != nil:
		// The id may still have resolved (a status read that failed after
		// the lookup); report what is KNOWN.
		out.EnvironmentID = st.EnvironmentID
		out.Note = "could not read the control plane's status: " + err.Error()
		return out
	}
	out.EnvironmentID = st.EnvironmentID
	out.Verdict = st.Verdict
	out.Workloads = st.Workloads
	sort.Slice(out.Workloads, func(i, j int) bool { return out.Workloads[i].Name < out.Workloads[j].Name })
	return out
}

// writeHostedStatus prints where a hosted env's workloads answer: the
// platform URL of each workload that has one, then the custom-domain block —
// per workload, each declared hostname's state, the DNS records still
// required, and the last error. Silent for an env that is not hosted or whose
// control plane could not be read — the JSON path carries the read failure
// in `hosted_note`, and repeating it here would put a network error in the
// middle of the report.
//
// The URLs are here because the platform allocates them: the author never
// wrote the hostname down, so without this block the only way to find a
// hosted site is to read the platform's own objects. The domains are here
// because a custom domain is the one piece of hosted state whose next action
// belongs to the AUTHOR, not to forge and not to the platform: until they
// set the record at their registrar, nothing converges.
func writeHostedStatus(ctx context.Context, w io.Writer, envName string, e *KCLEntities, read hostedStatusReader) {
	if e == nil || read == nil || destinationOf(e) != destinationHosted {
		return
	}
	st, err := read(ctx, envName, e)
	if err != nil {
		return
	}
	if urls := deploytarget.FormatHostedURLs("  ", st.Workloads); urls != "" {
		fmt.Fprintf(w, "\nhosted (%s):\n%s", envName, urls)
	}
	var printed bool
	for _, ws := range st.Workloads {
		if len(ws.Domains) == 0 {
			continue
		}
		if !printed {
			fmt.Fprintf(w, "\ncustom domains (%s):\n", envName)
			printed = true
		}
		fmt.Fprintf(w, "  %s:\n", ws.Name)
		fmt.Fprint(w, deploytarget.FormatCustomDomains("    ", ws.Domains))
	}
}
