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
      // Bumped one step up from Tailwind's defaults so dense UI text reads
      // better. Spacing/layout (rem-based padding, gap, etc.) is unchanged so
      // existing layouts don't shift; only font sizes grow.
      //
      // Every step is rem-based ON PURPOSE: rem resolves against the root size
      // that the Appearance font-size preference sets on <html>, so the whole
      // scale moves with the user's choice. A px value anywhere in this scale —
      // or a `text-[10px]` arbitrary class at a call site — opts that element
      // out of the preference permanently. Reach for `text-3xs`/`text-2xs`
      // below instead of an arbitrary px value.
      //
      // The px figures in the comments are what each step renders at the
      // DEFAULT `md` preference, where the root is 14px (see
      // lib/rootFontSize.ts). They are not fixed sizes: at `xs` the root is
      // 12px and at `xl` it is 16px, so every value below scales by ±14%.
      // The two smallest steps exist so dense UI never needs an arbitrary px
      // value. Their line-heights are ~1.6x the font size, matching what body
      // sets, because the ~300 call sites they replaced were arbitrary classes
      // (`text-[10px]`) which carry NO line-height and so inherited that 1.6.
      // Shipping Tailwind's tighter default ratio here silently shortened every
      // one of those line boxes by ~1.8px, which reads as the app's spacing
      // having collapsed. Keep the ratio when adding a step.
      fontSize: {
        "3xs": ["0.643rem",  { lineHeight: "1.0288rem" }], //  9.0px / 14.4px @md
        "2xs": ["0.714rem",  { lineHeight: "1.1424rem" }], // 10.0px / 16.0px @md
        xs:    ["0.8125rem", { lineHeight: "1.3rem" }],    // 11.4px / 18.2px @md
        sm:    ["0.9375rem", { lineHeight: "1.5rem" }],    // 13.1px / 21.0px @md
        base:  ["1.0625rem", { lineHeight: "1.625rem" }],  // 14.9px @md
        lg:    ["1.1875rem", { lineHeight: "1.875rem" }],  // 16.6px @md
        xl:    ["1.375rem",  { lineHeight: "2rem" }],      // 19.3px @md
        "2xl": ["1.625rem",  { lineHeight: "2.25rem" }],   // 22.8px @md
        "3xl": ["2rem",      { lineHeight: "2.5rem" }],    // 28.0px @md
        "4xl": ["2.375rem",  { lineHeight: "2.75rem" }],   // 33.3px @md
      },
    },
  },
  plugins: [
    require('@tailwindcss/typography'),
  ],
}