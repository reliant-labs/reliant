---
name: frontend/runtime-config
description: Where a forge frontend gets per-env values and backend URLs — config_gen.ts and <basePath>/config.js at runtime (never NEXT_PUBLIC_*/VITE_* for per-env values), typed config vs runtime_config, forge.WorkloadURL and who resolves it per env, a hosted site's hostname, and CORS_ORIGINS naming the site.
---

# Runtime config and backend URLs

A frontend reads its per-env values from `src/lib/config_gen.ts` (`loadConfig()`), which reads `window.__FORGE_CONFIG__`. That global is set by **`<basePath>/config.js`**, a classic blocking `<script>` in the shell's `<head>`: `layout.tsx` for Next.js, `index.html` (`%BASE_URL%config.js`) for Vite. Values arrive at runtime, so one bundle promotes across envs: never read `NEXT_PUBLIC_*` / `VITE_*` for a per-env value (the bundler freezes it).

Two sources are layered into the document, key by key:

1. **Typed config**: the `(forge.v1.frontend_config)` proto message, with per-env values in `deploy/kcl/<env>/config.k`.
2. **`runtime_config`** on the `forge.Frontend`, which wins on the same key; an unread key fails the render (`frontend-runtime/config`). Use it when a value is **another workload's URL**:

```kcl
forge.Frontend {
    name = "web"
    runtime = forge.OnHost {}
    runtime_config = {
        API_URL = forge.WorkloadURL { workload = "api" }   # a sibling workload's public URL
        APP_NAME = "acme"                                   # or a literal
    }
}
```

Name the workload, never the hostname:

| Env | Who resolves `forge.WorkloadURL` | Who writes `config.js` |
|---|---|---|
| **Hosted** (the referrer is a frontend or a workload bound to `forge.OnHosted`) | The control plane: it allocates hostnames and rewrites the document when one moves. | The control plane, after every StaticSite sync (the release carries none). |
| Everything else (host, compose, cluster, `OnFirebase` / `OnBucket`) | forge, **at render time**: a route to the workload → a custom domain on its exposed port → its host `listen_ports` → a frontend's `port`. | forge: `public/config.js` in dev, per env on a Firebase / bucket deploy. |

Unknown names are refused at load; off hosting, so is a URL forge cannot know at render time (a host service without `listen_ports`).

A hosted site's own hostname is not a field: `forge domain add` (see `deploy`).

The backend half uses the same reference. A workload's `CORS_ORIGINS` names the site, not its URL:

```kcl
wl.api | {env = {CORS_ORIGINS = forge.WorkloadURL {workload = "web"}}}
```

## A frontend hosted elsewhere, calling a forge API

A frontend you keep on Vercel, Netlify or your own host can call a forge API on Reliant hosting (or any env). forge does not build or serve that frontend, so nothing above resolves its URL: the two halves are literal values.

1. **Give the API a hostname on the frontend's own registrable domain.** The session cookie forge's sign-in sets is `SameSite=Lax`, so a browser sends it only to the same *site*. `app.example.com` and `api.example.com` are the same site. `my-app.vercel.app` and the API's platform hostname are not, and there every call after sign-in answers 401. With no sign-in (public RPCs only), any hostname works.

   ```bash
   forge domain add api.example.com --env prod      # publish the DNS it prints
   forge domain bind api.example.com --env prod --target api
   ```

2. **Allow the frontend's origin on the API, with credentials.** Set both on the service in `deploy/kcl/<env>/main.k`. `CORS_ORIGINS` is the frontend's exact origin, comma-separated for several. `CORS_ALLOW_CREDENTIALS` lets the browser send the cookie:

   ```kcl
   wl.api | {env = {CORS_ORIGINS = "https://app.example.com", CORS_ALLOW_CREDENTIALS = "true"}}
   ```

   A wildcard origin with credentials is refused at config load, because browsers reject it.

3. **Make the frontend's origin the app origin sign-in trusts.** The sign-in routes refuse a POST from any origin but the API's own and the app's (`devidp.NewCrossOriginProtection`, proven with the CORS layer in `pkg/devidp/external_frontend_signin_test.go`; a same-site sibling is refused like a cross-site one). The app origin is the one in `idp_login_uri`. With managed identity it is derived from the env's hosted frontend or first bound domain, so for a frontend forge does not host, declare the application's URIs literally in `deploy/kcl/identity.k`:

   ```kcl
   applications = [forge.IdentityApplication {
       name = "prod:api", workload = "api"
       login_uri = "https://app.example.com/auth/sign-in"           # the frontend's sign-in page
       redirect_uris = ["https://app.example.com/auth/callback"]
   }]
   ```

   Each env needs its own name and URIs, so pick the values by env (`option("env")`) when more than one env uses it. Without managed identity, set `IDP_LOGIN_URI` on the API to the frontend's sign-in URL. Skip this step and the browser's sign-in POST answers 403, however CORS is set.

4. **Call the API with credentials from the frontend.** Point its base URL at `https://api.example.com` through the frontend's own env config, and send `credentials: "include"` on every request. forge's generated `src/lib/connect.ts` does this (`fetchWithSession`); copy it if the frontend was not scaffolded by forge.

Preview deploys on the frontend's host get their own origins. List each one you need in `CORS_ORIGINS`, or sign in only on the production origin.
