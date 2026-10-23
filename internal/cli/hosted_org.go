package cli

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/hostedimage"
)

// The organization an env's hosted artifacts belong to is the organization the
// control-plane CREDENTIAL acts for. forge asks the control plane
// (cloud.ResolveOrganization) rather than reading a declaration: a declared
// copy could only agree with the token or be wrong, and the registry's realm
// scopes a push to the token's own org either way.
//
// Two ways to read it, because the surfaces differ:
//
//   - strict (organization, requireOrg): the authenticated entry points — a
//     build that pushes, a deploy, a release cut, `forge registry login`. They
//     already need the credential, so a failure to resolve the org is a
//     failure, said once, at the top, naming what to do.
//   - best-effort (declaredPushBase): render, plan, diff and lint. These run
//     with no credential on a fresh checkout and must not fail for want of one;
//     they compose a base when the org is knowable and say nothing when not.
//
// The answer — success OR failure — is cached for the process, so a command that
// reads it at several depths makes one call. Failure is cached on purpose: an
// offline render with a stored credential would otherwise wait out a network
// timeout at every depth it composes a base.

// orgState is one control-plane declaration's resolved organization.
type orgState struct {
	mu   sync.Mutex
	done bool
	org  string
	err  error
}

// bindOrgLookup attaches the state organization reads through. Called by the
// render, which is the one place a ControlPlaneEntity is born.
func (cp *ControlPlaneEntity) bindOrgLookup(env string) {
	if cp == nil {
		return
	}
	cp.env = env
	cp.org = &orgState{}
}

// setResolvedOrg pins the organization without a call. For tests, which state
// the org rather than standing up a control plane.
func (cp *ControlPlaneEntity) setResolvedOrg(org string) {
	if cp == nil {
		return
	}
	if cp.org == nil {
		cp.org = &orgState{}
	}
	cp.org.mu.Lock()
	defer cp.org.mu.Unlock()
	cp.org.done, cp.org.org, cp.org.err = true, org, nil
}

// organization is the strict read: the org the credential acts for, or an error
// that says why it could not be learned. Either answer is remembered for the
// process.
func (cp *ControlPlaneEntity) organization(ctx context.Context) (string, error) {
	if cp == nil {
		return "", fmt.Errorf("this env declares no control plane, so there is no organization to resolve")
	}
	if cp.org == nil {
		return resolveControlPlaneOrg(ctx, cp.env, cp)
	}
	cp.org.mu.Lock()
	defer cp.org.mu.Unlock()
	if cp.org.done {
		return cp.org.org, cp.org.err
	}
	org, err := resolveControlPlaneOrg(ctx, cp.env, cp)
	cp.org.done, cp.org.org, cp.org.err = true, org, err
	return org, err
}

// knownOrganization is the best-effort read: "" unless the org is already known
// or can be learned right now.
func (cp *ControlPlaneEntity) knownOrganization() string {
	org, err := cp.organization(context.Background())
	if err != nil {
		return ""
	}
	return org
}

// requireOrg resolves e's organization strictly, for an authenticated entry
// point. A no-op unless something in the env NEEDS a push base: an env with
// nothing hosted never does, and neither does one whose hosted images all name
// their registry (nothing to compose them under), so neither needs the call.
func requireOrg(ctx context.Context, e *KCLEntities) error {
	if e == nil || e.ControlPlane == nil || !e.HasHosted() || !hostedNeedsPushBase(e) {
		return nil
	}
	if _, err := e.ControlPlane.organization(ctx); err != nil {
		return err
	}
	return nil
}

// hostedNeedsPushBase reports whether any hosted item declares a BARE image —
// one with no registry host, which only resolves under the platform's push base.
func hostedNeedsPushBase(e *KCLEntities) bool {
	for _, it := range hostedImageItems(e) {
		if !hostedimage.HasRegistryHost(hostedimage.Repository(it.Image)) {
			return true
		}
	}
	return false
}

// orgLookupTimeout bounds one organization lookup.
const orgLookupTimeout = 10 * time.Second

// orgCache remembers each (endpoint, token env)'s answer for the process.
var orgCache sync.Map

// resolveControlPlaneOrg asks the control plane which organization the env's
// credential acts for. A var so a test states the answer.
var resolveControlPlaneOrg = func(ctx context.Context, env string, cp *ControlPlaneEntity) (string, error) {
	return resolveDeclarationOrg(ctx, env, &cloud.Declaration{Endpoint: cp.Endpoint, TokenEnv: cp.TokenEnv})
}

// orgResolver is resolveDeclarationOrg as a var, so a test of the comparison in
// sameOrganization states each side's answer.
var orgResolver = resolveDeclarationOrg

// resolveDeclarationOrg is the call behind it, shared with the commands that
// hold a bare declaration rather than an entity (promote --from).
func resolveDeclarationOrg(ctx context.Context, env string, decl *cloud.Declaration) (string, error) {
	ep, err := cloud.ResolveEndpoint(env, decl)
	if err != nil {
		return "", err
	}
	key := ep.URL + "\x00" + ep.TokenEnv
	if v, ok := orgCache.Load(key); ok {
		return v.(string), nil
	}
	cred, err := cloud.ResolveCredential("", ep)
	if err != nil {
		return "", fmt.Errorf("env %q needs its organization, which forge reads from the control plane at %s with your credential: %w",
			env, ep.URL, err)
	}
	// Bounded well under the client's own timeout: this runs before work the
	// author is waiting on, and a control plane that does not answer is not
	// going to.
	ctx, cancel := context.WithTimeout(ctx, orgLookupTimeout)
	defer cancel()
	org, err := cloud.ResolveOrganization(ctx, hostedDeployClient(ep, cred))
	if err != nil {
		return "", fmt.Errorf("env %q (%s): %w", env, ep.URL, err)
	}
	orgCache.Store(key, org)
	return org, nil
}
