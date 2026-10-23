package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/cloud"
)

// hostedSecretWriter is the narrow surface `forge secret set/list/unset` use
// for an env whose secret_provider is forge.HostedSecrets.
//
// SEPARATE FROM secrets.Provider ON PURPOSE. A Provider RESOLVES values; the
// hosted one never does (its API is write-only and values are materialized
// in-cluster by the control plane). Widening Provider with Set/Delete would
// give every provider a write half it does not have, so the write half is
// declared here, at the one call site that needs it.
//
// List returns NAMES and presence only — the hosted API has no RPC that
// returns a value, and nothing in this file could carry one.
type hostedSecretWriter interface {
	Set(ctx context.Context, name, value string) (version uint32, err error)
	// Create is Set with check-and-set 0: it fails when the name already has
	// a version, which is what `forge secret import` needs so a re-run can
	// never overwrite a value a person has since rotated.
	Create(ctx context.Context, name, value string) (version uint32, err error)
	List(ctx context.Context) ([]hostedSecretSummary, error)
	Delete(ctx context.Context, name string) error
}

// hostedSecretSummary is the value-free list row.
type hostedSecretSummary struct {
	Name           string `json:"name"`
	CurrentVersion uint32 `json:"currentVersion"`
	// CurrentVersionDeleted / Destroyed report whether the CURRENT version
	// is usable. A deleted current version is absent for delivery.
	CurrentVersionDeleted   bool `json:"currentVersionDeleted"`
	CurrentVersionDestroyed bool `json:"currentVersionDestroyed"`
}

func (s hostedSecretSummary) present() bool {
	return s.CurrentVersion > 0 && !s.CurrentVersionDeleted && !s.CurrentVersionDestroyed
}

const (
	procSetSecret    = "controlplane.v1.SecretStoreService/SetSecret"
	procListSecrets  = "controlplane.v1.SecretStoreService/ListSecrets"
	procDeleteSecret = "controlplane.v1.SecretStoreService/DeleteSecret"
)

// cloudSecretWriter speaks SecretStoreService over Connect JSON. Field names
// are proto3 JSON (lowerCamel), declared here rather than generated: forge
// does not import control-plane (see cloud.Client).
type cloudSecretWriter struct {
	client        cloudCaller
	environmentID string
}

func (w cloudSecretWriter) Set(ctx context.Context, name, value string) (uint32, error) {
	return w.set(ctx, name, value, nil)
}

func (w cloudSecretWriter) Create(ctx context.Context, name, value string) (uint32, error) {
	zero := uint32(0)
	return w.set(ctx, name, value, &zero)
}

func (w cloudSecretWriter) set(ctx context.Context, name, value string, cas *uint32) (uint32, error) {
	var resp struct {
		Version uint32 `json:"version"`
	}
	req := map[string]any{"environmentId": w.environmentID, "name": name, "secretValue": value}
	if cas != nil {
		req["cas"] = *cas
	}
	if err := w.client.Call(ctx, procSetSecret, req, &resp); err != nil {
		// cloud.Client errors quote the server's message, which never
		// contains the request body — the value does not reach this error.
		return 0, err
	}
	return resp.Version, nil
}

func (w cloudSecretWriter) List(ctx context.Context) ([]hostedSecretSummary, error) {
	var resp struct {
		Secrets []hostedSecretSummary `json:"secrets"`
	}
	if err := w.client.Call(ctx, procListSecrets, map[string]any{"environmentId": w.environmentID}, &resp); err != nil {
		return nil, err
	}
	sort.Slice(resp.Secrets, func(i, j int) bool { return resp.Secrets[i].Name < resp.Secrets[j].Name })
	return resp.Secrets, nil
}

func (w cloudSecretWriter) Delete(ctx context.Context, name string) error {
	return w.client.Call(ctx, procDeleteSecret, map[string]any{"environmentId": w.environmentID, "name": name}, nil)
}

