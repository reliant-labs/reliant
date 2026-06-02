/** @type {import('tailwindcss').Config} */
export default {
  darkMode: 'class',
  content: [
    "./index.html",
    "./src/**/*.{js,ts,jsx,tsx}",
  ],
  theme: {
    extend: {
      colors: {
        border: "hsl(var(--border) / <alpha-value>)",
        input: "hsl(var(--input) / <alpha-value>)",
        ring: "hsl(var(--ring) / <alpha-value>)",
        background: "hsl(var(--background) / <alpha-value>)",
        foreground: "hsl(var(--foreground) / <alpha-value>)",
        primary: {
          DEFAULT: "hsl(var(--primary) / <alpha-value>)",
          foreground: "hsl(var(--primary-foreground) / <alpha-value>)",
        },
        secondary: {
          DEFAULT: "hsl(var(--secondary) / <alpha-value>)",
          foreground: "hsl(var(--secondary-foreground) / <alpha-value>)",
        },
        destructive: {
          DEFAULT: "hsl(var(--destructive) / <alpha-value>)",
          foreground: "hsl(var(--destructive-foreground) / <alpha-value>)",
        },
        success: {
          DEFAULT: "hsl(var(--success) / <alpha-value>)",
          foreground: "hsl(var(--success-foreground) / <alpha-value>)",
        },
        warning: {
          DEFAULT: "hsl(var(--warning) / <alpha-value>)",
          foreground: "hsl(var(--warning-foreground) / <alpha-value>)",
        },
        info: {
          DEFAULT: "hsl(var(--info) / <alpha-value>)",
          foreground: "hsl(var(--info-foreground) / <alpha-value>)",
        },
        muted: {
          DEFAULT: "hsl(var(--muted) / <alpha-value>)",
          foreground: "hsl(var(--muted-foreground) / <alpha-value>)",
        },
        accent: {
          DEFAULT: "hsl(var(--accent) / <alpha-value>)",
          foreground: "hsl(var(--accent-foreground) / <alpha-value>)",
        },
        popover: {
          DEFAULT: "hsl(var(--popover) / <alpha-value>)",
          foreground: "hsl(var(--popover-foreground) / <alpha-value>)",
        },
        card: {
          DEFAULT: "hsl(var(--card) / <alpha-value>)",
          foreground: "hsl(var(--card-foreground) / <alpha-value>)",
        },
      },
      borderRadius: {
        lg: "var(--radius)",
        md: "calc(var(--radius) - 2px)",
        sm: "calc(var(--radius) - 4px)",
      },
      // Bumped one step up from Tailwind defaults so dense UI text reads better.
      // Spacing/layout (rem-based padding, gap, etc.) is unchanged so existing
      // layouts don't shift; only font sizes grow.
      fontSize: {
        xs:    ["0.8125rem", { lineHeight: "1.125rem" }],  // 13px (was 12px)
        sm:    ["0.9375rem", { lineHeight: "1.375rem" }],  // 15px (was 14px)
        base:  ["1.0625rem", { lineHeight: "1.625rem" }],  // 17px (was 16px)
        lg:    ["1.1875rem", { lineHeight: "1.875rem" }],  // 19px (was 18px)
        xl:    ["1.375rem",  { lineHeight: "2rem" }],      // 22px (was 20px)
        "2xl": ["1.625rem",  { lineHeight: "2.25rem" }],   // 26px (was 24px)
        "3xl": ["2rem",      { lineHeight: "2.5rem" }],    // 32px (was 30px)
        "4xl": ["2.375rem",  { lineHeight: "2.75rem" }],   // 38px (was 36px)
      },
    },
  },
  plugins: [
    require('@tailwindcss/typography'),
  ],
}