package cli

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/devidentity"
	"github.com/reliant-labs/forge/internal/hostinfra"
	"github.com/reliant-labs/forge/internal/secrets"
	"github.com/reliant-labs/forge/pkg/release"
)

// hostIdentity returns the env's managed identity when it runs on the env's
// own host-run IdP (forge.IdentityOnHost), else nil.
func hostIdentity(e *KCLEntities) *release.ShapeIdentity {
	if e == nil || e.Identity == nil || e.Identity.Runtime != release.IdentityRuntimeHost {
		return nil
	}
	return e.Identity
}

// withoutHostIdentityRefs drops the secret references a host-run identity
// FILLS: `forge env up` writes those store keys itself, after the IdP is up,
// so demanding them from the operator beforehand would be asking for values
// that cannot exist yet.
func withoutHostIdentityRefs(e *KCLEntities, refs []secrets.SecretRef) []secrets.SecretRef {
	id := hostIdentity(e)
	if id == nil {
		return refs
	}
	owned := release.IdentitySecretPrefix + id.Name
	out := refs[:0:0]
	for _, r := range refs {
		if r.SecretName != owned {
			out = append(out, r)
		}
	}
	return out
}

// hostIdentityRefs are the identity-backed env vars of the env's host
// workloads: what the render lowered each forge.IdentityRef to.
func hostIdentityRefs(e *KCLEntities, id *release.ShapeIdentity) []devidentity.Ref {
	owned := release.IdentitySecretPrefix + id.Name
	var refs []devidentity.Ref
	for i := range e.Workloads {
		if !e.Workloads[i].OnRuntime(RuntimeHost) {
			continue
		}
		for _, r := range secretRefsForService(&e.Workloads[i]) {
			if r.SecretName == owned {
				refs = append(refs, devidentity.Ref{EnvName: r.EnvName, SecretName: r.SecretName, SecretKey: r.SecretKey})
			}
		}
	}
	return refs
}

// devIdentityInput resolves everything one convergence needs from the
// rendered env. It performs no I/O beyond reading the project's own
// declaration, so it is testable without an IdP.
func devIdentityInput(e *KCLEntities, id *release.ShapeIdentity, projectDir string, prov secrets.Provider) (devidentity.Input, error) {
	var zitadel *HostInfraEntity
	for i := range e.Infra {
		if e.Infra[i].Engine == "zitadel" {
			if zitadel != nil {
				return devidentity.Input{}, fmt.Errorf("identity %q runs on the host IdP, but the env declares two zitadel infra entries (%s, %s)", id.Name, zitadel.Name, e.Infra[i].Name)
			}
			zitadel = &e.Infra[i]
		}
	}
	if zitadel == nil {
		return devidentity.Input{}, fmt.Errorf("identity %q runs on the host IdP, but the env declares no forge.HostInfra with engine \"zitadel\"", id.Name)
	}
	if e.SecretProvider == nil || e.SecretProvider.Type != "file" {
		return devidentity.Input{}, fmt.Errorf("identity %q keeps its credentials in the env's local secret store: declare `secret_provider = forge.FileSecrets {path = \"secrets/%s.yaml\"}` on the Bundle", id.Name, e.Env)
	}
	store := e.SecretProvider.Path
	if store == "" {
		store = filepath.Join("secrets", e.Env+".yaml")
	}
	// THIS checkout's store, never the primary checkout's: a worktree runs its
	// own IdP, so its tokens are its own, and writing them into the shared
	// store would hand every other worktree a credential its IdP rejects.
	if !filepath.IsAbs(store) {
		store = filepath.Join(projectDir, store)
	}

	patSpec := hostinfra.Spec{IDPPATPath: zitadel.IDPPATPath}
	in := devidentity.Input{
		Project:         e.Project,
		Identity:        id,
		BaseURL:         fmt.Sprintf("http://localhost:%d", zitadel.Port),
		PATPath:         patSpec.PATPath(projectDir),
		WorkloadOrigins: map[string]string{},
		Refs:            hostIdentityRefs(e, id),
		StorePath:       store,
		OutputsPath:     filepath.Join(projectDir, ".forge", "identity", id.Name+".json"),
		KeysDir:         filepath.Join(projectDir, ".forge", "identity", id.Name+".keys"),
		Logger:          slog.New(slog.DiscardHandler),
	}
	if prov != nil {
		in.Secret = func(name string) (string, bool) {
			v, ok := prov.Resolve(name)
			return v, ok && v != ""
		}
	}
	for _, f := range e.Frontends {
		if f.Runtime.Type != "" && f.Runtime.Type != FrontendRuntimeHost {
			continue
		}
		if f.Port > 0 {
			in.FrontendOrigin = fmt.Sprintf("http://localhost:%d%s", f.Port, strings.TrimSuffix(f.BasePath, "/"))
			break
		}
	}
	for _, w := range e.Workloads {
		if w.OnRuntime(RuntimeHost) {
			if ports := w.HostPorts(); len(ports) > 0 {
				in.WorkloadOrigins[w.Name] = fmt.Sprintf("http://localhost:%d", ports[0])
			}
		}
	}
	return in, nil
}

// convergeHostIdentity is the identity step of `forge env up`: it runs the
// same pkg/identity reconciler the control plane runs against hosted
// instances, against the env's host-run Zitadel, and publishes what it
// produced. A failure degrades rather than stops the stack, as the
// idp-provision job it replaces did: the backend and frontend come up without
// sign-in, and the message says what is missing.
func convergeHostIdentity(ctx context.Context, e *KCLEntities, projectDir string, prov secrets.Provider) {
	id := hostIdentity(e)
	if id == nil {
		return
	}
	in, err := devIdentityInput(e, id, projectDir, prov)
	if err != nil {
		fmt.Printf("[up] identity %s: %v\n[up] sign-in is unavailable this run; everything else still starts.\n", id.Name, err)
		return
	}
	res, err := devidentity.Converge(ctx, in)
	if err != nil {
		fmt.Printf("[up] identity %s FAILED — continuing, because sign-in is the only thing that needs it:\n%v\n", id.Name, err)
		fmt.Printf("[up] the IdP's log is %s\n", filepath.Join(".forge", "hostinfra", "idp", "zitadel.log"))
		return
	}
	for _, w := range res.Warnings {
		fmt.Printf("[up] identity %s: %s\n", id.Name, w)
	}
	for _, code := range res.Report.SignInCodes {
		fmt.Printf("[up] identity %s: one-time sign-in code for %s: %s\n", id.Name, code.Email, code.Code)
	}
	switch {
	case res.Report.Changed():
		fmt.Printf("[up] identity %s: converged %s (%d change(s))\n", id.Name, in.BaseURL, len(res.Report.Changes))
		for _, c := range res.Report.Changes {
			fmt.Printf("[up]   %s\n", c)
		}
	default:
		fmt.Printf("[up] identity %s: already converged at %s\n", id.Name, in.BaseURL)
	}
	if len(res.StoreKeys) > 0 {
		fmt.Printf("[up] identity %s: wrote %s to %s\n", id.Name, strings.Join(res.StoreKeys, ", "), relTo(projectDir, in.StorePath))
	}
	fmt.Printf("[up] identity %s: outputs in %s\n", id.Name, relTo(projectDir, in.OutputsPath))
}

func relTo(base, p string) string {
	if rel, err := filepath.Rel(base, p); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return p
}
