import type { Config } from "tailwindcss";

/*
 * Tailwind is driven entirely by CSS variables declared in globals.css so
 * the Tweaks panel (Phase 13) can swap accent / sidebar / density at
 * runtime. Color utilities therefore resolve to var(--token) rather than
 * a hard-coded hex; Tailwind opacity modifiers on these still work when
 * the variable is defined as a solid hex (e.g. bg-accent/20 is fine).
 *
 * Legacy aliases (navy.*, ink.muted, ink.line, accent.strong, accent.soft)
 * are intentionally NOT carried over — any surface still using them is
 * going to be rebuilt phase-by-phase and those classes will simply no-op
 * until the page is ported.
 */
const config: Config = {
  content: ["./src/**/*.{ts,tsx,mdx}"],
  theme: {
    extend: {
      colors: {
        // Light main-surface hierarchy
        app: {
          DEFAULT: "var(--bg)",
          2: "var(--bg2)",
          3: "var(--bg3)",
        },
        // Dark rail + sidebar
        rail: "var(--rail-bg)",
        sidebar: "var(--sb-bg)",

        // Text tones (on light surface)
        ink: {
          DEFAULT: "var(--text)",
          2: "var(--text2)",
          3: "var(--text3)",
        },

        // Borders / hairlines
        line: "var(--border)",

        // Brand
        accent: {
          DEFAULT: "var(--accent)",
          hover: "var(--accent-hover)",
          dim: "var(--accent-dim)",
        },

        // Presence / status
        status: {
          green: "var(--green)",
          yellow: "var(--yellow)",
          red: "var(--red)",
        },
      },
      fontFamily: {
        // Wired to the next/font variable set in layout.tsx.
        sans: ["var(--font-sans)", "system-ui", "sans-serif"],
      },
      fontSize: {
        "2xs": "10.5px",
        xs: "11.5px",
        sm: "13px",
        base: "14px",
        md: "15px",
        lg: "16px",
        xl: "18px",
        "2xl": "22px",
        "3xl": "28px",
        "4xl": "34px",
      },
      borderRadius: {
        sm: "var(--radius-sm)",
        DEFAULT: "var(--radius-md)",
        md: "var(--radius-md)",
        lg: "var(--radius-lg)",
        xl: "14px",
      },
      boxShadow: {
        sm: "var(--shadow-sm)",
        md: "var(--shadow-md)",
        lg: "var(--shadow-lg)",
      },
      spacing: {
        rail: "var(--rail-w)",
        sidebar: "var(--sb-w)",
      },
    },
  },
  plugins: [],
};

export default config;
