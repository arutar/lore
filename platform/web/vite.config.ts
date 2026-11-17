import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { fileURLToPath } from "node:url";

// `vite build --mode demo` (npm run build:demo) makes the read-only demo for GitHub Pages: the
// API answers from fictional data in the browser (src/demo), and asset paths are relative so the
// site works under any path, such as https://<owner>.github.io/<repository>/.
export default defineConfig(({ mode }) => ({
  base: mode === "demo" ? "./" : "/",
  build: mode === "demo" ? { outDir: "dist-demo" } : {},
  // Outside the demo, src/api.ts's import of the demo resolves to an empty stand-in.
  resolve: mode === "demo" ? {} : { alias: [{ find: /^\.\/demo\/api$/, replacement: fileURLToPath(new URL("./src/demo/off.ts", import.meta.url)) }] },
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/web": "http://localhost:8080",
      "/healthz": "http://localhost:8080",
    },
  },
}));
