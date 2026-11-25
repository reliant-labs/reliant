/**
 * The client must never be what ends the live update stream.
 *
 * Prod (read-path-latency, 2026-10-09): Envoy recorded the BROWSER resetting
 * 157 of 162 StreamUserUpdates streams, 25 of them at exactly 10.0s — the
 * length of the default unary budget (DEFAULT_GRPC_TIMEOUT_MS). Every one of
 * those was followed by a new stream and a ListChats/ListArchivedChats/
 * GetChat/ListDaemons refetch burst. A server-streaming call is open for as
 * long as the page is; its lifetime is its owner's to manage (heartbeat
 * watchdog, reconnect), never a request deadline.
 *
 * Pinned here against every server-streaming method the generated services
 * declare — so a stream added later cannot inherit the 10s default.
 */

import { describe, expect, it, vi } from "vitest";
import type { DescService } from "@bufbuild/protobuf";

vi.mock("@/lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() },
}));

describe("every server-streaming RPC the web can call", () => {
  const modules = import.meta.glob<Record<string, unknown>>("../../gen/reliant/v1/*_pb.ts", {
    eager: true,
  });
  const streams: Array<{ service: string; method: string }> = [];
  for (const mod of Object.values(modules)) {
    for (const value of Object.values(mod)) {
      const service = value as Partial<DescService>;
      if (service?.kind !== "service" || !service.methods) continue;
      for (const method of service.methods) {
        if (method.methodKind === "server_streaming") {
          streams.push({ service: service.typeName!, method: method.name });
        }
      }
    }
  }

  it("includes both update streams (the scan works)", () => {
    expect(streams).toContainEqual({
      service: "reliant.v1.StreamingService",
      method: "StreamUserUpdates",
    });
    expect(streams).toContainEqual({
      service: "reliant.v1.StreamingService",
      method: "StreamChatUpdates",
    });
  });

  it("has no client deadline", async () => {
    const { timeoutForProcedure } = await import("../transport");
    const withDeadline = streams.filter(
      ({ service, method }) => timeoutForProcedure(service, method) !== 0,
    );
    expect(withDeadline).toEqual([]);
  });
});
