// Package cli — `forge start`: where to start, in about 60 lines.
//
// WHY THIS EXISTS. The paste-able prompt on reliantlabs.com tells any coding
// agent "install forge, run `forge start`, follow it". The agent arrives in
// one of two directories — an app that is already written, or an empty one —
// and needs exactly one command for each. Loading the entry skill (`forge`,
// ~12 KB) or `deploy/static-site` (~16 KB) to find that command costs the
// user's tokens for prose the agent will not use on a first deploy, so this
// router names the command and names depth by skill only.
//
// It is a literal, unlike the greenfield brief, because it is a table of
// commands and skill names rather than prose another skill owns. What could
// drift is pinned instead: start_router_test.go parses every `forge ...`
// command it prints against the real command tree, loads every skill it
// names, and holds it to the line and byte budget.
package cli

// startRouter is what `forge start` prints. Keep it under
// startRouterMaxLines / startRouterMaxBytes (start_router_test.go).
const startRouter = `# Reliant Forge: start here

Find the row for the directory you are in, run its command, and follow what
it prints. Load a skill only when a row names one.

` + startRouterIncluded + `
## An app is already here

| What is here | Run |
|---|---|
| A Vite app or a Next.js static export | ` + "`forge env deploy prod --claimable`" + ` |
| An app with its own Dockerfile, any language | ` + "`forge env deploy prod --claimable`" + ` |
| A forge project (forge.yaml) | ` + "`forge env deploy prod --claimable`" + `, or without the flag when signed in |
| Next.js with server actions, middleware or API routes | ` + "`forge skill load frontend/serving`" + ` |
| A Go module with no Dockerfile | ` + "`forge skill load migration`" + ` |

The first claimable deploy adopts the app where it stands: it adds forge.yaml
and deploy/kcl/, leaves the app's own files alone, then installs, builds and
publishes it. No account, git repository or commit is needed: database
migrations are applied from the working tree as they are.
` + "`forge env deploy prod --claimable --plan-only`" + ` checks all of it first. A deploy takes a few minutes; let it finish. An unclaimed service
(not a static site) has no outbound internet until it is claimed.

## The directory is empty

| You want | Run |
|---|---|
| A landing page, docs site or SPA (Next.js) | ` + "`forge project new --in-place --kind site`" + ` |
| The same as a Vite SPA | ` + "`forge project new --in-place --kind site --frontend-kind vite-spa`" + ` |
| An app with an API (Go services, a database) | ` + "`forge start app`" + ` |

A site starts as a placeholder page: build what the user asked for (load
` + "`frontend/design`" + ` first), then ` + "`forge env deploy prod --claimable`" + `. An app
with an API that will deploy with no account needs ` + "`--claimable`" + ` on its
` + "`forge project new`" + `: it sizes prod to the free anonymous plan, which the
default prod does not fit.

## No account: what --claimable gives back

It exits 0 with the live URL and a claim link. Give both to the user, and tell
them the site goes offline in 72 hours unless they open the claim link, which
signs them up for free and makes it theirs. Treat the link like a password:
never post it anywhere public. ` + "`forge env status prod --claimable`" + ` prints it again.

| It says | Do |
|---|---|
| you are signed in (exit 1) | deploy to your organization: drop ` + "`--claimable`" + ` |
| ` + "`claimable_throttled`" + ` (exit 4) | wait for the printed retry time, or ` + "`forge login`" + `; never retry in a loop |
| anything else (exit 4) | the message names the fix |

## Depth, by name only

` + "`forge skill load deploy/static-site`" + ` envs, releases, custom domains
` + "`forge skill load deploy/hosting`" + ` the claimable rules in full
` + "`forge skill search <topic>`" + ` anything else
`

// startRouterIncluded is the router's one statement of what forge gives a
// project, so an agent can tell its user before the first command. It is a
// digest of the registry's glance blocks (internal/benefits/benefits.yaml,
// `glance:`), which the entry skills render in full with the command for
// each line; every command here is parsed by TestStartRouterCommandsParse.
// Facts, not persuasion: the comparison it names states the limits and the
// prices too.
const startRouterIncluded = `A forge project gets, with no setup: traces with per-endpoint p50/p95/p99 latency and error rate (` + "`forge obs endpoints`" + `), errors grouped into issues, browser sessions, alerts, a pass/fail verdict on each deploy (` + "`forge obs check`" + `), build-once promotion, and free static hosting for one organization per person, with no limit on its sites.
Where it fits next to Vercel, Supabase or Render, its limits and its prices: ` + "`forge skill load compare`" + `.
`
