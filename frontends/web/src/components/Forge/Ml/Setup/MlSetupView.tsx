// Copyright (c) 2026 Reliant Labs

/** PLACEHOLDER until this view is built: the ML "setup" view's route component. */

import { MlFrame } from "../MlParts";

export function MlSetupView({ sub }: { sub: readonly string[] }) {
  return (
    <MlFrame view="setup">
      <p className="text-ui text-ink-muted">{sub.join(" / ")}</p>
    </MlFrame>
  );
}
