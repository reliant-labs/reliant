// Copyright (c) 2026 Reliant Labs

/** PLACEHOLDER until this view is built: the ML "queues" view's route component. */

import { MlFrame } from "../MlParts";

export function MlQueuesView({ sub }: { sub: readonly string[] }) {
  return (
    <MlFrame view="queues">
      <p className="text-ui text-ink-muted">{sub.join(" / ")}</p>
    </MlFrame>
  );
}
