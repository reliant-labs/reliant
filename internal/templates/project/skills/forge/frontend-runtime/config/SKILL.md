---
name: frontend-runtime/config
description: Per-env values a forge frontend reads at runtime — config_gen.ts and window.__FORGE_CONFIG__ from <basePath>/config.js, which runtime_config keys a bundle accepts (config_schema_gen.json, did-you-mean on a typo), required and required_when keys, the mode selector, and a frontend whose code lives in another repo (FORGE_RUNTIME_CONFIG_FILE, forgeRuntimeConfig()).
---

# Runtime config

Per-env VALUES (API origin, feature flags) are not part of this package. They
come from the generated `src/lib/config_gen.ts`, which reads
`window.__FORGE_CONFIG__` from `<basePath>/config.js`. Dev, Firebase, own-bucket
and hosted deploys all serve the same `{KEY: "value"}` shape, so the
bundle cannot tell them apart. See `frontend/runtime-config`.

Every `runtime_config` key must be one the bundle reads: a field of the
frontend's config message, or a forge runtime key (`API_URL`, `ENVIRONMENT`,
`OTEL_ENDPOINT`, `CONSOLE_CAPTURE`, `REPLAY_SAMPLE_RATE`). `forge generate`
lists them in `src/lib/config_schema_gen.json` beside `config_gen.ts`, and every
render checks the env's `runtime_config` against the frontend directory's copy
— so a project declaring a frontend from ANOTHER repo gets a did-you-mean on a
typo instead of a field silently left on its default. No schema file (no config
message) means no check.

The schema also says which keys a document MUST carry, and the render refuses a
foreign frontend's `runtime_config` that lacks one (for an in-tree frontend the
typed KCL schema checks it). A field `required: true` with no default is
required in every mode. A field required only in one MODE says so with
`required_when`, a condition on a sibling string field:

```protobuf
string hosting = 6 [(forge.v1.config) = {env_var: "HOSTING", allowed_values: ["cloud", "self-hosted"]}];
string api_url = 1 [(forge.v1.config) = {
  env_var: "API_URL"
  required_when: [{field: "hosting", equals: "cloud"}]
}];
```

In the mode it names, the document must carry the key with a value other than
its default (the default is the other mode's value). `config_gen.ts`'s
`loadConfig()` throws on a miss, and exports `missingRequiredConfig()` for an app
that renders its own boot error. It is for frontend configs only: `forge
generate` refuses it on a binary config, alongside `required: true`, or naming a
field that is not a sibling string field.

A field a condition names (`hosting` above) is the document's MODE SELECTOR, and
a foreign frontend's `runtime_config` must state it, with a value, even for the
default mode. Without that rule the document that lost every key reads as a
valid default-mode one, since no condition holds, and the bundle boots in the
wrong mode with nothing failing. So give the default mode its own spelling in
`allowed_values` (`"self-hosted"` above) and have each env set it.

A frontend whose code is in ANOTHER repo (a sibling `path = "../other/web"` in
dev, or a source pin) never has a file written into that repo: forge renders its
`config.js` to `.forge/runtime-config/<frontend>/config.js` in the declaring
project, and `forge env up` hands the path to its dev server as
`FORGE_RUNTIME_CONFIG_FILE`. `forgeRuntimeConfig()` from
`@reliantlabs/forge-web-runtime/vite` serves it at `<base>config.js` (the
vite-spa scaffold includes it; it does nothing when the variable is unset).
