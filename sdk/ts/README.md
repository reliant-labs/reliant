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
| `fetchContentBlockBodies` | The tool inputs, results and thinking a `toolBodies: "stub"` read left out, by block id: batched 100 a request, deferred ids asked again. Also `ChatSession.fetchContentBlockBodies`. |

The host owns what it must: `AuthTokenProvider` (storage, refresh, sign-out),
the `WakeSource` (online/visibility signals; the default listens on `window`
when there is one), and any extra interceptors (upgrade prompts, error
reporting).

### Tool bodies on demand

A long agent chat is mostly tool inputs and outputs. `toolBodies: "stub"` (on
`reliant.chat(...)` or `reliant.streams.chat(...)`) opens it without the
bodies of its finished calls: the snapshot, the replay and `loadOlder`'s pages
send each such block with `bodyElided` set and `bodySize` its stored length. A
call's `input` is then a skeleton (every key kept, long strings cut) that
labels the call but must not be rendered as its input; a result is a short
stub, also `truncated`, and its `toolResults` entry has `elided: true` and the
`block_id` to fetch it by. Live frames and calls in flight are always whole.

```ts
const chat = reliant.chat({ chatId, toolBodies: "stub" }).start();
// The user expands a call: its input by the call block's id, its result by the result block's.
const { bodies, notFound } = await chat.fetchContentBlockBodies(
  [callBlock.id, chat.getState().toolResults[callId].block_id],
  { full: false }, // true: results as stored, for copy and export
);
```

Bodies stay out of `ChatState` (a finished call's body never changes: cache
them yourself, per block id). The reducer keeps what it holds: a stub never
displaces a body it has, by block id, in a message or in `toolResults`, and a
body always replaces a stub, so a reconnect does not blank a call watched live.
The default is `"full"`, what every client has always had; a server that
predates the option sends full bodies, so act on each block's `bodyElided`,
never on the mode asked for.

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
