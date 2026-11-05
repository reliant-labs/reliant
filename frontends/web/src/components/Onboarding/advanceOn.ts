/**
 * `advanceOn`: a journey step's required action, and `phases`.
 *
 * `watchAdvance` is the framework-free core — it subscribes to everything a
 * set of conditions names and calls `onFire` once, the first time one is met
 * while the step is open. `useAdvanceOn` / `useStepPhase` bind it to the real
 * router and stores. The split keeps the semantics unit-testable with a fake
 * env.
 *
 * Semantics (JOURNEYS.md has the content-author version):
 *   - click / event are edges: they fire when the action happens.
 *   - route / checklist / store / element / when are levels: reaching the
 *     state while the step is open fires. A state that ALREADY holds when the
 *     step opens does not fire — it reports `initiallySatisfied`, which shows
 *     Next — because auto-advancing on entry would make Back bounce straight
 *     forward again. `advanceIfAlreadyDone` opts into the immediate advance.
 *   - Once met, Next shows with a check; `autoAdvance` moves on after 600 ms.
 *   - A watcher fires at most once.
 *   - `when` predicates see `signals`: the last payload of each tour signal
 *     emitted since the watcher started.
 */

import { useEffect, useRef, useState } from "react";
import { useRouter } from "@tanstack/react-router";
import { useOnboardingChecklistStore } from "../../store/onboardingChecklistStore";
import { useTourStore } from "../../store/tourStore";
import { tourEvents, type TourEventMap } from "./tourEvents";
import { TOUR_SIGNAL_NAMES } from "./journeys/signals";
import type { JourneyId } from "./tourParam";
import type { AdvanceContext, AdvanceOn, ChecklistItemId, JourneyContext, JourneyStep } from "./types";

/** Delay before `autoAdvance` moves on, so the user sees the check. */
export const AUTO_ADVANCE_MS = 600;

/** Build a `store` condition from any zustand-shaped store. */
export function whenStore<S>(
  store: { getState: () => S; subscribe: (listener: (state: S) => void) => () => void },
  test: (state: S) => boolean,
): AdvanceOn {
  return {
    kind: "store",
    test: () => test(store.getState()),
    subscribe: (onChange) => store.subscribe(() => onChange()),
  };
}

export interface AdvanceEnv {
  getPathname: () => string;
  getSearch: () => Record<string, unknown>;
  /** Called on every location change (path or search). */
  subscribeLocation: (onChange: () => void) => () => void;
  checklistHas: (item: ChecklistItemId) => boolean;
  subscribeChecklist: (onChange: () => void) => () => void;
  getContext: () => JourneyContext;
  subscribeContext: (onChange: () => void) => () => void;
  document: Document;
}

export interface AdvanceWatcher {
  /** A level condition already held when the watcher started. */
  initiallySatisfied: boolean;
  dispose: () => void;
}

export function conditionsOf(step: Pick<JourneyStep, "advanceOn">): AdvanceOn[] {
  return step.advanceOn ? [step.advanceOn].flat() : [];
}

function routeMatches(path: string | RegExp, pathname: string): boolean {
  if (typeof path !== "string") return path.test(pathname);
  return pathname === path || pathname.startsWith(path.endsWith("/") ? path : `${path}/`);
}

export function watchAdvance(
  conditions: AdvanceOn[],
  context: { target?: string },
  env: AdvanceEnv,
  onFire: () => void,
): AdvanceWatcher {
  let fired = false;
  let disposed = false;
  const cleanups: (() => void)[] = [];
  const fire = () => {
    if (fired || disposed) return;
    fired = true;
    onFire();
  };

  let initiallySatisfied = false;
  const levelRechecks: (() => void)[] = [];
  const watchLevel = (test: () => boolean, subscribe?: (onChange: () => void) => () => void) => {
    let previous = safe(test);
    if (previous) initiallySatisfied = true;
    const recheck = () => {
      const now = safe(test);
      if (now && !previous) fire();
      previous = now;
    };
    if (subscribe) cleanups.push(subscribe(recheck));
    else levelRechecks.push(recheck);
  };

  // `when` predicates: re-evaluated on any location change, signal or context write.
  const signals: Partial<TourEventMap> = {};
  const ctx = (): AdvanceContext => ({
    location: { pathname: env.getPathname(), search: env.getSearch() },
    signals,
    context: env.getContext(),
  });

  for (const condition of conditions) {
    switch (condition.kind) {
      case "click": {
        const selector = condition.selector ?? context.target;
        if (!selector) break;
        // Capture phase, and deferred a tick: the click's own handler (often
        // a navigation) runs first. Keyboard activation of a button is a click.
        const onClick = (event: Event) => {
          const node = event.target;
          if (node instanceof Element && node.closest(selector)) setTimeout(fire, 0);
        };
        env.document.addEventListener("click", onClick, true);
        cleanups.push(() => env.document.removeEventListener("click", onClick, true));
        break;
      }
      case "event": {
        const where = condition.where as ((p: unknown) => boolean) | undefined;
        cleanups.push(
          tourEvents.on(condition.event, (payload) => {
            if (!where || where(payload)) fire();
          }),
        );
        break;
      }
      case "route":
        watchLevel(() => routeMatches(condition.path, env.getPathname()), env.subscribeLocation);
        break;
      case "checklist":
        watchLevel(() => env.checklistHas(condition.item), env.subscribeChecklist);
        break;
      case "store":
        watchLevel(condition.test, condition.subscribe);
        break;
      case "element": {
        const doc = env.document;
        watchLevel(
          () => doc.querySelector(condition.selector) !== null,
          (onChange) => {
            const observer = new MutationObserver(onChange);
            observer.observe(doc.body, { childList: true, subtree: true });
            return () => observer.disconnect();
          },
        );
        break;
      }
      case "when":
        watchLevel(() => condition.test(ctx()));
        break;
    }
  }

  if (levelRechecks.length > 0) {
    const recheckAll = () => levelRechecks.forEach((recheck) => recheck());
    cleanups.push(env.subscribeLocation(recheckAll), env.subscribeContext(recheckAll));
    for (const name of TOUR_SIGNAL_NAMES) {
      cleanups.push(
        tourEvents.on(name, (payload) => {
          (signals as Record<string, unknown>)[name] = payload;
          recheckAll();
        }),
      );
    }
  }

  return {
    initiallySatisfied,
    dispose: () => {
      disposed = true;
      for (const cleanup of cleanups) cleanup();
    },
  };
}

