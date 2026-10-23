package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/reliant-labs/forge/internal/cloud"
)

// A LOCAL environment declares control_plane + forge.HostedSecrets but no
// hosted tier: its workloads run on this machine (`forge env up`) and the
// control plane is only its secret store. Its values are PULLABLE through
// controlplane.v1.LocalSecretService — a separately-scoped read path the
// control plane confines to local environments, so a persistent env's
// values stay structurally unreadable.
//
// `forge env up` pulls ONCE, right after the render, and arms the result for
// the rest of the process. Every consumer of the env's secret provider
// (host services, one-shot jobs, compose/external infra, frontends, the DSN
// resolvers) then resolves from the same in-memory map. Nothing here writes
// a value to disk or prints one.

const procPullLocalSecrets = "controlplane.v1.LocalSecretService/PullSecrets"

// localSecretValue is one pulled secret. Proto3-JSON field names, declared
// here because forge does not import control-plane.
type localSecretValue struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Version uint32 `json:"version"`
}

// pullLocalSecretsFor is a seam: tests state the control plane's answer.
var pullLocalSecretsFor = pullLocalSecrets

// pullLocalSecrets resolves the env's control plane and credential, resolves
// the env's id by (project, name) through the shared read resolver — never
// ensuring: `env up` is not a write — and pulls its values.
//
// An env the control plane has never seen has, by definition, no values: it
// yields an empty map, and the declared-refs pre-flight then names every
// missing key with the `forge secret set --env` fix (set creates the env).
// Every other failure is returned: starting services without their secrets
// is the outcome this exists to prevent.
func pullLocalSecrets(ctx context.Context, envName string, entities *KCLEntities) (map[string]string, error) {
	ep, err := cloud.ResolveEndpoint(envName, declarationFromEntities(entities))
	if err != nil {
		return nil, err
	}
	cred, err := cloud.ResolveCredential("", ep)
	if err != nil {
		return nil, fmt.Errorf("env %q keeps its secrets on the control plane at %s: %w", envName, ep.URL, err)
	}
	client := hostedDeployClient(ep, cred)
	return pullLocalSecretsWith(ctx, client, envName, hostedProjectName(), ep.URL)
}

// pullLocalSecretsWith is the transport half of pullLocalSecrets, split out
// so tests drive it with a fake caller.
func pullLocalSecretsWith(ctx context.Context, client cloudCaller, envName, project, endpoint string) (map[string]string, error) {
	envID, err := cloudEnvResolver{client: client, project: project}.ResolveEnvironmentID(ctx, envName)
	if errors.Is(err, errHostedEnvNotFound) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pull secrets for LOCAL env %q from %s: %w", envName, endpoint, err)
	}
	var resp struct {
		Secrets []localSecretValue `json:"secrets"`
	}
	if err := client.Call(ctx, procPullLocalSecrets, map[string]any{"environmentId": envID}, &resp); err != nil {
		// cloud.Client errors quote the server's message, never the
		// response body of a successful call — no value reaches this.
		return nil, fmt.Errorf("pull secrets for LOCAL env %q from %s: %w\n"+
			"fix: %s if the credential is missing or expired; the env must be LOCAL on the control plane "+
			"(declared with control_plane + forge.HostedSecrets and no hosted tier)", envName, endpoint, err, cloud.SignInHint())
	}
	out := make(map[string]string, len(resp.Secrets))
	for _, s := range resp.Secrets {
		if s.Name == "" {
			continue
		}
		out[s.Name] = s.Value
	}
	return out, nil
}

// armedPulledSecrets holds a LOCAL env's pulled values for the rest of the
// process. `forge env up` brings up exactly one env per process, so one slot
// suffices; the env name is kept so a render of a DIFFERENT env can never
// resolve these values.
var armedPulledSecrets struct {
	sync.Mutex
	env    string
	values map[string]string
}

func armPulledSecrets(env string, values map[string]string) {
	armedPulledSecrets.Lock()
	defer armedPulledSecrets.Unlock()
	armedPulledSecrets.env, armedPulledSecrets.values = env, values
}

func disarmPulledSecrets() { armPulledSecrets("", nil) }

// pulledSecretsFor returns the armed values when e is a LOCAL env and a pull
// has been armed in this process (only `forge env up` arms one, for the one
// env it brings up), or nil.
func pulledSecretsFor(e *KCLEntities) (map[string]string, bool) {
	armedPulledSecrets.Lock()
	defer armedPulledSecrets.Unlock()
	if armedPulledSecrets.values == nil || !isLocalControlPlaneEnv(e) {
		return nil, false
	}
	return armedPulledSecrets.values, true
}

// armLocalSecretsForUp is `forge env up`'s pull step. A no-op unless the env
// is LOCAL and its provider is HostedSecrets.
func armLocalSecretsForUp(ctx context.Context, envName string, entities *KCLEntities, logf func(string, ...any)) error {
	if !isLocalControlPlaneEnv(entities) || !isHostedSecretEnv(entities) {
		return nil
	}
	values, err := pullLocalSecretsFor(ctx, envName, entities)
	if err != nil {
		return err
	}
	armPulledSecrets(envName, values)
	endpoint := ""
	if decl := declarationFromEntities(entities); decl != nil {
		endpoint = strings.TrimRight(decl.Endpoint, "/")
	}
	// A COUNT, never a name=value pair.
	logf("[up] secrets: pulled %d value(s) for LOCAL env %q from %s (held in memory only)\n", len(values), envName, endpoint)
	return nil
}

// secretStoreLabel names an env's secret store for a pre-flight message:
// the file path, or the control plane that holds a hosted env's values.
func secretStoreLabel(e *KCLEntities) string {
	if e == nil || e.SecretProvider == nil {
		return ""
	}
	if isHostedSecretEnv(e) {
		if decl := declarationFromEntities(e); decl != nil {
			return "control plane " + strings.TrimRight(decl.Endpoint, "/")
		}
		return "control plane"
	}
	return e.SecretProvider.Path
}
