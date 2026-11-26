// Copyright (c) 2026 Reliant Labs

/** PLACEHOLDER until this view is built: the ML "recommendations" view's route component. */

import { MlFrame } from "../MlParts";

export function MlRecommendationsView({ sub }: { sub: readonly string[] }) {
  return (
    <MlFrame view="recommendations">
      <p className="text-ui text-ink-muted">{sub.join(" / ")}</p>
    </MlFrame>
  );
}
