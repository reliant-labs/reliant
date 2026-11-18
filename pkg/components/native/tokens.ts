/**
 * Design tokens for `@<scope>/ui-native`.
 *
 * Pure TS objects — no theme provider, no context. Components import
 * tokens directly and pick between `colors.light` / `colors.dark`
 * based on `useColorScheme()`.
 *
 * Naming mirrors the web component library's semantic palette
 * (primary, secondary, destructive, muted) so cross-platform code can
 * carry the same mental model. The actual hex values are kept close
 * to the web defaults (Tailwind blue-600 / red-600 / gray-100/500/900)
 * so a primary button on iOS reads the same as on the web.
 *
 * If you outgrow this — want runtime theme switching, brand variants,
 * a token graph — install Tamagui or Unistyles; see the
 * `ui-native-package` skill for the migration path.
 */

export type ColorPalette = {
  background: string;
  foreground: string;
  card: string;
  border: string;
  primary: string;
  primaryForeground: string;
  secondary: string;
  secondaryForeground: string;
  muted: string;
  mutedForeground: string;
  destructive: string;
  destructiveForeground: string;
};

export const colors: { light: ColorPalette; dark: ColorPalette } = {
  light: {
    background: "#ffffff",
    foreground: "#111827", // gray-900
    card: "#ffffff",
    border: "#e5e7eb", // gray-200
    primary: "#2563eb", // blue-600
    primaryForeground: "#ffffff",
    secondary: "#f3f4f6", // gray-100
    secondaryForeground: "#111827",
    muted: "#f3f4f6",
    mutedForeground: "#6b7280", // gray-500
    destructive: "#dc2626", // red-600
    destructiveForeground: "#ffffff",
  },
  dark: {
    background: "#0b0f17",
    foreground: "#f9fafb", // gray-50
    card: "#111827", // gray-900
    border: "#1f2937", // gray-800
    primary: "#3b82f6", // blue-500 (slightly lifted for dark surfaces)
    primaryForeground: "#ffffff",
    secondary: "#1f2937",
    secondaryForeground: "#f9fafb",
    muted: "#1f2937",
    mutedForeground: "#9ca3af", // gray-400
    destructive: "#ef4444", // red-500
    destructiveForeground: "#ffffff",
  },
};

/**
 * Spacing — multiples of 4. Sparse on purpose (not every integer is a
 * valid choice) so layouts stay on the scale. Components pick keys
 * by name; consumers do too.
 */
export const spacing = {
  0: 0,
  1: 4,
  2: 8,
  3: 12,
  4: 16,
  5: 20,
  6: 24,
  8: 32,
  10: 40,
  12: 48,
} as const;

/**
 * Corner radius — matches the web Card / Button feel. `full` is for
 * pills and avatars.
 */
export const radius = {
  none: 0,
  sm: 4,
  md: 6,
  lg: 8,
  xl: 12,
  full: 9999,
} as const;

/**
 * Text sizes — pixel values appropriate for native (iOS / Android
 * default systems are denser than browser defaults; these are slightly
 * larger than the web library's tailwind text-* defaults).
 */
export const textSizes = {
  xs: 12,
  sm: 14,
  base: 16,
  lg: 18,
  xl: 20,
  "2xl": 24,
  "3xl": 30,
} as const;

export type SpacingKey = keyof typeof spacing;
export type RadiusKey = keyof typeof radius;
export type TextSizeKey = keyof typeof textSizes;
