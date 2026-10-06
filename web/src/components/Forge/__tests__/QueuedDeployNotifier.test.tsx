// Copyright (c) 2025 Reliant Labs

/**
 * The notification for a deploy this session saw queued: it fires ONCE, when
 * the release is confirmed running, from whichever Live reading lands in the
 * cache — and under the user's notification settings, like every other
 * "it finished" in the app.
 */

import { act, render } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { LiveConvergenceState, LiveEnv } from "@/services/forge/live";

const navigate = vi.fn();
const location = { pathname: "/settings/billing" };
vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigate,
  useLocation: () => location,
}));

vi.mock("@/services/controlPlane/config", () => ({
  CONTROL_PLANE_API_URL: "http://127.0.0.1:8090",
  hasControlPlane: true,
}));

// The watch's own polls never answer here: every reading the test wants is
// put in the cache directly, which is exactly how a page's reading reaches it.
vi.mock("@/services/forge/live", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/forge/live")>()),
  getLiveView: () => new Promise(() => {}),
}));

const showNotification = vi.fn((..._args: unknown[]) => true);
vi.mock("@/lib/notifications", () => ({
  showNotification: (...args: unknown[]) => showNotification(...args),
}));

const shouldShowNotificationSync = vi.fn((_viewing: boolean) => true);
vi.mock("@/store/notificationStore", () => ({
  shouldShowNotificationSync: (viewing: boolean) => shouldShowNotificationSync(viewing),
  getNotificationSoundOptions: () => ({ enabled: true }),
}));

import { forgeKeys } from "@/hooks/forge-queries";
import { useQueuedDeployStore } from "@/store/queuedDeployStore";

import { QueuedDeployNotifier } from "../QueuedDeployNotifier";

function prod(args: { queued?: boolean; observed?: LiveConvergenceState }): LiveEnv {
  return {
    id: "cp-prod",
    name: "prod",
    project: "hounders",
    kind: "persistent",
    declaredShape: null,
    declaredBy: null,
    release: "v13",
    releaseProvenance: null,
    promotedByActor: "",
    promotedByUserId: "",
    phase: args.queued ? "held" : "unspecified",
    observed: { state: args.queued ? "queued" : (args.observed ?? "not-reported") },
    drift: { state: "not-reported" },
    driftDetail: "",
    provenance: "",
    holds: args.queued
      ? [{ kind: "billing", promotionId: "promo-2", reason: "", fix: "", actionUrl: "", callerCanResolve: true }]
      : [],
  };
}

function setup() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <QueuedDeployNotifier />
    </QueryClientProvider>
  );
  const reading = (env: LiveEnv) =>
    act(() => {
      client.setQueryData(forgeKeys.liveView("hounders"), { availability: "available", envs: [env], detail: "" });
    });
  return { client, reading };
}

beforeEach(() => {
  vi.clearAllMocks();
  useQueuedDeployStore.getState().reset();
  location.pathname = "/settings/billing";
});

describe("QueuedDeployNotifier", () => {
  it("notifies once when a deploy it saw queued is confirmed running", () => {
    const { reading } = setup();

    reading(prod({ queued: true }));
    reading(prod({ observed: "converging" }));
    expect(showNotification).not.toHaveBeenCalled();

    reading(prod({ observed: "converged" }));
    expect(showNotification).toHaveBeenCalledTimes(1);
    const [options] = showNotification.mock.calls[0] as [{ title: string; body: string; onClick: () => void }];
    expect(options.title).toBe("prod is live");
    expect(options.body).toMatch(/Release v13 is running/);
    // On the billing page, not looking at prod.
    expect(shouldShowNotificationSync).toHaveBeenCalledWith(false);

    // A second reading of the same state — another screen, a poll — is silent.
    reading(prod({ observed: "converged" }));
    expect(showNotification).toHaveBeenCalledTimes(1);

    // The click opens the environment, by the forge project the control plane knows.
    const focus = vi.spyOn(window, "focus").mockImplementation(() => {});
    options.onClick();
    expect(focus).toHaveBeenCalled();
    expect(navigate).toHaveBeenCalledWith({
      to: "/forge/env/$env",
      params: { env: "prod" },
      search: { forgeProject: "hounders" },
    });
  });

  it("never notifies for an environment this session did not see queued", () => {
    const { reading } = setup();
    reading(prod({ observed: "converged" }));
    expect(showNotification).not.toHaveBeenCalled();
  });

  it("says so when the released rollout fails", () => {
    const { reading } = setup();
    reading(prod({ queued: true }));
    reading(prod({ observed: "failed" }));
    const [options] = showNotification.mock.calls[0] as [{ title: string }];
    expect(options.title).toBe("prod didn't finish deploying");
  });

  it("defers to the notification settings, and knows when the user is looking at the page", () => {
    location.pathname = "/forge/env/prod";
    shouldShowNotificationSync.mockReturnValueOnce(false);
    const { reading } = setup();
    reading(prod({ queued: true }));
    reading(prod({ observed: "converged" }));
    expect(shouldShowNotificationSync).toHaveBeenCalledWith(true);
    expect(showNotification).not.toHaveBeenCalled();
    // Settled all the same: it does not fire later, either.
    reading(prod({ observed: "converged" }));
    expect(showNotification).not.toHaveBeenCalled();
  });

  it("does not take an unanswered reading for every environment deleted", () => {
    const { client, reading } = setup();
    reading(prod({ queued: true }));
    act(() => {
      client.setQueryData(forgeKeys.liveView("hounders"), { availability: "unreachable", envs: [], detail: "" });
    });
    expect(useQueuedDeployStore.getState().watched).toHaveProperty("cp-prod");
  });
});
