# Human login and hosted project ownership

## Why this changed

The old CLI minted org-bound, automation-style `rlat_` tokens for humans. That
was a legacy design, not a requirement of OAuth or multi-organization accounts.
It confused authentication (who signed in) with authorization (what that person
may do to a particular target).

`forge login` now authenticates the person once per endpoint. Its revocable
human credential is stored separately from delegated automation and claimable
capabilities. It carries no selected organization and no snapshot of org grants.
There is no `forge org use`, and login/logout take no organization selector.

## First deployment

```sh
forge login
forge env deploy prod --org <owner-org-id>
```

The server creates a hosted project with an immutable organization owner. If
there is exactly one membership, an unbound creation can use it. Otherwise it
fails with an actionable `--org` requirement: no browser prompt, oldest-org
choice, or agent-dependent interaction. Existing project names must resolve
unambiguously; names are labels, not authorization.

Forge records the returned server project ID per endpoint under
`.forge/hosted-projects.json`. This is a target binding, not an org preference or
credential choice. Keep this file when switching branches or org defaults.
Subsequent requests carry that project ID; requests addressing a resource also
have their stored ownership checked. The server rechecks current membership and
operation permissions. Neither changing your default org nor supplying another
org can move a bound project. A new checkout can resolve a unique accessible
project by name; ambiguous names require its established server identity.

## Credentials and logout

Browser login uses the `forge-cli-human` credential namespace. Explicit
`--token`/environment credentials remain delegated capabilities and do not
replace the human entry. Claimable credentials retain their separate project/env
namespace. They never become human sessions or gain cross-org authority.

`forge logout` revokes the current device's human session across all orgs at the
selected endpoint, then removes it locally. It does not revoke automation or
claimable tokens. Other endpoints and devices are unaffected. A failed revocation
retains the local entry so it can be retried; an already revoked/expired session
can be forgotten safely.

A control plane that predates human identities answers the browser flow with
an org-bound token and no `credential_kind`. forge stores that as a delegated
`org-login` entry (client `forge-cli`), never as an identity, so every command
takes the delegated path that control plane serves (`GetTenant`, no
`ResolveHostedProject`), and `forge logout` forgets it locally. Once the control
plane issues human identities, the next `forge login` stores one instead; no
forge upgrade is needed. Any other unknown `credential_kind` is refused.
Automation keeps its existing restricted path. No persistent org-use state or
per-org human login format is introduced.
