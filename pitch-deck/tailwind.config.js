/** @type {import('tailwindcss').Config} */
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        // Reliant brand palette, pulled from the reliantlabs.io logo + app surface.
        brand: {
          blue: "#0d8cff", // logo .cls-1
          purple: "#6e39ff", // logo .cls-2
        },
        // App surface tokens (dark-first identity).
        background: "#0a0a0a",
        surface: "#111827",
        "surface-raised": "#1f2937",
        border: "#1f2937",
        "border-strong": "#374151",
        foreground: "#ffffff",
        muted: "#9ca3af",
        "muted-strong": "#d1d5db",
        success: "#10b981",
        amber: "#f59e0b",
        danger: "#ef4444",
      },
      fontFamily: {
        sans: [
          "Inter",
          "system-ui",
          "-apple-system",
          "sans-serif",
        ],
        mono: [
          "ui-monospace",
          "SFMono-Regular",
          "Menlo",
          "Monaco",
          "Consolas",
          "Liberation Mono",
          "Courier New",
          "monospace",
        ],
      },
      backgroundImage: {
        // Signature blue -> purple accent used for gradient text and rules.
        "brand-gradient":
          "linear-gradient(135deg, #0d8cff 0%, #6e39ff 100%)",
        // Midnight-navy section divider gradient from the site.
        "midnight-gradient":
          "linear-gradient(135deg, #1a1a2e 0%, #16213e 25%, #0f3460 50%, #1a1a2e 75%, #16213e 100%)",
      },
      keyframes: {
        "fade-up": {
          "0%": { opacity: "0", transform: "translateY(10px)" },
          "100%": { opacity: "1", transform: "translateY(0)" },
        },
        "fade-in": {
          "0%": { opacity: "0" },
          "100%": { opacity: "1" },
        },
        "grow-bar": {
          "0%": { transform: "scaleX(0)" },
          "100%": { transform: "scaleX(1)" },
        },
      },
      animation: {
        "fade-up": "fade-up 300ms ease-out both",
        "fade-in": "fade-in 250ms ease-out both",
        "grow-bar": "grow-bar 600ms ease-out both",
      },
    },
  },
  plugins: [],
};
