import type { BackgroundProcess } from "../../hooks/background-queries";
import { formatElapsed, parseProcessTime } from "../../lib/processStatus";

/**
 * What the machine's idle reaper will do with a running process, in a line a
 * person reads: a background command a finished chat left behind is stopped
 * once idle, unless its agent asked to keep it alive. Null when there is
 * nothing worth saying (an older machine that does not report it).
 */
export function reaperNote(
  process: Pick<BackgroundProcess, "keep_alive" | "stops_after" | "stop_exempt">,
  now: number = Date.now(),
): string | null {
  if (process.keep_alive) return "Kept alive: the idle reaper leaves it running.";
  const stopsAfter = parseProcessTime(process.stops_after);
  if (stopsAfter !== null) {
    return stopsAfter <= now
      ? "Idle: its machine may stop it soon."
      : `Stopped if idle for ${formatElapsed(now, stopsAfter)} more, once the chat is done.`;
  }
  return null;
}
