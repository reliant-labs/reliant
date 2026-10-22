// Copyright (c) 2025 Reliant Labs

/**
 * "Add trigger" is one picker everywhere (research/WORKFLOW_EDITOR_UX_REVIEW.md
 * §2 Q1): the step palette's Triggers kind. What a pick becomes depends on
 * whether the workflow's definition can change:
 *
 *   - your own workflow: the trigger is DECLARED in its `triggers:` (the
 *     WHEN, saved in the YAML) and its card's panel opens;
 *   - a built-in (read-only): the definition can't gain a declaration, so
 *     the pick becomes your PERSONAL trigger — a trigger row with that source
 *     inline — through the activate dialog's personal mode.
 *
 * Both paths start from the same declaration shape, so the user never
 * chooses between "declare" and "automation"; the model difference stays
 * out of the UI.
 */

import { useCallback, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";

import { catalogSearchGrpc, type CatalogEntry, type CatalogEntrySummary } from "../../../api/catalog-search-grpc";
import { connectionKeys } from "../../../hooks/connection-queries";
import { triggerFromBuiltin, triggerFromCatalog, type DeclaredTrigger } from "../../../lib/declaredTriggers";

export interface PersonalTriggerDraft {
  trigger: DeclaredTrigger;
  /** The catalog trigger type it was picked from, for its payload schema and connections. */
  catalogRef?: string;
}

export interface UseAddTriggerOptions {
  /** Whether the definition can gain a declaration (false for a built-in). */
  canEditDefinition: boolean;
  /** The workflow's declared triggers, for unique names. */
  declared: readonly DeclaredTrigger[];
  /** Declare a trigger in the definition (and open its panel). */
  declare: (trigger: DeclaredTrigger, catalogRef?: string) => void;
  /** Close the palette the pick came from. */
  closePalette: () => void;
}

export function useAddTrigger({ canEditDefinition, declared, declare, closePalette }: UseAddTriggerOptions) {
  const queryClient = useQueryClient();
  const [personal, setPersonal] = useState<PersonalTriggerDraft | null>(null);

  const add = useCallback(
    (trigger: DeclaredTrigger, catalogRef?: string) => {
      closePalette();
      if (canEditDefinition) declare(trigger, catalogRef);
      else setPersonal({ trigger, catalogRef });
    },
    [canEditDefinition, declare, closePalette],
  );

  const chooseBuiltin = useCallback(
    (source: "schedule" | "webhook" | "workflow_event") => add(triggerFromBuiltin(source, declared)),
    [add, declared],
  );

  /** A catalog trigger type: its events are the type's event list (the payload schema's `event` enum). */
  const chooseCatalog = useCallback(
    async (entry: CatalogEntrySummary) => {
      let full = queryClient.getQueryData<CatalogEntry>(connectionKeys.catalogEntry(entry.ref));
      if (!full) {
        try {
          full = await queryClient.fetchQuery({
            queryKey: connectionKeys.catalogEntry(entry.ref),
            queryFn: () => catalogSearchGrpc.get(entry.ref),
            staleTime: 5 * 60_000,
          });
        } catch {
          full = undefined;
        }
      }
      const events = ((full?.payloadSchema?.properties?.event?.enum ?? []) as unknown[]).map(String);
      add(triggerFromCatalog(entry, events, declared), entry.ref);
    },
    [add, declared, queryClient],
  );

  return { chooseBuiltin, chooseCatalog, personal, closePersonal: useCallback(() => setPersonal(null), []) };
}
