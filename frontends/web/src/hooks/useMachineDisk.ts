// Copyright (c) 2025 Reliant Labs

import { create } from "@bufbuild/protobuf";
import { useMutation, useQueryClient } from "@tanstack/react-query";

import { grpcClient } from "@/api/grpc-client";
import { ClearMachineCachesRequestSchema } from "@/gen/reliant/v1/daemon_registry_pb";
import { invalidateDaemonList } from "@/hooks/useDaemonStatus";
import { inboxKeys } from "@/hooks/inbox-queries";

/**
 * Clears the rebuildable caches the user confirmed on one machine. The machine
 * decides what is safe and runs the clean-up in the background; progress shows
 * as `DiskConsumer.clearing` on the next disk report, which the gateway
 * announces — so this only refreshes the lists, it does not poll.
 */
export function useClearMachineCaches() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (request: { daemonId: string; kinds: string[] }) =>
      grpcClient.daemonRegistry().clearMachineCaches(create(ClearMachineCachesRequestSchema, request)),
    onSettled: () => {
      invalidateDaemonList(queryClient);
      void queryClient.invalidateQueries({ queryKey: inboxKeys.all });
    },
  });
}
