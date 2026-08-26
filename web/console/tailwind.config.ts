import type { Config } from "tailwindcss";

const config: Config = {
  content: ["./src/**/*.{js,ts,jsx,tsx,mdx}"],
  theme: {
    extend: {
      colors: {
        bg: "#0D1312",
        surface: "#131C1B",
        "surface-2": "#1A2523",
        border: "#293633",
        text: "#E6ECEA",
        "text-muted": "#96A6A1",
        accent: "#37D9C4",
        warn: "#E7A63D",
        crit: "#EC7368",
      },
    },
  },
  plugins: [],
};

export default config;
