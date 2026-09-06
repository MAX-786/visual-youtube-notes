import { sveltekit } from "@sveltejs/kit/vite";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [sveltekit()],
  test: {
    // Unit tests only; Playwright owns tests/e2e.
    include: ["src/**/*.test.ts"],
    environment: "node",
  },
  server: {
    // Local development talks to a Go API started on 8080 (`go run ./cmd/api`
    // with the VYN_* variables set); production has Caddy in front (ADR 0008).
    proxy: {
      "/api": {
        target: "http://127.0.0.1:8080",
        changeOrigin: false,
      },
    },
  },
});
