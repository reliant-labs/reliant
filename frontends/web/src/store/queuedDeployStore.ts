// Copyright (c) 2025 Reliant Labs

/**
 * The session's watch over queued deploys (services/forge/queuedDeploys.ts).
 *
 * In memory on purpose: "this session saw it queued" is the whole premise of
 * the notification, and a reload that forgot it simply means the environment
 * page, not an OS notification, tells the user where it stands.
 *
 * The forge tour's sample app is never watched: its deploys are a story
 * (fixtures/scope.tsx), and a watch would have the root notifier poll it on
 * the live transport (which refuses it: lib/sampleGuard.ts). Its readings
 * live in its own scope's cache, which the notifier never observes; this is
 * the second lock.
 */

import { create } from "zustand";

import { isSampleApp } from "@/components/Forge/Sample/sampleApp";
import type { LiveEnv } from "@/services/forge/live";
import { observeQueuedDeploys, type SettledDeploy, type WatchedDeploys } from "@/services/forge/queuedDeploys";

interface QueuedDeployState {
  watched: WatchedDeploys;
  /** Fold an ANSWERED Live reading (in `orgId`, "" for none) in; returns the deploys it settled, each exactly once. */
  observe: (forgeProject: string, envs: LiveEnv[], orgId?: string) => SettledDeploy[];
  reset: () => void;
}

export const useQueuedDeployStore = create<QueuedDeployState>((set, get) => ({
  watched: {},
  observe: (forgeProject, envs, orgId = "") => {
    if (isSampleApp(forgeProject)) return [];
    const { watched, settled } = observeQueuedDeploys(get().watched, forgeProject, envs, orgId);
    if (watched !== get().watched) set({ watched });
    return settled;
  },
  reset: () => set({ watched: {} }),
}));
