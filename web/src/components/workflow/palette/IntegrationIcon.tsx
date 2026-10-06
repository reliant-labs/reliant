// Copyright (c) 2025 Reliant Labs

import { IntegrationLogoTile } from "../../icons/IntegrationLogo";

/**
 * @deprecated Use `IntegrationLogoTile` (or the bare `IntegrationLogo`) from
 * `components/icons/IntegrationLogo`. This shim keeps the one remaining
 * caller, ConnectIntegrationDialog, rendering brand logos until it moves
 * over; delete it then.
 */
export function IntegrationIcon({ hint, className, size = "md" }: { hint?: string; className?: string; size?: "sm" | "md" }) {
  return <IntegrationLogoTile icon={hint} size={size} className={className} />;
}
