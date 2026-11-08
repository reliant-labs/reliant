import React from "react";
import { useColorScheme, type StyleProp, type ViewStyle } from "react-native";
import {
  SafeAreaView as RNSafeAreaView,
  type SafeAreaViewProps as RNSafeAreaViewProps,
} from "react-native-safe-area-context";
import { colors } from "../tokens";

/**
 * SafeAreaView — re-export of `react-native-safe-area-context`'s
 * SafeAreaView with sensible defaults (background color from the
 * palette, edges defaulting to top + bottom + sides). Use as the root
 * of a screen so notches / home indicators don't clip content.
 *
 * The `react-native-safe-area-context` version is preferred over RN's
 * deprecated SafeAreaView; Expo ships it preinstalled.
 */
export interface SafeAreaViewProps extends RNSafeAreaViewProps {
  style?: StyleProp<ViewStyle>;
}

export default function SafeAreaView({
  style,
  edges = ["top", "right", "bottom", "left"],
  children,
  ...rest
}: SafeAreaViewProps) {
  const scheme = useColorScheme() ?? "light";
  const palette = colors[scheme];
  return (
    <RNSafeAreaView
      edges={edges}
      style={[{ flex: 1, backgroundColor: palette.background }, style]}
      {...rest}
    >
      {children}
    </RNSafeAreaView>
  );
}
