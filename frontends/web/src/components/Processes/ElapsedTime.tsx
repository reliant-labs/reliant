import { useEffect, useState } from "react";
import { formatElapsed } from "../../lib/processStatus";
import { cn } from "../../lib/utils";

interface ElapsedTimeProps {
  /** When it started (epoch ms); nothing renders without one. */
  startMs: number | null;
  /** When it ended; unset keeps the clock running. */
  endMs?: number | null;
  className?: string;
}

/** How long something has run, ticking each second until it ends: "8s", "4m", "1h12m". */
export function ElapsedTime({ startMs, endMs, className }: ElapsedTimeProps) {
  const ended = endMs != null;
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    if (startMs === null || ended) return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [startMs, ended]);

  if (startMs === null) return null;
  return (
    <span className={cn("tabular-nums", className)}>{formatElapsed(startMs, ended ? endMs : now)}</span>
  );
}
