// Copyright (c) 2025 Reliant Labs

/**
 * A transport SCOPE: a subtree of the app that reads its own Connect
 * transport and keeps its own query cache, while everything outside it stays
 * on the app's transport (lib/connect.ts). The case it exists for is the
 * forge tour's sample app: its console pages and the shell nav's console
 * model read the shop fixture pack, and the person's chats, projects and org
 * stay live.
 *
 * It is `@reliantlabs/forge-web-runtime/transport-scope` (forge#2735,
 * published in web-runtime 0.13.23), re-exported so callers keep importing
 * `@/lib/transportScope`. There are NO reliant extensions; keep it that way.
 *
 * In reliant:
 *   - the fallback is the module transport: `useTransport(transport)` and
 *     `useClient(Svc, connectClient(Svc))`, both from lib/connect.ts (its
 *     routing and test seam included);
 *   - a scope that should cache and retry like the rest of the app passes
 *     `queryClient: createAppQueryClient()` (lib/query-client.ts): forge's
 *     default is a bare `new QueryClient()`;
 *   - `readOnly` is a flag for the UI only. The scope enforces nothing: a
 *     read-only scope's TRANSPORT refuses writes (the fixture transport's
 *     readOnly, src/fixtures/transport.ts).
 */

export * from "@reliantlabs/forge-web-runtime/transport-scope";
