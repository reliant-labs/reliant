import React from "react";
import {
  Switch as RNSwitch,
  useColorScheme,
  type SwitchProps as RNSwitchProps,
} from "react-native";
import { colors } from "../tokens";

/**
 * Switch — thin wrapper over RN's built-in Switch with palette-aware
 * track + thumb colors so it doesn't look out of place against the rest
 * of the primitives.
 *
 * The underlying Switch already does the right thing on iOS (continuous
 * track) and Android (Material). We only override colors.
 */
export interface SwitchProps extends RNSwitchProps {}

export default function Switch({ trackColor, thumbColor, ...rest }: SwitchProps) {
  const scheme = useColorScheme() ?? "light";
  const palette = colors[scheme];
  return (
    <RNSwitch
      trackColor={
        trackColor ?? {
          false: palette.muted,
          true: palette.primary,
        }
      }
      thumbColor={thumbColor ?? palette.background}
      ios_backgroundColor={palette.muted}
      {...rest}
    />
  );
}
