package cli

// How a bundle authenticates to the platform registry.
//
// THE BUNDLE PUSH PRESENTS THE ENV'S CONTROL-PLANE IDENTITY, DIRECTLY. It does
// not go through `docker login` and it never reads the docker credential
// store. The registry's token realm authenticates on the control-plane
// credential alone (hosted-registry skill: ONE TOKEN), so forge resolves that
// credential in memory — the same one that records the release on the same
// control plane — and hands it to oras, which exchanges it at the realm for an
// access token that lives minutes.
//
// WHY NOT THE STORE. Before this, a bundle push relied on an earlier
// `docker login` having written the host into the store, and that login ran
// only when an IMAGE was pushed to the platform host (autoLoginForPush). An env
// whose images live elsewhere (control-plane prod pushes to GAR) never logged
// in, so the push presented whatever an old session had left in the store —
// a long-lived `rlat_` that had since expired — and the realm answered 401.
// Two defects, one root: the push's credential was ambient state rather than
// the identity forge was already using. Removing the ambient state removes
// both, and there is no case in which a stale entry can shadow a fresh
// credential, because the store is not consulted. Nor is it written: nothing
// long-lived is left on disk by a push.
//
// THE CREDENTIAL IS RESOLVED LAZILY, per token exchange, through
// cloud.ResolveCredential. A credential-helper token is minutes-to-hours
// lived and the helper memoizes it against its own expiry, so a long push
// that outlives one token asks the helper again instead of failing on the old
// value.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/errcode"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/hostedimage"
)

// bundleRegistryAuth is the identity a bundle is pushed under, and the words
// to describe it in an error.
//
// The zero value means "no identity of ours": the transport falls back to the
// ambient docker store, which is only right for a registry forge does not own.
type bundleRegistryAuth struct {
	// Host is the registry this identity is for. It is never presented to any
	// other host, however the transport got there.
	Host string
	// Identity describes the credential for a person — its source and the
	// control plane it belongs to, never its value.
	Identity string
	// Fix is what repairs a refusal of this identity: the sign-in that fits
	// this process, or the variable a CI job should refresh.
	Fix string

	credential func(context.Context) (string, error)
}

// credentialFunc is the oras hook: the control-plane credential for the
// platform host and nothing for any other host. nil for the zero value, which
// leaves the transport on its default.
func (a bundleRegistryAuth) credentialFunc() auth.CredentialFunc {
	if a.credential == nil {
		return nil
	}
	return func(ctx context.Context, hostport string) (auth.Credential, error) {
		if !strings.EqualFold(hostport, a.Host) {
			return auth.EmptyCredential, nil
		}
		token, err := a.credential(ctx)
		if err != nil {
			return auth.EmptyCredential, err
		}
		return auth.Credential{Username: platformRegistryUsername, Password: token}, nil
	}
}

// bundleRegistryAuthFor resolves the identity env's bundle is pushed under. A
// var so a test can state the identity without a KCL tree.
var bundleRegistryAuthFor = declaredBundleRegistryAuth

// declaredBundleRegistryAuth reads the env's control-plane declaration and
// resolves its credential.
func declaredBundleRegistryAuth(ctx context.Context, projectDir, env string) (bundleRegistryAuth, error) {
	entities, err := RenderKCL(ctx, projectDir, env)
	if err != nil {
		return bundleRegistryAuth{}, fmt.Errorf("render deploy/kcl/%s to learn the control plane its bundle is pushed under: %w", env, err)
	}
	return bundleRegistryAuthOf(env, entities)
}

// bundleRegistryAuthOf is the render-free half: the env's declared control
// plane is the identity, and its registry_host is the only host it is for.
func bundleRegistryAuthOf(env string, e *KCLEntities) (bundleRegistryAuth, error) {
	host := platformRegistryHost(e)
	decl := declarationFromEntities(e)
	if host == "" || decl == nil {
		return bundleRegistryAuth{}, fmt.Errorf("env %q declares no control plane, so there is no identity to push its bundle to a platform registry with", env)
	}
	ep, err := projectEndpoint(env, decl)
	if err != nil {
		return bundleRegistryAuth{}, err
	}
	// Resolved once now rather than at the first exchange: a missing
	// credential is named here, with the remedies, instead of surfacing as an
	// anonymous 401 from the registry that says nothing about forge.
	cred, err := cloud.ResolveCredential("", ep)
	if err != nil {
		return bundleRegistryAuth{}, fmt.Errorf("env %q pushes its bundle to %s with its control-plane credential: %w", env, host, err)
	}
	return bundleRegistryAuth{
		Host:     host,
		Identity: fmt.Sprintf("the control-plane credential for %s (%s: %s)", ep.URL, cred.Source, cred.From),
		Fix: fmt.Sprintf("%s — or, in CI, refresh $%s. Check the credential's scopes with `forge cloud token list` (a push needs deploy:write)",
			cloud.SignInHint(), ep.TokenEnv),
		credential: func(context.Context) (string, error) {
			fresh, err := cloud.ResolveCredential("", ep)
			if err != nil {
				return "", err
			}
			return fresh.Token, nil
		},
	}, nil
}

