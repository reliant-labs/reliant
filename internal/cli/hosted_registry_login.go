package cli

// ONE TOKEN (ADR-0003 F3). The platform's container registry is authenticated
// with the SAME `rlat_` forge already resolves for the control plane, so a
// hosted author never mints a registry credential, never runs `docker login`
// by hand, and never puts a second secret in a CI job.
//
// The mechanism is deliberately `docker login` and nothing else. All three of
// forge's push paths read the DOCKER CREDENTIAL STORE — `docker push` for
// images, oras for a bundle, go-containerregistry for a static site — so one
// login to a host makes all three work. A second keychain, a forge-owned
// credential cache or a per-path auth header would be three ways to be half
// logged in.
//
// WHY THE CREDENTIAL IS NOT THE AUTHOR'S PROBLEM. The registry's realm
// derives the org from the token row, scopes the token to that org's subtree,
// and refuses everything else. So the push credential is a FUNCTION of the
// control-plane credential, and asking for it separately asked the author to
// restate a value forge already held — the same defect ADR-0003 F1 closed for
// the push base. It also made the in-app Deploy button impossible: a managed
// daemon has a deposited `rlat_` (pkg/cloudcred) and no way to be handed a
// registry password, which is gap G7.

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/hostedimage"
	"github.com/reliant-labs/forge/pkg/cloudcred"
)

// platformRegistryUsername is the username forge presents to our realm.
//
// The realm accepts ANY username and authenticates on the password alone
// (ADR-0003 C1), so this is a label in a log rather than an identity. A
// constant and not the author's name, because a per-user value would read as
// though it were being checked.
const platformRegistryUsername = "forge"

// platformRegistryHost is the registry host this env's control-plane
// declaration names — `forge.ControlPlane.registry_host`, defaulted to
// forge's own DefaultRegistryHost exactly as PushBase defaults it.
//
// "" when the env declares no control plane. That is the common case and not
// an error: an env with no control plane has no platform registry, and every
// host its workloads name is the author's own.
//
// It does NOT require an organization. A declaration with no org composes no
// push base, so a bare hosted image is refused long before anything pushes;
// but a host-BEARING image may still name our registry, and logging in to it
// is right whether or not forge could have composed the address itself.
func platformRegistryHost(e *KCLEntities) string {
	if e == nil || e.ControlPlane == nil {
		return ""
	}
	host := strings.TrimSpace(e.ControlPlane.RegistryHost)
	if host == "" {
		host = hostedimage.DefaultRegistryHost
	}
	return hostedimage.NormalizeBase(host)
}

// declaredTokenEnv is the env var NAME this env's control-plane declaration
// reads its credential from, so a message names the variable the author would
// actually set rather than forge's default.
func declaredTokenEnv(e *KCLEntities) string {
	if e != nil && e.ControlPlane != nil {
		if v := strings.TrimSpace(e.ControlPlane.TokenEnv); v != "" {
			return v
		}
	}
	return cloud.DefaultTokenEnv
}

// platformLogins records the hosts this PROCESS has already logged in to, so
// the login happens once however many artifacts a build pushes.
//
// MEMOIZED, because a build pushes several things to one host — N images, a
// site release, the env's bundle — and each push path reaches the credential
// store independently. Without this, a deploy would resolve the credential and
// shell out to `docker login` once per artifact: the same write to the same
// store, N times, N lines of noise in the log, and N chances for a transient
// failure to fail a deploy that was already authenticated.
//
// Keyed by HOST and not by (env, host), because the docker credential store
// is itself keyed by host. Two envs naming one registry share one entry there,
// so a second login would not add anything — it would overwrite.
var platformLogins sync.Map

