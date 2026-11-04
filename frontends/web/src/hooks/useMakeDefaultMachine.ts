// Copyright (c) 2025 Reliant Labs

/**
 * "Make default": makes one of the user's machines the one work runs on when
 * it names none (DaemonRegistryService.SetDefaultDaemon). The generated
 * mutation invalidates the shared daemon list (hooks/daemon-registry-queries),
 * so every reader — the machine picker, Settings → Machines, the composer's
 * wake status — sees the new `isDefault` on its next render.
 */

import { toast } from "@/lib/toast-manager";
import { useSetDefaultDaemon } from "./daemon-registry-service-hooks_gen";

export function useMakeDefaultMachine(): { makeDefault: (daemonId: string) => void; isPending: boolean } {
  const mutation = useSetDefaultDaemon({
    onError: (err) => void toast.error(err),
  });
  return {
    makeDefault: (daemonId: string) => mutation.mutate({ daemonId }),
    isPending: mutation.isPending,
  };
}
