// Copyright (c) 2026 Reliant Labs

/**
 * GTM › Positioning & facts: the route's page (routes.tsx, lazy). A placeholder behind
 * the gate until the page itself lands in its own change.
 */

import EmptyState from "@/components/forge-ui/empty_state";

import { GtmShell } from "../shared/GtmShell";

export function PositioningPage() {
  return (
    <GtmShell page="positioning">
      <EmptyState title="Positioning & facts" description="This page is on its way." />
    </GtmShell>
  );
}
