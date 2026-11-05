# @reliantlabs/sdk

The TypeScript SDK for [Reliant](https://github.com/reliant-labs/reliant). It is
versioned with Reliant's API and licensed Apache-2.0.

```ts
import { createReliantClient, staticTokenProvider } from "@reliantlabs/sdk/client";
import { createConnectTransport } from "@connectrpc/connect-web";

const reliant = createReliantClient({
  baseUrl: "https://api.reliantapi.com",
  auth: staticTokenProvider(token),
  createTransport: ({ baseUrl, interceptors }) =>
    createConnectTransport({ baseUrl, interceptors }),
});

const chat = reliant.chat({ chatId }).start();
chat.subscribe(() => render(chat.getState()));   // messages, streaming, approvals, ...
await chat.send("deploy it");                    // retry-safe: one id per logical send
```

## `/client`

Framework-free: no React, nothing Node-only, so it runs in a browser, an
Electron renderer, a worker or a server.

| | |
|---|---|
| `createReliantClient` | A transport (auth, error normalisation and tracing from `@reliantlabs/forge-web-runtime/interceptors`, plus your own interceptors) and the streams on it. |
| `ReconnectingStream`, `ChatStream`, `UserStream`, `ProcessOutputStream` | One reconnecting stream. Backoff that only a wake resets, a half-open watchdog, replace-on-resume, supersession of replaced connections. A chat stream resumes by sequence number: a replay of what it missed, or a snapshot when the server cannot replay, then exactly one caught-up marker. |
| `ChatUpdate`, `decodeChatUpdate`, `decodeUserUpdate` | The typed events and the one place that decodes them from the wire's JSON payloads. |
| `reduceChat`, `createChatState` | The pure fold of a chat's stream: snapshot replaces, replay merges, deltas build a placeholder under the pre-allocated message id, finalize markers retire it, tools never regress, results join their calls. `(state, action) -> state`, no clock, no store. |
| `ChatSession` | A chat, live: the stream, the state, and `send`/`retry` with ids that make a retry safe. Bind it to any framework with `subscribe`/`getState`. |

The host owns what it must: `AuthTokenProvider` (storage, refresh, sign-out),
the `WakeSource` (online/visibility signals; the default listens on `window`
when there is one), and any extra interceptors (upgrade prompts, error
reporting).

### Peer dependencies

`@bufbuild/protobuf`, `@connectrpc/connect`, `@opentelemetry/api` and
`@reliantlabs/forge-web-runtime`. Pass a Connect transport of your own with
`transport` to use a mock or your app's routing.

## Developing

This package is a member of the `frontends/web` pnpm workspace, so the web app
and the SDK share one copy of every dependency and the app compiles the SDK
from source.

```sh
cd frontends/web && pnpm install
cd ../../sdk/ts && npx tsc -p tsconfig.json    # typecheck
npx vitest run                                 # tests
node scripts/build.mjs                         # dist/
```

`src/gen/` is generated from Reliant's protos by `buf.gen.sdk-ts.yaml` (the
`sdk-ts-gen` hook of `forge generate`); never edit it. To expose another
service, add it to `types` in that template.

Not published by hand: the release train cuts it.
