import React from "react";
import { ActivityIndicator, useColorScheme, type ActivityIndicatorProps } from "react-native";
import { colors } from "../tokens";

/**
 * Spinner — thin wrapper over RN's ActivityIndicator that defaults the
 * color to the active palette's primary so spinners match the rest of
 * the UI without per-call-site theming.
 */
export interface SpinnerProps extends ActivityIndicatorProps {}

export default function Spinner({ color, size = "small", ...rest }: SpinnerProps) {
  const scheme = useColorScheme() ?? "light";
  const palette = colors[scheme];
  return <ActivityIndicator size={size} color={color ?? palette.primary} {...rest} />;
}
