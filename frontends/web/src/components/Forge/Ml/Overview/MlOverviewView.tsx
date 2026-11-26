// Copyright (c) 2026 Reliant Labs

/** PLACEHOLDER until this view is built: the ML "overview" view's route component. */

import { MlFrame } from "../MlParts";

export function MlOverviewView({ sub }: { sub: readonly string[] }) {
  return (
    <MlFrame view="overview">
      <p className="text-ui text-ink-muted">{sub.join(" / ")}</p>
    </MlFrame>
  );
}
