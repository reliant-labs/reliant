# vite-spa

[![Deploy with Reliant Forge](https://raw.githubusercontent.com/reliant-labs/forge/main/docs/assets/deploy-with-reliant-forge.svg)](https://docs.reliantlabs.io/forge/deploy-without-an-account)

A Vite single-page app with client-side routes (`/` and `/about`), on Reliant
hosted static hosting. `reliant forge project new --in-place --kind site`
adopted it untouched and wrote `forge.yaml` and `deploy/kcl/`: the frontend
`web` (type `vite`, built into `dist/`), served by its dev server in dev and by
hosted static hosting in staging and prod.

Hosting serves `index.html` for any unknown path without a file extension, so a
reload on `/about` works with no rewrite rule.

## Prerequisites

Node 20 or newer.

## Run it locally

```bash
npm install
npm run dev                     # or: reliant forge env up dev (mirrors the browser console into .forge/logs/dev/)
```

## Deploy it

```bash
npm install
npx -y @reliantlabs/forge@latest env deploy prod --claimable
```

A static site needs nothing extra to deploy anonymously. Unclaimed, it lives 72
hours. Claimed, or deployed signed in, it is your organization's one free
static site (each environment's site counts toward that one).

## Verify it

```bash
curl -s https://<your-host>/ | head -3         # the index.html
curl -s -o /dev/null -w '%{http_code}\n' https://<your-host>/about   # 200
```

Verified on 2026-11-25: `npm run build`, `reliant forge lint --static-export`,
`reliant forge env render prod`, and a real `--claimable` deploy (`/` and
`/about` answered 200).
