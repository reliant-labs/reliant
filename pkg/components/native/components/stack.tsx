import React from "react";
import { View, type StyleProp, type ViewProps, type ViewStyle } from "react-native";
import { spacing } from "../tokens";

/**
 * Stack — layout primitive. Lays children out in a row or column with a
 * consistent gap from the spacing scale. Maps to the everyday RN
 * pattern of a View with `flexDirection` + manual margin between
 * children, but with the gap math centralised so spacing stays on the
 * scale.
 *
 * Use HStack / VStack as the sugar exports if you prefer named
 * directions.
 */
export type StackDirection = "row" | "column";

// 0-8 maps to the spacing scale entries. The token export uses a sparse
// shape (1, 2, 3, 4, 6, 8, 12) so we constrain the prop to those keys.
export type StackGap = keyof typeof spacing;

export type StackAlign = "start" | "center" | "end" | "stretch";
export type StackJustify = "start" | "center" | "end" | "space-between" | "space-around";

export interface StackProps extends ViewProps {
  direction?: StackDirection;
  gap?: StackGap;
  align?: StackAlign;
  justify?: StackJustify;
  wrap?: boolean;
  style?: StyleProp<ViewStyle>;
}

const alignMap: Record<StackAlign, ViewStyle["alignItems"]> = {
  start: "flex-start",
  center: "center",
  end: "flex-end",
  stretch: "stretch",
};

const justifyMap: Record<StackJustify, ViewStyle["justifyContent"]> = {
  start: "flex-start",
  center: "center",
  end: "flex-end",
  "space-between": "space-between",
  "space-around": "space-around",
};

export default function Stack({
  direction = "column",
  gap = 3,
  align,
  justify,
  wrap,
  style,
  children,
  ...rest
}: StackProps) {
  const layout: ViewStyle = {
    flexDirection: direction,
    // RN supports `gap` from 0.71+. The current Expo template is on
    // 0.76, so this works without manual margin math.
    gap: spacing[gap],
    alignItems: align ? alignMap[align] : undefined,
    justifyContent: justify ? justifyMap[justify] : undefined,
    flexWrap: wrap ? "wrap" : "nowrap",
  };
  return (
    <View style={[layout, style]} {...rest}>
      {children}
    </View>
  );
}

/** HStack — horizontal sugar for `<Stack direction="row" />`. */
export function HStack(props: Omit<StackProps, "direction">) {
  return <Stack direction="row" {...props} />;
}

/** VStack — vertical sugar for `<Stack direction="column" />` (the default). */
export function VStack(props: Omit<StackProps, "direction">) {
  return <Stack direction="column" {...props} />;
}
