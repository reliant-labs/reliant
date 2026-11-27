// Copyright (c) 2026 Reliant Labs

/**
 * A service's request traces: Observability's trace explorer filtered to the
 * service (its `service.name`), each trace opening its waterfall: app →
 * gen_ai client → gateway and endpoint picker → vLLM queue / prefill /
 * decode, with the replica, node and GPU every span ran on.
 */

import type { GetMlInferenceServiceResponse } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { MlExplorer } from "../kit";
import { serviceSubject } from "./parts";

export function TracesTab({ d }: { d: GetMlInferenceServiceResponse }) {
  const svc = d.service!;
  return <MlExplorer view="traces" subject={serviceSubject(svc.id)} label={svc.name} range="24h" tabs={["traces"]} testId="ml-inference-traces" />;
}
