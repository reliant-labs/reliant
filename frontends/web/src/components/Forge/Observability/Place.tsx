// Copyright (c) 2025 Reliant Labs

/**
 * Where a browser was (geo.ts), as one muted line: "near Berlin, Germany". The
 * full place and the DB-IP attribution are its title. Renders nothing when the
 * place is unknown, so a server-only trace or an unlocated session shows no
 * empty label.
 */

import { MapPin } from "lucide-react";

import { cn } from "@/lib/utils";

import { placeLabel, placeTitle, type GeoFields } from "./geo";

export function Place({ geo, className, testId }: { geo: GeoFields; className?: string; testId?: string }) {
  const label = placeLabel(geo);
  if (!label) return null;
  return (
    <span className={cn("inline-flex min-w-0 items-center gap-1", className)} title={placeTitle(geo)} data-testid={testId}>
      <MapPin className="h-3 w-3 shrink-0" aria-hidden="true" />
      <span className="truncate">
        <span className="sr-only">near </span>
        {label}
      </span>
    </span>
  );
}
