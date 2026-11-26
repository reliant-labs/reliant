// Copyright (c) 2026 Reliant Labs

/** PLACEHOLDER until this view is built: the ML "models" view's route component. */

import { MlFrame } from "../MlParts";

export function MlModelsView({ sub }: { sub: readonly string[] }) {
  return (
    <MlFrame view="models">
      <p className="text-ui text-ink-muted">{sub.join(" / ")}</p>
    </MlFrame>
  );
}