// loginToPlatformRegistry logs docker in to host with the control-plane
// credential env resolves, once per host per process.
//
// A local registry is SKIPPED rather than refused: a k3d-local registry takes
// no credentials at all (isLocalRegistryHost), so a dev env that happens to
// declare a control plane must not be sent looking for a token it has no use
// for.
//
// NO CREDENTIAL IS A HARD FAILURE, and the caller places this before the first
// build step for that reason. A build that compiles, images and packs for
// minutes and only then cannot push has spent all of it to learn something
// knowable up front. cloud.ResolveCredential's own message already names the
// three remedies in the order forge checks them, so it is wrapped with the
// host and the env and not restated.
func loginToPlatformRegistry(ctx context.Context, env, host, flagToken string, decl *cloud.Declaration) error {
	if host == "" || isLocalRegistryHost(host) {
		return nil
	}
	if _, done := platformLogins.Load(host); done {
		return nil
	}
	ep, err := cloud.ResolveEndpoint(env, decl)
	if err != nil {
		return err
	}
	cred, err := cloud.ResolveCredential(flagToken, ep)
	if err != nil {
		return fmt.Errorf("log in to %s, the registry env %q declares: %w", host, env, err)
	}
	fmt.Printf("[registry] logging in to %s with env %s's control-plane credential (%s: %s)\n",
		host, env, cred.Source, cred.From)
	if err := runDockerLogin(ctx, host, platformRegistryUsername, cred.Token); err != nil {
		return err
	}
	platformLogins.Store(host, struct{}{})
	return nil
}

// autoLoginForPush logs in to the platform registry before this invocation's
// FIRST push can reach it.
//
// Called from renderBuildInputs — after the push plan is resolved, before any
// build step runs — which is the one place every pushing invocation passes
// through: `forge env build <env> --push`, `forge env up`, and `forge env
// deploy`, which builds through runBuild. Hooking the push itself
// would be later and worse: `docker push` is one of THREE push paths, and the
// other two would each need their own copy of this.
//
// Nothing happens unless this build PUSHES and one of its destinations is the
// host the env declared. A render, a `--plan`, a local build, and an env whose
// images all go to the author's own registry resolve no credential and make no
// subprocess call — which is what keeps the offline surfaces offline.
func autoLoginForPush(ctx context.Context, env string, declared *KCLEntities, plan pushPlan) error {
	if !plan.push {
		return nil
	}
	host := platformRegistryHost(declared)
	if host == "" {
		return nil
	}
	for _, h := range plan.hosts() {
		if h == host {
			return loginToPlatformRegistry(ctx, env, host, "", declarationFromEntities(declared))
		}
	}
	return nil
}

// errPlatformRegistryTakesNoFlags is `--username` / `--password-*` aimed at
// our own registry.
//
// REFUSED rather than honoured, because honouring it would make "one token" a
// coincidence. A hand-passed credential that happened to work would teach the
// author that our registry has a password of its own, and the next CI job they
// wrote would carry one — until it expired, in a pipeline nobody remembered
// had two secrets.
func errPlatformRegistryTakesNoFlags(env, host, tokenEnv string) error {
	return cliutil.UserErr("forge registry login "+env,
		fmt.Sprintf("%s is the platform registry for env %q (forge.ControlPlane's registry_host), and it takes your control-plane credential — not a --username and --password", host, env),
		fmt.Sprintf("deploy/kcl/%s/main.k", env),
		fmt.Sprintf("drop the flags: forge registry login %s\n"+
			"  forge resolves the same rlat_ it reaches the control plane with (--token, then $%s, then `forge login`, "+
			"then the credential helper $%s) and presents it to %s on stdin. There is no second credential to mint",
			env, tokenEnv, cloudcred.HelperEnv, host))
}

// errForeignRegistryNeedsFlags is an env that declares OUR registry and also
// names somebody else's.
//
// Both get logged in to, each with the credential that is actually its own —
// but only one of them can be resolved from the declaration, so the other
// still needs the flags. Refusing with the host NAMED is the whole value here:
// the author's own message would otherwise be "--username is required" on a
// command they just ran without it for a different host, in the same env.
func errForeignRegistryNeedsFlags(env string, hosts []string) error {
	return cliutil.UserErr("forge registry login "+env,
		fmt.Sprintf("env %q also pushes to %s, which is not the platform registry and has its own credential",
			env, strings.Join(hosts, ", ")),
		"deploy/kcl/workloads.k",
		fmt.Sprintf("pass that registry's credential as well — the platform's half needs nothing:\n"+
			"  printf '%%s' \"$TOKEN\" | forge registry login %s --username <user> --password-stdin\n"+
			"  forge logs in to the platform registry from your control-plane credential in the same run", env))
}
