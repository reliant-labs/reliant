import React from "react";
import {
  StyleSheet,
  View,
  useColorScheme,
  type StyleProp,
  type ViewProps,
  type ViewStyle,
} from "react-native";
import { colors, radius, spacing } from "../tokens";

/**
 * Card — surface primitive. A bordered, rounded panel with optional
 * inner padding. Same `padding` taxonomy as the web Card, plus a subtle
 * shadow tuned to look right on both iOS and Android.
 *
 * For complex layouts use <Stack> inside the card body — the card
 * deliberately does not impose a flex direction.
 */
export type CardPadding = "none" | "sm" | "md" | "lg";

export interface CardProps extends ViewProps {
  padding?: CardPadding;
  style?: StyleProp<ViewStyle>;
}

export default function Card({ padding = "md", style, children, ...rest }: CardProps) {
  const scheme = useColorScheme() ?? "light";
  const palette = colors[scheme];
  return (
    <View
      style={[
        styles.card,
        {
          backgroundColor: palette.card,
          borderColor: palette.border,
        },
        paddingStyles[padding],
        style,
      ]}
      {...rest}
    >
      {children}
    </View>
  );
}

const styles = StyleSheet.create({
  card: {
    borderWidth: 1,
    borderRadius: radius.lg,
    // Cross-platform elevation: iOS shadow + Android elevation.
    shadowColor: "#000",
    shadowOpacity: 0.04,
    shadowOffset: { width: 0, height: 1 },
    shadowRadius: 2,
    elevation: 1,
  },
});

const paddingStyles: Record<CardPadding, ViewStyle> = {
  none: { padding: 0 },
  sm: { padding: spacing[3] },
  md: { padding: spacing[4] },
  lg: { padding: spacing[6] },
};
