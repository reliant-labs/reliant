// Copyright (c) 2026 Reliant Labs

/** PLACEHOLDER until this view is built: the ML "inference" view's route component. */

import { MlFrame } from "../MlParts";

export function MlInferenceView({ sub }: { sub: readonly string[] }) {
  return (
    <MlFrame view="inference">
      <p className="text-ui text-ink-muted">{sub.join(" / ")}</p>
    </MlFrame>
  );
}
