// Copyright (c) 2026 Reliant Labs

/** PLACEHOLDER until this view is built: the ML "evals" view's route component. */

import { MlFrame } from "../MlParts";

export function MlEvalsView({ sub }: { sub: readonly string[] }) {
  return (
    <MlFrame view="evals">
      <p className="text-ui text-ink-muted">{sub.join(" / ")}</p>
    </MlFrame>
  );
}
