---
name: hosted-registry
description: The platform container registry — ONE TOKEN (your control-plane credential, no registry password), why hosted pushes log themselves in, what a bare hosted image resolves to, and how to triage a denied push (realm 401) without reaching for docker login.
---

# The hosted registry: ONE TOKEN

A hosted env's images go to the PLATFORM registry, and you authenticate it with
the credential you already have. There is no registry password to mint, no
second CI secret, and nothing to rotate.

```
forge registry login <env>        # no --username, no --password-*
```

forge resolves the same `rlat_` it reaches the control plane with — `--token`,
then the env's declared `token_env` (default `$FORGE_CONTROL_PLANE_TOKEN`), then
what `forge login` stored, then the credential helper `$FORGE_CREDENTIAL_HELPER`
(Reliant's session — set for `reliant forge …` and Reliant agents' shells). It
presents that on **stdin** as username `forge`;
the realm authenticates on the credential alone, so the username is a label in
a log rather than an identity.

**Passing `--username` / `--password-*` for the platform host is REFUSED.** Not
pedantry: a hand-passed credential that happened to work would teach you this
registry has a password of its own, and the next pipeline you wrote would carry
one — until it expired, in a job nobody remembered had two secrets.

## You usually run no login at all

`forge env build <env> --push`, `forge env deploy <env>` and `forge env up` log
themselves in before their first push, once per host per process. So:

- the scaffolded `release.yml` has **no login step**;
- `build-images.yml` has one only for a non-hosted build env;
- a missing credential fails **before anything is built**, rather than after
  minutes of compiling, with the three remedies named in the order forge checks
  them.

That is also what lets the in-app Deploy button push from a cloud daemon: the
daemon's token is deposited where forge already looks, so no part of the flow
needs a registry password it has no way to obtain.

Two of forge's push paths read the docker credential store — `docker push` for
images and go-containerregistry for a static site — so the login above covers
both. The env's **config bundle does not use the store at all**: it presents the
env's control-plane credential to the registry's token realm itself, in memory,
on every push. So a bundle reaches `registry.reliantapi.com` even when no image
went there (control-plane-style envs push images to GAR and never log in to the
platform host), and a stale `docker login` entry for that host can neither
shadow the credential nor be written to.

A refused bundle push names the registry, the repository, the identity that was
presented (its source, never its value) and the fix. It fails the build; it is
not degraded to a local bundle, because an answered refusal is a fact about the
credential that a retry will not change. An *unreachable* registry is still
tolerated for an env that keeps only a ledger, and refused for an env its
control plane's hub converges, which deploys nothing but what it has recorded.

## Hosted images are built for the PLATFORM's arch, not yours

A hosted image runs on the platform's nodes (amd64 on Reliant), never on
the machine running `forge build`. So forge takes the image arch from
`forge.OnHosted.platform` (default `"amd64"`), not from your laptop: an
Apple-silicon Mac still pushes `linux/amd64`. Declare
`runtime = forge.OnHosted {platform = "arm64"}` only for a control plane whose
pools are arm64. `--target-arch` overrides per invocation.

forge refuses, rather than guesses, in two places: a pushed image for a remote
cluster with no declared arch (`platform` on the env's `ClusterTarget`,
`deploy.target_arch`, or `--target-arch`), and a release whose pushed manifest
lacks the hosted target's platform (`expected linux/amd64, found linux/arm64` —
rebuild and cut a new release; the pods would otherwise die with
`exec format error`, a CrashLoopBackOff with no output).

## A bare image is the normal hosted shape

```
image = "api"      # on a forge.OnHosted workload
```

resolves to `<registry_host>/<org>/<project>/api`: the host from the env's
`forge.ControlPlane` (defaulting to Reliant's registry), `<org>` from the
credential — forge asks the control plane which organization your token acts
for, so there is no `organization` to declare and none to get wrong. A cluster or compose workload still
must name its host: no platform owns that registry, so there is nothing to
compose from.

A host-BEARING reference on a hosted workload is used verbatim — an author who
named one meant it — and `forge env render` / `forge lint` report it when it
sits outside the subtree the platform will admit.

An env that pushes to the platform registry AND one of your own logs in to both
in one run: ours from the control-plane credential, yours from the flags.

## A denied push (realm 401 / `DENIED`)

forge composes the push address from the org your credential acts for, so a
denial is never a mis-declared org. In order:

1. **The credential expired or was revoked.** Under Reliant, sign in to Reliant
   again (`reliant auth login` / the app); standalone, `forge login` again; in
   CI, refresh the secret.
2. **The token holds `deploy:read` but not `deploy:write`.** A push needs both
   the org's subtree and write access; check the token's scopes with
   `forge cloud token list`.

**Re-running a manual `docker login` fixes neither.** If a push is being
refused, the question is what your token may write, not whether a credential
reached the keychain.

## When forge cannot learn your org

A build, deploy, release cut or `forge registry login` for a hosted env asks the
control plane which organization the credential acts for (one call per
command). With no credential it stops at the top and says so — authenticate and
re-run. `forge env render` and `forge lint` need no credential: with none they
compose no base, and report a host-bearing image as the weaker, unverified fact.
