// Shared UI primitives for the in-app cloud settings sections
// (Billing / Environments / Reliant AI). Ported from admin-web's
// `frontends/admin-web/src/components/ui/*` but re-implemented against
// reliant's CSS-variable theme tokens so both light and dark themes render
// correctly. Vertical sections import from `./ui`; buttons and tables come
// from forge's library (`@/components/forge-ui/button`, `.../data_table`).
//
// The barrel exports exactly the contract surface:
//   Card, CardHeader, CardTitle, CardContent, Badge, StatusDot, EmptyState
export { Card, CardHeader, CardTitle, CardContent, CardInset } from "./card";
export type {
  CardProps,
  CardHeaderProps,
  CardTitleProps,
  CardContentProps,
} from "./card";


export { Badge } from "./badge";
export type { BadgeProps, BadgeVariant, BadgeSize } from "./badge";

export { StatusDot } from "./status_dot";
export type {
  StatusDotProps,
  StatusDotVariant,
  StatusDotSize,
} from "./status_dot";

export { EmptyState } from "./empty_state";
export type { EmptyStateProps } from "./empty_state";
