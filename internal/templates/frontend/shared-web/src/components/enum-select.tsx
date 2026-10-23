import React from "react";

import Select, { type SelectProps } from "@/components/ui/select";
import { enumOptions } from "@/lib/format-utils";

/**
 * EnumSelect — a <select> populated from a protobuf-es enum, with no
 * hand-written option list and no hand-rolled option typing.
 *
 * protoc-gen-es (`target=ts`) emits proto enums as NUMERIC TypeScript enums,
 * whose runtime object carries BOTH the forward (name → number) AND the reverse
 * (number → name) mappings. Its value type is therefore `string | number`, not
 * `Record<string, number>` — the mismatch a hand-rolled enum select trips on
 * (TS2322). `enumOptions` consumes that honest shape; this component just wires
 * it to the component-library <Select>. The `UNSPECIFIED` / 0 sentinel is
 * dropped unless you ask for it:
 *
 *   <EnumSelect enumObject={OrderStatus} {...register("status")} />
 *   <EnumSelect enumObject={OrderStatus} includeUnspecified {...register("status")} />
 *
 * It forwards its ref and spreads every native <select> prop through to the
 * underlying control, so it drops into a react-hook-form `register(...)` spread
 * unchanged. Register the field with
 * `z.coerce.number().pipe(z.nativeEnum(OrderStatus))` so the numeric option
 * value round-trips to the wire enum.
 */
export interface EnumSelectProps extends Omit<SelectProps, "options"> {
  /** The protobuf-es enum object, e.g. `OrderStatus`. */
  enumObject: Record<string, string | number>;
  /** Include the `UNSPECIFIED` / 0 sentinel as a selectable option. */
  includeUnspecified?: boolean;
}

export const EnumSelect = React.forwardRef<HTMLSelectElement, EnumSelectProps>(
  function EnumSelect({ enumObject, includeUnspecified, ...rest }, ref) {
    return (
      <Select
        ref={ref}
        options={enumOptions(enumObject, { includeUnspecified })}
        {...rest}
      />
    );
  },
);
