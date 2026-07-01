// Shared UI primitives for the in-app cloud settings sections
// (Billing / Environments / Reliant AI). Ported from admin-web's
// `frontends/admin-web/src/components/ui/*` but re-implemented against
// reliant's CSS-variable theme tokens so both light and dark themes render
// correctly. Vertical sections import from `./ui`.
//
// The barrel exports exactly the contract surface:
//   Button, Card, CardHeader, CardTitle, CardContent, PageHeader,
//   Table, Thead, Tbody, Tr, Th, Td, Badge, StatusDot, EmptyState
export { Button } from "./button";
export type { ButtonProps, ButtonVariant, ButtonSize } from "./button";

export { Card, CardHeader, CardTitle, CardContent } from "./card";
export type {
  CardProps,
  CardHeaderProps,
  CardTitleProps,
  CardContentProps,
} from "./card";

export { PageHeader } from "./page_header";
export type { PageHeaderProps } from "./page_header";

export { Table, Thead, Tbody, Tr, Th, Td } from "./table";
export type {
  TableProps,
  TheadProps,
  TbodyProps,
  TrProps,
  ThProps,
  TdProps,
} from "./table";

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