// hostedSecretTarget is everything a hosted secret command needs, resolved
// once from the env's KCL: the declaration, and a writer bound to the
// control-plane environment of this env's NAME.
//
// newHostedSecretWriter is a seam so tests can bind a writer to an httptest
// control plane without rendering KCL.
//
// ensure is true for the MUTATING commands (set, unset): they create the env
// by name first, so a secret can be set BEFORE the first deploy and a
// managedSecret pod never starts without its value. List is a read and passes
// false: an env the control plane has not seen has no secrets, and listing
// must not create it.
var newHostedSecretWriter = func(ctx context.Context, envName string, entities *KCLEntities, ensure bool) (hostedSecretWriter, cloud.Endpoint, error) {
	ep, err := cloud.ResolveEndpoint(envName, declarationFromEntities(entities))
	if err != nil {
		return nil, cloud.Endpoint{}, err
	}
	cred, err := cloud.ResolveCredential("", ep)
	if err != nil {
		return nil, cloud.Endpoint{}, err
	}
	client := cloud.NewClient(ep, cred)
	ref := hostedEnvRefFor(envName, entities)
	var envID string
	if ensure {
		envID, err = ensureHostedEnv(ctx, client, ref)
	} else {
		envID, err = cloudEnvResolver{client: client, project: ref.Project}.ResolveEnvironmentID(ctx, envName)
	}
	if err != nil {
		// The endpoint is known even when the env is not: list reports
		// where it looked.
		return nil, ep, err
	}
	return cloudSecretWriter{client: client, environmentID: envID}, ep, nil
}

// isHostedSecretEnv reports whether the env's KCL declares forge.HostedSecrets.
func isHostedSecretEnv(entities *KCLEntities) bool {
	return entities != nil && entities.SecretProvider != nil &&
		strings.EqualFold(entities.SecretProvider.Type, "hosted")
}

func runHostedSecretSet(ctx context.Context, envName, key string, entities *KCLEntities, value string, out io.Writer) error {
	w, ep, err := newHostedSecretWriter(ctx, envName, entities, true)
	if err != nil {
		return err
	}
	version, err := w.Set(ctx, key, value)
	if err != nil {
		return err
	}
	// Never echo the value — only that it landed, where, and its version.
	fmt.Fprintf(out, "%s: %s (%d bytes) in hosted env %q at %s\n", key, hostedSetVerb(version), len(value), envName, ep.URL)
	return nil
}

// hostedSetVerb renders a SetSecret outcome from the version the store
// reports: the first version CREATES the secret, every later one ROTATES it
// (a new version is the rotation — there is no separate command). A store
// that reports no version gets the neutral "set".
func hostedSetVerb(version uint32) string {
	switch {
	case version == 1:
		return "created v1"
	case version > 1:
		return fmt.Sprintf("rotated to v%d", version)
	default:
		return "set"
	}
}

func runHostedSecretUnset(ctx context.Context, envName, key string, entities *KCLEntities, out io.Writer) error {
	w, ep, err := newHostedSecretWriter(ctx, envName, entities, true)
	if err != nil {
		return err
	}
	if err := w.Delete(ctx, key); err != nil {
		return err
	}
	fmt.Fprintf(out, "unset %s in hosted env %q at %s (soft-deleted; the version history is kept)\n", key, envName, ep.URL)
	return nil
}

// hostedSecretListStore reads a hosted env's names and current versions —
// value-free by construction (ListSecrets has no value field) — into the
// same presence facts every provider reports, so `forge secret list --json`
// has one shape whatever the provider.
//
// An env the control plane has never seen holds no secrets: every declared
// one is missing, and listing does not create it.
func hostedSecretListStore(ctx context.Context, envName string, entities *KCLEntities) (secretListStore, error) {
	w, ep, err := newHostedSecretWriter(ctx, envName, entities, false)
	if errors.Is(err, errHostedEnvNotFound) {
		return secretListStore{provider: "hosted", location: ep.URL, verifiable: true, present: map[string]bool{}}, nil
	}
	if err != nil {
		return secretListStore{}, err
	}
	stored, err := w.List(ctx)
	if err != nil {
		return secretListStore{}, err
	}
	store := secretListStore{provider: "hosted", location: ep.URL, exists: true, verifiable: true,
		present: map[string]bool{}, versions: map[string]uint32{}}
	for _, s := range stored {
		if s.present() {
			store.present[s.Name] = true
			store.versions[s.Name] = s.CurrentVersion
		}
	}
	return store, nil
}
