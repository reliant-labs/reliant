/**
 * Fixture mode in lib/connect.ts (installFixtureTransport): each route gets a
 * fixture transport built with THAT route's real interceptor chain, built
 * once, and the precedence is test seam > shell > fixtures > mock > live.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { createRouterTransport, type Interceptor, type Transport } from "@connectrpc/connect";

import { buildInterceptors } from "@/api/transport";
import { UserService } from "@/gen/controlplane/services/user/v1/user_pb";
import { SettingsService } from "@/gen/reliant/v1/settings_pb";
import { StreamingService } from "@/gen/reliant/v1/streaming_pb";
import { connectClient, installFixtureTransport, installShellTransport, setTransport, type TransportRoute } from "@/lib/connect";

vi.mock("@/lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() },
}));

/** A transport that answers ListSettings with one setting naming `who`, through no interceptor. */
const answering = (who: string): Transport =>
  createRouterTransport((router) => router.service(SettingsService, { listSettings: () => ({ settings: [{ key: who, value: who }] }) }));

const listSettings = async () => (await connectClient(SettingsService).listSettings({})).settings[0]?.key;

afterEach(() => {
  installFixtureTransport(null);
  installShellTransport(null);
});

describe("installFixtureTransport", () => {
  it("builds one transport per route, with that route's real interceptor chain", async () => {
    const made: { route: TransportRoute; interceptors: Interceptor[] }[] = [];
    installFixtureTransport((route, interceptors) => {
      made.push({ route, interceptors });
      return createRouterTransport((router) => {
        router.service(SettingsService, { listSettings: () => ({}) });
        router.service(UserService, { getCurrentUser: () => ({}) });
        router.service(StreamingService, {});
      });
    });
    await connectClient(SettingsService).listSettings({});
    await connectClient(SettingsService).listSettings({});
    await connectClient(UserService).getCurrentUser({}).catch(() => undefined);

    expect(made.map((m) => m.route)).toEqual(["reliant-api", "control-plane"]);
    // The interceptors are module-level functions: the same chain, by identity.
    expect(made[0].interceptors).toEqual(buildInterceptors({ withAuth: true, backend: "reliant-api" }));
    expect(made[1].interceptors).toEqual(buildInterceptors({ withAuth: true, backend: "control-plane" }));
  });

  it("sits below the test seam and a shell's transport, and above the live routes", async () => {
    installFixtureTransport(() => answering("fixtures"));
    expect(await listSettings()).toBe("fixtures");

    installShellTransport(answering("shell"));
    expect(await listSettings()).toBe("shell");

    const restore = setTransport(answering("seam"));
    expect(await listSettings()).toBe("seam");
    restore();
    installShellTransport(null);
    expect(await listSettings()).toBe("fixtures");
  });

  it("consults no base URL, so a route with none configured still answers", async () => {
    // No CONTROL_PLANE_API_URL in this test's config: the live route would throw "not configured".
    installFixtureTransport(() => createRouterTransport((router) => router.service(UserService, { getCurrentUser: () => ({}) })));
    await expect(connectClient(UserService).getCurrentUser({})).resolves.toBeDefined();
    installFixtureTransport(null);
    await expect(connectClient(UserService).getCurrentUser({})).rejects.toThrow(/Control plane API URL not configured/);
  });
});
