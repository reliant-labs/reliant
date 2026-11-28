// Copyright (c) 2026 Reliant Labs

/**
 * Mounts the aggregate page registered for a console page (registry.ts):
 * its chunk behind a loading frame. ConsoleLayout renders it in place of the
 * route's Outlet while the shell shows several projects; another host (the
 * org console) can mount it the same way, with the projects it shows.
 */

import { Suspense } from "react";

import SkeletonLoader from "@/components/forge-ui/skeleton_loader";

import { aggregatePageOf, type AggregatePageProps } from "./registry";

export function AggregateHost({ page, apps, mode }: AggregatePageProps) {
  const Page = aggregatePageOf(page);
  if (!Page) return null;
  return (
    <div className="min-w-0" data-testid="aggregate-host" data-page={page.kind}>
      <Suspense
        fallback={
          <div aria-busy="true" aria-label="Loading" className="space-y-4">
            <SkeletonLoader variant="text" count={1} width="30%" />
            <SkeletonLoader variant="table-row" count={4} />
          </div>
        }
      >
        <Page page={page} apps={apps} mode={mode} />
      </Suspense>
    </div>
  );
}
