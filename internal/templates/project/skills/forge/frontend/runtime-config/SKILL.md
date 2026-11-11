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
