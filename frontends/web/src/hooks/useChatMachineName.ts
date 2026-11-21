import { machineDisplayName } from "../lib/machineName";
import { useDaemonList } from "./useOnboardingQueries";

/** Name the machine recorded on this chat, not a guess at where it could run. */
export function useChatMachineName(chat?: {
  activeDaemonId?: string;
  noMachine?: boolean;
  viewerAccess?: boolean;
}): string | undefined {
  const daemonId = chat?.noMachine || chat?.viewerAccess ? undefined : chat?.activeDaemonId;
  const { data } = useDaemonList({ enabled: Boolean(daemonId), refetchInterval: false });
  const machine = daemonId ? data?.find((entry) => entry.daemonId === daemonId) : undefined;
  return machine ? machineDisplayName(machine) : undefined;
}
