// Copyright (c) 2025 Reliant Labs

/**
 * lib/connect.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts). The demo has no backend: EVERY Connect call
 * goes to the transport the demo shell installs at boot (demoShellBoot.ts:
 * forge's mock transport with the `demo` scenario), and there is no other
 * route, base URL, interceptor chain, credential or daemon in this bundle.
 *
 * An RPC the scenario has no sample data for is refused by the mock engine
 * with Unimplemented; it reaches the page as SAMPLE_DATA_UNAVAILABLE, which
 * the console's error states print, rather than as an engine message.
 */

import type { DescService } from "@bufbuild/protobuf";
import { Code, ConnectError, createClient, type Client, type Transport } from "@connectrpc/connect";

import type * as Real from "./connect";

/** What a page shows for a read the demo has no sample data for. */
export const SAMPLE_DATA_UNAVAILABLE = "Sample data isn't available for this in the demo.";

let _override: Transport | null = null;
let _shell: Transport | Promise<Transport> | null = null;

export const installShellTransport: typeof Real.installShellTransport = (t) => {
  _shell = t;
};

export const setTransport: typeof Real.setTransport = (t) => {
  const previous = _override;
  _override = t;
  // Match the app seam: an older preview's cleanup must not remove a newer
  // override, and normal cleanup restores the transport it replaced.
  return () => { if (_override === t) _override = previous; };
};

async function target(): Promise<Transport> {
  if (_override) return _override;
  if (_shell) return _shell;
  throw new ConnectError("The demo's transport is not installed (demoShellBoot.ts).", Code.Unavailable);
}

function friendly(err: unknown): never {
  const error = ConnectError.from(err);
  if (error.code === Code.Unimplemented) throw new ConnectError(SAMPLE_DATA_UNAVAILABLE, Code.Unimplemented, undefined, undefined, error);
  throw error;
}

export const transport: typeof Real.transport = {
  async unary(method, signal, timeoutMs, header, input, contextValues) {
    const t = await target();
    try {
      return await t.unary(method, signal, timeoutMs, header, input, contextValues);
    } catch (err) {
      friendly(err);
    }
  },
  async stream(method, signal, timeoutMs, header, input, contextValues) {
    const t = await target();
    try {
      return await t.stream(method, signal, timeoutMs, header, input, contextValues);
    } catch (err) {
      friendly(err);
    }
  },
};

const _clients = new WeakMap<DescService, Client<DescService>>();

export function connectClient<S extends DescService>(service: S): Client<S> {
  let client = _clients.get(service);
  if (!client) {
    client = createClient(service, transport);
    _clients.set(service, client);
  }
  return client as Client<S>;
}
// The same signature as the real module's.
connectClient satisfies typeof Real.connectClient;
