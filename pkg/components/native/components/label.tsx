import React from "react";
import {
  StyleSheet,
  Text,
  useColorScheme,
  type StyleProp,
  type TextProps,
  type TextStyle,
} from "react-native";
import { colors, spacing, textSizes } from "../tokens";

/**
 * Label — standalone label primitive for grouping captions above non-
 * Input controls (e.g. a Switch row, a Pressable card). The Input
 * primitive renders its own label internally; use this for everything
 * else.
 */
export interface LabelProps extends TextProps {
  required?: boolean;
  style?: StyleProp<TextStyle>;
}

export default function Label({ required, children, style, ...rest }: LabelProps) {
  const scheme = useColorScheme() ?? "light";
  const palette = colors[scheme];
  return (
    <Text
      style={[styles.label, { color: palette.foreground }, style]}
      accessibilityRole="text"
      {...rest}
    >
      {children}
      {required ? <Text style={{ color: palette.destructive }}> *</Text> : null}
    </Text>
  );
}

const styles = StyleSheet.create({
  label: {
    fontSize: textSizes.sm,
    fontWeight: "500",
    marginBottom: spacing[1],
  },
});
