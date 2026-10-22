import React from "react";
import { cn } from "../../../../lib/utils";

/**
 * Table primitives — thin wrappers over the native table elements applying
 * admin-web's border/spacing/typography rhythm, re-tokenized against
 * reliant's theme (`divide-border`, `bg-muted`, `text-muted-foreground`).
 *
 * Exported as Table / Thead / Tbody / Tr / Th / Td (the names the vertical
 * sections import) rather than admin-web's TableHeader/TableBody/… names.
 */
export interface TableProps extends React.HTMLAttributes<HTMLTableElement> {
  /** Wrap in a bordered, horizontally-scrollable container. Defaults true. */
  bordered?: boolean;
}

export function Table({
  bordered = true,
  className,
  children,
  ...rest
}: TableProps) {
  const table = (
    <table
      className={cn("min-w-full divide-y divide-border", className)}
      {...rest}
    >
      {children}
    </table>
  );
  if (!bordered) return table;
  return (
    <div className="overflow-x-auto rounded-lg border border-border bg-card shadow-sm">
      {table}
    </div>
  );
}

export type TheadProps = React.HTMLAttributes<HTMLTableSectionElement>;

export function Thead({ className, children, ...rest }: TheadProps) {
  return (
    <thead className={cn("bg-muted/50", className)} {...rest}>
      {children}
    </thead>
  );
}

export type TbodyProps = React.HTMLAttributes<HTMLTableSectionElement>;

export function Tbody({ className, children, ...rest }: TbodyProps) {
  return (
    <tbody className={cn("divide-y divide-border", className)} {...rest}>
      {children}
    </tbody>
  );
}

export interface TrProps extends React.HTMLAttributes<HTMLTableRowElement> {
  clickable?: boolean;
}

export function Tr({ clickable, className, children, ...rest }: TrProps) {
  return (
    <tr
      className={cn(
        "transition-colors",
        clickable && "cursor-pointer hover:bg-muted/40",
        className
      )}
      {...rest}
    >
      {children}
    </tr>
  );
}

export interface ThProps
  extends React.ThHTMLAttributes<HTMLTableCellElement> {
  sortable?: boolean;
}

export function Th({ sortable, className, children, scope, ...rest }: ThProps) {
  return (
    <th
      scope={scope ?? "col"}
      className={cn(
        "px-4 py-3 text-left text-xs font-semibold uppercase tracking-wider text-muted-foreground",
        sortable && "cursor-pointer select-none",
        className
      )}
      {...rest}
    >
      {children}
    </th>
  );
}

export type TdProps = React.TdHTMLAttributes<HTMLTableCellElement>;

export function Td({ className, children, ...rest }: TdProps) {
  return (
    <td
      className={cn(
        "whitespace-nowrap px-4 py-3 text-sm text-foreground",
        className
      )}
      {...rest}
    >
      {children}
    </td>
  );
}

export default Table;