// registryTakesNoCredentials is isLocalRegistryHost, as a var so a test can put
// a fake registry on a loopback address and still exercise the authenticated path.
var registryTakesNoCredentials = isLocalRegistryHost

// bundleRegistryAuths memoizes the resolved identity per (project, env) for
// the process. A build opens the bundle repository several times — look up the
// release's bundle, push, tag — and each open would otherwise render the env's
// KCL again just to relearn its control plane. Only successes are kept, and the
// credential inside re-resolves on its own schedule (see the file comment).
var bundleRegistryAuths sync.Map

func cachedBundleRegistryAuth(ctx context.Context, projectDir, env string) (bundleRegistryAuth, error) {
	key := projectDir + "\x00" + env
	if v, ok := bundleRegistryAuths.Load(key); ok {
		return v.(bundleRegistryAuth), nil
	}
	a, err := bundleRegistryAuthFor(ctx, projectDir, env)
	if err != nil {
		return bundleRegistryAuth{}, err
	}
	bundleRegistryAuths.Store(key, a)
	return a, nil
}

// openBundleRepository opens the registry repository reference names, under the
// identity env's control plane declares when the reference is on that control
// plane's platform registry.
//
// A reference on any other host gets the ambient docker store, because that
// registry is not ours and the author's own `docker login` is the only
// credential it can mean. A local registry takes no credentials, so it is not
// sent one — and does not make a dev env hunt for a token it has no use for.
func openBundleRepository(ctx context.Context, projectDir, env, reference string) (*platformBundleRepo, error) {
	host, _, _ := strings.Cut(reference, "/")
	if registryTakesNoCredentials(host) {
		r, err := bundle.NewRepository(reference)
		if err != nil {
			return nil, err
		}
		return &platformBundleRepo{Repository: r}, nil
	}
	id, err := cachedBundleRegistryAuth(ctx, projectDir, env)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(host, id.Host) {
		r, err := bundle.NewRepository(reference)
		if err != nil {
			return nil, err
		}
		return &platformBundleRepo{Repository: r}, nil
	}
	r, err := bundle.NewRepositoryWithCredential(reference, id.credentialFunc())
	if err != nil {
		return nil, err
	}
	return &platformBundleRepo{Repository: r, id: id, reference: reference}, nil
}

// platformBundleRepo is a bundle repository that explains its own refusals.
//
// It embeds the oras repository, so it satisfies bundleRegistry (Resolve,
// Fetch, Tag) and bundle.Pusher unchanged; the four methods it overrides only
// add words to a 401/403, never change what is returned otherwise — an absent
// tag is still errdef.ErrNotFound to the caller that tests for it.
type platformBundleRepo struct {
	*remote.Repository
	id        bundleRegistryAuth
	reference string
}

func (r *platformBundleRepo) Push(ctx context.Context, expected ocispec.Descriptor, content io.Reader) error {
	return r.explain(r.Repository.Push(ctx, expected, content))
}

func (r *platformBundleRepo) Resolve(ctx context.Context, reference string) (ocispec.Descriptor, error) {
	desc, err := r.Repository.Resolve(ctx, reference)
	return desc, r.explain(err)
}

func (r *platformBundleRepo) Fetch(ctx context.Context, target ocispec.Descriptor) (io.ReadCloser, error) {
	rc, err := r.Repository.Fetch(ctx, target)
	return rc, r.explain(err)
}

func (r *platformBundleRepo) Tag(ctx context.Context, desc ocispec.Descriptor, reference string) error {
	return r.explain(r.Repository.Tag(ctx, desc, reference))
}

func (r *platformBundleRepo) explain(err error) error {
	if err == nil || r.id.credential == nil || !hostedimage.IsDenied(err, "") {
		return err
	}
	return &bundleRegistryDeniedError{registry: r.id.Host, repository: r.reference, id: r.id, cause: err}
}

// bundleRegistryDeniedError is the platform registry refusing the identity a
// bundle was pushed under. It is an ANSWER, not a transport failure, so the
// callers that tolerate an unreachable registry do not tolerate this one: a
// green build over a refused push leaves every reader on a bundle that does
// not exist.
type bundleRegistryDeniedError struct {
	registry, repository string
	id                   bundleRegistryAuth
	cause                error
}

func (e *bundleRegistryDeniedError) Unwrap() error { return e.cause }

func (e *bundleRegistryDeniedError) Error() string {
	status := ""
	var resp *errcode.ErrorResponse
	if errors.As(e.cause, &resp) {
		status = fmt.Sprintf(" (HTTP %d)", resp.StatusCode)
	}
	return fmt.Sprintf("the registry %s refused the bundle push to %s%s: %v\n"+
		"  identity presented: %s\n"+
		"  forge presented that credential to the registry's token realm itself and did not read the docker credential store, so a stale `docker login` entry is not the cause.\n"+
		"  likely: the credential expired or was revoked, or it holds deploy:read without the deploy:write a push needs.\n"+
		"  fix: %s",
		e.registry, e.repository, status, e.cause, e.id.Identity, e.id.Fix)
}
