// Copyright (c) 2026 Reliant Labs

/**
 * The ML "inference" view's route component (02-views §6):
 *
 *   inference                   the services list
 *   inference/<service>         one service (?tab, ?sel, ?filter)
 *   inference/traces/<trace>    one request trace, full page
 */

import { MlSubjectKind } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { MlFrame, MlRead } from "../MlParts";
import { mlHref, MlTraceBody, useMl } from "../kit";
import { ServiceDetail } from "./ServiceDetail";
import { ServicesList } from "./ServicesList";

export function MlInferenceView({ sub }: { sub: readonly string[] }) {
  if (sub[0] === "traces" && sub[1]) return <RequestTracePage traceId={sub[1]} />;
  if (sub[0]) return <ServiceDetail serviceId={sub[0]} />;
  return <ServicesList />;
}

/** A request trace on its own page, with the service and replica it ran on in the breadcrumbs. */
function RequestTracePage({ traceId }: { traceId: string }) {
  const trace = useMl("getMlTrace", { traceId });
  const summary = trace.data?.kind === "ok" ? trace.data.data.summary : undefined;
  const replica = summary?.subject?.kind === MlSubjectKind.REPLICA ? summary.subject : undefined;
  const serviceId = replica?.id.split("/")[0];
  return (
    <MlFrame
      view="inference"
      title={summary?.title ?? "Request trace"}
      subtitle={summary ? `${summary.attributes.model ?? ""} · ${summary.attributes.revision ?? ""} · ${summary.attributes.finish_reason ?? ""} · tenant ${summary.attributes.tenant ?? ""}` : undefined}
      crumbs={[
        ...(serviceId ? [{ label: serviceId, href: mlHref("inference", [serviceId]) }, { label: "Traces", href: mlHref("inference", [serviceId], { tab: "traces" }) }] : []),
        ...(replica && serviceId ? [{ label: replica.label, href: mlHref("inference", [serviceId], { tab: "replicas", sel: replica.id }) }] : []),
        { label: traceId.slice(0, 12) },
      ]}
      testId="ml-inference-trace-page"
    >
      <MlRead query={trace} what="this trace">
        {(t) => <MlTraceBody trace={t} />}
      </MlRead>
    </MlFrame>
  );
}
