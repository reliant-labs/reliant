// Copyright (c) 2025 Reliant Labs

/**
 * A failure from a per-machine project call (browse a machine's folders,
 * add a folder as a project, attach or detach a machine) in words for the
 * user.
 *
 * The server already writes these refusals as sentences ("/x is already the
 * project "api" on gpu-box", "… is still being cloned onto …"), so its
 * message is shown as it is, without the Connect "[code]" prefix. Only the
 * codes whose server text is terse get our own copy: an offline machine
 * ("no daemon connected") and a machine that is not the caller's.
 */

import { Code, ConnectError } from "@connectrpc/connect";

export function describeMachineFolderError(err: unknown, machineName: string): string {
  if (err instanceof ConnectError) {
    switch (err.code) {
      case Code.Unavailable:
        return `${machineName} is offline. Start it, or wait for it to reconnect, then try again.`;
      case Code.NotFound:
        if (/daemon|machine/i.test(err.rawMessage)) {
          return `${machineName} is not one of your machines any more. Choose another machine.`;
        }
        return err.rawMessage || `Not found on ${machineName}.`;
      default:
        return err.rawMessage || err.message;
    }
  }
  if (err instanceof Error && err.message) return err.message;
  return `Something went wrong on ${machineName}.`;
}
