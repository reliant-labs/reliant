// Mounts the hooks against a scripted Connect server, under a fresh
// QueryClient and a client with no wake source. Not part of the published package.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { createReliantClient } from "../client/client.js";
import type { WakeSource } from "../client/reconnecting-stream.js";
import { ReliantProvider } from "../react/context.js";
import { scriptedServer, type UnaryHandler } from "./scripted-server.js";

const noWake: WakeSource = { subscribe: () => () => {}, isVisible: () => true };

export function harness(unary: Record<string, UnaryHandler> = {}) {
  const server = scriptedServer(unary);
  const client = createReliantClient({ transport: server.transport, wake: noWake });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } });
  const wrapper = ({ children }: { children?: ReactNode }) => (
    <QueryClientProvider client={queryClient}>
      <ReliantProvider client={client}>{children}</ReliantProvider>
    </QueryClientProvider>
  );
  return {
    server,
    client,
    queryClient,
    wrapper,
    render: <T,>(hook: () => T) => renderHook(hook, { wrapper }),
  };
}

/** Let promises, microtasks and 0ms timers settle inside act(). */
export const settle = () =>
  act(async () => {
    await Promise.resolve();
    await new Promise((r) => setTimeout(r, 0));
    await Promise.resolve();
  });
