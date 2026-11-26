// Copyright (c) 2026 Reliant Labs

/**
 * The ML "Fleet" view's route component: the overview, a pool, a node or a
 * GPU, by the path under /forge/ml/fleet (kit/links.ts route table):
 *
 *   []                      overview
 *   ["pools", cluster, pool] pool
 *   [cluster, node]         node
 *   ["gpus", uuid]          device
 */

import { DevicePage } from "./DevicePage";
import { FleetOverview } from "./FleetOverview";
import { parseFleetRoute } from "./fleetModel";
import { NodePage } from "./NodePage";
import { PoolPage } from "./PoolPage";

export function MlFleetView({ sub }: { sub: readonly string[] }) {
  const route = parseFleetRoute(sub);
  switch (route.page) {
    case "pool":
      return <PoolPage key={route.poolId} poolId={route.poolId} cluster={route.cluster} pool={route.pool} />;
    case "node":
      return <NodePage key={route.nodeId} nodeId={route.nodeId} cluster={route.cluster} node={route.node} />;
    case "device":
      return <DevicePage key={route.uuid} uuid={route.uuid} />;
    default:
      return <FleetOverview />;
  }
}
