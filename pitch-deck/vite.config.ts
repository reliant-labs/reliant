import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Relative base so the built deck loads assets correctly from any path
// (file://, subdirectory hosting, etc.) — deep SPA routes shouldn't break.
export default defineConfig({
  base: "./",
  plugins: [react()],
});
