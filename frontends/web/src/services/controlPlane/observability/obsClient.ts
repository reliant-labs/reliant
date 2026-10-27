// Copyright (c) 2025 Reliant Labs

/**
 * The control-plane client for services whose reads spend the org's ClickHouse
 * query quota (ObservabilityService, IssuesService). It is getControlPlaneClient
 * with one addition: a call made as a background poll (quotaStore.runAsPoll)
 * carries `Forge-Poll: 1`, so the server can refuse polls early to keep a
 * reserve for a person's own query.
 */

import type { DescService } from "@bufbuild/protobuf";
import type { Client } from "@connectrpc/connect";

import { getControlPlaneClient } from "../client";
import { callOptions, isPollRead } from "./quotaStore";

export function obsClient<S extends DescService>(service: S): Client<S> {
  const base = getControlPlaneClient(service);
  return new Proxy(base, {
    get(target, prop, receiver) {
      const member = Reflect.get(target, prop, receiver) as unknown;
      if (typeof member !== "function") return member;
      return (request: unknown, options?: { headers?: Record<string, string> }) => {
        const poll = callOptions({ poll: isPollRead() });
        if (!poll) return (member as (...a: unknown[]) => unknown).call(target, request, options);
        return (member as (...a: unknown[]) => unknown).call(target, request, { ...options, headers: { ...options?.headers, ...poll.headers } });
      };
    },
  });
}
