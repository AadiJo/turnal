import { defineConfig } from "vite";
import preact from "@preact/preset-vite";
import path from "node:path";

export default defineConfig({
  root: path.resolve(__dirname, "internal/viewer/web"),
  publicDir: path.resolve(__dirname, "assets"),
  base: "/__TURNAL_BASE__/",
  plugins: [preact()],
  resolve: {
    alias: {
      react: "preact/compat",
      "react-dom": "preact/compat",
      "react/jsx-runtime": "preact/jsx-runtime",
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false,
    target: "es2020",
    cssCodeSplit: false,
    rollupOptions: {
      output: {
        entryFileNames: "assets/app-[hash].js",
        chunkFileNames: "assets/chunk-[hash].js",
        assetFileNames: "assets/app-[hash][extname]",
      },
    },
  },
  server: {
    host: "127.0.0.1",
    port: 4178,
  },
});
