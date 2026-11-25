// Copyright (c) 2025 Reliant Labs

/**
 * A fake controlplane.v1.ChangesService behind the app's transport
 * (setTransport): every call is recorded as its protojson request, and each
 * handler answers in protojson too, so a test reads and asserts the wire as
 * the control plane sees it. A method without a handler answers
 * Unimplemented, as a control plane without it does.
 */

import { fromJson, toJson, type JsonValue } from "@bufbuild/protobuf";
import { Code, ConnectError, createRouterTransport, type ServiceImpl } from "@connectrpc/connect";

import { ChangesService } from "@/gen/controlplane/services/changes/v1/changes_pb";
import { setTransport } from "@/lib/connect";

export type FakeHandler = (body: Record<string, unknown>) => unknown;

export interface FakeChangesService {
  calls: { method: string; body: Record<string, unknown> }[];
  of: (method: string) => { method: string; body: Record<string, unknown> }[];
  /** Puts the app's transport back. */
  restore: () => void;
}

/** Installs the fake as the app's transport; handlers are keyed by rpc name (`ListChanges`). */
export function installFakeChangesService(handlers: Record<string, FakeHandler>): FakeChangesService {
  const calls: FakeChangesService["calls"] = [];
  const impl: Record<string, (req: never) => Promise<unknown>> = {};
  for (const method of ChangesService.methods) {
    impl[method.localName] = async (req: never) => {
      const body = toJson(method.input, req) as Record<string, unknown>;
      calls.push({ method: method.name, body });
      const handler = handlers[method.name];
      if (!handler) throw new ConnectError(`no handler for ${method.name}`, Code.Unimplemented);
      // Through JSON text, so a fixture's `field: undefined` reads as unset.
      return fromJson(method.output, JSON.parse(JSON.stringify(await handler(body))) as JsonValue);
    };
  }
  const restore = setTransport(createRouterTransport(({ service }) => service(ChangesService, impl as unknown as Partial<ServiceImpl<typeof ChangesService>>)));
  return { calls, of: (method) => calls.filter((c) => c.method === method), restore };
}