function safe(test: () => boolean): boolean {
  try {
    return test();
  } catch {
    return false;
  }
}

function useAdvanceEnv(journey: JourneyId): AdvanceEnv {
  const router = useRouter();
  return {
    getPathname: () => router.history.location.pathname,
    getSearch: () => (router.latestLocation.search ?? {}) as Record<string, unknown>,
    subscribeLocation: (onChange) => router.history.subscribe(onChange),
    // The checklist store is Reliant's; it is read only by a `checklist`
    // condition, which a forge-shell step has no reason to use.
    checklistHas: (item) => useOnboardingChecklistStore.getState().completedItems.has(item),
    subscribeChecklist: (onChange) => useOnboardingChecklistStore.subscribe(onChange),
    getContext: () => useTourStore.getState().contexts?.[journey] ?? {},
    subscribeContext: (onChange) => useTourStore.subscribe(onChange),
    document,
  };
}

/**
 * Watch the open step's `advanceOn`. `pending` is true while the step has
 * conditions and none has been met; `satisfied` once one has. `onAdvance`
 * is called when the tour should move on by itself: `autoAdvance` steps
 * (600 ms after the action) and `advanceIfAlreadyDone` steps.
 */
export function useAdvanceOn(
  journey: JourneyId,
  step: JourneyStep,
  stepKey: string,
  onAdvance: () => void,
): { pending: boolean; satisfied: boolean } {
  const env = useAdvanceEnv(journey);
  const envRef = useRef(env);
  envRef.current = env;
  const onAdvanceRef = useRef(onAdvance);
  onAdvanceRef.current = onAdvance;
  const hasConditions = conditionsOf(step).length > 0;
  const [satisfied, setSatisfied] = useState<{ key: string; value: boolean }>({ key: stepKey, value: false });

  useEffect(() => {
    const conditions = conditionsOf(step);
    if (conditions.length === 0) return;
    let timer: ReturnType<typeof setTimeout> | null = null;
    const watcher = watchAdvance(conditions, { target: step.target }, envRef.current, () => {
      setSatisfied({ key: stepKey, value: true });
      if (step.autoAdvance) timer = setTimeout(() => onAdvanceRef.current(), AUTO_ADVANCE_MS);
    });
    setSatisfied({ key: stepKey, value: watcher.initiallySatisfied });
    if (watcher.initiallySatisfied && step.advanceIfAlreadyDone) {
      watcher.dispose();
      onAdvanceRef.current();
      return;
    }
    return () => {
      watcher.dispose();
      if (timer) clearTimeout(timer);
    };
    // The step object is static content; stepKey identifies it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [stepKey]);

  const isSatisfied = hasConditions && satisfied.key === stepKey && satisfied.value;
  return { pending: hasConditions && !isSatisfied, satisfied: isSatisfied };
}

/**
 * The active phase of a step with `phases`: the last whose `when` has been
 * met. Phases latch — once reached, a phase stays — and a state already true
 * on entry counts as reached.
 */
export function useStepPhase(journey: JourneyId, step: JourneyStep, stepKey: string): number {
  const env = useAdvanceEnv(journey);
  const envRef = useRef(env);
  envRef.current = env;
  const [reached, setReached] = useState<{ key: string; index: number }>({ key: stepKey, index: 0 });

  useEffect(() => {
    const phases = step.phases ?? [];
    if (phases.length < 2) return;
    let highest = 0;
    const reach = (index: number) => {
      if (index <= highest) return;
      highest = index;
      setReached({ key: stepKey, index });
    };
    setReached({ key: stepKey, index: 0 });
    const watchers = phases.flatMap((phase, index) => {
      if (index === 0 || !phase.when) return [];
      const watcher = watchAdvance([phase.when], { target: phase.target ?? step.target }, envRef.current, () =>
        reach(index),
      );
      if (watcher.initiallySatisfied) reach(index);
      return [watcher];
    });
    return () => watchers.forEach((watcher) => watcher.dispose());
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [stepKey]);

  return reached.key === stepKey ? reached.index : 0;
}
