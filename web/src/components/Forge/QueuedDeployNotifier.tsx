// Copyright (c) 2025 Reliant Labs

/**
 * TELLS THE USER WHEN A DEPLOY THEY SAW QUEUED GOES OUT — once, through the
 * OS notification every other "it finished" in the app uses, and under the
 * same notification settings.
 *
 * Mounted once at the root, renders nothing, and costs nothing until a forge
 * screen has shown a queued deploy: it watches only what this session saw
 * queued (services/forge/queuedDeploys.ts), and it must sit ABOVE the forge
 * routes because the moment it exists for — billing set up, the release
 * rolling out — is usually spent on the billing page, or in a chat.
 *
 * ── IT READS THE SAME LIVE QUERY THE PAGES DO ──────────────────────────────
 *
 * Nothing here fetches on its own terms. It observes every Live answer that
 * lands in the query cache, whichever screen asked, and it keeps the Live
 * query of a watched project polling (liveViewQuery: same key, same fetch).
 * So an open environment page settles in step with the notification, and
 * there is never a second request for the same reading.
 */

import { useEffect, useMemo, useRef } from "react";
import { useQueries, useQueryClient, type Query } from "@tanstack/react-query";
import { useLocation, useNavigate } from "@tanstack/react-router";

import { forgeKeys, liveViewQuery, type LiveViewResult } from "@/hooks/forge-queries";
import { showNotification } from "@/lib/notifications";
import { hasLiveControlPlane } from "@/services/forge/live";
import { watchPollInterval, watchedProjects, type SettledDeploy } from "@/services/forge/queuedDeploys";
import { getNotificationSoundOptions, shouldShowNotificationSync } from "@/store/notificationStore";
import { useQueuedDeployStore } from "@/store/queuedDeployStore";

import { forgeEnvPath } from "./ForgeShell";

const LIVE_VIEW_PREFIX = [...forgeKeys.all, "live-view"] as const;

export function QueuedDeployNotifier() {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const { pathname } = useLocation();

  // Read at notification time, not captured when the cache subscription was
  // made: the subscription lives across navigations.
  const pathnameRef = useRef(pathname);
  pathnameRef.current = pathname;
  const navigateRef = useRef(navigate);
  navigateRef.current = navigate;

  const watched = useQueuedDeployStore((state) => state.watched);
  const projects = useMemo(() => watchedProjects(watched), [watched]);

  // Keep every watched project's Live reading fresh — slowly while its deploy
  // waits on a person, quickly once it is on its way out.
  useQueries({
    queries: projects.map((project) => ({
      ...liveViewQuery(project),
      enabled: hasLiveControlPlane(),
      refetchInterval: watchPollInterval(watched, project),
    })),
  });

  useEffect(() => {
    const cache = queryClient.getQueryCache();
    const observe = (query: Query) => {
      if (!isLiveViewKey(query.queryKey)) return;
      const project = String(query.queryKey[LIVE_VIEW_PREFIX.length] ?? "");
      const reading = query.state.data as LiveViewResult | undefined;
      // Only an ANSWER may be folded in: an unavailable reading lists no
      // environments, which would read as every watched one deleted.
      if (project === "" || !reading || reading.availability !== "available") return;
      for (const settled of useQueuedDeployStore.getState().observe(project, reading.envs)) {
        notifySettled(settled, pathnameRef.current, () =>
          void navigateRef.current({
            to: "/forge/env/$env",
            params: { env: settled.deploy.envName },
            search: { forgeProject: settled.deploy.forgeProject },
          })
        );
      }
    };
    for (const query of cache.findAll({ queryKey: LIVE_VIEW_PREFIX })) observe(query);
    return cache.subscribe((event) => {
      if (event.type === "updated" && event.action.type === "success") observe(event.query);
    });
  }, [queryClient]);

  return null;
}

function isLiveViewKey(key: readonly unknown[]): boolean {
  return LIVE_VIEW_PREFIX.every((part, index) => key[index] === part);
}

/**
 * The notification itself, gated like every other: notifications enabled and
 * permitted, and — unless the user asked to always be told — not while they
 * are looking at this environment's page in a focused window, which already
 * shows it.
 */
export function notifySettled(settled: SettledDeploy, pathname: string, openEnvironment: () => void): boolean {
  const { envName } = settled.deploy;
  const viewing = safeDecode(pathname) === safeDecode(forgeEnvPath(envName));
  if (!shouldShowNotificationSync(viewing)) return false;

  const release = settled.release !== "" ? `Release ${settled.release}` : "The release";
  return showNotification(
    {
      title: settled.outcome === "live" ? `${envName} is live` : `${envName} didn't finish deploying`,
      body:
        settled.outcome === "live"
          ? `${release} is running. The deploy that was queued went out on its own.`
          : `${release} was released from the queue, but its rollout failed. Open the environment to see why.`,
      // One per environment: a later outcome replaces an unread earlier one.
      tag: `forge-queued-deploy-${settled.deploy.envId}`,
      onClick: () => {
        window.focus();
        openEnvironment();
      },
    },
    getNotificationSoundOptions()
  );
}

function safeDecode(path: string): string {
  try {
    return decodeURI(path);
  } catch {
    return path;
  }
}
