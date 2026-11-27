// Copyright (c) 2026 Reliant Labs

/**
 * An ML read from inside a view: `useMl("getMlRun", { run })`. The org is the
 * gate's (MlGateContext), so a view mounted under any area that wraps it in
 * the ML gate reads the same data. Render the result with <MlRead> (MlParts),
 * which draws loading, failure, not-entitled and no-data.
 */

import { useMlRead } from "@/hooks/ml-queries";
import type { MlMethod, MlRequest } from "@/services/controlPlane/ml/mlApi";

import { useMlGate } from "../MlParts";

export function useMl<M extends MlMethod>(method: M, req: MlRequest<M>, opts?: { enabled?: boolean }) {
  return useMlRead(useMlGate().orgId, method, req, opts);
}
