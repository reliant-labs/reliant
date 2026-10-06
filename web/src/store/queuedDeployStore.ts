// Copyright (c) 2025 Reliant Labs

/**
 * The session's watch over queued deploys (services/forge/queuedDeploys.ts).
 *
 * In memory on purpose: "this session saw it queued" is the whole premise of
 * the notification, and a reload that forgot it simply means the environment
 * page, not an OS notification, tells the user where it stands.
 */

import { create } from "zustand";

import type { LiveEnv } from "@/services/forge/live";
import { observeQueuedDeploys, type SettledDeploy, type WatchedDeploys } from "@/services/forge/queuedDeploys";

interface QueuedDeployState {
  watched: WatchedDeploys;
  /** Fold an ANSWERED Live reading in; returns the deploys it settled, each exactly once. */
  observe: (forgeProject: string, envs: LiveEnv[]) => SettledDeploy[];
  reset: () => void;
}

export const useQueuedDeployStore = create<QueuedDeployState>((set, get) => ({
  watched: {},
  observe: (forgeProject, envs) => {
    const { watched, settled } = observeQueuedDeploys(get().watched, forgeProject, envs);
    if (watched !== get().watched) set({ watched });
    return settled;
  },
  reset: () => set({ watched: {} }),
}));
