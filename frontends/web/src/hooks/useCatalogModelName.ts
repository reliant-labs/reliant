import { useEffect, useState } from "react";
import { api } from "../api/client";
import { splitModelId } from "../lib/modelId";

// One ListModelsByProvider request per driver per session: a provider's
// catalog names do not change while the app runs. A failed request is
// forgotten so the next render can retry it.
const namesByDriver = new Map<string, Promise<Map<string, string>>>();

function catalogNames(driverId: string): Promise<Map<string, string>> {
  let names = namesByDriver.get(driverId);
  if (!names) {
    names = api.models
      .listByProvider(driverId)
      .then((models) => new Map(models.map((m) => [m.id, m.name])))
      .catch((error: unknown) => {
        namesByDriver.delete(driverId);
        throw error;
      });
    namesByDriver.set(driverId, names);
  }
  return names;
}

/** Test seam: forget every cached provider catalog. */
export function resetCatalogModelNames(): void {
  namesByDriver.clear();
}

/**
 * The catalog name of a pinned model ("gpt-5.6-sol@codex" → "GPT-5.6 Sol")
 * that is missing from the user's model list.
 *
 * The picker's list carries only models a connected provider can serve, so a
 * chat still pinned to a disconnected provider's model has no name there.
 * ListModelsByProvider is not gated on what the user has connected: it names
 * every model the provider offers. Pass `undefined` when the name is already
 * known and nothing is fetched.
 *
 * Returns undefined until the name is known (or when the provider does not
 * list the model); callers fall back to the bare model id.
 */
export function useCatalogModelName(pinnedId: string | undefined): string | undefined {
  const [resolved, setResolved] = useState<{ pinnedId: string; name: string } | null>(null);

  useEffect(() => {
    if (!pinnedId) return;
    const { modelId, driverId } = splitModelId(pinnedId);
    if (!driverId) return;
    let cancelled = false;
    catalogNames(driverId)
      .then((names) => {
        const name = names.get(modelId);
        if (!cancelled && name) setResolved({ pinnedId, name });
      })
      .catch(() => {
        // The bare model id is an honest fallback; nothing to surface.
      });
    return () => {
      cancelled = true;
    };
  }, [pinnedId]);

  return resolved && resolved.pinnedId === pinnedId ? resolved.name : undefined;
}
