/**
 * ForgeService on forge's generated keys (hooks/forge-queries over
 * forge-service-hooks_gen). Each read caches the server's response and selects
 * it into a ForgeOutcome (forge-model); the control plane's reads stay on the
 * hand forgeCloudKeys. These pin the writes against both families:
 *
 *   1. an APPLIED promote refetches the ForgeService reads AND the
 *      control-plane entries the generated invalidation cannot reach;
 *   2. a REFUSED promote resolves as `{ kind: "refused" }` and invalidates
 *      nothing;
 *   3. forgeRetry retries once, and never on an answer;
 *   4. the plan reads are queries, and are not cached past their reader;
 *   5. a verify merges into the cached topology and never refetches it, and a
 *      build-and-plan invalidates nothing — the two writes that do not ride
 *      the generated mutation, and why.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import type { ReactNode } from "react";

import {
  ForgeDeployJobStatus,
  ForgePromoteRefusalReason,
  ForgePromoteRefusalSchema,
} from "@/gen/reliant/v1/forge_pb";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { serviceHandlers } from "@/test/serviceTransport";
import type { ForgePromotePlan } from "@/services/forge/promote";
import {
  forgeCloudKeys,
  useApplyForgePromote,
  useForgeDeployPlan,
  useForgePromotePlan,
  useForgeTopology,
  useStartForgeDeployPlan,
  useVerifyForgeEnv,
} from "../forge-queries";
import { forgeRetry } from "../forge-model";
import { forgeServiceKeys } from "../forge-service-hooks_gen";
import { releaseKeys } from "../release-queries";

const META = { isForgeProject: true, supported: true };

const TOPOLOGY = {
  project: "hounders",
  environments: [
    { env: "staging", release: "v1.3.0", images: [{ image: "api", state: "not_verified" }] },
    { env: "prod", release: "v1.2.0", images: [{ image: "api", state: "not_verified" }] },
  ],
};

const PLAN: ForgePromotePlan = {
  env: "staging",
  current: { bound: true, release: "v1.3.0" },
  target: { release: "v1.5.15" },
} as ForgePromotePlan;

const getTopology = vi.fn(async () => ({ meta: META, reportJson: JSON.stringify(TOPOLOGY) }));
const applyPromote = vi.fn(async () => ({ meta: META, reportJson: JSON.stringify({ applied: true }) }));
const planPromote = vi.fn(async () => ({ meta: META, reportJson: JSON.stringify({ dry_run: true }) }));
const planDeploy = vi.fn(async () => ({ meta: META, reportJson: JSON.stringify({ mode: "dry_run" }) }));
const verifyEnv = vi.fn(async () => ({
  meta: META,
  reportJson: JSON.stringify({ env: "staging", images: [{ image: "api", state: "match", running: "sha256:aa" }] }),
}));
const startDeployPlan = vi.fn(async () => ({
  meta: META,
  handle: "plan-1",
  env: "staging",
  startedAt: "2026-10-20T10:00:00Z",
  jobStatus: ForgeDeployJobStatus.RUNNING,
}));

let queryClient: QueryClient;

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

/** Control-plane entries a screen would hold, so their invalidation is observable. */
function seedCloudEntries() {
  queryClient.setQueryData(forgeCloudKeys.cloudStatus("cp-staging"), { verdict: "converged" });
  queryClient.setQueryData(forgeCloudKeys.cloudPromotions("cp-staging"), []);
  queryClient.setQueryData(forgeCloudKeys.liveView("hounders"), { availability: "available", envs: [], detail: "" });
  queryClient.setQueryData([...releaseKeys.root, "list", "hounders"], []);
}

function cloudInvalidated() {
  return [
    forgeCloudKeys.cloudStatus("cp-staging"),
    forgeCloudKeys.cloudPromotions("cp-staging"),
    forgeCloudKeys.liveView("hounders"),
    [...releaseKeys.root, "list", "hounders"],
  ].map((key) => queryClient.getQueryState(key)?.isInvalidated ?? false);
}

beforeEach(() => {
  queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  vi.clearAllMocks();
  setTransport(
    mockTransport(
      serviceHandlers("ForgeService", { getTopology, applyPromote, planPromote, planDeploy, verifyEnv, startDeployPlan })
    )
  );
});

afterEach(() => {
  cleanup();
  setTransport(null);
  queryClient.clear();
});

