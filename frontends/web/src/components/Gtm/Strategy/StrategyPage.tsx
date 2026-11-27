// Copyright (c) 2026 Reliant Labs

/**
 * GTM › Strategy: the route's page (routes.tsx, lazy). A placeholder behind
 * the gate until the page itself lands in its own change.
 */

import EmptyState from "@/components/forge-ui/empty_state";

import { GtmShell } from "../shared/GtmShell";

export function StrategyPage() {
  return (
    <GtmShell page="strategy">
      <EmptyState title="Strategy" description="This page is on its way." />
    </GtmShell>
  );
}
