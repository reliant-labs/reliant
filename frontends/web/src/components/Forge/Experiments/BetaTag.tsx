// Copyright (c) 2026 Reliant Labs

/**
 * The "Beta" label beside an Experiments page's title. Forge Changes is
 * labelled Beta wherever it is surfaced until delivery, readout and the UI
 * ship (owner decision); the nav row carries the same word
 * (Layout/sidebar/navConfig/items.tsx experimentsRow).
 */

import Tag from "@/components/forge-ui/tag";

export const BETA_NOTE = "Experiments is in beta: what it does and how it looks may still change.";

export function BetaTag() {
  return (
    <span data-testid="experiments-beta" title={BETA_NOTE}>
      <Tag>
        Beta
        <span className="sr-only">{`. ${BETA_NOTE}`}</span>
      </Tag>
    </span>
  );
}