describe("ApplyPromote", () => {
  it("an applied promote refetches the ForgeService reads and the control-plane entries", async () => {
    seedCloudEntries();
    const { result } = renderHook(() => ({ topology: useForgeTopology("proj-1"), apply: useApplyForgePromote("proj-1") }), {
      wrapper,
    });
    await waitFor(() => expect(result.current.topology.data?.kind).toBe("report"));
    expect(getTopology).toHaveBeenCalledTimes(1);

    let outcome: Awaited<ReturnType<typeof result.current.apply.mutateAsync>> | undefined;
    await act(async () => {
      outcome = await result.current.apply.mutateAsync(PLAN);
    });

    expect(outcome?.kind).toBe("applied");
    // The request carried the plan's claim and target.
    expect(applyPromote).toHaveBeenCalledWith(
      expect.objectContaining({ env: "staging", release: "v1.5.15", expectedCurrentRelease: "v1.3.0", expectUnbound: false })
    );
    // The generated invalidation refetched the mounted topology...
    await waitFor(() => expect(getTopology).toHaveBeenCalledTimes(2));
    // ...and the domain hook named what that invalidation cannot reach.
    expect(cloudInvalidated()).toEqual([true, true, true, true]);
  });

  it("a refusal resolves as data and invalidates nothing", async () => {
    seedCloudEntries();
    applyPromote.mockRejectedValueOnce(
      new ConnectError("promote refused, nothing was written", Code.FailedPrecondition, undefined, [
        {
          desc: ForgePromoteRefusalSchema,
          value: create(ForgePromoteRefusalSchema, {
            reason: ForgePromoteRefusalReason.STALE_CURRENT_RELEASE,
            actualBound: true,
            actualCurrentRelease: "v1.4.2",
          }),
        },
      ])
    );
    const { result } = renderHook(() => ({ topology: useForgeTopology("proj-1"), apply: useApplyForgePromote("proj-1") }), {
      wrapper,
    });
    await waitFor(() => expect(result.current.topology.data?.kind).toBe("report"));

    await act(async () => {
      result.current.apply.mutate(PLAN);
    });
    await waitFor(() => expect(result.current.apply.isSuccess).toBe(true));

    const data = result.current.apply.data;
    expect(data?.kind).toBe("refused");
    if (data?.kind === "refused") expect(data.refusal.actualCurrentRelease).toBe("v1.4.2");
    // Not an error: a refusal is its own outcome.
    expect(result.current.apply.error).toBeNull();
    // Nothing was written, so nothing is refetched.
    expect(queryClient.getQueryState(forgeServiceKeys.getTopology({ projectId: "proj-1" }))?.isInvalidated).toBe(false);
    expect(getTopology).toHaveBeenCalledTimes(1);
    expect(cloudInvalidated()).toEqual([false, false, false, false]);
  });
});

describe("forgeRetry", () => {
  it("retries a transport failure once", () => {
    const unavailable = new ConnectError("no daemon connected", Code.Unavailable);
    expect(forgeRetry(0, unavailable)).toBe(true);
    expect(forgeRetry(1, unavailable)).toBe(false);
  });

  it.each([
    Code.DeadlineExceeded,
    Code.FailedPrecondition,
    Code.NotFound,
    Code.InvalidArgument,
    Code.PermissionDenied,
    Code.Unauthenticated,
  ])("never retries code %s — asking again cannot change the answer", (code) => {
    expect(forgeRetry(0, new ConnectError("answer", code))).toBe(false);
  });
});

describe("the plan reads", () => {
  it("are queries, and their entries do not outlive the reader", async () => {
    const { result, unmount } = renderHook(
      () => ({
        promote: useForgePromotePlan("proj-1", "staging", "v1.5.15"),
        deploy: useForgeDeployPlan("proj-1", "prod"),
      }),
      { wrapper }
    );
    await waitFor(() => expect(result.current.promote.data?.kind).toBe("report"));
    await waitFor(() => expect(result.current.deploy.data?.kind).toBe("report"));
    expect(planPromote).toHaveBeenCalledTimes(1);
    expect(planDeploy).toHaveBeenCalledTimes(1);

    unmount();
    // gcTime 0: a plan becomes a confirmation token, so a reopened dialog
    // computes a fresh one rather than resurrecting a cached claim.
    await waitFor(() => {
      expect(queryClient.getQueryCache().findAll({ queryKey: forgeServiceKeys.planPromote.all })).toHaveLength(0);
      expect(queryClient.getQueryCache().findAll({ queryKey: forgeServiceKeys.planDeploy.all })).toHaveLength(0);
    });
  });
});

describe("the writes that bypass the generated mutation", () => {
  it("a verify merges into the cached topology and does not refetch it", async () => {
    const { result } = renderHook(() => ({ topology: useForgeTopology("proj-1"), verify: useVerifyForgeEnv("proj-1") }), {
      wrapper,
    });
    await waitFor(() => expect(result.current.topology.data?.kind).toBe("report"));

    act(() => result.current.verify.verify("staging"));
    await waitFor(() => {
      const outcome = result.current.topology.data;
      if (outcome?.kind !== "report") throw new Error("expected a report");
      const staging = outcome.report.environments?.find((env) => env.env === "staging");
      expect(staging?.images?.[0]?.state).toBe("match");
    });

    const outcome = result.current.topology.data;
    if (outcome?.kind !== "report") throw new Error("expected a report");
    // Only the verified env moved; prod is still honestly unverified.
    expect(outcome.report.environments?.find((env) => env.env === "prod")?.images?.[0]?.state).toBe("not_verified");
    // A refetch would land the unverified ledger over the merge.
    expect(getTopology).toHaveBeenCalledTimes(1);
    expect(queryClient.getQueryState(forgeServiceKeys.getTopology({ projectId: "proj-1" }))?.isInvalidated).toBe(false);
  });

  it("a build-and-plan invalidates nothing", async () => {
    const { result } = renderHook(
      () => ({ topology: useForgeTopology("proj-1"), plan: useStartForgeDeployPlan("proj-1") }),
      { wrapper }
    );
    await waitFor(() => expect(result.current.topology.data?.kind).toBe("report"));

    let outcome: Awaited<ReturnType<typeof result.current.plan.mutateAsync>> | undefined;
    await act(async () => {
      outcome = await result.current.plan.mutateAsync({ env: "staging" });
    });

    expect(outcome?.kind).toBe("started");
    if (outcome?.kind === "started") expect(outcome.handle).toBe("plan-1");
    expect(queryClient.getQueryState(forgeServiceKeys.getTopology({ projectId: "proj-1" }))?.isInvalidated).toBe(false);
    expect(getTopology).toHaveBeenCalledTimes(1);
  });
});
