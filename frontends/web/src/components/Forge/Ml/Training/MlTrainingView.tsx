// Copyright (c) 2026 Reliant Labs

/** PLACEHOLDER until this view is built: the ML "runs" view's route component. */

import { MlFrame } from "../MlParts";

export function MlTrainingView({ sub }: { sub: readonly string[] }) {
  return (
    <MlFrame view="runs">
      <p className="text-ui text-ink-muted">{sub.join(" / ")}</p>
    </MlFrame>
  );
}
