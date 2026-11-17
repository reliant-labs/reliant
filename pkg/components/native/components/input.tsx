import React from "react";
import {
  StyleSheet,
  Text,
  TextInput,
  View,
  useColorScheme,
  type StyleProp,
  type TextInputProps,
  type TextStyle,
  type ViewStyle,
} from "react-native";
import { colors, radius, spacing, textSizes } from "../tokens";

/**
 * Input — wraps a native TextInput with an optional label + error message
 * row so a field renders consistently across screens. Mirrors the web
 * Input props (inputSize, invalid) but adds `label` and `errorText`
 * because there's no equivalent <Label>+<Input> sibling pattern that
 * composes cleanly on RN — keeping them in one component avoids
 * accidental layout drift.
 *
 * All TextInput props are forwarded; pair with controlled `value` +
 * `onChangeText`.
 */
export type InputSize = "sm" | "md" | "lg";

export interface InputProps extends Omit<TextInputProps, "style"> {
  inputSize?: InputSize;
  invalid?: boolean;
  label?: string;
  errorText?: string;
  required?: boolean;
  containerStyle?: StyleProp<ViewStyle>;
  inputStyle?: StyleProp<TextStyle>;
}

const Input = React.forwardRef<TextInput, InputProps>(function Input(
  {
    inputSize = "md",
    invalid,
    label,
    errorText,
    required,
    containerStyle,
    inputStyle,
    editable,
    ...rest
  },
  ref,
) {
  const scheme = useColorScheme() ?? "light";
  const palette = colors[scheme];

  const isInvalid = invalid || !!errorText;
  const borderColor = isInvalid ? palette.destructive : palette.border;

  const size = sizeStyles[inputSize];

  return (
    <View style={[styles.wrapper, containerStyle]}>
      {label ? (
        <Text style={[styles.label, { color: palette.foreground }]}>
          {label}
          {required ? <Text style={{ color: palette.destructive }}> *</Text> : null}
        </Text>
      ) : null}
      <TextInput
        ref={ref}
        editable={editable}
        accessibilityLabel={label}
        accessibilityState={{ disabled: editable === false }}
        placeholderTextColor={palette.mutedForeground}
        style={[
          styles.input,
          size,
          {
            color: palette.foreground,
            backgroundColor: palette.background,
            borderColor,
          },
          editable === false && {
            backgroundColor: palette.muted,
            color: palette.mutedForeground,
          },
          inputStyle,
        ]}
        {...rest}
      />
      {errorText ? (
        <Text style={[styles.error, { color: palette.destructive }]}>{errorText}</Text>
      ) : null}
    </View>
  );
});

export default Input;

const styles = StyleSheet.create({
  wrapper: { width: "100%" },
  label: {
    fontSize: textSizes.sm,
    fontWeight: "500",
    marginBottom: spacing[1],
  },
  input: {
    borderWidth: 1,
    borderRadius: radius.md,
    paddingHorizontal: spacing[3],
  },
  error: {
    marginTop: spacing[1],
    fontSize: textSizes.xs,
  },
});

const sizeStyles: Record<InputSize, ViewStyle & TextStyle> = {
  sm: { height: 36, fontSize: textSizes.xs },
  md: { height: 44, fontSize: textSizes.sm },
  lg: { height: 52, fontSize: textSizes.base },
};
