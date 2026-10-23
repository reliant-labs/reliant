import React from "react";
import {
  ActivityIndicator,
  Pressable,
  StyleSheet,
  Text,
  View,
  useColorScheme,
  type PressableProps,
  type StyleProp,
  type ViewStyle,
  type TextStyle,
} from "react-native";
import { colors, radius, spacing, textSizes } from "../tokens";

/**
 * Button — generic action primitive for React Native. Wraps Pressable
 * with the same variant/size taxonomy as the web Button so cross-platform
 * teams can keep their mental model.
 *
 * Variants: primary | secondary | outline | ghost | danger.
 * Sizes:    sm | md | lg.
 *
 * Use `onPress` (RN convention) — `onClick` does not exist on Pressable.
 * Pass `style` to extend; the variant defaults are inline so a single
 * prop override is enough for one-off tweaks.
 */
export type ButtonVariant = "primary" | "secondary" | "outline" | "ghost" | "danger";
export type ButtonSize = "sm" | "md" | "lg";

export interface ButtonProps extends Omit<PressableProps, "children" | "style"> {
  children?: React.ReactNode;
  variant?: ButtonVariant;
  size?: ButtonSize;
  fullWidth?: boolean;
  isLoading?: boolean;
  style?: StyleProp<ViewStyle>;
  textStyle?: StyleProp<TextStyle>;
}

export default function Button({
  children,
  variant = "primary",
  size = "md",
  fullWidth,
  isLoading,
  disabled,
  style,
  textStyle,
  onPress,
  ...rest
}: ButtonProps) {
  const scheme = useColorScheme() ?? "light";
  const palette = colors[scheme];

  const variantStyle = variantStyles(palette)[variant];
  const variantText = variantTextStyles(palette)[variant];
  const sizeStyle = sizeStyles[size];

  const isDisabled = disabled || isLoading;

  return (
    <Pressable
      accessibilityRole="button"
      accessibilityState={{ disabled: !!isDisabled, busy: !!isLoading }}
      disabled={isDisabled}
      onPress={onPress}
      style={({ pressed }) => [
        styles.base,
        variantStyle.container,
        sizeStyle.container,
        fullWidth && styles.fullWidth,
        pressed && !isDisabled && { opacity: 0.7 },
        isDisabled && { opacity: 0.5 },
        style,
      ]}
      {...rest}
    >
      {isLoading ? (
        <ActivityIndicator
          size="small"
          color={variantText.color}
          style={{ marginRight: spacing[2] }}
        />
      ) : null}
      {typeof children === "string" ? (
        <Text style={[styles.label, sizeStyle.label, variantText, textStyle]}>{children}</Text>
      ) : (
        <View style={styles.row}>{children}</View>
      )}
    </Pressable>
  );
}

const styles = StyleSheet.create({
  base: {
    flexDirection: "row",
    alignItems: "center",
    justifyContent: "center",
    borderRadius: radius.md,
    borderWidth: 1,
    borderColor: "transparent",
  },
  fullWidth: { alignSelf: "stretch" },
  row: { flexDirection: "row", alignItems: "center" },
  label: { fontWeight: "600" },
});

function variantStyles(p: (typeof colors)["light"]) {
  return {
    primary: {
      container: { backgroundColor: p.primary, borderColor: p.primary },
    },
    secondary: {
      container: { backgroundColor: p.muted, borderColor: p.muted },
    },
    outline: {
      container: { backgroundColor: "transparent", borderColor: p.border },
    },
    ghost: {
      container: { backgroundColor: "transparent", borderColor: "transparent" },
    },
    danger: {
      container: { backgroundColor: p.destructive, borderColor: p.destructive },
    },
  } satisfies Record<ButtonVariant, { container: ViewStyle }>;
}

function variantTextStyles(p: (typeof colors)["light"]) {
  return {
    primary: { color: p.primaryForeground },
    secondary: { color: p.mutedForeground },
    outline: { color: p.foreground },
    ghost: { color: p.foreground },
    danger: { color: p.destructiveForeground },
  } satisfies Record<ButtonVariant, TextStyle>;
}

const sizeStyles = {
  sm: {
    container: { paddingHorizontal: spacing[3], paddingVertical: spacing[1] },
    label: { fontSize: textSizes.xs },
  },
  md: {
    container: { paddingHorizontal: spacing[4], paddingVertical: spacing[2] },
    label: { fontSize: textSizes.sm },
  },
  lg: {
    container: { paddingHorizontal: spacing[5], paddingVertical: spacing[3] },
    label: { fontSize: textSizes.base },
  },
} satisfies Record<ButtonSize, { container: ViewStyle; label: TextStyle }>;
