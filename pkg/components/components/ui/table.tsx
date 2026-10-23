import React from "react";

/**
 * Table — bare structural primitives for building data tables. Each
 * subcomponent wraps the corresponding native table element and applies
 * forge's default border/spacing/typography rules. The default export is
 * the outer <Table> shell (rounded border + horizontal scroll); compose
 * with TableHeader / TableBody / TableRow / TableCell / TableHead for the
 * full tree.
 *
 * Reach for these when you are laying out a bespoke table. A list view
 * over a Connect list RPC should use `<Resource>` from
 * `@reliantlabs/forge-web-runtime` instead — it owns the loading / error /
 * empty ladder and cursor pagination, and is what the generated CRUD
 * list pages render.
 */
export interface TableProps extends React.HTMLAttributes<HTMLTableElement> {
  /** Wrap in a bordered container with overflow-x. Defaults to true. */
  bordered?: boolean;
}

function Table({ bordered = true, className, children, ...rest }: TableProps) {
  const tableClass = ["min-w-full divide-y divide-border", className ?? ""]
    .filter(Boolean)
    .join(" ");

  if (!bordered) {
    return (
      <table className={tableClass} {...rest}>
        {children}
      </table>
    );
  }

  return (
    <div className="overflow-x-auto rounded-lg border border-border bg-surface">
      <table className={tableClass} {...rest}>
        {children}
      </table>
    </div>
  );
}

export default Table;

export type TableHeaderProps = React.HTMLAttributes<HTMLTableSectionElement>;

export function TableHeader({ className, children, ...rest }: TableHeaderProps) {
  const composed = ["bg-surface-muted", className ?? ""].filter(Boolean).join(" ");
  return (
    <thead className={composed} {...rest}>
      {children}
    </thead>
  );
}

export type TableBodyProps = React.HTMLAttributes<HTMLTableSectionElement>;

export function TableBody({ className, children, ...rest }: TableBodyProps) {
  const composed = ["divide-y divide-border", className ?? ""].filter(Boolean).join(" ");
  return (
    <tbody className={composed} {...rest}>
      {children}
    </tbody>
  );
}

export interface TableRowProps extends React.HTMLAttributes<HTMLTableRowElement> {
  /** Striped (alternating row) styling. Caller passes the row index. */
  striped?: boolean;
  /** Render as clickable; adds hover/cursor styling. */
  clickable?: boolean;
}

export function TableRow({
  striped,
  clickable,
  className,
  children,
  onClick,
  onKeyDown,
  ...rest
}: TableRowProps) {
  const composed = [
    "transition-colors",
    clickable
      ? "cursor-pointer hover:bg-accent-surface focus-visible:bg-accent-surface focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent"
      : "",
    striped ? "bg-surface-muted" : "bg-surface",
    className ?? "",
  ]
    .filter(Boolean)
    .join(" ");

  // Row-link pattern: a clickable row is keyboard-reachable (tabIndex) and
  // activates on Enter/Space like the link it stands in for. Without this,
  // generated list pages are mouse-only.
  const interactive = clickable && onClick;

  return (
    <tr
      className={composed}
      onClick={onClick}
      {...(interactive
        ? {
            tabIndex: 0,
            onKeyDown: (e: React.KeyboardEvent<HTMLTableRowElement>) => {
              onKeyDown?.(e);
              if (e.defaultPrevented) return;
              if (e.key === "Enter" || e.key === " ") {
                e.preventDefault();
                onClick?.(e as unknown as React.MouseEvent<HTMLTableRowElement>);
              }
            },
          }
        : { onKeyDown })}
      {...rest}
    >
      {children}
    </tr>
  );
}

export interface TableHeadProps extends React.ThHTMLAttributes<HTMLTableCellElement> {
  /** Mark the header cell as sortable; flips the cursor. */
  sortable?: boolean;
}

export function TableHead({ sortable, className, children, scope, ...rest }: TableHeadProps) {
  const composed = [
    "h-9 px-4 text-left text-xs font-medium text-ink-muted",
    sortable ? "cursor-pointer select-none" : "",
    className ?? "",
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <th scope={scope ?? "col"} className={composed} {...rest}>
      {children}
    </th>
  );
}

export type TableCellProps = React.TdHTMLAttributes<HTMLTableCellElement>;

export function TableCell({ className, children, ...rest }: TableCellProps) {
  const composed = ["whitespace-nowrap px-4 py-3 text-sm text-ink", className ?? ""]
    .filter(Boolean)
    .join(" ");
  return (
    <td className={composed} {...rest}>
      {children}
    </td>
  );
}
