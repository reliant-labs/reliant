---
name: llm
description: Using an LLM from a forge app's Go backend — the decision first (agentic or judgement work belongs in a Reliant workflow and keeps the app LLM-free; small inline classify, summarise or extract calls may use the gateway), then the call itself with the official anthropic-go and openai-go clients unmodified, model names, who pays, and the errors (out of credit, budget, video routes). Declaring the gateway in KCL is deploy/llm.
---

# LLM in a forge app

An app never holds a model key. Reliant's gateway speaks the OpenAI and
Anthropic wire formats and bills the environment's org wallet; the platform
puts the credentials in the pod. This skill is how to *use* it. Declaring it
(`forge.LLM`, `forge.llm_env()`) is `deploy/llm`.

## 1. Workflow or inline call?

| the work is | do it in |
|---|---|
| agentic: several steps, tools, loops, or a judgement the business depends on (triage, planning, "should we do X") | a **Reliant workflow** (`.reliant/workflows/<name>.yaml`) that calls the app's API through fixed steps. The app stays LLM-free |
| one bounded call with a structured result: classify, summarise, extract, rewrite | an **inline call** from a Go handler or worker |

The split that works: **the LLM proposes, code decides.** Money, limits,
approvals and halts are rules in the app (`internal/...`), enforced whatever
the model says. The workflow's agent can call only the endpoints you expose
to it, and none of them may approve its own decision or reach a third-party
system. An inline call follows the same rule: its output is untrusted input,
validated like a request body, and it never carries a decision that code
should make. If an inline call grows tools or a loop, move it to a workflow.

## 2. Wire it

```kcl
# deploy/kcl/prod/main.k   (and staging)
llm = forge.LLM {runtime = forge.OnHosted {}}

# deploy/kcl/workloads.k
env = forge.llm_env() | {LLM_MODEL = "claude-haiku-4-5"}
```

Dev and your own cluster use a `forge.LLMEndpoint`: `deploy/llm`.

## 3. Call it: Go, official clients, no configuration

```go
// github.com/anthropics/anthropic-sdk-go: reads ANTHROPIC_BASE_URL + ANTHROPIC_API_KEY
client := anthropic.NewClient()
msg, err := client.Messages.New(ctx, anthropic.MessageNewParams{
    Model:     anthropic.Model(cfg.LLMModel), // "claude-haiku-4-5"
    MaxTokens: 512,
    Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(text))},
})
```

`openai.NewClient()` (`github.com/openai/openai-go/v3`) reads `OPENAI_BASE_URL`
and `OPENAI_API_KEY` the same way. Use the clients **unmodified**: no wrapper,
no `option.WithBaseURL`, no key in code or config.

- **Model names** are the gateway catalog spellings (`claude-haiku-4-5`,
  `gemini-3.8-flash`, ...; list in `deploy/llm`). There are no tags or aliases
  such as "fast" yet. Keep the name in config, not a literal.
- **Go only, backend only.** Never call from the frontend (the key would ship
  to the browser): the frontend calls your RPC, the handler calls the model.
- Put a timeout on `ctx`. Cap `MaxTokens`. A user-facing rpc should not wait
  on a model for minutes: queue it to a worker.

## 4. Who pays, and what stops

Each claimed hosted environment has its own key, billed to **its org's
wallet**, with a default cap of **$50 per 30 days**. The key exists only for
**claimed hosted environments**: an unclaimed deploy and plain local dev have
none, so the variables are blank. Write the feature to degrade ("LLM
unavailable") when `ANTHROPIC_API_KEY` is empty, never to crash at startup.

| response | meaning | do |
|---|---|---|
| 429 `insufficient_quota` | the org wallet is empty | surface "out of credit"; **do not retry** |
| 429 or 400 naming a budget or rate limit | the env's cap or rate limit | back off, or tell the user it is used up until the window resets |
| 403 `env_key_route_refused` | video routes are refused to environment keys | not an env-key feature |
| 401 / 403 naming a model | the key's tier does not list that model | change the model |
| 401 `unauthorized` | key missing or revoked | check the workload has `forge.llm_env()` |

Both SDKs retry a 429 twice by default, which repeats a billed call that
`insufficient_quota` will refuse again. Turn retries off and handle the 429
yourself. `option.WithMaxRetries` (each SDK's own `option` package) is a
`RequestOption`: pass it to `NewClient` for every call, or as the trailing
argument of one call to override it there:

```go
client := anthropic.NewClient(option.WithMaxRetries(0)) // every call
msg, err := client.Messages.New(ctx, params, option.WithMaxRetries(0)) // or just this one

var apierr *anthropic.Error
if errors.As(err, &apierr) && apierr.StatusCode == http.StatusTooManyRequests &&
    strings.Contains(apierr.RawJSON(), "insufficient_quota") {
    return svcerr.FailedPrecondition("out of Reliant credit")
}
```

(`openai.Error` has the same `StatusCode` and `RawJSON()`.)

## Rules

- The app holds no provider key, ever, and the frontend never calls a model.
- Treat model output as untrusted input; validate before it touches data.
- Hard rules live in code, not in a prompt.
- Judgement or multi-step work goes to a workflow, not a loop in a handler.
