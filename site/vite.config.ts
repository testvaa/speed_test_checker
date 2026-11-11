import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "node:path";
import { fileURLToPath } from "node:url";

const r = (p: string) => path.resolve(path.dirname(fileURLToPath(import.meta.url)), p);

// GitHub Pages project site: https://testvaa.github.io/speed_test_checker/
export default defineConfig({
  base: "/speed_test_checker/",
  plugins: [react(), tailwindcss()],
  resolve: { alias: { "@": r("src") } },
  build: {
    outDir: "../docs",
    emptyOutDir: true,
  },
});
