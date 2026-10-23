// Copyright (c) 2025 Reliant Labs

/**
 * A console `<table>` that reads as a list of cards below `md`
 * (lib/stackedTable.ts has the why and the how). A drop-in for `<table>`:
 * same props, its header row names each stacked line.
 */

import type { TableHTMLAttributes } from "react";

import { cn } from "@/lib/utils";
import { STACKED_TABLE_CLASS, useStackedTableLabels } from "@/lib/stackedTable";

export function StackedTable({ className, ...props }: TableHTMLAttributes<HTMLTableElement>) {
  const ref = useStackedTableLabels();
  return <table ref={ref} className={cn(STACKED_TABLE_CLASS, className)} {...props} />;
}
