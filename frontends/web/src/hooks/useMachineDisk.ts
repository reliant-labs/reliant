// Copyright (c) 2025 Reliant Labs

import { useQueryClient } from "@tanstack/react-query";

import { useClearMachineCaches as useClearMachineCachesRpc } from "@/hooks/daemon-registry-service-hooks_gen";
import { inboxServiceKeys } from "@/hooks/inbox-service-hooks_gen";

/**
 * Clears the rebuildable caches the user confirmed on one machine. The machine
 * decides what is safe and runs the clean-up in the background; progress shows
 * as `DiskConsumer.clearing` on the next disk report, which the gateway
 * announces — so this only refreshes the lists, it does not poll.
 *
 * The generated write refetches the daemon list (every DaemonRegistryService
 * read); the disk-pressure inbox item is refreshed here.
 */
export function useClearMachineCaches() {
  const queryClient = useQueryClient();
  return useClearMachineCachesRpc({
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: inboxServiceKeys.all });
    },
  });
}
