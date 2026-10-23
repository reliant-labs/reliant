import Badge from "@/components/ui/badge";
import { enumBadgeVariant, enumLabel, type EnumRef } from "@/lib/format-utils";

/**
 * StatusBadge — render a proto enum value as a colored status badge without
 * hand-mapping and without a reverse lookup at the call site.
 *
 * Hand it the FIELD and the ENUM. protobuf-es v2 emits proto enums as NUMERIC
 * TypeScript enums, so `order.status` is the number `1` at runtime, not
 * "PENDING" — and the enum OBJECT is the only thing carrying the number → name
 * reverse map. Pass it and the badge resolves the member name, drops the
 * redundant `ORDER_STATUS_` prefix, humanizes it, and colors it:
 *
 *   <StatusBadge value={order.status} enumType={OrderStatus} />
 *   <StatusBadge value="PAYMENT_CAPTURED" enumType={OrderStatus} dot={false} />
 *
 * For a plain string column with no proto enum behind it, `enumType` also
 * accepts the TS type NAME so a fully-qualified DB token still collapses to the
 * same label — `<StatusBadge value="ORDER_STATUS_PAID" enumType="OrderStatus" />`.
 * A name string carries no reverse map, though, so pairing it with a runtime
 * NUMBER is unresolvable; rather than leak the ordinal ("1") into the UI as if
 * it were a status, that pairing renders the unset badge. Pass the object.
 *
 * Labels and colors come from `format-utils` (`enumLabel` + `enumBadgeVariant`),
 * which already knows the built-in lifecycle vocabulary and the app's registered
 * domain statuses. Unset values render a neutral em-dash badge. To recolor a
 * domain status, register it once via `registerStatusVariants` — never edit this
 * component.
 */
export interface StatusBadgeProps {
  /** The raw field — a runtime enum number, or a status string. */
  value: string | number | null | undefined;
  /** The protobuf-es enum object (e.g. `OrderStatus`), or its TS type name. */
  enumType?: EnumRef;
  size?: "sm" | "md" | "lg";
  /** Show the leading status dot. Defaults to true. */
  dot?: boolean;
}

export function StatusBadge({
  value,
  enumType,
  size = "md",
  dot = true,
}: StatusBadgeProps) {
  // A type-NAME string cannot reverse an ordinal into a member name. Treat that
  // pairing as unset so the badge never renders a bare enum number as a status.
  const resolvable = !(
    typeof value === "number" && typeof enumType === "string"
  );
  const safeValue = resolvable ? value : null;

  return (
    <Badge
      label={enumLabel(safeValue, enumType)}
      variant={enumBadgeVariant(safeValue, enumType)}
      size={size}
      dot={dot}
    />
  );
}
