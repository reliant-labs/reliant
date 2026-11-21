/**
 * Token-bridge probe — NOT a product surface.
 *
 * Enumerates every forge semantic token so the Tailwind content scan emits
 * each utility, letting a browser check resolve them to concrete colors.
 * The bridge in index.css is verified against this; see its comment block.
 */
export const FORGE_TOKEN_PROBE_CLASSES = [
  "bg-surface",
  "bg-surface-muted",
  "border-border",
  "border-border-strong",
  "text-ink",
  "text-ink-muted",
  "text-ink-subtle",
  "bg-accent",
  "bg-accent-hover",
  "bg-accent-surface",
  "text-accent",
  "text-accent-ink",
  "text-on-accent",
  "border-accent-border",
  "ring-accent",
  "bg-danger",
  "bg-danger-hover",
  "bg-danger-surface",
  "text-danger",
  "text-danger-ink",
  "text-on-danger",
  "border-danger-border",
  "bg-success",
  "bg-success-surface",
  "text-success",
  "text-success-ink",
  "border-success-border",
  "bg-warning",
  "bg-warning-surface",
  "text-warning",
  "text-warning-ink",
  "text-on-warning",
  "border-warning-border",
] as const;

export default function ForgeTokenProbe() {
  return (
    <div className="forge-ui">
      {FORGE_TOKEN_PROBE_CLASSES.map((cls) => (
        <div key={cls} data-token={cls} className={cls} />
      ))}
    </div>
  );
}
