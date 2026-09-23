// Inline `style` below carries RUNTIME-computed width/height (caller-provided
// px or percentages) that cannot be expressed as static Tailwind utilities.
// Each frontend's own eslint config exempts src/components/ui/** for exactly
// this reason; the exemption lives there and NOT as an in-file directive,
// because a directive naming a framework-specific rule is a hard error in any
// tree whose config does not load that plugin (a Vite SPA does not load
// eslint-plugin-react or @next/eslint-plugin-next).
import React from "react";

type SkeletonVariant =
  | "text"
  | "circular"
  | "rectangular"
  | "card"
  | "table-row"
  | "list-item"
  | "form-field";

interface SkeletonLoaderProps {
  variant?: SkeletonVariant;
  count?: number;
  width?: string;
  height?: string;
  className?: string;
}

function SkeletonPulse({
  className = "",
  style,
}: {
  className?: string;
  style?: React.CSSProperties;
}) {
  return (
    <div
      className={`animate-pulse rounded bg-border ${className}`}
      style={style}
    />
  );
}

function TextSkeleton({ width }: { width?: string }) {
  return (
    <div className="space-y-2.5">
      <SkeletonPulse className="h-4" style={{ width: width ?? "100%" }} />
      <SkeletonPulse className="h-4 w-4/5" />
      <SkeletonPulse className="h-4 w-3/5" />
    </div>
  );
}

function CardSkeleton() {
  return (
    <div className="rounded-lg border border-border bg-surface p-4">
      <SkeletonPulse className="mb-3 h-4 w-2/3" />
      <SkeletonPulse className="mb-2 h-3 w-full" />
      <SkeletonPulse className="mb-4 h-3 w-4/5" />
      <div className="flex items-center gap-2">
        <SkeletonPulse className="h-6 w-6 rounded-full" />
        <SkeletonPulse className="h-3 w-24" />
      </div>
    </div>
  );
}

function TableRowSkeleton() {
  return (
    <div className="flex items-center gap-4 border-b border-border px-4 py-3">
      <SkeletonPulse className="h-4 w-8" />
      <SkeletonPulse className="h-4 w-32" />
      <SkeletonPulse className="h-4 w-24" />
      <SkeletonPulse className="h-4 w-20" />
      <SkeletonPulse className="ml-auto h-4 w-16" />
    </div>
  );
}

function ListItemSkeleton() {
  return (
    <div className="flex items-center gap-3 py-3">
      <SkeletonPulse className="h-10 w-10 rounded-full" />
      <div className="flex-1 space-y-1.5">
        <SkeletonPulse className="h-4 w-40" />
        <SkeletonPulse className="h-3 w-64" />
      </div>
    </div>
  );
}

function FormFieldSkeleton() {
  return (
    <div className="space-y-1.5">
      <SkeletonPulse className="h-3 w-20" />
      <SkeletonPulse className="h-10 w-full rounded-md" />
    </div>
  );
}

export default function SkeletonLoader({
  variant = "text",
  count = 1,
  width,
  height,
  className,
}: SkeletonLoaderProps) {
  const items = Array.from({ length: count });

  const renderers: Record<SkeletonVariant, () => React.ReactNode> = {
    text: () => <TextSkeleton width={width} />,
    circular: () => (
      <SkeletonPulse
        className={`rounded-full ${className ?? ""}`}
        style={{ width: width ?? "40px", height: height ?? "40px" }}
      />
    ),
    rectangular: () => (
      <SkeletonPulse
        className={className}
        style={{ width: width ?? "100%", height: height ?? "120px" }}
      />
    ),
    card: () => <CardSkeleton />,
    "table-row": () => <TableRowSkeleton />,
    "list-item": () => <ListItemSkeleton />,
    "form-field": () => <FormFieldSkeleton />,
  };

  return (
    <div className="space-y-3" role="status" aria-label="Loading">
      {items.map((_, i) => (
        <React.Fragment key={i}>{renderers[variant]()}</React.Fragment>
      ))}
    </div>
  );
}
