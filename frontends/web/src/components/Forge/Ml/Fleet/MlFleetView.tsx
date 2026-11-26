// Copyright (c) 2026 Reliant Labs

/** PLACEHOLDER until this view is built: the ML "fleet" view's route component. */

import { MlFrame } from "../MlParts";

export function MlFleetView({ sub }: { sub: readonly string[] }) {
  return (
    <MlFrame view="fleet">
      <p className="text-ui text-ink-muted">{sub.join(" / ")}</p>
    </MlFrame>
  );
}
