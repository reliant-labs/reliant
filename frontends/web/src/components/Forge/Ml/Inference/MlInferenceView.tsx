// Copyright (c) 2026 Reliant Labs

/**
 * The ML "inference" view's route component (02-views §6):
 *
 *   inference                   the services list
 *   inference/<service>         one service (?tab, ?sel, ?filter)
 *   inference/traces/<trace>    one request trace, full page (Observability's trace detail)
 */

import { MlFrame } from "../MlParts";
import { MlTrace } from "../kit";
import { ServiceDetail } from "./ServiceDetail";
import { ServicesList } from "./ServicesList";

export function MlInferenceView({ sub }: { sub: readonly string[] }) {
  if (sub[0] === "traces" && sub[1]) return <RequestTracePage traceId={sub[1]} />;
  if (sub[0]) return <ServiceDetail serviceId={sub[0]} />;
  return <ServicesList />;
}

/** A request trace on its own page. */
function RequestTracePage({ traceId }: { traceId: string }) {
  return (
    <MlFrame view="inference" title="Request trace" crumbs={[{ label: traceId.slice(0, 12) }]} testId="ml-inference-trace-page">
      <MlTrace traceId={traceId} label="this request" />
    </MlFrame>
  );
}
