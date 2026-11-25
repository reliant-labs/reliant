// Copyright (c) 2026 Reliant Labs

/**
 * JourneyLiveWatch and the forge journey's `onLiveChange`: while the tour is
 * live in the signed-in shell, the switcher lists the sample app; however it
 * stops (the URL loses `?tour=`), the row goes and the sample's scope forgets
 * everything it cached. Walking from step to step changes nothing.
 */

import { act, render } from "@testing-library/react";
import { createMemoryHistory, createRootRoute, createRouter, Outlet, RouterProvider } from "@tanstack/react-router";
import { afterEach, describe, expect, it } from "vitest";

import { SAMPLE_APP } from "@/components/Forge/Sample/sampleApp";
import { appScope, resetSampleScopeForTests, SAMPLE_ROW, useSampleTour, withSampleApp } from "@/fixtures/scope";

import { JourneyLiveWatch, liveJourneyOf } from "../JourneyLiveWatch";
import { setReliantTourHooks } from "../tourShell";

const SAMPLE = `/forge/p/${SAMPLE_APP}`;

function signIn() {
  setReliantTourHooks({ landOnChat: async () => undefined, afterTour: async () => undefined, inAppShell: () => true });
}

function mount(url: string) {
  const root = createRootRoute({
    component: () => (
      <>
        <JourneyLiveWatch />
        <Outlet />
      </>
    ),
  });
  const router = createRouter({ routeTree: root, history: createMemoryHistory({ initialEntries: [url] }) });
  render(<RouterProvider router={router} />);
  const go = async (href: string) => {
    await act(async () => {
      await router.navigate({ href });
    });
  };
  return { router, go };
}

/** What the switcher's catalog is right now (ProjectSelector's useSelectorCatalog decorates it the same way). */
const switcherRows = () => (useSampleTour.getState().active ? withSampleApp({ orgId: "o", rows: [] } as never) : { rows: [] }).rows;

afterEach(() => {
  setReliantTourHooks(null);
  resetSampleScopeForTests();
});

describe("liveJourneyOf", () => {
  it("names the journey a `?tour=` step belongs to, and nothing for the picker or no tour", () => {
    expect(liveJourneyOf("forge:fg-intro")).toBe("forge");
    expect(liveJourneyOf("pick:menu")).toBeNull();
    expect(liveJourneyOf(undefined)).toBeNull();
  });
});

describe("the forge tour's sample app, while the tour is live", () => {
  it("is listed while the tour walks it, and leaving the tour takes the row and the sample's cache with it", async () => {
    signIn();
    const { go } = mount("/projects");
    expect(useSampleTour.getState().active).toBe(false);
    expect(switcherRows()).toEqual([]);

    await go(`${SAMPLE}?tour=forge:fg-intro`);
    expect(useSampleTour.getState().active).toBe(true);
    expect(switcherRows()).toEqual([SAMPLE_ROW]);

    // What the sample's pages read while it was live.
    const cache = appScope(SAMPLE_APP)!.queryClient;
    cache.setQueryData(["sample-read"], { releases: 3 });

    // Step to step (and off the sample's pages, the tour still open): still live, cache kept.
    const changes: boolean[] = [];
    const unsubscribe = useSampleTour.subscribe((state) => changes.push(state.active));
    await go(`${SAMPLE}/releases?tour=forge:fg-deployments`);
    await go(`/projects?tour=forge:fg-deployments`);
    expect(changes).toEqual([]);
    expect(cache.getQueryData(["sample-read"])).toEqual({ releases: 3 });

    // Finish, Exit tour, Back off the first step and a link all leave a URL with no `?tour=`.
    await go("/projects");
    unsubscribe();
    expect(changes).toEqual([false]);
    expect(switcherRows()).toEqual([]);
    expect(cache.getQueryCache().getAll()).toEqual([]);
  });

  it("is not listed for the picker, another journey, or someone signed out", async () => {
    const { go } = mount("/projects?tour=pick:menu");
    expect(useSampleTour.getState().active).toBe(false);
    await go("/projects?tour=workflows:wf-example");
    expect(useSampleTour.getState().active).toBe(false);
    // Signed out, the forge tour hands off to the demo host: no sample here.
    await go(`${SAMPLE}?tour=forge:fg-intro`);
    expect(useSampleTour.getState().active).toBe(false);
  });

  it("starts live from a tour link opened cold, and a reload's memory starts empty", async () => {
    signIn();
    expect(useSampleTour.getState().active).toBe(false);
    mount(`${SAMPLE}/observability/traces?tour=forge:fg-trace`);
    await act(async () => undefined);
    expect(useSampleTour.getState().active).toBe(true);
  });
});
