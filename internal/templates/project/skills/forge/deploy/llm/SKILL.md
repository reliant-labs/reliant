---
name: deploy/llm
description: Call an LLM from a forge app — declare forge.LLM per env (forge.OnHosted = Reliant's gateway, OpenAI- and Anthropic-compatible; forge.LLMEndpoint = any compatible endpoint), give a workload forge.llm_env(), then use the stock OpenAI or Anthropic SDK with no configuration in code. The per-env key, spend caps and model tiers on Reliant hosting (claimed and unclaimed claimable deploys), what is refused, and the errors you will see.
---

# LLM calls: forge.LLM + forge.llm_env()

An app never holds a provider key. On Reliant hosting it calls **Reliant's
gateway** with a key the platform mints **per environment**, capped and billed
to the org. The gateway speaks both wire formats, so the official SDKs work by
base-URL swap:

| API | path | SDK reads |
|---|---|---|
| OpenAI-compatible chat | `POST $OPENAI_BASE_URL/chat/completions` (= `<root>/v1/...`) | `OPENAI_BASE_URL`, `OPENAI_API_KEY` |
| Anthropic-compatible messages | `POST $ANTHROPIC_BASE_URL/v1/messages` | `ANTHROPIC_BASE_URL`, `ANTHROPIC_API_KEY` |
| OpenAI image generation | `POST $OPENAI_BASE_URL/images/generations` | same as chat |

Streaming (`stream: true`) works on both. There is no `/v1/models`: pick a
model id from the table in section 4.

## 1. Declare the LLM, per env

`Bundle.llm` says where the env's model gateway is. There is no default.

```kcl
# deploy/kcl/prod/main.k (and staging): Reliant's gateway
bundle = forge.Bundle {
    llm = forge.LLM {runtime = forge.OnHosted {}}
    ...
}
```

```kcl
# deploy/kcl/dev/main.k: the same gateway, with your own token
bundle = forge.Bundle {
    llm = forge.LLM {runtime = forge.LLMEndpoint {
        base_url = "https://admin.reliantapi.com"
        api_key = forge.ManagedSecret {name = "RELIANT_LLM_API_KEY"}
    }}
    ...
}
```

For dev, create a personal access token with the `llm:invoke` scope on the
Reliant console's access-token page (an `rlat_` token; `reliant auth token
create` mints `reliant:api` only, which the gateway refuses) and store it:
`forge secret set --env dev RELIANT_LLM_API_KEY`. It is billed to you. A local
model works too: `forge.LLMEndpoint {base_url = "http://localhost:11434",
api_key = "ollama"}` (Ollama; a literal key is accepted only for an endpoint on
this machine, and `openai_base_url` overrides the derived `<base_url>/v1`).

## 2. Give the workload the variables

```kcl
# deploy/kcl/workloads.k
api = fw.Workload {
    name = "api"
    env = forge.llm_env() | {LLM_MODEL = "claude-haiku-4-5"}
    ...
}
```

`forge.llm_env()` is six `forge.LLMRef`s:

| variable | holds |
|---|---|
| `RELIANT_LLM_BASE_URL` | the gateway root (an OpenAI-compatible client takes it + `/v1`) |
| `RELIANT_LLM_API_KEY` | the key |
| `OPENAI_BASE_URL` | root + `/v1` |
| `OPENAI_API_KEY` | the key |
| `ANTHROPIC_BASE_URL` | root |
| `ANTHROPIC_API_KEY` | the key |

**On `forge.OnHosted` these names are the contract.** forge renders each one
blank and the platform's workload operator fills it when it runs the pod (the
key from the env's Secret `reliant-llm-auth`, the URLs from its own config), the
way it fills the OTLP variables. So:

- use `forge.llm_env()`, or an `LLMRef` under one of those names with its own
  key; any other name is refused at render (it would stay blank forever);
- do not set these names yourself in the same workload: a value you set wins
  and the platform leaves it alone;
- a workload that is not hosted (host, compose, cluster) cannot read a hosted
  LLM and is refused at render: bind that env to a `forge.LLMEndpoint`.

**On a `forge.LLMEndpoint`** each variable is the declared value, and the key
reaches the workload the way its runtime delivers that reference: a hosted or
cluster pod reads a `forge.ManagedSecret` from the env's store (a cluster pod
can also read a `forge.SecretRef`), and `forge env up` hands a host process
the store's value under each key variable (one `forge secret set --env dev
RELIANT_LLM_API_KEY` feeds all three). A **compose** container's environment
is the compose file's, and the compose process env forge passes it holds
literals, so a referenced key is refused for a compose workload: give it the
URLs only and let the compose file read the key from the store, which is in
the compose process env under its store name:

```kcl
env = {k: v for k, v in forge.llm_env() if not k.endswith("_API_KEY")}
```

```yaml
# docker-compose.yml
environment:
  OPENAI_BASE_URL: ${OPENAI_BASE_URL}
  OPENAI_API_KEY: ${RELIANT_LLM_API_KEY}
```

## 3. Call it: no configuration in code

```ts
import OpenAI from "openai";
const openai = new OpenAI(); // OPENAI_BASE_URL + OPENAI_API_KEY
const r = await openai.chat.completions.create({
  model: process.env.LLM_MODEL ?? "claude-haiku-4-5",
  messages: [{ role: "user", content: prompt }],
});
```

```ts
import Anthropic from "@anthropic-ai/sdk";
const anthropic = new Anthropic(); // ANTHROPIC_BASE_URL + ANTHROPIC_API_KEY
const m = await anthropic.messages.create({
  model: "claude-haiku-4-5", max_tokens: 1024,
  messages: [{ role: "user", content: prompt }],
});
```

```go
// github.com/openai/openai-go: NewClient reads OPENAI_BASE_URL and OPENAI_API_KEY.
client := openai.NewClient()
resp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
    Model:    "claude-haiku-4-5",
    Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage(prompt)},
})
```

Python's `openai.OpenAI()` and `anthropic.Anthropic()` read the same variables.
Any other client: base URL `$RELIANT_LLM_BASE_URL/v1` (OpenAI format) or
`$RELIANT_LLM_BASE_URL` (Anthropic format), bearer `$RELIANT_LLM_API_KEY`.

Make the model id configuration (an env var or a config field), not a literal
in code: the tiers below differ between an unclaimed and a claimed deploy.

## 4. Caps, models and who pays (Reliant hosting)

One key per **environment**, shared by its LLM-declaring workloads, enforced at
the gateway:

| | unclaimed claimable deploy | claimed / signed-in |
|---|---|---|
| models | cheap tier: `claude-haiku-4-5`, `gemini-3.8-flash` | the curated catalog: `claude-haiku-4-5`, `claude-sonnet-5-5`, `claude-opus-5-5`, `gemini-3.8-flash`, `gemini-3.1-pro-preview`, … |
| spend cap | **$1 per env, for the deploy's life**; 20 requests/min, 2 in flight | **$50 per env per 30 days** by default, and the org's credit |
| billed to | the platform (free) | the org's wallet, like every other Reliant LLM call |

Claiming the deploy replaces the key (the pods roll onto the claimed tier on
the claim's first reconcile); you change nothing. Deleting the env revokes it.

## 5. Unclaimed (`--claimable`) deploys

An unclaimed backend has **no outbound internet** (`deploy/hosting`), so
`api.openai.com` and every other provider fail by design. Declaring
`forge.LLM {runtime = forge.OnHosted {}}` with `forge.llm_env()` configures
Reliant's gateway contract, but does **not** prove an unclaimed app can use it.

Until the platform's in-cluster route to the gateway is enabled for unclaimed
compute, the variables of an unclaimed pod stay **blank** (the workload is not
failed for it). Write the app so a missing key degrades (a "LLM unavailable"
message, or the feature hidden), never a crash at startup. A claimed deploy
gets its key on the normal hosted network.

For a direct third-party provider key, ask the user to claim the deploy and
arrange backend billing first, then `forge login`, store the key as a managed
secret and redeploy without `--claimable`. Keep the key on a backend, never in
a browser bundle. Verify an actual server-side model response: a successful
render or healthy rollout alone does not prove model access. The public
[recovery guide](https://docs.reliantlabs.io/forge/deploy-without-an-account#what-claiming-gives-you)
links to the detailed LLM configuration and verification flow.

## 6. Errors you will see

| response | meaning | do |
|---|---|---|
| 429 `insufficient_quota` | the org is out of Reliant credit (claimed) | add credit; not a retry |
| 400/429 mentioning a budget | the env's spend cap is used up | wait for the window, or raise the cap (claimed) |
| 401 / 403 naming the model | the model is not in this env's tier | use a model from section 4 |
| 401 `unauthorized` | the key is missing or revoked | on hosting, check the workload uses `forge.llm_env()` and nothing else sets those names; a key is replaced by rolling the pods, so an old pod can briefly hold a revoked one |
| 503 with `Retry-After` | the gateway could not load the key | retry with backoff |
| connection refused / no such host, unclaimed | the in-cluster route is not enabled yet (section 5) | degrade |

## 7. What is refused at render

- an `LLMRef` with no `Bundle.llm`;
- a hosted `forge.LLM` read from a host, compose or cluster workload;
- an `LLMRef` on a hosted LLM under a non-canonical name;
- a literal `api_key` for an endpoint that is not on this machine (an
  `http://` URL whose host is exactly `localhost`, `127.0.0.1`, `[::1]`,
  `host.docker.internal` or `host.k3d.internal`);
- an endpoint URL with user info, a query or a trailing slash;
- an endpoint read by a hosted workload whose host is not a public DNS name:
  any IP literal (IPv4 or IPv6, public or private, in any spelling such as
  `0177.0.0.1` or `2130706433`), a single-label name, or `*.localhost` /
  `*.local` / `*.internal` / `*.svc` / `*.lan` / `*.home.arpa`. From
  Reliant's network such a host may be the platform's;
- an endpoint read by a hosted workload over plaintext `http://` (either
  `base_url` or `openai_base_url`): the key would cross the internet in the
  clear;
- a `forge.SecretRef` key on an endpoint read by a hosted workload (use
  `forge.ManagedSecret`).
- a `forge.ManagedSecret` or `forge.SecretRef` key read by a compose workload
  (drop the key variables and read the key in the compose file, section 2).
