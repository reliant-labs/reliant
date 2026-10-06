// Copyright (c) 2025 Reliant Labs

/**
 * Where QueuedDeployNotifier is mounted from: the app ROOT, because the
 * moment it exists for — billing set up, the queued release going out — is
 * usually spent outside the forge screens.
 *
 * A thin, lazy door rather than the notifier itself. The notifier reads the
 * forge queries, which live in the (lazy) forge chunk; importing it here would
 * put all of that in the entry chunk of every page for a feature most
 * sessions never touch. So it loads only while it has something to do: on a
 * forge screen (where queued deploys are first seen, and the chunk is loaded
 * anyway), or while this session is still watching one.
 */

import { lazy, Suspense } from "react";

import { useQueuedDeployStore } from "@/store/queuedDeployStore";

const QueuedDeployNotifier = lazy(() =>
  import("./QueuedDeployNotifier").then((module) => ({ default: module.QueuedDeployNotifier }))
);

export function isForgePath(pathname: string): boolean {
  return pathname === "/forge" || pathname.startsWith("/forge/");
}

export function QueuedDeployWatch({ pathname }: { pathname: string }) {
  const watching = useQueuedDeployStore((state) => Object.keys(state.watched).length > 0);
  if (!watching && !isForgePath(pathname)) return null;
  return (
    <Suspense fallback={null}>
      <QueuedDeployNotifier />
    </Suspense>
  );
}
