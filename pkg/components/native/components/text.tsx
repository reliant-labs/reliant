import React from "react";
import {
  Text as RNText,
  StyleSheet,
  useColorScheme,
  type StyleProp,
  type TextProps as RNTextProps,
  type TextStyle,
} from "react-native";
import { colors, textSizes } from "../tokens";

/**
 * Text — wraps RN's Text with a size / weight / tone taxonomy so call
 * sites pick from named tokens instead of hand-rolling fontSize +
 * fontWeight. Mirrors the typography shapes you typically reach for
 * across screens; deliberately small surface — for one-off prose, drop
 * to plain `<Text>` from react-native.
 *
 * Naming aliases the web library where it makes sense
 * (`muted`, `destructive`) so cross-platform code reads consistently.
 */
export type TextSize = keyof typeof textSizes;
export type TextWeight = "regular" | "medium" | "semibold" | "bold";
export type TextTone = "default" | "muted" | "primary" | "destructive";

export interface TextProps extends RNTextProps {
  size?: TextSize;
  weight?: TextWeight;
  tone?: TextTone;
  style?: StyleProp<TextStyle>;
}

const weightMap: Record<TextWeight, TextStyle["fontWeight"]> = {
  regular: "400",
  medium: "500",
  semibold: "600",
  bold: "700",
};

export default function Text({
  size = "base",
  weight = "regular",
  tone = "default",
  style,
  children,
  ...rest
}: TextProps) {
  const scheme = useColorScheme() ?? "light";
  const palette = colors[scheme];

  const toneColor =
    tone === "muted"
      ? palette.mutedForeground
      : tone === "primary"
        ? palette.primary
        : tone === "destructive"
          ? palette.destructive
          : palette.foreground;

  return (
    <RNText
      style={[
        styles.base,
        {
          fontSize: textSizes[size],
          fontWeight: weightMap[weight],
          color: toneColor,
        },
        style,
      ]}
      {...rest}
    >
      {children}
    </RNText>
  );
}

const styles = StyleSheet.create({
  base: {
    // Sensible reading default. Apps that need tighter leading can
    // override via the `style` prop.
    lineHeight: 22,
  },
});
