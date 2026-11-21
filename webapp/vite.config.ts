/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

// The build lands in the Go embed directory; `make web` runs before `go build`.
export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: "../internal/web/dist",
    emptyOutDir: false,
  },
  server: {
    proxy: { "/api": "http://127.0.0.1:8080" },
  },
  test: {
    environment: "jsdom",
  },
});
