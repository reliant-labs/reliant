# Workflow editor: user feedback from dev (2026-10-06) and the fix plan

This is a brief for two build streams, E and G, and one review stream, R. The
verified facts are from reliant main `e70b4f4e`. Treat them as settled.

## The user's feedback, verbatim in substance

1. "Sign in isn't set up on this deployment: RELIANT_OAUTH_GMAIL_CLIENT_ID and
   RELIANT_OAUTH_GMAIL_CLIENT_SECRET are not set"
2. There is no Integrations item in the left Add-step dropdown. Integrations only
   show up when you search.
3. "Why do we have Execute Tools and Invoke Tool?"
4. "Lots of inputs are super unclear about what values look like." Example: the
   Execute Tools "Tool calls" field is an empty box with only an Aa/{} toggle.
5. "Twilio needs SID in parameters, and the account connection?"
6. "Should we add more params to some of the fetch options?"
7. Adding triggers could be easier. Should triggers be a box on the left-hand
   side of the canvas? "What if I don't want some chats to be able to create a
   workflow?"
8. Add the logos for Gmail, Twilio, etc.
9. Seen in a screenshot: the Workflow start panel says `trigger.kind` is
   `"chat.start", "schedule" or "agent.start_run"`, but webhook, integration,
   workflow_event and builder.test are missing.

## Verified facts

- **(1)** The text comes from
  `web/src/components/workflow/connections/ConnectIntegrationDialog.tsx:199`.
  - It renders `methodStatus.unavailableReason` verbatim, so an env-var name
    reaches end users.
  - The API logs "connection method unavailable" for github, gmail and slack
    oauth2 in dev.
  - The OAuth client env is `RELIANT_OAUTH_<ID>_CLIENT_ID/_SECRET`
    (`internal/connections/providers.go:29`).
  - The callback path is `/connections/oauth/callback`
    (`internal/connections/oauth.go:33`).
  - In the dev stack the PUBLIC_URL is `http://localhost:3091`
    (`_reliant_public_url` in control-plane `deploy/kcl/dev/main.k:289`).
  - Wiring a real dev client needs a Google OAuth client from the user.
- **(2)** `web/src/components/workflow/palette/StepPalette.tsx` lists built-in
  groups. Integration actions and triggers come only from `useCatalogSearch`
  when `query` is non-empty (around line 185). `paletteItems.ts` has
  `CatalogPaletteItem`.
- **(3)** They are two different nodes, but nothing in the editor explains the
  difference:
  - `execute_tools` runs the tool calls an upstream `call_llm` produced. Its
    input is `tool_calls`, normally `{{nodes.<call_llm>.tool_calls}}`. It is the
    low-level half of an agent loop that the `Agent` node already packages.
  - `invoke_tool` (`internal/workflow/runtime/activities/handlers/invoke_tool.go`)
    calls ONE named tool with parameters the workflow author writes. No LLM is
    involved.

  In the palette both sit under AGENTIC, and Invoke Tool has the same robot icon
  as Call LLM.
- **(5)** It is NOT a design flaw. In `internal/integrations/catalog/twilio/manifest.yaml`:
  - The Account SID comes from the connection. The base URL uses
    `{{ connection.params.account_sid }}`, and auth is basic with
    `username_param: account_sid`.
  - The required `sid` param shown is on `message.get` (path
    `/Messages/{{ params.sid }}.json`). It is the MESSAGE SID (`SM…`). The form
    labels it "Sid" with no description or example, which is why it reads like
    the account SID is being asked for twice. This is an instance of (4).
- **(6)** The HTTP integration's `request` action
  (`internal/integrations/catalog/http/manifest.yaml`) takes url, method,
  headers, query, body and connection. It has no timeout and no response-format
  control. The `fetch` agent tool (`internal/llm/tools/fetch.go`) takes url,
  format, timeout and max_size. It is a page reader, not an API client.
- **(8)** Manifests already carry `icon:` names: http `globe`, plus `slack`,
  `gmail`, `github` and `twilio` (proto `IntegrationManifest.icon` = 5). The web
  has no brand-logo mapping (grep for `'twilio'`/`'gmail'` in web/src finds
  nothing).
- **(9)** The text is in `web/src/lib/trigger-cel-fields.ts:28`. The kinds are
  defined in `internal/db/core/trigger.go`: chat.start, schedule,
  agent.start_run, builder.test, webhook, integration and workflow_event.
- Declared triggers already exist in YAML (`triggers:`) and have
  `web/src/components/workflow/config/DeclaredTriggerPanel.tsx`.
  `TriggerPayloadPanel.tsx` is the Workflow start panel.
- **Standing requirement** (project memory): every workflow feature must work in
  all three surfaces:
  1. YAML
  2. the create/edit_workflow agent tools
  3. the UI builder

  With hundreds of integrations, browsing needs search plus sensible defaults,
  not a full dump.

## Streams and file ownership

| Area | E (palette, naming, logos) | G (fields, params, connections) | R (UX review) |
|---|---|---|---|
| `palette/*`, `StepPalette`, `paletteItems.ts` | owns | — | read-only |
| `node-metadata.ts` (icons, colours, display names, groups) | owns | — | read-only |
| Brand logo component + assets | owns | uses it | read-only |
| Node creation defaults (e.g. auto-wire `tool_calls` when Execute Tools is added after Call LLM) | owns | — | — |
| `trigger-cel-fields.ts` (item 9) | owns | — | — |
| Config-panel field rendering: placeholders, examples, descriptions, type hints | — | owns | — |
| Manifest params (titles, descriptions, examples) in `internal/integrations/catalog/*` | — | owns | — |
| HTTP request params (item 6) | — | owns | — |
| `ConnectIntegrationDialog` messaging (item 1) | — | owns | — |
| Workflow node field schemas/descriptions (backend) | — | owns | — |
| Triggers on canvas, restricting workflow authoring (item 7) | — | — | designs options; no code |
