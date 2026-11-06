import React from "react";
import {
  Pressable as RNPressable,
  type PressableProps as RNPressableProps,
  type StyleProp,
  type ViewStyle,
} from "react-native";

/**
 * Pressable — wraps RN's Pressable with a default pressed-state opacity
 * so every tappable area has consistent feedback without re-implementing
 * the press handler per call site. Pass `style` as a static ViewStyle
 * (the wrapper composes the pressed style for you) or as a function for
 * full control.
 *
 * Reach for this when you want a touch target that isn't a Button —
 * e.g. a tappable Card, an avatar trigger, a list row.
 */
export interface PressableProps extends Omit<RNPressableProps, "style"> {
  style?: StyleProp<ViewStyle>;
  /** Opacity to apply while pressed. Defaults to 0.7. */
  pressedOpacity?: number;
}

export default function Pressable({
  style,
  pressedOpacity = 0.7,
  children,
  disabled,
  ...rest
}: PressableProps) {
  return (
    <RNPressable
      disabled={disabled}
      style={({ pressed }) => [
        style,
        pressed && !disabled ? { opacity: pressedOpacity } : null,
        disabled ? { opacity: 0.5 } : null,
      ]}
      {...rest}
    >
      {children as React.ReactNode}
    </RNPressable>
  );
}
