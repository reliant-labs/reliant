// Copyright (c) 2025 Reliant Labs

import { Users } from "lucide-react";

import { cn } from "@/lib/utils";
import { seatsFull, seatSummary, type SeatUsage } from "@/services/controlPlane/members";

/** "3 of 5 seats": a pending invitation holds a seat until it is answered. */
export function SeatCount({ seats, className }: { seats: SeatUsage; className?: string }) {
  const full = seatsFull(seats);
  return (
    <p className={cn("flex items-center gap-1.5 text-xs", full ? "text-warning-ink" : "text-muted-foreground", className)} data-testid="members-seats">
      <Users className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
      <span>{seatSummary(seats)}</span>
      {full && <span>· every seat is taken</span>}
    </p>
  );
}
