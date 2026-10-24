/**
 * A provider's account routing mode — a user setting (SettingsService, served
 * from settingsSync's ListSettings cache), not account data, so it lives
 * beside the account hooks rather than under providerAccountServiceKeys.
 */

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { readSetting, upsertStringSetting } from "../lib/settingsPersistence";

/**
 * How a provider spreads work across its accounts.
 *  - "balance":  least used account first (the default).
 *  - "priority": the list order — fill the first account until it is limited,
 *                then the next.
 * Neither moves a chat off the account it already runs on, except on a limit.
 */
export type RoutingMode = "balance" | "priority";

export const DEFAULT_ROUTING_MODE: RoutingMode = "balance";

/** The setting the account router reads; one per provider driver id. */
export const routingSettingKey = (provider: string) => `provider.routing.${provider}`;

/**
 * Its own key, not settingsServiceKeys.getSetting: the value comes from the
 * settingsSync cache and is parsed, so it is not a GetSettingResponse.
 */
const routingModeKey = (provider: string) => ["provider-routing", provider] as const;

function parseRoutingMode(value: string | undefined): RoutingMode {
  return value === "priority" ? "priority" : DEFAULT_ROUTING_MODE;
}

export function useRoutingMode(provider: string) {
  const qc = useQueryClient();
  const queryKey = routingModeKey(provider);
  const q = useQuery({
    queryKey,
    queryFn: async () => {
      const read = await readSetting(routingSettingKey(provider));
      return parseRoutingMode(read.status === "found" ? read.value : undefined);
    },
    staleTime: 60_000,
  });
  const m = useMutation({
    mutationFn: async (mode: RoutingMode) => {
      await upsertStringSetting(routingSettingKey(provider), mode);
    },
    onMutate: async (mode) => {
      await qc.cancelQueries({ queryKey });
      const prev = qc.getQueryData<RoutingMode>(queryKey);
      qc.setQueryData<RoutingMode>(queryKey, mode);
      return { prev };
    },
    onError: (_e, _mode, ctx) => qc.setQueryData(queryKey, ctx?.prev),
  });
  return {
    mode: q.data ?? DEFAULT_ROUTING_MODE,
    setMode: (mode: RoutingMode) => m.mutateAsync(mode),
  };
}
